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

message CutCode {
  string text = 1 [(sbe.length) = 8];
}

message CutRow {
  string tag = 1 [(sbe.length) = 8];
}

message Cut {
  option (sbe.template_id) = 2;
  string text = 1 [(sbe.length) = 8];
  bytes raw = 2 [(sbe.length) = 8];
  CutCode code = 3;
  repeated CutRow rows = 4;
}

message CutTiny {
  option (sbe.template_id) = 3;
  string text = 1 [(sbe.length) = 3];
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

// Draft -01 § SBE Annotations (trendvidia/protowire#495): an overlong string is
// cut at the last whole character at or before the limit and NUL-padded, so it
// still decodes; a bytes field is cut at the limit.
func TestOverlongStringCutOnCharacterBoundary(t *testing.T) {
	fd := compileUTF8Proto(t)
	desc := fd.Messages().ByName("Cut")
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	marshal := func(text string) []byte {
		t.Helper()
		msg := dynamicpb.NewMessage(desc)
		fields := desc.Fields()
		msg.Set(fields.ByName("text"), protoreflect.ValueOfString(text))
		msg.Set(fields.ByName("raw"), protoreflect.ValueOfBytes([]byte(text)))
		code := msg.Mutable(fields.ByName("code")).Message()
		code.Set(code.Descriptor().Fields().ByName("text"), protoreflect.ValueOfString(text))
		rows := msg.Mutable(fields.ByName("rows")).List()
		row := rows.NewElement()
		row.Message().Set(row.Message().Descriptor().Fields().ByName("tag"), protoreflect.ValueOfString(text))
		rows.Append(row)
		frame, err := codec.Marshal(msg)
		require.NoError(t, err)
		return frame
	}
	// Header 8; block: text 0-8, raw 8-16, code.text 16-24; group header 4;
	// the one row's tag 28-36.
	const block = 8
	sites := []struct {
		name string
		at   int
	}{{"root field", block}, {"composite field", block + 16}, {"group field", block + 28}}

	cases := []struct {
		name, in, text string
	}{
		{"2-byte character across the limit", "ABCDEFGé", "ABCDEFG"},
		{"3-byte character across the limit", "ABCDEF€", "ABCDEF"},
		{"3-byte character, one byte over", "ABCDE€x", "ABCDE€"},
		{"4-byte character across the limit", "ABCDE😀", "ABCDE"},
		{"limit between two 4-byte characters", "😀😀😀", "😀😀"},
		{"cut already on a boundary", "ABCDEFGHI", "ABCDEFGH"},
		{"fits exactly", "ABCDEFé", "ABCDEFé"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := marshal(c.in)
			want := append([]byte(c.text), make([]byte, 8-len(c.text))...)
			for _, s := range sites {
				require.Equal(t, want, frame[s.at:s.at+8], s.name)
			}
			require.Equal(t, []byte(c.in)[:8], frame[block+8:block+16], "bytes field")

			got, err := codec.UnmarshalDescriptor(frame, desc)
			require.NoError(t, err)
			require.Equal(t, c.text, got.Get(desc.Fields().ByName("text")).String())
		})
	}

	// The case the issue names, byte for byte.
	frame := marshal("ABCDEFGé")
	require.Equal(t, []byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x00}, frame[block:block+8])
	require.Equal(t, []byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0xC3}, frame[block+8:block+16])

	// A limit shorter than the first character leaves the field empty.
	tiny := fd.Messages().ByName("CutTiny")
	msg := dynamicpb.NewMessage(tiny)
	msg.Set(tiny.Fields().ByName("text"), protoreflect.ValueOfString("😀"))
	frame, err = codec.Marshal(msg)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0, 0}, frame[block:block+3])
	got, err := codec.UnmarshalDescriptor(frame, tiny)
	require.NoError(t, err)
	require.Equal(t, "", got.Get(tiny.Fields().ByName("text")).String())
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
