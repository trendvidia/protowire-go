// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"math"
	"math/big"
	"testing"
)

// TestPow2Digits checks pow2Digits, which counts the decimal digits of
// 2^p without computing 2^p, against the count of the power itself: every
// p up to 2^15, which takes in the default limit's bound, p13603, with
// room. Past that the exact counts are pinned from an independent
// computation (log10(2) to 2000 bits, by series), at the three p below
// 2^32 where float64's p·log10(2) is one digit high, which is why
// pow2Digits does not use it.
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
		if got := pow2Digits(p); got != want {
			t.Fatalf("pow2Digits(%d) = %d, want %d", p, got, want)
		}
	}
	for _, c := range []struct {
		p    uint32
		want int
	}{{146964308, 44240665}, {345060773, 103873643}, {1923400330, 579001193}} {
		if got := pow2Digits(c.p); got != c.want {
			t.Errorf("pow2Digits(%d) = %d, want %d", c.p, got, c.want)
		}
		if f := int(float64(c.p)*math.Log10(2)) + 1; f == c.want {
			t.Errorf("p %d: float64 gives %d too; the doc's reason for fixed point no longer holds here", c.p, f)
		}
	}
	// The draft's form of the bound: N exceeds L exactly when prec
	// exceeds floor((L-1)/log10(2)), 13603 at the default.
	if _, ok := bigFloatDigits(13603, MaxNumericLiteralDigits); !ok {
		t.Error("p13603 is within the default limit")
	}
	if _, ok := bigFloatDigits(13604, MaxNumericLiteralDigits); ok {
		t.Error("p13604 is past the default limit")
	}
	if n, ok := bigFloatDigits(math.MaxUint32, MaxNumericLiteralDigits); ok || n != 1292913988 {
		t.Errorf("bigFloatDigits(MaxUint32) = %d, %v; want 1292913988, false", n, ok)
	}
}

// TestLexPrecisionStreamingCut: a lexer that stops on the last byte of a
// buffer either has the whole token or says more input could change it
// (sawEnd), which is what DatasetReader trusts. A number's precision
// suffix is read from the byte after its 'p', so a buffer ending on the
// 'p' is one such place.
func TestLexPrecisionStreamingCut(t *testing.T) {
	for _, in := range []string{"1p53", "2.5p8", "1e5p9", "-3p2"} {
		fl := newLexer([]byte(in))
		full := fl.Next()
		if fl.prec == "" {
			t.Fatalf("%q lexes without its suffix", in)
		}
		for k := 1; k < len(in); k++ {
			l := newLexer([]byte(in[:k]))
			tok := l.Next()
			if !l.sawEnd && (tok.Kind != full.Kind || tok.Value != full.Value || l.prec != fl.prec) {
				t.Errorf("%q cut to %q: token %s %q p%q, sawEnd false; the whole input lexes %s %q p%q", in, in[:k], tok.Kind, tok.Value, l.prec, full.Kind, full.Value, fl.prec)
			}
		}
	}
}
