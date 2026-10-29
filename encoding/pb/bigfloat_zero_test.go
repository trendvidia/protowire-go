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
