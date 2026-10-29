// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"bytes"
	"math/big"
	"testing"
)

// TestBigFloatPrecZeroKeepsItsSign: a prec-0 big.Float is ±0, and PXF
// writes the two apart, "0" and "-0" (protowire#410). The bytes are the
// spec corpus's (testdata/bigfloat dec-zero and dec-negative-zero): -0 is
// {negative: true} with no precision. Unmarshal used to read it as +0
// and at 53 bits, and Marshal wrote nothing for it, so a "-0" that came
// through PB lost its sign.
func TestBigFloatPrecZeroKeepsItsSign(t *testing.T) {
	for _, c := range []struct {
		name string
		wire []byte
		neg  bool
	}{
		{"zero", []byte{0x0a, 0x00}, false},
		{"negative zero", []byte{0x0a, 0x02, 0x20, 0x01}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var h bigFloatHolder
			if err := Unmarshal(c.wire, &h); err != nil {
				t.Fatal(err)
			}
			if h.F.Sign() != 0 || h.F.Prec() != 0 || h.F.Signbit() != c.neg {
				t.Fatalf("% x reads as %v at prec %d, signbit %v; want a zero at prec 0, signbit %v", c.wire, h.F, h.F.Prec(), h.F.Signbit(), c.neg)
			}
		})
	}
	neg := new(big.Float).Neg(new(big.Float))
	out, err := Marshal(&bigFloatHolder{F: neg})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x0a, 0x02, 0x20, 0x01}; !bytes.Equal(out, want) {
		t.Fatalf("prec-0 -0 marshals to % x, want % x", out, want)
	}
	var back bigFloatHolder
	if err := Unmarshal(out, &back); err != nil || !back.F.Signbit() || back.F.Prec() != 0 {
		t.Fatalf("round trip: %v at prec %d, signbit %v (%v); want -0 at prec 0", back.F, back.F.Prec(), back.F.Signbit(), err)
	}
	// +0 at prec 0 stays the omitted field it was.
	if out, err := Marshal(&bigFloatHolder{F: new(big.Float)}); err != nil || len(out) != 0 {
		t.Fatalf("prec-0 +0 marshals to % x (%v), want nothing", out, err)
	}
}

// TestBigFloatZeroHasOneEncoding: a zero at any prec is written with an
// empty mantissa and exponent 0, keeping prec and negative, which is
// what a PXF decoder stores for 0p64 (draft -01 § PXF Annotations;
// #196). The bytes are the spec corpus's dec-zero-p64 and
// dec-negative-zero-p64. Marshal used to write exponent -prec, a second
// encoding of the same value.
func TestBigFloatZeroHasOneEncoding(t *testing.T) {
	for _, c := range []struct {
		name string
		neg  bool
		want []byte
	}{
		{"zero", false, []byte{0x0a, 0x02, 0x18, 0x40}},
		{"negative zero", true, []byte{0x0a, 0x04, 0x18, 0x40, 0x20, 0x01}},
	} {
		t.Run(c.name, func(t *testing.T) {
			z := new(big.Float).SetPrec(64)
			if c.neg {
				z.Neg(z)
			}
			out, err := Marshal(&bigFloatHolder{F: z})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, c.want) {
				t.Fatalf("zero at prec 64 marshals to % x, want % x", out, c.want)
			}
			var back bigFloatHolder
			if err := Unmarshal(out, &back); err != nil {
				t.Fatal(err)
			}
			if back.F.Sign() != 0 || back.F.Prec() != 64 || back.F.Signbit() != c.neg {
				t.Fatalf("round trip: %v at prec %d, signbit %v; want a zero at prec 64, signbit %v", back.F, back.F.Prec(), back.F.Signbit(), c.neg)
			}
		})
	}
	// A zero mantissa with any exponent still reads as zero: bytes the
	// old encoder wrote stay readable.
	old := []byte{0x0a, 0x0f, 0x10, 0xc0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 0x18, 0x40, 0x20, 0x01}
	var h bigFloatHolder
	if err := Unmarshal(old, &h); err != nil {
		t.Fatal(err)
	}
	if h.F.Sign() != 0 || h.F.Prec() != 64 || !h.F.Signbit() {
		t.Fatalf("% x reads as %v at prec %d, signbit %v; want -0 at prec 64", old, h.F, h.F.Prec(), h.F.Signbit())
	}
}

// A zero writes no exponent, so its precision does not bound one: at prec
// 2^32-1, which Unmarshal reads, exponent -prec left int32 and Marshal
// refused the zero (#196).
func TestBigFloatZeroAtMaxPrec(t *testing.T) {
	for _, neg := range []bool{false, true} {
		z := new(big.Float).SetPrec(big.MaxPrec)
		if neg {
			z.Neg(z)
		}
		out, err := Marshal(&bigFloatHolder{F: z})
		if err != nil {
			t.Fatalf("-0 %v at prec %d: %v", neg, uint(big.MaxPrec), err)
		}
		var back bigFloatHolder
		if err := Unmarshal(out, &back); err != nil {
			t.Fatal(err)
		}
		if back.F.Sign() != 0 || back.F.Prec() != big.MaxPrec || back.F.Signbit() != neg {
			t.Fatalf("% x reads as %v at prec %d, signbit %v; want a zero at prec %d, signbit %v", out, back.F, back.F.Prec(), back.F.Signbit(), uint(big.MaxPrec), neg)
		}
	}
}
