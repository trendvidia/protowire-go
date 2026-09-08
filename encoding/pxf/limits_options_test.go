// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// nestDesc compiles a message that nests itself, for depth limits.
func nestDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, "nest.proto", map[string]string{
		"nest.proto": `syntax = "proto3"; package nest; message N { N child = 1; int32 v = 2; repeated N kids = 3; }`,
	})
	md := fd.Messages().ByName("N")
	require.NotNil(t, md)
	return md
}

// TestUnmarshalOptionsLimits pins #101 on the PXF side: UnmarshalOptions
// carries the draft's per-call limits with the package constants as
// their zero-value defaults, a lowered value rejects a document the
// default accepts, and the Full variant honours it too.
func TestUnmarshalOptionsLimits(t *testing.T) {
	t.Run("MaxNestingDepth", func(t *testing.T) {
		md := nestDesc(t)
		doc := []byte("child { child { child { v = 1 } } }")
		_, err := pxf.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "default")
		_, err = pxf.UnmarshalOptions{}.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "zero options are the default")
		// Three nested blocks are three descents from a root at depth 0
		// (HARDENING.md § Recursion; #111), in the decoder as in the parser.
		_, err = pxf.UnmarshalOptions{MaxNestingDepth: 3}.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "at the bound")
		_, err = pxf.UnmarshalOptions{MaxNestingDepth: 2}.UnmarshalDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxNestingDepth=2")
		_, _, err = pxf.UnmarshalOptions{MaxNestingDepth: 2}.UnmarshalFullDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxNestingDepth=2", "the Full variant honours it too")
	})

	t.Run("MaxNumericLiteralDigits", func(t *testing.T) {
		md := bigNumDesc(t, "BigNumDemo")
		doc := []byte("big_int_field = 1234567890")
		_, err := pxf.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "default")
		_, err = pxf.UnmarshalOptions{MaxNumericLiteralDigits: 10}.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "at the bound")
		_, err = pxf.UnmarshalOptions{MaxNumericLiteralDigits: 9}.UnmarshalDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxNumericLiteralDigits=9")
		_, _, err = pxf.UnmarshalOptions{MaxNumericLiteralDigits: 9}.UnmarshalFullDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxNumericLiteralDigits=9", "the Full variant honours it too")
	})
}

// TestParseOptionsLimits pins #101 for the AST parser behind fmt and
// validate: ParseOptions carries MaxNestingDepth, zero meaning the
// package constant, in both the strict and the tolerant mode.
func TestParseOptionsLimits(t *testing.T) {
	src := []byte("a { b { c { v = 1 } } }")
	_, err := pxf.Parse(src)
	require.NoError(t, err, "default")
	_, err = pxf.ParseOptions{}.Parse(src)
	require.NoError(t, err, "zero options are the default")
	_, err = pxf.ParseOptions{MaxNestingDepth: 3}.Parse(src)
	require.NoError(t, err, "at the bound")
	_, err = pxf.ParseOptions{MaxNestingDepth: 2}.Parse(src)
	require.ErrorContains(t, err, "MaxNestingDepth=2")

	doc, errs := pxf.ParseTolerant(src)
	require.Empty(t, errs)
	require.NotNil(t, doc)
	doc, errs = pxf.ParseOptions{MaxNestingDepth: 2}.ParseTolerant(src)
	require.NotNil(t, doc, "tolerant mode still returns a document")
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Msg, "MaxNestingDepth=2")
}

// TestNestingDepthBoundary pins where the counter starts (#111): the root is
// depth 0 and every `{` or `[` is one descent (HARDENING.md § Recursion),
// so exactly MaxNestingDepth levels are accepted and one more is not — in
// the direct decoder and the AST parser alike, through blocks alone and
// through alternating lists and blocks, which count alike. These are the
// corpus rows deep-nesting-100 / -101 and deep-nesting-lists-100 / -101.
func TestNestingDepthBoundary(t *testing.T) {
	md := nestDesc(t)
	blocks := func(n int) []byte {
		return []byte(strings.Repeat("child { ", n) + "v = 1" + strings.Repeat(" }", n))
	}
	// pairs of `kids [{ … }]` — a list and a block each — plus extra blocks
	// at the deepest point.
	mixed := func(pairs, extra int) []byte {
		return []byte(strings.Repeat("kids = [{ ", pairs) + strings.Repeat("child { ", extra) + "v = 1" + strings.Repeat(" }", extra) + strings.Repeat(" }]", pairs))
	}
	for _, tc := range []struct {
		name string
		doc  []byte
		ok   bool
	}{
		{"blocks at the bound", blocks(pxf.MaxNestingDepth), true},
		{"blocks one past", blocks(pxf.MaxNestingDepth + 1), false},
		{"lists and blocks at the bound", mixed(pxf.MaxNestingDepth/2, 0), true},
		{"lists and blocks one past", mixed(pxf.MaxNestingDepth/2, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, derr := pxf.UnmarshalDescriptor(tc.doc, md)
			_, perr := pxf.Parse(tc.doc)
			if tc.ok {
				require.NoError(t, derr, "decoder")
				require.NoError(t, perr, "parser")
				return
			}
			require.ErrorContains(t, derr, "MaxNestingDepth=100", "decoder")
			require.ErrorContains(t, perr, "MaxNestingDepth=100", "parser")
		})
	}
}
