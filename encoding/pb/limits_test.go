// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// decimalWire builds field 1 of a struct as a pxf.Decimal message with
// the given scale, by hand, so the decoder sees scales the encoder never
// writes. unscaled is the two bytes for 25.
func decimalWire(scale int32, negative bool) []byte {
	var sub []byte
	sub = protowire.AppendTag(sub, 1, protowire.BytesType)
	sub = protowire.AppendBytes(sub, []byte{25})
	sub = protowire.AppendTag(sub, 2, protowire.VarintType)
	// A plain int32 varint: sign-extended to 64 bits, as protobuf-go writes it.
	sub = protowire.AppendVarint(sub, uint64(int64(scale)))
	if negative {
		sub = protowire.AppendTag(sub, 3, protowire.VarintType)
		sub = protowire.AppendVarint(sub, 1)
	}
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	return protowire.AppendBytes(out, sub)
}

type decimalHolder struct {
	D *big.Rat `protowire:"1"`
}

// promptly runs f and fails the test if it has not returned within the
// budget. A regression in a magnitude bound shows up as work proportional
// to the hostile value — on these inputs, minutes to never — so a plain
// call would hang the suite rather than fail it. The goroutine leaks on
// failure, which is fine for a test that has already failed.
func promptly(t *testing.T, budget time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(budget):
		t.Fatalf("did not return within %v: the bound is not applied before the work it bounds", budget)
		return nil
	}
}

// TestDecimalScaleBound pins #95: a Decimal whose scale magnitude exceeds
// MaxNumericLiteralDigits is rejected before 10^|scale| is materialised,
// on both signs, and one at the bound still decodes to its exact value.
func TestDecimalScaleBound(t *testing.T) {
	pow := func(n int64) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil) }
	cases := []struct {
		name  string
		scale int32
		want  *big.Rat // nil: expect the bound's error
	}{
		{"at the bound", MaxNumericLiteralDigits, new(big.Rat).SetFrac(big.NewInt(25), pow(MaxNumericLiteralDigits))},
		{"at the negative bound", -MaxNumericLiteralDigits, new(big.Rat).SetInt(new(big.Int).Mul(big.NewInt(25), pow(MaxNumericLiteralDigits)))},
		{"one past the bound", MaxNumericLiteralDigits + 1, nil},
		{"one past the negative bound", -MaxNumericLiteralDigits - 1, nil},
		{"MaxInt32", math.MaxInt32, nil},
		{"MinInt32", math.MinInt32, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h decimalHolder
			err := promptly(t, 5*time.Second, func() error { return Unmarshal(decimalWire(tc.scale, false), &h) })
			if tc.want == nil {
				if err == nil {
					t.Fatalf("scale %d: decoded %s, want an error", tc.scale, h.D)
				}
				if !strings.Contains(err.Error(), "MaxNumericLiteralDigits") {
					t.Fatalf("scale %d: error %q does not name the limit", tc.scale, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("scale %d: %v", tc.scale, err)
			}
			if h.D.Cmp(tc.want) != 0 {
				t.Fatalf("scale %d: got %s, want %s", tc.scale, h.D.FloatString(8), tc.want.FloatString(8))
			}
		})
	}
}

type bigFloatHolder struct {
	F *big.Float `protowire:"1"`
}

// bigFloatWire builds field 1 as a pxf.BigFloat by hand.
func bigFloatWire(mant []byte, exp int32, prec uint32) []byte {
	var sub []byte
	sub = protowire.AppendTag(sub, 1, protowire.BytesType)
	sub = protowire.AppendBytes(sub, mant)
	sub = protowire.AppendTag(sub, 2, protowire.VarintType)
	sub = protowire.AppendVarint(sub, uint64(int64(exp)))
	sub = protowire.AppendTag(sub, 3, protowire.VarintType)
	sub = protowire.AppendVarint(sub, uint64(prec))
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	return protowire.AppendBytes(out, sub)
}

