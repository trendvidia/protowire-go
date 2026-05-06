// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

import (
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

// fuzzTarget mirrors the shapes the public API is expected to handle:
// scalars, nested message, repeated message, byte slice, zigzag.
type fuzzTarget struct {
	S     string       `protowire:"1"`
	I     int64        `protowire:"2"`
	U     uint32       `protowire:"3"`
	B     bool         `protowire:"4"`
	F32   float32      `protowire:"5"`
	F64   float64      `protowire:"6"`
	Z     int64        `protowire:"7,zigzag"`
	Buf   []byte       `protowire:"8"`
	Inner *fuzzInner   `protowire:"9"`
	List  []*fuzzInner `protowire:"10"`
}

type fuzzInner struct {
	Name string `protowire:"1"`
	N    int32  `protowire:"2"`
}

// FuzzUnmarshal feeds arbitrary bytes through pb.Unmarshal. Any
// out-of-bounds slice, infinite loop, or panic is a bug — the public
// API must always return an error on malformed input.
func FuzzUnmarshal(f *testing.F) {
	// Seed corpus: round-trip a few representative shapes so the fuzzer
	// has well-formed mutations to start from.
	seeds := []*fuzzTarget{
		{S: "hello", I: 42, U: 7, B: true, F32: 1.5, F64: 2.5},
		{Z: -100, Buf: []byte{0x00, 0xff, 0x10}},
		{Inner: &fuzzInner{Name: "x", N: -1}},
		{List: []*fuzzInner{{Name: "a", N: 1}, {Name: "b", N: 2}}},
		{},
	}
	for _, s := range seeds {
		data, err := pb.Marshal(s)
		if err != nil {
			f.Fatalf("seed marshal: %v", err)
		}
		f.Add(data)
	}
	// Pathological seeds.
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Add([]byte{0x08})                                     // tag-only, truncated varint
	f.Add([]byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}) // length-delim with absurd length

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("pb.Unmarshal panicked: %v\ninput=%x", r, data)
			}
		}()
		var dst fuzzTarget
		_ = pb.Unmarshal(data, &dst)
	})
}
