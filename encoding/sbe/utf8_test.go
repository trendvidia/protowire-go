// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

const utf8ProtoSrc = `
syntax = "proto3";
package utf8.v1;
import "sbe/annotations.proto";

option (sbe.schema_id) = 2;
option (sbe.version) = 0;

message Code {
  string text = 1 [(sbe.length) = 4];
}

message Row {
  string tag = 1 [(sbe.length) = 4];
}

message Quote {
  option (sbe.template_id) = 1;
  string symbol = 1 [(sbe.length) = 4];
  bytes raw = 2 [(sbe.length) = 4];
  Code code = 3;
  repeated Row rows = 4;
}
`

func compileUTF8Proto(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"sbe/annotations.proto": sbeAnnotationsSrc,
					"utf8.proto":            utf8ProtoSrc,
				}),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "utf8.proto")
	require.NoError(t, err)
	return result.FindFileByPath("utf8.proto")
}

// quoteFrame lays out a Quote by hand, because Marshal is not the producer
// under test: the bytes come from a feed. Each string is padded to its 4 bytes.
func quoteFrame(symbol, raw, code string, rows ...string) []byte {
	pad := func(b []byte, s string) []byte {
		return append(b, append([]byte(s), make([]byte, 4-len(s))...)...)
	}
	b := make([]byte, 8)
	binary.LittleEndian.PutUint16(b[0:], 12) // block length
	binary.LittleEndian.PutUint16(b[2:], 1)  // template
	binary.LittleEndian.PutUint16(b[4:], 2)  // schema
	b = pad(pad(pad(b, symbol), raw), code)
	b = binary.LittleEndian.AppendUint16(b, 4)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(rows)))
	for _, r := range rows {
		b = pad(b, r)
	}
	return b
}

// HARDENING.md § UTF-8. Each decode path, each place a string can sit, and
// each way a byte sequence can be invalid.
func TestStringFieldRejectsInvalidUTF8(t *testing.T) {
	fd := compileUTF8Proto(t)
	desc := fd.Messages().ByName("Quote")
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	sites := []struct {
		name  string
		frame func(bad string) []byte
		field string
	}{
		{"root field", func(s string) []byte { return quoteFrame(s, "", "") }, "symbol"},
		{"composite field", func(s string) []byte { return quoteFrame("", "", s) }, "text"},
		{"group field", func(s string) []byte { return quoteFrame("", "", "", "ok", s) }, "tag"},
	}
	sequences := []struct{ name, bad string }{
		{"stray byte", "AB\xff"},
		{"lone continuation", "\x80"},
		{"latin-1", "caf\xe9"},
		{"cut off at the end of the field", "abc\xc3"},
		{"overlong", "\xc0\x80"},
		{"surrogate", "\xed\xa0\x80"},
		{"past U+10FFFF", "\xf4\x90\x80\x80"},
	}
	decoders := []struct {
		name   string
		decode func(frame []byte) error
	}{
		{"Unmarshal", func(frame []byte) error {
			return codec.Unmarshal(frame, dynamicpb.NewMessage(desc))
		}},
		{"UnmarshalDescriptor", func(frame []byte) error {
			_, err := codec.UnmarshalDescriptor(frame, desc)
			return err
		}},
		{"Decode", func(frame []byte) error {
			return codec.NewDecoder(bytes.NewReader(sofh(frame))).Decode(dynamicpb.NewMessage(desc))
		}},
	}
	for _, dec := range decoders {
		for _, site := range sites {
			for _, seq := range sequences {
				t.Run(dec.name+"/"+site.name+"/"+seq.name, func(t *testing.T) {
					require.EqualError(t, dec.decode(site.frame(seq.bad)),
						"sbe: invalid UTF-8 in string field "+site.field)
				})
			}
		}
	}
}

func sofh(body []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(6+len(body)))
	b = binary.BigEndian.AppendUint16(b, 0xEB50)
	return append(b, body...)
}

// The check must not refuse what the rule allows: a bytes field takes anything,
// and valid text keeps its multi-byte runes and an interior NUL.
func TestStringFieldAcceptsValidUTF8(t *testing.T) {
	fd := compileUTF8Proto(t)
	desc := fd.Messages().ByName("Quote")
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	got, err := codec.UnmarshalDescriptor(quoteFrame("€a", "\xff\xfe\x80\xc0", "a\x00b", "😀"), desc)
	require.NoError(t, err)

	fields := desc.Fields()
	require.Equal(t, "€a", got.Get(fields.ByName("symbol")).String())
	require.Equal(t, []byte("\xff\xfe\x80\xc0"), got.Get(fields.ByName("raw")).Bytes())
	code := got.Get(fields.ByName("code")).Message()
	require.Equal(t, "a\x00b", code.Get(code.Descriptor().Fields().ByName("text")).String())
	rows := got.Get(fields.ByName("rows")).List()
	require.Equal(t, 1, rows.Len())
	row := rows.Get(0).Message()
	require.Equal(t, "😀", row.Get(row.Descriptor().Fields().ByName("tag")).String())
}

// Bad text is a bad message, not a bad stream: the frame was read whole, so the
// next one still decodes.
func TestDecoderContinuesPastInvalidUTF8(t *testing.T) {
	fd := compileUTF8Proto(t)
	desc := fd.Messages().ByName("Quote")
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	stream := append(sofh(quoteFrame("AB\xff", "", "")), sofh(quoteFrame("GOOD", "", ""))...)
	dec := codec.NewDecoder(bytes.NewReader(stream))

	require.EqualError(t, dec.Decode(dynamicpb.NewMessage(desc)), "sbe: invalid UTF-8 in string field symbol")

	next := dynamicpb.NewMessage(desc)
	require.NoError(t, dec.Decode(next))
	require.Equal(t, "GOOD", next.Get(desc.Fields().ByName("symbol")).String())
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), io.EOF)
}
