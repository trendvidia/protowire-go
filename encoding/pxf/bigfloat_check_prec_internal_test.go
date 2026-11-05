// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// parseBigFloatPrec asks big.Float.Parse for no more than bigFloatCheckPrec
// bits, because Parse's cost grows with the square of the precision (#274).

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/internal/bigprec"
)

// The widest precision the default MaxNumericLiteralDigits admits is read
// exactly as before: Parse runs at it, uncapped.
func TestBigFloatCheckPrecCoversDefaultLimits(t *testing.T) {
	widest := uint32(0)
	for p := uint32(1); p < 1<<20; p++ {
		if _, ok := bigprec.Digits(p, MaxNumericLiteralDigits); ok {
			widest = p
		}
	}
	require.Equal(t, uint32(13603), widest)
	require.LessOrEqual(t, widest, uint32(bigFloatCheckPrec))
}

// Literals that took minutes when Parse ran at the suffix's precision. Each
// gets the answer it got then, in well under a second.
func TestBigFloatWidePrecisionIsQuick(t *testing.T) {
	const digits = 1_300_000_000 // admits p4294967295
	cases := []struct{ lit, prec, err string }{
		{"1e-300", "2147483647", "big float literal 1e-300 is below the wire's exponent range"},
		{"1e646456993", "2147483647", "big float literal 1e646456993 is above big.Float's range"},
		{"1e646456993", "4294967295", "big float literal 1e646456993 is above big.Float's range"},
		{"1e-646456993", "2147483647", "big float literal 1e-646456993 is below the wire's exponent range"},
		{"1e-646457000", "2147483647", "big float literal 1e-646457000 is below big.Float's range"},
		{"1.5", "4294967295", "big float literal 1.5: precision p4294967295 exceeds the widest mantissa big.Float can hold, 2147483647 bits"},
		{"1e5000", "2147483647", "big float literal 1e5000 has binary exponent 16610, and MaxBigFloatExponent=16384 bounds its magnitude"},
	}
	for _, c := range cases {
		t.Run(c.lit+"p"+c.prec, func(t *testing.T) {
			start := time.Now()
			_, err := parseBigFloatPrec(c.lit, c.prec, digits, MaxBigFloatExponent)
			require.EqualError(t, err, c.err)
			require.Less(t, time.Since(start), 5*time.Second)
		})
	}
}

// A literal a wide precision accepts is rounded exactly at that precision,
// not at the one Parse ran at, and a zero keeps its suffix.
func TestBigFloatWidePrecisionValue(t *testing.T) {
	const prec = 50_000_000
	digits, _ := bigprec.Digits(prec, math.MaxInt)
	start := time.Now()
	got, err := parseBigFloatPrec("0.1", fmt.Sprint(prec), digits, MaxBigFloatExponent)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
	want := new(big.Float).SetPrec(prec).SetMode(big.ToNearestEven).Quo(big.NewFloat(1), big.NewFloat(10))
	require.Equal(t, uint(prec), got.Prec())
	require.Zero(t, got.Cmp(want))

	zero, err := parseBigFloatPrec("0.0", fmt.Sprint(prec), digits, MaxBigFloatExponent)
	require.NoError(t, err)
	require.Equal(t, uint(prec), zero.Prec())
	require.Equal(t, 0, zero.Sign())
}

// Just above bigFloatCheckPrec, where Parse at the full precision is still
// quick, the capped read gives what the uncapped one gave: the same value,
// or the same error text.
func TestBigFloatCheckPrecAgreesAboveCap(t *testing.T) {
	lits := []string{
		"0", "-0.0", "1", "0.1", "-3.14159", "1e300", "1e-300", "123456789e-4950", "1e4931", "1e4932", "-1e4933",
		"1e-4932", "1e-4933", "8.5e-4933", "1e646456993", "1e-646456993", "2.5e646456992", "1.18973149535723176502e+4932",
		"1.18973149535723176508575932662800702e4932", "3.3621031431120935063e-4932",
	}
	const digits = 100_000
	refused, accepted := 0, 0
	for _, prec := range []string{"16385", "20000", "33333"} {
		for _, lit := range lits {
			got, gotErr := parseBigFloatPrec(lit, prec, digits, MaxBigFloatExponent)
			want, wantErr := parseBigFloatPrecUncapped(lit, prec, digits, MaxBigFloatExponent)
			if wantErr != nil {
				require.EqualError(t, gotErr, wantErr.Error(), "%sp%s", lit, prec)
				refused++
				continue
			}
			require.NoError(t, gotErr, "%sp%s", lit, prec)
			require.Equal(t, want.Prec(), got.Prec(), "%sp%s", lit, prec)
			require.Equal(t, want.Signbit(), got.Signbit(), "%sp%s", lit, prec)
			require.Zero(t, want.Cmp(got), "%sp%s", lit, prec)
			accepted++
		}
	}
	t.Logf("%d refused, %d accepted", refused, accepted)
	require.Positive(t, refused)
	require.Positive(t, accepted)
}

