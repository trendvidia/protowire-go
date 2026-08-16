// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lexAll drains the lexer and returns every token before EOF.
func lexAll(t *testing.T, input string) []Token {
	t.Helper()
	l := newLexer([]byte(input))
	var toks []Token
	for i := 0; i < 64; i++ {
		tok := l.Next()
		if tok.Kind == EOF {
			return toks
		}
		toks = append(toks, tok)
	}
	t.Fatalf("lexer did not reach EOF within 64 tokens on %q", input)
	return nil
}

// TestLexDurationFractionalAndMicro pins the tokenisation of duration
// literals against draft-01 §3.3:
//
//	duration-segment = 1*DIGIT [ "." 1*DIGIT ] time-unit
//	time-unit        = "ns" / "us" / micro-us / "ms" / "s" / "m" / "h"
//	micro-us         = %xC2.B5 %x73    ; UTF-8 of "µs"
//
// Before #75 the lexer decided FLOAT on seeing "." before it looked for a
// unit, so "1.5ms" came out as FLOAT "1.5" + IDENT "ms", and it never
// admitted the two-byte "µ", so "2µs" was INT "2" + ILLEGAL. Both are what
// pxf.Marshal writes for any Duration that is not a whole multiple of its
// largest unit (time.Duration.String()), so the reference codec could not
// read its own output.
func TestLexDurationFractionalAndMicro(t *testing.T) {
	type tok struct {
		kind  TokenKind
		value string
	}
	cases := []struct {
		input string
		want  []tok
	}{
		// §3.10 examples.
		{"30s", []tok{{DURATION, "30s"}}},
		{"1h30m", []tok{{DURATION, "1h30m"}}},
		{"500ms", []tok{{DURATION, "500ms"}}},
		{"1.5h", []tok{{DURATION, "1.5h"}}},
		{"2µs", []tok{{DURATION, "2µs"}}},
		{"2us", []tok{{DURATION, "2us"}}},

		// What time.Duration.String() emits for measured values.
		{"1.234567ms", []tok{{DURATION, "1.234567ms"}}},
		{"1.5ms", []tok{{DURATION, "1.5ms"}}},
		{"312.5µs", []tok{{DURATION, "312.5µs"}}},
		{"1.234µs", []tok{{DURATION, "1.234µs"}}},
		{"1h30m0.5s", []tok{{DURATION, "1h30m0.5s"}}},
		{"-1.5s", []tok{{DURATION, "-1.5s"}}},
		{"-312.5µs", []tok{{DURATION, "-312.5µs"}}},
		{"0s", []tok{{DURATION, "0s"}}},

		// Every unit, fractional.
		{"1.5ns", []tok{{DURATION, "1.5ns"}}},
		{"1.5us", []tok{{DURATION, "1.5us"}}},
		{"1.5µs", []tok{{DURATION, "1.5µs"}}},
		{"1.5s", []tok{{DURATION, "1.5s"}}},
		{"1.5m", []tok{{DURATION, "1.5m"}}},

		// A fraction in any segment, not only the first (§3.3 puts the
		// optional fraction inside duration-segment).
		{"1h30.5m", []tok{{DURATION, "1h30.5m"}}},
		{"1.5h30.5m1.5s", []tok{{DURATION, "1.5h30.5m1.5s"}}},

		// Unchanged forms.
		{"1h30m500ms", []tok{{DURATION, "1h30m500ms"}}},
		{"1ms234us567ns", []tok{{DURATION, "1ms234us567ns"}}},
		{"250ms", []tok{{DURATION, "250ms"}}},

		// Negatives: still a float when no unit follows...
		{"1.5", []tok{{FLOAT, "1.5"}}},
		{"1.5e3", []tok{{FLOAT, "1.5e3"}}},
		{"1.5E-3", []tok{{FLOAT, "1.5E-3"}}},
		{"-1.5", []tok{{FLOAT, "-1.5"}}},
		{"1.", []tok{{FLOAT, "1."}}},
		{"1.e3", []tok{{FLOAT, "1.e3"}}},
		// ...an exponent is not a unit, so "1.5e3ms" is a float and an
		// identifier, exactly as before...
		{"1.5e3ms", []tok{{FLOAT, "1.5e3"}, {IDENT, "ms"}}},
		{"1e3s", []tok{{FLOAT, "1e3"}, {IDENT, "s"}}},
		// ...a fraction with no digits after the "." is not a
		// duration-segment, so the float branch keeps it...
		{"1.ms", []tok{{FLOAT, "1."}, {IDENT, "ms"}}},
		// ...and a non-unit letter is an identifier following the number.
		{"1.5x", []tok{{FLOAT, "1.5"}, {IDENT, "x"}}},
		{"1.5 x", []tok{{FLOAT, "1.5"}, {IDENT, "x"}}},
		{"5x", []tok{{INT, "5"}, {IDENT, "x"}}},

		// A unit letter that starts a longer word is consumed into the
		// duration attempt and rejected there (unchanged: "5min" was
		// already ILLEGAL); the fraction does not change that.
		{"1.5min", []tok{{ILLEGAL, "invalid duration: 1.5min"}}},
		{"5min", []tok{{ILLEGAL, "invalid duration: 5min"}}},

		// Only U+00B5 MICRO SIGN (C2 B5) is micro-us. U+03BC GREEK SMALL
		// LETTER MU (CE BC) is not in the grammar even though Go's
		// time.ParseDuration would accept it, and must not sneak in via
		// the lexer.
		// (ILLEGAL renders each stray byte via string(byte), hence the
		// Latin-1 spelling of CE BC.)
		{"2μs", []tok{{INT, "2"}, {ILLEGAL, string(rune(0xCE))}, {ILLEGAL, string(rune(0xBC))}, {IDENT, "s"}}},
		// A bare micro sign with no "s" is not a unit either.
		{"2µ", []tok{{ILLEGAL, "invalid duration: 2µ"}}},
		{"2µm", []tok{{ILLEGAL, "invalid duration: 2µm"}}},

		// The token ends where the value ends.
		{"1.5ms,", []tok{{DURATION, "1.5ms"}, {COMMA, ","}}},
		{"1.5ms]", []tok{{DURATION, "1.5ms"}, {RBRACKET, "]"}}},
		{"1.5ms}", []tok{{DURATION, "1.5ms"}, {RBRACE, "}"}}},
		{"1.5ms#c", []tok{{DURATION, "1.5ms"}, {COMMENT, "#c"}}},
		{"1.5ms\n", []tok{{DURATION, "1.5ms"}, {NEWLINE, ""}}},
		{"2µs 3", []tok{{DURATION, "2µs"}, {INT, "3"}}},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := lexAll(t, tc.input)
			require.Len(t, got, len(tc.want), "tokens: %+v", got)
			for i, w := range tc.want {
				assert.Equal(t, w.kind, got[i].Kind, "token %d kind (%+v)", i, got[i])
				assert.Equal(t, w.value, got[i].Value, "token %d value", i)
			}
		})
	}
}

// TestLexDurationTolerantMode: the tolerant lexer shares the number
// path, so the fix must hold there too (ParseTolerant is what editors
// see).
func TestLexDurationTolerantMode(t *testing.T) {
	l := newLexer([]byte("1.234567ms"))
	l.tolerant = true
	l.onErr = func(pos Position, msg string) { t.Fatalf("unexpected lexical error at %v: %s", pos, msg) }
	tok := l.Next()
	assert.Equal(t, DURATION, tok.Kind)
	assert.Equal(t, "1.234567ms", tok.Value)
	assert.Equal(t, EOF, l.Next().Kind)
}
