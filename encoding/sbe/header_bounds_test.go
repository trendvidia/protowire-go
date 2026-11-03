// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// boundsFile compiles one schema for the header-bounds tests. Every SBE
// header field is a uint16, so each value the schema declares, and each
// block it lays out, must fit in 65535 (issue #250).
func boundsFile(t *testing.T, schemaID, version, body string) protoreflect.FileDescriptor {
	t.Helper()
	src := fmt.Sprintf(`
syntax = "proto3";
package bounds.v1;
import "sbe/annotations.proto";
option (sbe.schema_id) = %s;
option (sbe.version) = %s;
%s
`, schemaID, version, body)
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{
				"sbe/annotations.proto": sbeAnnotationsSrc,
				"bounds.proto":          src,
			}),
		}),
	}
	result, err := comp.Compile(context.Background(), "bounds.proto")
	require.NoError(t, err)
	return result.FindFileByPath("bounds.proto")
}

// TestNewCodecHeaderBounds pairs each schema the SBE header cannot describe
// with the same shape at the bound, which must bind and round-trip. Before
// the fix the first row bound and wrote a 1-byte field, and the block rows
// bound and then panicked in Marshal.
func TestNewCodecHeaderBounds(t *testing.T) {
	cases := []struct {
		name      string
		schemaID  string
		version   string
		body      string
		reject    string // a substring of the error; empty means it must bind
		blockSize uint16 // the root blockLength when it binds
	}{
		{
			name:   "length past 65535",
			body:   `message M { option (sbe.template_id) = 1; bytes b = 1 [(sbe.length) = 65537]; uint32 t = 2; }`,
			reject: "field bounds.v1.M.b: (sbe.length) = 65537 is past 65535",
		},
		{
			name:      "length at 65535",
			body:      `message M { option (sbe.template_id) = 1; bytes b = 1 [(sbe.length) = 65535]; }`,
			blockSize: 65535,
		},
		{
			name:   "string length past 65535",
			body:   `message M { option (sbe.template_id) = 1; string s = 1 [(sbe.length) = 70000]; }`,
			reject: "field bounds.v1.M.s: (sbe.length) = 70000 is past 65535",
		},
		{
			name:   "root block past 65535",
			body:   `message M { option (sbe.template_id) = 1; bytes a = 1 [(sbe.length) = 30000]; bytes b = 2 [(sbe.length) = 30000]; bytes c = 3 [(sbe.length) = 30000]; uint32 t = 4; }`,
			reject: "field bounds.v1.M.c: ends at byte 90000 of its block, past 65535",
		},
		{
			name:   "root block one byte past 65535",
			body:   `message M { option (sbe.template_id) = 1; bytes a = 1 [(sbe.length) = 65532]; uint32 t = 2; }`,
			reject: "field bounds.v1.M.t: ends at byte 65536 of its block",
		},
		{
			name:      "root block at 65535",
			body:      `message M { option (sbe.template_id) = 1; bytes a = 1 [(sbe.length) = 65531]; uint32 t = 2; }`,
			blockSize: 65535,
		},
		{
			name:   "group block past 65535",
			body:   `message E { bytes a = 1 [(sbe.length) = 40000]; bytes b = 2 [(sbe.length) = 40000]; } message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
			reject: "group field bounds.v1.E.b: ends at byte 80000 of its block",
		},
		{
			name: "group block at 65535",
			body: `message E { bytes a = 1 [(sbe.length) = 40000]; bytes b = 2 [(sbe.length) = 25535]; } message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
		},
		{
			name:   "composite past 65535",
			body:   `message C { bytes a = 1 [(sbe.length) = 40000]; bytes b = 2 [(sbe.length) = 40000]; } message M { option (sbe.template_id) = 1; C c = 1; }`,
			reject: "composite bounds.v1.M.c: composite field bounds.v1.C.b: ends at byte 80000",
		},
		{
			name:   "composite fits, block past 65535",
			body:   `message C { bytes a = 1 [(sbe.length) = 40000]; } message M { option (sbe.template_id) = 1; C c = 1; C d = 2; }`,
			reject: "composite bounds.v1.M.d: ends at byte 80000",
		},
		{
			name:   "composite in group past 65535",
			body:   `message C { bytes a = 1 [(sbe.length) = 40000]; } message E { C c = 1; C d = 2; } message M { option (sbe.template_id) = 1; repeated E e = 1; }`,
			reject: "composite in group bounds.v1.E.d: ends at byte 80000",
		},
		{
			name:   "nested composite past 65535",
			body:   `message D { bytes a = 1 [(sbe.length) = 40000]; } message C { D x = 1; D y = 2; } message M { option (sbe.template_id) = 1; C c = 1; }`,
			reject: "composite field bounds.v1.C.y: ends at byte 80000",
		},
		{
			name:   "template_id past 65535",
			body:   `message M { option (sbe.template_id) = 65537; uint32 t = 1; }`,
			reject: "message bounds.v1.M: (sbe.template_id) = 65537 is past 65535",
		},
		{
			name:      "template_id at 65535",
			body:      `message M { option (sbe.template_id) = 65535; uint32 t = 1; }`,
			blockSize: 4,
		},
		{
			name:     "schema_id past 65535",
			schemaID: "65536",
			body:     `message M { option (sbe.template_id) = 1; uint32 t = 1; }`,
			reject:   "file bounds.proto: (sbe.schema_id) = 65536 is past 65535",
		},
		{
			name:      "schema_id at 65535",
			schemaID:  "65535",
			body:      `message M { option (sbe.template_id) = 1; uint32 t = 1; }`,
			blockSize: 4,
		},
		{
			name:    "version past 65535",
			version: "65536",
			body:    `message M { option (sbe.template_id) = 1; uint32 t = 1; }`,
			reject:  "file bounds.proto: (sbe.version) = 65536 is past 65535",
		},
		{
			name:      "version at 65535",
			version:   "65535",
			body:      `message M { option (sbe.template_id) = 1; uint32 t = 1; }`,
			blockSize: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schemaID, version := tc.schemaID, tc.version
			if schemaID == "" {
				schemaID = "1"
			}
			if version == "" {
				version = "0"
			}
			fd := boundsFile(t, schemaID, version, tc.body)
			codec, err := sbe.NewCodec(fd)
			if tc.reject != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.reject)
				return
			}
			require.NoError(t, err)

			md := fd.Messages().ByName("M")
			msg := dynamicpb.NewMessage(md)
			if e := md.Fields().ByName("e"); e != nil {
				list := msg.Mutable(e).List()
				list.Append(list.NewElement())
			}
			data, err := codec.Marshal(msg)
			require.NoError(t, err)

			// The header states exactly what the schema declared.
			require.GreaterOrEqual(t, len(data), 8)
			if tc.blockSize != 0 {
				require.Equal(t, tc.blockSize, le16(data[0:]), "blockLength")
			}
			require.Equal(t, schemaID, fmt.Sprint(le16(data[4:])), "schemaId")
			require.Equal(t, version, fmt.Sprint(le16(data[6:])), "version")

			_, err = codec.UnmarshalDescriptor(data, md)
			require.NoError(t, err)
		})
	}
}

// TestMarshalGroupCountBound: numInGroup is a uint16. Before the fix 65536
// entries were written with a count of 0, and the message read back with
// no entries and no error.
func TestMarshalGroupCountBound(t *testing.T) {
	fd := boundsFile(t, "1", "0", `message E { uint32 a = 1; } message M { option (sbe.template_id) = 1; repeated E e = 1; }`)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	md := fd.Messages().ByName("M")
	efd := md.Fields().ByName("e")

	withEntries := func(n int) *dynamicpb.Message {
		msg := dynamicpb.NewMessage(md)
		list := msg.Mutable(efd).List()
		for i := 0; i < n; i++ {
			list.Append(list.NewElement())
		}
		return msg
	}

	data, err := codec.Marshal(withEntries(65535))
	require.NoError(t, err)
	require.Equal(t, uint16(65535), le16(data[8+2:]), "numInGroup")
	back, err := codec.UnmarshalDescriptor(data, md)
	require.NoError(t, err)
	require.Equal(t, 65535, back.Get(efd).List().Len())

	_, err = codec.Marshal(withEntries(65536))
	require.Error(t, err)
	require.Contains(t, err.Error(), "field bounds.v1.M.e has 65536 entries, past 65535")
}

func le16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
