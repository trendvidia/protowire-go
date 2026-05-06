// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// FuzzUnmarshal feeds arbitrary bytes through codec.Unmarshal against
// every registered template. Any panic indicates a missing bounds
// check on the wire path — the public API must always return an error
// on malformed input. The View constructor is exercised on the same
// input for the same reason.
func FuzzUnmarshal(f *testing.F) {
	fd := compileTestProto(f)
	codec, err := sbe.NewCodec(fd)
	if err != nil {
		f.Fatalf("NewCodec: %v", err)
	}

	// Resolve descriptors once; fuzz iterations reuse them.
	names := []protoreflect.Name{"Simple", "Order", "WithComposite", "WithNarrow"}
	descs := make([]protoreflect.MessageDescriptor, 0, len(names))
	for _, n := range names {
		if d := fd.Messages().ByName(n); d != nil {
			descs = append(descs, d)
		}
	}

	// Seed corpus: marshal a few representative messages so the fuzzer
	// has structurally-valid starting points.
	for _, d := range descs {
		if good, err := codec.Marshal(dynamicpb.NewMessage(d)); err == nil {
			f.Add(good)
		}
	}

	// Pathological seeds.
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})                                     // empty header
	f.Add([]byte{0xff, 0xff, 1, 0, 0, 0, 0, 0})                               // huge blockLength, template 1
	f.Add([]byte{0, 0, 0xff, 0xff, 0, 0, 0, 0})                               // unknown template
	f.Add([]byte{8, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}) // group with absurd numInGroup

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("sbe decode panicked: %v\ninput=%x", r, data)
			}
		}()
		for _, d := range descs {
			msg := dynamicpb.NewMessage(d)
			_ = codec.Unmarshal(data, msg)
		}
		_, _ = codec.View(data)
	})
}
