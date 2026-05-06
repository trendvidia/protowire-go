// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

func newSimple(desc protoreflect.MessageDescriptor, id uint32, value int32) *dynamicpb.Message {
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint32(id))
	m.Set(desc.Fields().ByName("value"), protoreflect.ValueOfInt32(value))
	return m
}

func TestStreamRoundTrip(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Simple")
	in := []*dynamicpb.Message{
		newSimple(desc, 1, -100),
		newSimple(desc, 2, 0),
		newSimple(desc, 1<<20, 2147483647),
	}

	var buf bytes.Buffer
	enc := codec.NewEncoder(&buf)
	for i, m := range in {
		require.NoError(t, enc.Encode(m), "encode[%d]", i)
	}

	dec := codec.NewDecoder(&buf)
	for i, want := range in {
		got := dynamicpb.NewMessage(desc)
		require.NoError(t, dec.Decode(got), "decode[%d]", i)
		require.Equal(t,
			want.Get(desc.Fields().ByName("id")).Uint(),
			got.Get(desc.Fields().ByName("id")).Uint(),
			"frame %d id", i)
		require.Equal(t,
			want.Get(desc.Fields().ByName("value")).Int(),
			got.Get(desc.Fields().ByName("value")).Int(),
			"frame %d value", i)
	}
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), io.EOF)
}

func TestStreamCleanEOF(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")

	dec := codec.NewDecoder(&bytes.Buffer{})
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), io.EOF)
}

func TestStreamPartialHeader(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")

	dec := codec.NewDecoder(bytes.NewReader([]byte{0x00, 0x00, 0x00}))
	err = dec.Decode(dynamicpb.NewMessage(desc))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	// Sticky.
	err2 := dec.Decode(dynamicpb.NewMessage(desc))
	require.Equal(t, err, err2)
}

func TestStreamPartialBody(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")

	var buf bytes.Buffer
	hdr := make([]byte, 6)
	binary.BigEndian.PutUint32(hdr[0:4], 100)
	binary.BigEndian.PutUint16(hdr[4:6], 0xEB50)
	buf.Write(hdr)
	buf.Write(make([]byte, 10))

	dec := codec.NewDecoder(&buf)
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), io.ErrUnexpectedEOF)
}

func TestStreamWrongEncodingType(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")

	hdr := make([]byte, 6)
	binary.BigEndian.PutUint32(hdr[0:4], 6)
	binary.BigEndian.PutUint16(hdr[4:6], 0x5BE0) // SBE 1.0 big-endian — we only accept LE
	dec := codec.NewDecoder(bytes.NewReader(hdr))
	err = dec.Decode(dynamicpb.NewMessage(desc))
	require.ErrorIs(t, err, sbe.ErrFramingCorrupt)
	// Sticky.
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), sbe.ErrFramingCorrupt)
}

func TestStreamLengthBelowHeader(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")

	hdr := make([]byte, 6)
	binary.BigEndian.PutUint32(hdr[0:4], 4)
	binary.BigEndian.PutUint16(hdr[4:6], 0xEB50)
	dec := codec.NewDecoder(bytes.NewReader(hdr))
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), sbe.ErrFramingCorrupt)
}

func TestStreamOversized(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")

	hdr := make([]byte, 6)
	binary.BigEndian.PutUint32(hdr[0:4], 6+(1<<30))
	binary.BigEndian.PutUint16(hdr[4:6], 0xEB50)
	dec := codec.NewDecoder(bytes.NewReader(hdr))
	dec.SetMaxFrameSize(1 << 16)
	require.ErrorIs(t, dec.Decode(dynamicpb.NewMessage(desc)), sbe.ErrFramingCorrupt)
}

func FuzzStreamDecode(f *testing.F) {
	fd := compileTestProto(f)
	codec, err := sbe.NewCodec(fd)
	if err != nil {
		f.Fatal(err)
	}
	desc := fd.Messages().ByName("Simple")

	f.Add([]byte{0, 0, 0, 6, 0xEB, 0x50})
	f.Add([]byte{0, 0, 0, 16, 0xEB, 0x50, 0, 8, 0, 2, 0, 1, 1, 0, 0, 0, 0xff, 0xff, 0xff, 0xfe})
	f.Fuzz(func(t *testing.T, data []byte) {
		dec := codec.NewDecoder(bytes.NewReader(data))
		dec.SetMaxFrameSize(1 << 20)
		for range 64 {
			if err := dec.Decode(dynamicpb.NewMessage(desc)); err != nil {
				return
			}
		}
	})
}
