// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// TestUnmarshalGroupZeroBlockLength: a group header with blockLength 0 and
// a count above 0 is refused whatever the template's block length (draft
// -01, Repeating Groups, item 4). Before #291 an empty entry message, whose
// template block is 0, let any count through and appended that many
// entries while consuming no bytes. Each refusal is paired with the same
// shape at count 0, which must still decode.
func TestUnmarshalGroupZeroBlockLength(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wire   string // the whole message: header, root block, group header
		reject string // a substring of the error; empty means it must decode
	}{
		{
			name:   "empty entry, three entries",
			body:   `message E {} message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
			wire:   "000001000100000000000300",
			reject: "sbe: group e declares 3 entries of blockLength 0",
		},
		{
			name:   "empty entry, count at the cap",
			body:   `message E {} message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
			wire:   "00000100010000000000ffff",
			reject: "sbe: group e declares 65535 entries of blockLength 0",
		},
		{
			name: "empty entry, no entries",
			body: `message E {} message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
			wire: "000001000100000000000000",
		},
		{
			name:   "non-empty entry, wire blockLength 0",
			body:   `message E { uint32 x = 1; } message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
			wire:   "000001000100000000000300",
			reject: "sbe: group e wire blockLength 0 < schema blockLength 4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := boundsFile(t, "1", "0", tc.body)
			codec, err := sbe.NewCodec(fd)
			require.NoError(t, err)
			wire, err := hex.DecodeString(tc.wire)
			require.NoError(t, err)

			md := fd.Messages().ByName("M")
			msg := dynamicpb.NewMessage(md)
			err = mustNotPanic(t, "Unmarshal", func() error { return codec.Unmarshal(wire, msg) })
			if tc.reject != "" {
				require.ErrorContains(t, err, tc.reject)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 0, msg.Get(md.Fields().ByName("e")).List().Len())
		})
	}
}

// TestMarshalGroupZeroBlockLength: Marshal refuses what Unmarshal refuses.
// Before #293 three empty entries wrote 000001000100000000000300, which
// TestUnmarshalGroupZeroBlockLength shows is refused on the way back. An
// empty list still writes and reads back.
func TestMarshalGroupZeroBlockLength(t *testing.T) {
	fd := boundsFile(t, "1", "0", `message E {} message M { option (sbe.template_id) = 1; repeated E e = 1; }`)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	md := fd.Messages().ByName("M")
	ed := fd.Messages().ByName("E")

	for _, n := range []int{1, 3} {
		src := dynamicpb.NewMessage(md)
		list := src.Mutable(md.Fields().ByName("e")).List()
		for range n {
			list.Append(protoreflect.ValueOfMessage(dynamicpb.NewMessage(ed)))
		}
		wire, err := codec.Marshal(src)
		require.EqualError(t, err, fmt.Sprintf("sbe: field bounds.v1.M.e has %d entries of blockLength 0, which a decoder must refuse", n))
		require.Nil(t, wire)
	}

	wire, err := codec.Marshal(dynamicpb.NewMessage(md))
	require.NoError(t, err)
	require.Equal(t, "000001000100000000000000", hex.EncodeToString(wire))
	require.NoError(t, codec.Unmarshal(wire, dynamicpb.NewMessage(md)))
}

// TestNewCodecGroupOneof: a oneof in a group entry is refused, as in the
// root message and in a composite. Before #292 it bound, gave each member
// its own block slot, and lost the first member on the way back: {a: 5}
// read back as {a: 0}. A proto3 optional field is a synthetic oneof, and
// still binds and round-trips.
func TestNewCodecGroupOneof(t *testing.T) {
	t.Run("oneof refused", func(t *testing.T) {
		fd := boundsFile(t, "1", "0", `
message E { oneof o { uint32 a = 1; uint32 b = 2; } }
message M { option (sbe.template_id) = 1; repeated E e = 1; }`)
		_, err := sbe.NewCodec(fd)
		require.EqualError(t, err, "sbe: oneof field in group bounds.v1.E not supported")
	})

	t.Run("oneof inside a composite in a group refused", func(t *testing.T) {
		fd := boundsFile(t, "1", "0", `
message C { oneof o { uint32 a = 1; uint32 b = 2; } }
message E { C c = 1; }
message M { option (sbe.template_id) = 1; repeated E e = 1; }`)
		_, err := sbe.NewCodec(fd)
		require.ErrorContains(t, err, "composite bounds.v1.C contains oneof field a")
	})

	t.Run("proto3 optional binds and round-trips", func(t *testing.T) {
		fd := boundsFile(t, "1", "0", `
message E { optional uint32 a = 1; uint32 b = 2; }
message M { option (sbe.template_id) = 1; repeated E e = 1; }`)
		codec, err := sbe.NewCodec(fd)
		require.NoError(t, err)

		md := fd.Messages().ByName("M")
		ed := fd.Messages().ByName("E")
		src := dynamicpb.NewMessage(md)
		list := src.Mutable(md.Fields().ByName("e")).List()
		for _, v := range [][2]uint32{{5, 0}, {0, 6}} {
			e := dynamicpb.NewMessage(ed)
			e.Set(ed.Fields().ByName("a"), protoreflect.ValueOfUint32(v[0]))
			e.Set(ed.Fields().ByName("b"), protoreflect.ValueOfUint32(v[1]))
			list.Append(protoreflect.ValueOfMessage(e))
		}
		wire, err := codec.Marshal(src)
		require.NoError(t, err)

		got := dynamicpb.NewMessage(md)
		require.NoError(t, codec.Unmarshal(wire, got))
		gl := got.Get(md.Fields().ByName("e")).List()
		require.Equal(t, 2, gl.Len())
		for i, v := range [][2]uint32{{5, 0}, {0, 6}} {
			e := gl.Get(i).Message()
			require.Equal(t, uint64(v[0]), e.Get(ed.Fields().ByName("a")).Uint(), "entry %d a", i)
			require.Equal(t, uint64(v[1]), e.Get(ed.Fields().ByName("b")).Uint(), "entry %d b", i)
		}
	})
}