// parseBigFloatPrecUncapped is parseBigFloatPrec as it was before #274,
// with Parse at the suffix's precision, kept here as the reference.
func parseBigFloatPrecUncapped(s, prec string, maxDigits, maxExp int) (*big.Float, error) {
	readPrec := uint(bigFloatDefaultPrec)
	suffixed := prec != ""
	if suffixed {
		p, err := uint64(0), error(nil)
		if len(prec) <= len("4294967295") {
			p, err = strconv.ParseUint(prec, 10, 32)
		}
		if p == 0 || err != nil {
			return nil, fmt.Errorf("big float literal %sp%s: precision suffix p%s is not a positive bit count", clipLiteral(s), clipLiteral(prec), clipLiteral(prec))
		}
		if n, ok := bigFloatDigits(uint32(p), maxDigits); !ok {
			return nil, fmt.Errorf("big float literal %sp%d: precision p%d renders to %d digits; MaxNumericLiteralDigits=%d", clipLiteral(s), p, p, n, maxDigits)
		}
		readPrec = uint(p)
	}
	if err := checkLiteralDigits(s, maxDigits); err != nil {
		return nil, err
	}
	bf, _, err := new(big.Float).SetPrec(readPrec).SetMode(big.ToNearestEven).Parse(s, 10)
	if err != nil {
		return nil, fmt.Errorf("invalid big float: %s", s)
	}
	// big.Float.Parse substitutes ±Inf for a value above its range and
	// ±0 for one below it, and reports neither. A finite, non-zero
	// literal that came back as either is a value pxf.BigFloat cannot
	// carry, which draft -01 §mandatory-limits makes an error rather
	// than a substitution (protowire#278). The overflow used to reach
	// setBigFloatFields, whose mant.Int returned nil for +Inf — a panic
	// on a sixteen-byte document.
	if bf.IsInf() {
		return nil, fmt.Errorf("big float literal %s is above big.Float's range", clipLiteral(s))
	}
	if bf.Sign() == 0 && hasNonZeroMantissaDigit(s) {
		return nil, fmt.Errorf("big float literal %s is below big.Float's range", clipLiteral(s))
	}
	// setBigFloatFields scales a value up to prec bits, which leaves
	// big.Float's range at 2^31 bits or more: its mant.Int then returns nil
	// (#229). Only a MaxNumericLiteralDigits raised past 646 million admits
	// a suffix that wide. A zero has no mantissa to scale.
	if bf.Sign() != 0 && readPrec > math.MaxInt32 {
		return nil, fmt.Errorf("big float literal %s: precision p%d exceeds the widest mantissa big.Float can hold, %d bits", clipLiteral(s), readPrec, math.MaxInt32)
	}
	// The wire carries exp - prec as an int32 (pxf/bignum.proto). A value
	// near big.Float's floor parses but cannot be encoded, and the int32
	// subtraction in setBigFloatFields wrapped silently. A zero is stored
	// with exponent 0 at any precision.
	if bf.Sign() != 0 && int64(bf.MantExp(nil))-int64(readPrec) < math.MinInt32 {
		return nil, fmt.Errorf("big float literal %s is below the wire's exponent range", clipLiteral(s))
	}
	// The value's binary exponent is bounded as one read off the wire is
	// (MaxBigFloatExponent, protowire#281). Zero has none.
	if e := bf.MantExp(nil); bf.Sign() != 0 && bigFloatExponentExceeds(int64(e), maxExp) {
		return nil, fmt.Errorf("big float literal %s has binary exponent %d, and MaxBigFloatExponent=%d bounds its magnitude", clipLiteral(s), e, maxExp)
	}
	// big.Float.Parse scales a decimal exponent by powers of ten at a
	// working precision and then rounds again, so a value within a hair of
	// a tie can round the wrong way (#260). The draft rounds the literal's
	// exact value once, ties to even. The checks above bound the decimal
	// exponent and the digits, so the exact rational is of bounded size.
	// Rounding it can carry the exponent past the bound, so it is asked
	// again.
	if bf.Sign() != 0 {
		exact, ok := roundDecimalLiteral(s, readPrec)
		if !ok {
			return nil, fmt.Errorf("invalid big float: %s", s)
		}
		bf = exact
		e := bf.MantExp(nil)
		if bigFloatExponentExceeds(int64(e), maxExp) {
			return nil, fmt.Errorf("big float literal %s has binary exponent %d, and MaxBigFloatExponent=%d bounds its magnitude", clipLiteral(s), e, maxExp)
		}
		if int64(e)-int64(readPrec) < math.MinInt32 {
			return nil, fmt.Errorf("big float literal %s is below the wire's exponent range", clipLiteral(s))
		}
	}
	// A zero written without a suffix has no precision to keep: prec 0,
	// its sign kept (protowire#410).
	if bf.Sign() == 0 && !suffixed {
		z := new(big.Float)
		if bf.Signbit() {
			z.Neg(z)
		}
		bf = z
	}
	return bf, nil
}