// TestBigFloatExponentNeedsNoBound is the written reason #95 asks for:
// decoding is prompt across the whole int32 exponent range because
// big.Float stores the exponent rather than materialising 2^exponent,
// and the decoded value is exact.
func TestBigFloatExponentNeedsNoBound(t *testing.T) {
	for _, exp := range []int32{math.MinInt32, -1_000_000, -1, 0, 1, 1_000_000, math.MaxInt32 - 8} {
		t.Run(big.NewInt(int64(exp)).String(), func(t *testing.T) {
			var h bigFloatHolder
			err := promptly(t, 5*time.Second, func() error { return Unmarshal(bigFloatWire([]byte{25}, exp, 64), &h) })
			if err != nil {
				t.Fatalf("exponent %d: %v", exp, err)
			}
			// 25 = 0b11001, so v = 25 × 2^exp has MantExp exponent exp+5.
			if got := h.F.MantExp(nil); got != int(exp)+5 {
				t.Fatalf("exponent %d: decoded MantExp %d, want %d", exp, got, int(exp)+5)
			}
			if h.F.IsInf() {
				t.Fatalf("exponent %d: decoded to an infinity", exp)
			}
		})
	}
}

// TestBigFloatOverflowIsAnError: a mantissa whose bits push the exponent
// past big.Float's range used to decode as +Inf, which pxf.BigFloat
// cannot represent. protowire#278 makes that an error.
func TestBigFloatOverflowIsAnError(t *testing.T) {
	var h bigFloatHolder
	err := promptly(t, 5*time.Second, func() error { return Unmarshal(bigFloatWire([]byte{25}, math.MaxInt32, 64), &h) })
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("err = %v, want an overflow error", err)
	}
}

// TestMarshalRefusesWhatUnmarshalRejects: the encoders refuse a value the
// wire cannot carry, instead of writing bytes no conformant decoder
// accepts (a scale past the limit), a wrong exponent (int32 wrap), or
// panicking (an infinity).
func TestMarshalRefusesWhatUnmarshalRejects(t *testing.T) {
	two := func(k int64) *big.Int { return new(big.Int).Exp(big.NewInt(2), big.NewInt(k), nil) }

	t.Run("Decimal scale past the limit", func(t *testing.T) {
		_, err := Marshal(&decimalHolder{D: new(big.Rat).SetFrac(big.NewInt(1), two(MaxNumericLiteralDigits+1))})
		if err == nil || !strings.Contains(err.Error(), "MaxNumericLiteralDigits") {
			t.Fatalf("err = %v, want the MaxNumericLiteralDigits error", err)
		}
	})
	t.Run("Decimal scale at the limit round-trips", func(t *testing.T) {
		in := &decimalHolder{D: new(big.Rat).SetFrac(big.NewInt(1), two(MaxNumericLiteralDigits))}
		data, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out decimalHolder
		if err := Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		if in.D.Cmp(out.D) != 0 {
			t.Fatalf("round trip changed the value")
		}
	})
	t.Run("infinite BigFloat", func(t *testing.T) {
		// At precision 0 the encoder used to write an empty message that
		// read back as zero; at any other precision it dereferenced nil.
		for _, prec := range []uint{0, 64} {
			for _, sign := range []int{1, -1} {
				_, err := Marshal(&bigFloatHolder{F: new(big.Float).SetPrec(prec).SetInf(sign < 0)})
				if err == nil || !strings.Contains(err.Error(), "infinite") {
					t.Fatalf("prec %d sign %d: err = %v, want the infinity error", prec, sign, err)
				}
			}
		}
	})
	t.Run("BigFloat exponent below int32 after adjustment", func(t *testing.T) {
		// exp - prec leaves int32: 0.75 × 2^(MinInt32+3) with 53 bits of
		// precision has wire exponent MinInt32+3-53.
		f := new(big.Float).SetPrec(53).SetMantExp(big.NewFloat(0.75), math.MinInt32+3)
		_, err := Marshal(&bigFloatHolder{F: f})
		if err == nil || !strings.Contains(err.Error(), "does not fit int32") {
			t.Fatalf("err = %v, want the exponent-range error", err)
		}
	})
	t.Run("BigFloat exponent just inside int32 round-trips", func(t *testing.T) {
		in := &bigFloatHolder{F: new(big.Float).SetPrec(53).SetMantExp(big.NewFloat(0.75), math.MinInt32+53)}
		data, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		out := &bigFloatHolder{F: new(big.Float)}
		if err := Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
		if in.F.Cmp(out.F) != 0 {
			t.Fatalf("round trip changed the value: %s → %s", in.F.Text('p', 0), out.F.Text('p', 0))
		}
	})
}
