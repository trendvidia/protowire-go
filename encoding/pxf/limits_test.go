// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// inputBudget is the time a decode of doc may take when its magnitude
// bound works: 2s plus five times what Parse takes to lex the same bytes.
// A bound applied before the work leaves the decode costing about what
// lexing costs; a regression costs the big-number conversion (measured
// 2026-10-23: 7.95s for 4,000,000 digits, 8.77s under -race, against a
// lex of well under a second either way). Deriving the budget from the
// lex keeps a slow machine, a loaded one or the race detector, which
// slow both alike, from failing the test on time alone (#154).
func inputBudget(t *testing.T, doc string) time.Duration {
	t.Helper()
	start := time.Now()
	_, _ = pxf.Parse([]byte(doc))
	return 2*time.Second + 5*time.Since(start)
}

// promptly runs f and fails the test if it has not returned within the
// budget: a regression in a magnitude bound is work proportional to the
// hostile value, which would hang the suite rather than fail it.
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

// TestNumericLiteralDigitCap: MaxNumericLiteralDigits counts digits, on
// each of the three arbitrary-precision targets, and is checked before
// the big-number parser sees the literal — the four-million-digit case
// cost seconds in big.Int.SetString and now costs one length comparison.
func TestNumericLiteralDigitCap(t *testing.T) {
	md := bigNumDesc(t, "BigNumDemo")
	sevens := func(n int) string { return strings.Repeat("7", n) }
	const max = pxf.MaxNumericLiteralDigits
	cases := []struct {
		name, field, lit string
		wantErr          bool
	}{
		{"big int at the cap", "big_int_field", sevens(max), false},
		{"big int one past", "big_int_field", sevens(max + 1), true},
		{"negative big int one past", "big_int_field", "-" + sevens(max+1), true},
		{"decimal at the cap", "decimal_field", "0." + sevens(max-1), false},
		{"decimal one past", "decimal_field", "0." + sevens(max), true},
		{"big float at the cap", "big_float_field", sevens(max-1) + ".5", false},
		{"big float one past", "big_float_field", sevens(max) + ".5", true},
		{"big float with exponent one past", "big_float_field", sevens(max) + ".5e3", true},
		{"four million digits", "big_int_field", sevens(4_000_000), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.field + " = " + tc.lit
			var msg protoreflect.Message
			err := promptly(t, inputBudget(t, doc), func() error {
				m, err := pxf.UnmarshalDescriptor([]byte(doc), md)
				if m != nil {
					msg = m
				}
				return err
			})
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "MaxNumericLiteralDigits", "the error must name the limit")
				return
			}
			require.NoError(t, err)
			if tc.field == "big_int_field" {
				want, _ := new(big.Int).SetString(tc.lit, 10)
				require.Equal(t, 0, want.Cmp(readBigIntFromMsg(msg, tc.field)), "a literal at the cap decodes exactly")
			}
		})
	}
}

// TestBracketDefaultIsUnderTheDigitCap: a (pxf.default) string never
// passes through the lexer, so the cap has to live where the literal is
// parsed, not where it is tokenised.
func TestBracketDefaultIsUnderTheDigitCap(t *testing.T) {
	src := `syntax = "proto3"; package lim.v1;
import "pxf/bignum.proto"; import "pxf/annotations.proto";
message D { pxf.BigInt x = 1 [(pxf.default) = "` + strings.Repeat("7", pxf.MaxNumericLiteralDigits+1) + `"]; }`
	sources := map[string]string{
		"lim.proto":             src,
		"pxf/bignum.proto":      bignumProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{Resolver: protocompile.WithStandardImports(
		&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)})}
	files, err := comp.Compile(context.Background(), "lim.proto")
	require.NoError(t, err)
	var md protoreflect.MessageDescriptor
	for _, f := range files {
		if f.Path() == "lim.proto" {
			md = f.Messages().ByName("D")
		}
	}
	require.NotNil(t, md)
	err = promptly(t, 2*time.Second, func() error {
		_, _, err := pxf.UnmarshalFullDescriptor([]byte(""), md)
		return err
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "MaxNumericLiteralDigits")
}

