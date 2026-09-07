// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

import (
	"google.golang.org/protobuf/encoding/protowire"
	"math"
	"math/big"
	"strings"
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
	Nums  []uint32     `protowire:"11"` // packed repeated scalar
	Zigs  []int64      `protowire:"12,zigzag"`
	Reals []float32    `protowire:"13"`
	Rat   *big.Rat     `protowire:"14"`
	BF    *big.Float   `protowire:"15"`
}

type fuzzInner struct {
	Name string `protowire:"1"`
	N    int32  `protowire:"2"`
}

// FuzzUnmarshal feeds arbitrary bytes through pb.Unmarshal. Any
// out-of-bounds slice, infinite loop, or panic is a bug — the public
// API must always return an error on malformed input.
// hostileDecimal is a fuzzTarget with Rat set to a Decimal whose
// unscaled is 25 and whose scale is the given value — scales the encoder
// never writes, built by hand.
func hostileDecimal(scale int32) []byte {
	var sub []byte
	sub = protowire.AppendTag(sub, 1, protowire.BytesType)
	sub = protowire.AppendBytes(sub, []byte{25})
	sub = protowire.AppendTag(sub, 2, protowire.VarintType)
	sub = protowire.AppendVarint(sub, uint64(int64(scale)))
	var out []byte
	out = protowire.AppendTag(out, 14, protowire.BytesType)
	return protowire.AppendBytes(out, sub)
}

// TestHostileDecimalSeedsReachTheBound is the positive marker that the
// seeds above exercise the arm they are for: each must fail on the
// limit, not on a corrupt tag or a silent skip.
func TestHostileDecimalSeedsReachTheBound(t *testing.T) {
	for _, scale := range []int32{math.MaxInt32, math.MinInt32} {
		var dst fuzzTarget
		err := pb.Unmarshal(hostileDecimal(scale), &dst)
		if err == nil || !strings.Contains(err.Error(), "MaxNumericLiteralDigits") {
			t.Fatalf("scale %d: err = %v, want the MaxNumericLiteralDigits error", scale, err)
		}
	}
}

func FuzzUnmarshal(f *testing.F) {
	// Seed corpus: round-trip a few representative shapes so the fuzzer
	// has well-formed mutations to start from.
	seeds := []*fuzzTarget{
		{S: "hello", I: 42, U: 7, B: true, F32: 1.5, F64: 2.5},
		{Z: -100, Buf: []byte{0x00, 0xff, 0x10}},
		{Inner: &fuzzInner{Name: "x", N: -1}},
		{List: []*fuzzInner{{Name: "a", N: 1}, {Name: "b", N: 2}}},
		{Nums: []uint32{1, 0, 300}, Zigs: []int64{-1, 0, 2}, Reals: []float32{1.5, 0}},
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
	// A Decimal whose scale is 2^31-1 and one at -2^31, so the corpus
	// reaches the bound in unmarshalBigRatMsg (#95). Unbounded, either one
	// runs for longer than the fuzz deadline and reads as a hang, which is
	// why the fuzzer could not find the shape on its own.
	f.Add(hostileDecimal(math.MaxInt32))
	f.Add(hostileDecimal(math.MinInt32))

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
