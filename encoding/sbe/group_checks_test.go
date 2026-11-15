// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
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
