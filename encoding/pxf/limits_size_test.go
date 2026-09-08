// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// sizeDesc compiles a message with one field of each shape the size
// limits bound.
func sizeDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, "size.proto", map[string]string{
		"size.proto": `syntax = "proto3"; package size;
message S { string s = 1; bytes b = 2; repeated int32 xs = 3; map<string, int32> m = 4; }`,
	})
	md := fd.Messages().ByName("S")
	require.NotNil(t, md)
	return md
}

// TestMaxMessageSize pins #112: the input to one decode or parse call is
// capped at MaxMessageSize (HARDENING.md § Mandatory limits) before the
// first token is read — at the default, and lowered per call, on every
// entry point.
func TestMaxMessageSize(t *testing.T) {
	md := sizeDesc(t)

	t.Run("default rejects 64 MiB + 1", func(t *testing.T) {
		doc := []byte(`s = "` + strings.Repeat("a", pxf.MaxMessageSize) + `"`)
		require.Greater(t, len(doc), pxf.MaxMessageSize)
		_, err := pxf.UnmarshalDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxMessageSize=67108864")
		_, _, err = pxf.UnmarshalFullDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxMessageSize=67108864")
		_, err = pxf.Parse(doc)
		require.ErrorContains(t, err, "MaxMessageSize=67108864")
	})
	t.Run("default accepts 64 MiB", func(t *testing.T) {
		doc := []byte(`s = "` + strings.Repeat("a", pxf.MaxMessageSize-16) + `"`)
		require.LessOrEqual(t, len(doc), pxf.MaxMessageSize)
		_, err := pxf.UnmarshalDescriptor(doc, md)
		require.NoError(t, err)
	})
	t.Run("lowered per call", func(t *testing.T) {
		doc := []byte(`s = "hello, world"`)
		_, err := pxf.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "default")
		_, err = pxf.UnmarshalOptions{MaxMessageSize: len(doc)}.UnmarshalDescriptor(doc, md)
		require.NoError(t, err, "at the bound")
		_, err = pxf.UnmarshalOptions{MaxMessageSize: 8}.UnmarshalDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxMessageSize=8")
		_, _, err = pxf.UnmarshalOptions{MaxMessageSize: 8}.UnmarshalFullDescriptor(doc, md)
		require.ErrorContains(t, err, "MaxMessageSize=8", "the Full variant")
		_, err = pxf.ParseOptions{MaxMessageSize: 8}.Parse(doc)
		require.ErrorContains(t, err, "MaxMessageSize=8", "the AST parser")
		_, errs := pxf.ParseOptions{MaxMessageSize: 8}.ParseTolerant(doc)
		require.NotEmpty(t, errs, "tolerant mode reports it")
		require.Contains(t, errs[0].Msg, "MaxMessageSize=8")
	})
}

// TestMaxBytesLiteralLength pins #112: a b"…" literal is refused from its
// length before it is decoded when its decoded length would exceed
// MaxBytesLiteralLength, in the decoder and in the AST parser.
func TestMaxBytesLiteralLength(t *testing.T) {
	md := sizeDesc(t)
	doc := []byte(`b = b"AAAAAAAA"`) // eight characters decode to six bytes
	_, err := pxf.UnmarshalDescriptor(doc, md)
	require.NoError(t, err, "default")
	_, err = pxf.UnmarshalOptions{MaxBytesLiteralLength: 6}.UnmarshalDescriptor(doc, md)
	require.NoError(t, err, "at the bound")
	_, err = pxf.UnmarshalOptions{MaxBytesLiteralLength: 5}.UnmarshalDescriptor(doc, md)
	require.ErrorContains(t, err, "MaxBytesLiteralLength=5")
	_, err = pxf.ParseOptions{MaxBytesLiteralLength: 5}.Parse(doc)
	require.ErrorContains(t, err, "MaxBytesLiteralLength=5", "the AST parser")
	_, err = pxf.ParseOptions{MaxBytesLiteralLength: 6}.Parse(doc)
	require.NoError(t, err)
}

// TestMaxRepeatedCount pins #112: the element count of a bracketed list
// and the entry count of a map literal are capped at MaxRepeatedCount,
// lowered per call.
func TestMaxRepeatedCount(t *testing.T) {
	md := sizeDesc(t)
	doc := []byte("xs = [1, 2, 3, 4, 5]\nm = { a: 1\n b: 2\n c: 3 }")
	_, err := pxf.UnmarshalDescriptor(doc, md)
	require.NoError(t, err, "default")
	_, err = pxf.UnmarshalOptions{MaxRepeatedCount: 5}.UnmarshalDescriptor(doc, md)
	require.NoError(t, err, "at the bound")
	_, err = pxf.UnmarshalOptions{MaxRepeatedCount: 4}.UnmarshalDescriptor(doc, md)
	require.ErrorContains(t, err, `repeated field "xs" exceeds MaxRepeatedCount=4`)
	_, err = pxf.UnmarshalOptions{MaxRepeatedCount: 2}.UnmarshalDescriptor([]byte("m = { a: 1\n b: 2\n c: 3 }"), md)
	require.ErrorContains(t, err, `map field "m" exceeds MaxRepeatedCount=2`)
}

// TestDatasetReaderMaxMessageSize pins #112 on the stream reader: the
// bytes it holds while looking for a row boundary are capped at
// MaxMessageSize, so a stream that never ends a row cannot grow the
// buffer without bound.
func TestDatasetReaderMaxMessageSize(t *testing.T) {
	in := "@dataset trades.v1.Trade (symbol, price)\n(\"AAPL\", 1)\n(\"MSFT\", 2)"
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err, "default")
	n := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		n++
	}
	require.Equal(t, 2, n)

	tr, err = pxf.UnmarshalOptions{MaxMessageSize: 16}.NewDatasetReader(strings.NewReader(in))
	if err == nil {
		_, err = tr.Next()
	}
	require.ErrorContains(t, err, "MaxMessageSize=16")
}
