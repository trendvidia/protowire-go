// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// A @default carrier's BigFloat is bounded after rounding to its prec, as
// Marshal's is (#261): 2^54-1 at exponent 16330 is at the bound as
// stored, and rounds to 2^54, binary exponent 16385, at prec 3.
func TestBigFloatCarrier_BoundAfterRounding(t *testing.T) {
	carrier := func(mantissa []byte) []byte {
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, mantissa)
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(int64(16330)))
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		b = protowire.AppendVarint(b, 3)
		return b
	}

	lit, msg := bigFloatLiteral(carrier([]byte{0x3f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))
	assert.Empty(t, lit)
	assert.Equal(t, "@default's big_float_value has binary exponent 16385 after rounding to prec 3, and MaxBigFloatExponent=16384 bounds its magnitude", msg)

	// Rounding that does not carry stays within the bound, and the literal
	// reads back.
	lit, msg = bigFloatLiteral(carrier([]byte{0x28, 0, 0, 0, 0, 0, 0}))
	require.Empty(t, msg)
	_, err := parseBigFloat(lit, MaxNumericLiteralDigits, MaxBigFloatExponent)
	require.NoError(t, err, lit)
}
