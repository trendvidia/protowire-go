// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

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
			err := promptly(t, 2*time.Second, func() error {
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
		{"1e646456992", ""}, // the largest decade big.Float holds
		{"0e999999999", ""}, // zero is zero at any exponent
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
