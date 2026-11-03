// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package bigprec

import (
	"math"
	"math/big"
	"testing"
)

// TestPow2Digits checks Pow2Digits, which counts the decimal digits of
// 2^p without computing 2^p, against the count of the power itself: every
// p up to 2^15, which takes in the default limit's bound, p13603, with
// room. Past that the exact counts are pinned from an independent
// computation (log10(2) to 2000 bits, by series), at the three p below
// 2^32 where float64's p·log10(2) is one digit high, which is why
// Pow2Digits does not use it.
func TestPow2Digits(t *testing.T) {
	pow := big.NewInt(1)   // 2^p
	next := big.NewInt(10) // the smallest power of ten above pow
	want := 1
	ten := big.NewInt(10)
	for p := uint32(0); p <= 1<<15; p++ {
		if p > 0 {
			pow.Lsh(pow, 1)
			if pow.Cmp(next) >= 0 {
				want++
				next.Mul(next, ten)
			}
		}
		if got := Pow2Digits(p); got != want {
			t.Fatalf("Pow2Digits(%d) = %d, want %d", p, got, want)
		}
	}
	for _, c := range []struct {
		p    uint32
		want int
	}{{146964308, 44240665}, {345060773, 103873643}, {1923400330, 579001193}} {
		if got := Pow2Digits(c.p); got != c.want {
			t.Errorf("Pow2Digits(%d) = %d, want %d", c.p, got, c.want)
		}
		if f := int(float64(c.p)*math.Log10(2)) + 1; f == c.want {
			t.Errorf("p %d: float64 gives %d too; the doc's reason for fixed point no longer holds here", c.p, f)
		}
	}
	// The draft's form of the bound: N exceeds L exactly when prec
	// exceeds floor((L-1)/log10(2)), 13603 at the default of 4096.
	if _, ok := Digits(13603, 4096); !ok {
		t.Error("p13603 is within the default limit")
	}
	if _, ok := Digits(13604, 4096); ok {
		t.Error("p13604 is past the default limit")
	}
	if n, ok := Digits(math.MaxUint32, 4096); ok || n != 1292913988 {
		t.Errorf("Digits(MaxUint32) = %d, %v; want 1292913988, false", n, ok)
	}
}
