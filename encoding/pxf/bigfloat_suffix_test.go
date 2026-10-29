// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// The edges of the precision suffix (protowire#406, #410) that the
// corpus, one singular field at the default limits, does not reach.

const suffixSchemaSrc = `
syntax = "proto3";
package bfs.v1;
import "pxf/bignum.proto";
message H {
  pxf.BigFloat bf = 1;
  int32 count = 2;
  repeated pxf.BigFloat l = 3;
  map<string, pxf.BigFloat> m = 4;
  map<int32, string> by_id = 5;
}
`

// A map key is an integer without a suffix (map-key = identifier /
// string / integer / bool). Parse used to accept "1p5:" and keep the key
// as "1", so formatting the document wrote a key the decoder had never
// seen, where the decoder rejects the original.
func TestPrecisionSuffixOnMapKeyIsRejectedByParse(t *testing.T) {
	const in = "by_id = {\n  1p5: \"x\"\n}\n"
	_, err := pxf.Parse([]byte(in))
	require.ErrorContains(t, err, "a key cannot carry a precision suffix (p5)")

	doc, errs := pxf.ParseTolerant([]byte(in))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "precision suffix")
	require.NotNil(t, doc)

	md := compilePlacementSrc(t, suffixSchemaSrc, "bfs.v1", "H")
	_, err = pxf.UnmarshalDescriptor([]byte(in), md)
	require.ErrorContains(t, err, "a key cannot carry a precision suffix (p5)")
}

// The suffix's bound follows the decoder's MaxNumericLiteralDigits, not
// the package default: at a limit of 10, N exceeds it exactly when prec
// exceeds floor(9 / log10(2)) = 29, in a field, a list element and a map
// value alike.
func TestPrecisionSuffixBoundFollowsTheLimit(t *testing.T) {
	md := compilePlacementSrc(t, suffixSchemaSrc, "bfs.v1", "H")
	opts := pxf.UnmarshalOptions{MaxNumericLiteralDigits: 10}
	for _, form := range []string{"bf = %s\n", "l = [%s]\n", "m = { k: %s }\n"} {
		at := strings.Replace(form, "%s", "1p29", 1)
		require.NoError(t, opts.Unmarshal([]byte(at), dynamicpb.NewMessage(md)), at)
		past := strings.Replace(form, "%s", "1p30", 1)
		require.ErrorContains(t, opts.Unmarshal([]byte(past), dynamicpb.NewMessage(md)), "precision p30 renders to 11 digits; MaxNumericLiteralDigits=10", past)
	}
}

// A suffix's digits go into an error clipped, as a literal's do: the
// input sets their length.
func TestPrecisionSuffixErrorsAreClipped(t *testing.T) {
	md := compilePlacementSrc(t, suffixSchemaSrc, "bfs.v1", "H")
	long := strings.Repeat("9", 1<<20)
	for _, in := range []string{
		"count = 5p" + long + "\n",
		"by_id = { 1p" + long + ": \"x\" }\n",
		"bf = 1p" + long + "\n",
	} {
		_, err := pxf.UnmarshalDescriptor([]byte(in), md)
		require.Error(t, err)
		assert.Less(t, len(err.Error()), 300, "%.20s…: %.300s", in, err.Error())
	}
	_, err := pxf.Parse([]byte("by_id = { 1p" + long + ": \"x\" }\n"))
	require.Error(t, err)
	assert.Less(t, len(err.Error()), 300)
}

// A prec-0 zero is the one BigFloat written without a suffix, and a
// negative one keeps its sign (protowire#410). The corpus writes only
// the positive one.
func TestPrecZeroNegativeZeroRoundTrips(t *testing.T) {
	md := compilePlacementSrc(t, suffixSchemaSrc, "bfs.v1", "H")
	msg, err := pxf.UnmarshalDescriptor([]byte("bf = -0\n"), md)
	require.NoError(t, err)
	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Equal(t, "bf = -0\n", string(out))
}
