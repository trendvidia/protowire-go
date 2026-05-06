// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Cross-port SBE microbench: Go implementation.
//
// Loads `testdata/sbe-bench.binpb` (FileDescriptorSet), populates a
// canonical `bench.v1.Order` (10 scalars + 2-entry Fill group), and
// times marshal + unmarshal for at least `--seconds` (default 3).
// Prints one JSON line per op:
//
//	{"port":"go","op":"sbe-marshal","ns_per_op":1050,"iterations":...,"bytes":94}
//	{"port":"go","op":"sbe-unmarshal","ns_per_op":6700,"mib_per_sec":13.4,"iterations":...,"bytes":94}
//
// The other ports' bench-sbe binaries print the same shape; the
// scripts/cross_sbe_bench.sh runner aggregates them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

type result struct {
	Port       string  `json:"port"`
	Op         string  `json:"op"`
	NsPerOp    int64   `json:"ns_per_op"`
	MibPerSec  float64 `json:"mib_per_sec,omitempty"`
	Iterations int64   `json:"iterations"`
	Bytes      int     `json:"bytes,omitempty"`
}

func main() {
	seconds := flag.Float64("seconds", 3.0, "minimum measurement window per op")
	dataDir := flag.String("testdata", "", "path to protowire/testdata (default: <cwd>/testdata)")
	flag.Parse()

	dir := *dataDir
	if dir == "" {
		cwd, err := os.Getwd()
		must(err, "getwd")
		dir = filepath.Join(cwd, "testdata")
	}

	fdsBytes, err := os.ReadFile(filepath.Join(dir, "sbe-bench.binpb"))
	must(err, "read sbe-bench.binpb")

	var fds descriptorpb.FileDescriptorSet
	must(proto.Unmarshal(fdsBytes, &fds), "decode FileDescriptorSet")
	files, err := protodesc.NewFiles(&fds)
	must(err, "build proto registry")

	d, err := files.FindDescriptorByName(protoreflect.FullName("bench.v1.Order"))
	must(err, "lookup bench.v1.Order")
	desc := d.(protoreflect.MessageDescriptor)
	fillDesc := desc.Messages().ByName("Fill")

	// Resolve the file descriptor that owns Order so we can register the
	// codec from it.
	codec, err := sbe.NewCodec(desc.ParentFile())
	must(err, "build codec")

	msg := buildOrder(desc, fillDesc)
	target := time.Duration(*seconds * float64(time.Second))

	// One marshal up front to populate our payload-bytes count and warm caches.
	wireBytes, err := codec.Marshal(msg)
	must(err, "warm-up marshal")

	itersM, elapsedM := timeLoop(target, func() {
		if _, err := codec.Marshal(msg); err != nil {
			panic(err)
		}
	})
	emit(result{
		Port:       "go",
		Op:         "sbe-marshal",
		NsPerOp:    elapsedM.Nanoseconds() / itersM,
		Iterations: itersM,
		Bytes:      len(wireBytes),
	})

	itersU, elapsedU := timeLoop(target, func() {
		out := dynamicpb.NewMessage(desc)
		if err := codec.Unmarshal(wireBytes, out); err != nil {
			panic(err)
		}
	})
	emit(result{
		Port:       "go",
		Op:         "sbe-unmarshal",
		NsPerOp:    elapsedU.Nanoseconds() / itersU,
		MibPerSec:  mibPerSec(len(wireBytes), itersU, elapsedU),
		Iterations: itersU,
		Bytes:      len(wireBytes),
	})
}

func buildOrder(desc, fillDesc protoreflect.MessageDescriptor) *dynamicpb.Message {
	msg := dynamicpb.NewMessage(desc)
	set := func(name string, v protoreflect.Value) {
		fd := desc.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			fail("field not found: " + name)
		}
		msg.Set(fd, v)
	}
	set("order_id", protoreflect.ValueOfUint64(1001))
	set("symbol", protoreflect.ValueOfString("AAPL"))
	set("price", protoreflect.ValueOfInt64(19150))
	set("quantity", protoreflect.ValueOfUint32(100))
	set("side", protoreflect.ValueOfEnum(1)) // SIDE_SELL
	set("active", protoreflect.ValueOfBool(true))
	set("weight", protoreflect.ValueOfFloat64(0.85))
	set("score", protoreflect.ValueOfFloat32(2.5))

	fillsFd := desc.Fields().ByName("fills")
	list := msg.Mutable(fillsFd).List()
	for _, f := range []struct {
		Price int64
		Qty   uint32
		ID    uint64
	}{
		{Price: 19155, Qty: 25, ID: 5001},
		{Price: 19160, Qty: 50, ID: 5002},
	} {
		fill := dynamicpb.NewMessage(fillDesc)
		fill.Set(fillDesc.Fields().ByName("fill_price"), protoreflect.ValueOfInt64(f.Price))
		fill.Set(fillDesc.Fields().ByName("fill_qty"), protoreflect.ValueOfUint32(f.Qty))
		fill.Set(fillDesc.Fields().ByName("fill_id"), protoreflect.ValueOfUint64(f.ID))
		list.Append(protoreflect.ValueOfMessage(fill))
	}
	return msg
}

func timeLoop(target time.Duration, fn func()) (int64, time.Duration) {
	start := time.Now()
	deadline := start.Add(target)
	var iters int64
	for {
		for i := 0; i < 64; i++ {
			fn()
		}
		iters += 64
		if time.Now().After(deadline) {
			break
		}
	}
	return iters, time.Since(start)
}

func mibPerSec(payloadBytes int, iters int64, elapsed time.Duration) float64 {
	return (float64(payloadBytes) * float64(iters)) / (1024 * 1024) / elapsed.Seconds()
}

func emit(r result) {
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fail(err.Error())
	}
}

func must(err error, what string) {
	if err != nil {
		fail(what + ": " + err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "bench-sbe:", msg)
	os.Exit(1)
}
