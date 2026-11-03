// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"testing"
)

// pow2Digits and its test moved to internal/bigprec (#244); the suffix's
// bound at the default limit is still pinned here.
func TestBigFloatDigitsAtDefaultLimit(t *testing.T) {
	if _, ok := bigFloatDigits(13603, MaxNumericLiteralDigits); !ok {
		t.Error("p13603 is within the default limit")
	}
	if _, ok := bigFloatDigits(13604, MaxNumericLiteralDigits); ok {
		t.Error("p13604 is past the default limit")
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
