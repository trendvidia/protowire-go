// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Cross-port PXF microbench: Go implementation.
//
// Reads `testdata/bench-test.binpb` (FileDescriptorSet) and
// `testdata/bench-test.pxf` (text payload), times unmarshal + marshal
// of `bench.v1.Config` for at least `--seconds` (default 3), and
// prints one JSON line per op:
//
//	{"port":"go","op":"unmarshal","ns_per_op":7045,"mib_per_sec":89.3,"iterations":429000,"bytes":624}
//	{"port":"go","op":"marshal","ns_per_op":5280,"iterations":571000}
//
// The other ports' bench-pxf binaries print the same shape; the
// scripts/cross_pxf_bench.sh runner aggregates them.
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
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
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

	fdsBytes, err := os.ReadFile(filepath.Join(dir, "bench-test.binpb"))
	must(err, "read bench-test.binpb")
	pxfBytes, err := os.ReadFile(filepath.Join(dir, "bench-test.pxf"))
	must(err, "read bench-test.pxf")

	desc := loadConfigDescriptor(fdsBytes)
	target := time.Duration(*seconds * float64(time.Second))

	// Warm-up.
	if _, err := pxf.UnmarshalDescriptor(pxfBytes, desc); err != nil {
		fail("warm-up unmarshal: " + err.Error())
	}

	iters, elapsed := timeLoop(target, func() {
		if _, err := pxf.UnmarshalDescriptor(pxfBytes, desc); err != nil {
			panic(err)
		}
	})
	emit(result{
		Port:       "go",
		Op:         "unmarshal",
		NsPerOp:    elapsed.Nanoseconds() / iters,
		MibPerSec:  mibPerSec(len(pxfBytes), iters, elapsed),
		Iterations: iters,
		Bytes:      len(pxfBytes),
	})

	msg, err := pxf.UnmarshalDescriptor(pxfBytes, desc)
	must(err, "seed unmarshal for marshal")
	iters2, elapsed2 := timeLoop(target, func() {
		if _, err := pxf.Marshal(msg); err != nil {
			panic(err)
		}
	})
	emit(result{
		Port:       "go",
		Op:         "marshal",
		NsPerOp:    elapsed2.Nanoseconds() / iters2,
		Iterations: iters2,
	})
}

func loadConfigDescriptor(fdsBytes []byte) protoreflect.MessageDescriptor {
	var fds descriptorpb.FileDescriptorSet
	must(proto.Unmarshal(fdsBytes, &fds), "decode FileDescriptorSet")

	files, err := protodesc.NewFiles(&fds)
	must(err, "build proto registry")

	d, err := files.FindDescriptorByName(protoreflect.FullName("bench.v1.Config"))
	must(err, "lookup bench.v1.Config")
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		fail("bench.v1.Config is not a message")
	}
	return md
}

func timeLoop(target time.Duration, fn func()) (int64, time.Duration) {
	start := time.Now()
	deadline := start.Add(target)
	var iters int64
	for {
		// Run in batches of 64 to keep timer overhead in the noise.
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
	totalBytes := float64(payloadBytes) * float64(iters)
	return (totalBytes / (1024 * 1024)) / elapsed.Seconds()
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
	fmt.Fprintln(os.Stderr, "bench-pxf:", msg)
	os.Exit(1)
}

// Touch protoregistry so go.mod's transitive imports don't get tidied away
// from this binary's eyes — protodesc.NewFiles already pulls it in, but
// keeping a direct reference makes the dependency explicit.
var _ = (*protoregistry.Files)(nil)
