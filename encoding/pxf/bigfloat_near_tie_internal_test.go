// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// A BigFloat literal is rounded from its exact value to prec bits, once,
// ties to even (draft -01 § PXF Annotations). big.Float.Parse scales a
// decimal exponent at a working precision and rounds again, so a value
// within a hair of a tie could round the wrong way (#260).

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The issue's literal: 2.5 - 1.7e-68 times 2^-16383, which at 2 bits
// rounds down to 2 times 2^-16383. Parse's answer was 3.
func TestBigFloatLiteral_NearTieReadsExactly(t *testing.T) {
	bf, err := parseBigFloatPrec("4.2026289288901168828283472716521907532475991810580890501360340372609e-4932", "2", MaxNumericLiteralDigits, MaxBigFloatExponent)
	require.NoError(t, err)
	want := new(big.Float).SetMantExp(big.NewFloat(2), -16383)
	require.Zero(t, bf.Cmp(want), "read %s, want %s", bf.Text('p', 0), want.Text('p', 0))
	require.Equal(t, uint(2), bf.Prec())
}

// A differential over literals on either side of a tie. The expected value
// does not come from rational arithmetic: each case is built around a tie
// t = (2a+1) * 2^(e-1) between lo = a * 2^e and hi = (a+1) * 2^e, which
// hold p bits. A literal of t cut to D significant digits is below t, so
// it rounds to lo; the same digits with the last one raised are above t,
// so they round to hi.
func TestBigFloatLiteral_NearTieDifferential(t *testing.T) {
	rng := rand.New(rand.NewPCG(260, 1))
	const cases = 3000
	ran := 0
	for i := range cases {
		p := 2 + rng.IntN(63)
		a := new(big.Int).SetUint64(rng.Uint64() >> (64 - p))
		a.SetBit(a, p-1, 1) // exactly p bits
		e := -16380 + rng.IntN(2*16380-p)
		digits := 40 + rng.IntN(81)

		below, above, ok := tieLiterals(a, e-1, digits)
		if !ok {
			continue // the tie has no more than D digits; not a near-tie
		}
		lo := new(big.Float).SetMantExp(new(big.Float).SetInt(a), e)
		hi := new(big.Float).SetMantExp(new(big.Float).SetInt(new(big.Int).Add(a, big.NewInt(1))), e)
		ran++
		prec := fmt.Sprint(p)
		for _, c := range []struct {
			lit  string
			want *big.Float
		}{{below, lo}, {above, hi}} {
			got, err := parseBigFloatPrec(c.lit, prec, MaxNumericLiteralDigits, MaxBigFloatExponent)
			require.NoError(t, err, "case %d: %sp%s", i, c.lit, prec)
			require.Zero(t, got.Cmp(c.want), "case %d: %sp%s read %s, want %s", i, c.lit, prec, got.Text('p', 0), c.want.Text('p', 0))
		}
	}
	// Nearly every tie has more significant digits than D; a test that
	// skipped them all would prove nothing.
	require.Greater(t, ran, cases*9/10, "near-tie cases run")
	t.Logf("%d near-tie cases, %d literals", ran, 2*ran)
}

// tieLiterals writes (2a+1) * 2^k cut to digits significant digits, and
// the same digits with the last raised by one. ok is false when the value
// has no more than digits significant digits, since the cut is then not
// below it.
func tieLiterals(a *big.Int, k, digits int) (below, above string, ok bool) {
	m := new(big.Int).Lsh(a, 1)
	m.SetBit(m, 0, 1) // 2a+1
	exp10 := 0
	if k >= 0 {
		m.Lsh(m, uint(k))
	} else {
		m.Mul(m, new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(-k)), nil))
		exp10 = k
	}
	s := m.String()
	if len(s) <= digits || strings.Trim(s[digits:], "0") == "" {
		return "", "", false
	}
	cut, _ := new(big.Int).SetString(s[:digits], 10)
	up := new(big.Int).Add(cut, big.NewInt(1))
	// Both are integers scaled by 10^(exp10 + len(s) - digits).
	scale := exp10 + len(s) - digits
	return fmt.Sprintf("%se%d", cut, scale), fmt.Sprintf("%se%d", up, scale), true
}

// BenchmarkParseBigFloatLiteral reads literals of three sizes: a short
// one, a default-precision one, and the largest the default limits admit
// at the exponent bound, where the exact rational is widest.
func BenchmarkParseBigFloatLiteral(b *testing.B) {
	worst := "0." + strings.Repeat("9", MaxNumericLiteralDigits-1) + "e-4931"
	for _, c := range []struct{ name, lit, prec string }{
		{"short", "3.14159265358979323846", "64"},
		{"default", "1.2345678901234567890123456789012345678901234567890123456789012345678901234567e300", ""},
		{"bound", worst, "256"},
	} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := parseBigFloatPrec(c.lit, c.prec, MaxNumericLiteralDigits, MaxBigFloatExponent); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
