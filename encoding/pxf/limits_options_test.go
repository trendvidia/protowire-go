// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// nestDesc compiles a message that nests itself, for depth limits.
func nestDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, "nest.proto", map[string]string{
		"nest.proto": `syntax = "proto3"; package nest; message N { N child = 1; int32 v = 2; }`,
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
		// The direct decoder counts the root message as depth 1, so three
		// nested blocks sit at depth 4 (the AST parser counts braces and
		// puts them at 3; see TestNestingDepthBoundary for that divergence).
		_, err = pxf.UnmarshalOptions{MaxNestingDepth: 4}.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "at the bound")
		_, err = pxf.UnmarshalOptions{MaxNestingDepth: 3}.UnmarshalDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxNestingDepth=3")
		_, _, err = pxf.UnmarshalOptions{MaxNestingDepth: 3}.UnmarshalFullDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxNestingDepth=3", "the Full variant honours it too")
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