// TestBigFloatLiteralOutsideTheRange pins protowire#278's third rule on
// the text path: big.Float.Parse substitutes +Inf above its range and 0
// below it, and the decoder used to take both — the first of them into
// a nil dereference. Each is an error now, and each returns promptly.
func TestBigFloatLiteralOutsideTheRange(t *testing.T) {
	md := bigNumDesc(t, "BigNumDemo")
	cases := []struct {
		lit     string
		wantErr string // "" for accept
	}{
		{"1e400", ""},
		// MaxBigFloatExponent bounds the value's binary exponent
		// (protowire#281): 2^±16384 is about 1e±4932.
		{"1e4932", ""},
		{"1e-4932", ""},
		{"1e4933", "MaxBigFloatExponent"},
		{"1e-4933", "MaxBigFloatExponent"},
		{"1e646456992", "MaxBigFloatExponent"}, // #281's thirteen-byte document
		{"0e999999999", ""},                    // zero is zero at any exponent
		{"1e999999999", "above"},
		{"1e2000000000", "above"},
		{"-1e999999999", "above"},
		{"1e-2000000000", "below big.Float"},
		{"1e-999999999", "below big.Float"},
		{"1e-646456992", "below the wire"}, // parses, but exp - prec leaves int32
	}
	for _, tc := range cases {
		t.Run(tc.lit, func(t *testing.T) {
			err := promptly(t, 2*time.Second, func() error {
				_, err := pxf.UnmarshalDescriptor([]byte("big_float_field = "+tc.lit), md)
				return err
			})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestMarshalRefusesBigFloatPastExponentBound: rendering a BigFloat in
// decimal costs time that grows with the square of |exponent| (measured,
// past 10 s at -2^20), so Marshal refuses an exponent past
// MaxBigFloatExponent before rendering anything (protowire#281). Before,
// -2^20 did not return, and 2^31-1 was written as +Inf, a substituted
// value draft -01 § Mandatory Limits forbids.
func TestMarshalRefusesBigFloatPastExponentBound(t *testing.T) {
	md := bigNumDesc(t, "BigNumDemo")
	field := md.Fields().ByName("big_float_field")
	// A one-bit mantissa: the value's binary exponent is exp + 1.
	for _, exp := range []int32{math.MinInt32, -1 << 20, -pxf.MaxBigFloatExponent - 2, pxf.MaxBigFloatExponent, math.MaxInt32} {
		t.Run(fmt.Sprint(exp), func(t *testing.T) {
			msg := dynamicpb.NewMessage(md)
			setBigFloat(msg.Mutable(field).Message(), big.NewInt(1), exp, 64)
			err := promptly(t, 2*time.Second, func() error { _, err := pxf.Marshal(msg); return err })
			require.ErrorContains(t, err, "MaxBigFloatExponent=16384")
		})
	}
	// At the bound on both sides, what the encoder writes the decoder
	// takes back, although it re-reads the literal at 256 bits. 3 × 2^exp
	// is 0.75 × 2^(exp+2): binary exponent ±16384, and its decimal literal
	// is nowhere near the next power of two. (A value within one rounding
	// of 2^±16384 itself can print as a literal on the far side of it,
	// which the decoder rejects: a decimal literal cannot hold a power of
	// two exactly.)
	for _, exp := range []int32{-pxf.MaxBigFloatExponent - 2, pxf.MaxBigFloatExponent - 2} {
		t.Run(fmt.Sprint(exp), func(t *testing.T) {
			msg := dynamicpb.NewMessage(md)
			setBigFloat(msg.Mutable(field).Message(), big.NewInt(3), exp, 64)
			out, err := pxf.Marshal(msg)
			require.NoError(t, err)
			_, err = pxf.UnmarshalDescriptor(out, md)
			require.NoError(t, err, "the decoder takes what the encoder wrote at the bound: %s", out)
		})
	}
}
