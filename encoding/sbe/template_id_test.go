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

// idFiles compiles one file per body, a.proto, b.proto, ..., each in its own
// package and with the schema id given for it.
func idFiles(t *testing.T, schemaIDs []int, bodies ...string) []protoreflect.FileDescriptor {
	t.Helper()
	srcs := map[string]string{"sbe/annotations.proto": sbeAnnotationsSrc}
	var names []string
	for i, body := range bodies {
		name := fmt.Sprintf("%c.proto", 'a'+i)
		names = append(names, name)
		srcs[name] = fmt.Sprintf(`
syntax = "proto3";
package ids.%c;
import "sbe/annotations.proto";
option (sbe.schema_id) = %d;
option (sbe.version) = 0;
%s
`, 'a'+i, schemaIDs[i], body)
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(srcs),
		}),
	}
	result, err := comp.Compile(context.Background(), names...)
	require.NoError(t, err)
	var fds []protoreflect.FileDescriptor
	for _, name := range names {
		fds = append(fds, result.FindFileByPath(name))
	}
	return fds
}

// TestTemplateIDUniqueInSchema: draft -01 § SBE Annotations, "sbe.template_id
// MUST be unique among messages within a schema". Before the fix NewCodec
// bound both messages, the second replaced the first in the id lookup, and
// View read A's bytes as B's (issue #253).
func TestTemplateIDUniqueInSchema(t *testing.T) {
	cases := []struct {
		name      string
		schemaIDs []int
		bodies    []string
		reject    string
	}{
		{
			name:      "two messages in one file",
			schemaIDs: []int{1},
			bodies:    []string{`message A { option (sbe.template_id) = 7; uint32 x = 1; } message B { option (sbe.template_id) = 7; uint64 y = 1; }`},
			reject:    "sbe: messages ids.a.A and ids.a.B share (sbe.template_id) = 7 in schema 1; it must be unique within a schema",
		},
		{
			name:      "two files of one schema",
			schemaIDs: []int{1, 1},
			bodies:    []string{`message A { option (sbe.template_id) = 7; uint32 x = 1; }`, `message B { option (sbe.template_id) = 7; uint64 y = 1; }`},
			reject:    "sbe: messages ids.a.A and ids.b.B share (sbe.template_id) = 7 in schema 1",
		},
		{
			name:      "a nested message",
			schemaIDs: []int{1},
			bodies:    []string{`message A { option (sbe.template_id) = 7; uint32 x = 1; message N { option (sbe.template_id) = 7; uint32 z = 1; } }`},
			reject:    "sbe: messages ids.a.A and ids.a.A.N share (sbe.template_id) = 7 in schema 1",
		},
		{
			name:      "distinct ids",
			schemaIDs: []int{1},
			bodies:    []string{`message A { option (sbe.template_id) = 7; uint32 x = 1; } message B { option (sbe.template_id) = 8; uint64 y = 1; }`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sbe.NewCodec(idFiles(t, tc.schemaIDs, tc.bodies...)...)
			if tc.reject == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.reject)
		})
	}
}

// TestTemplateIDSameFileTwice: a file passed twice registers the same
// messages again, which is not a duplicate.
func TestTemplateIDSameFileTwice(t *testing.T) {
	fds := idFiles(t, []int{1}, `message A { option (sbe.template_id) = 7; uint32 x = 1; }`)
	codec, err := sbe.NewCodec(fds[0], fds[0])
	require.NoError(t, err)

	md := fds[0].Messages().ByName("A")
	msg := dynamicpb.NewMessage(md)
	msg.Set(md.Fields().ByName("x"), protoreflect.ValueOfUint32(42))
	data, err := codec.Marshal(msg)
	require.NoError(t, err)
	v, err := codec.View(data)
	require.NoError(t, err)
	require.Equal(t, uint64(42), v.Uint("x"))
}

// TestTemplateIDAcrossSchemas: uniqueness is per schema, so two schemas may
// each use template 7 and both bind. View picks the template from the
// header's schemaId and templateId together (issue #253, owner's call).
// Before, it looked the id up alone, found whichever message was registered
// last, and failed on A's bytes with "wire blockLength 4 < schema
// blockLength 8 for template 7"; with B's block no longer than A's it would
// have read A as B with no error.
func TestTemplateIDAcrossSchemas(t *testing.T) {
	fds := idFiles(t, []int{1, 2},
		`message A { option (sbe.template_id) = 7; uint32 x = 1; message Only1 { option (sbe.template_id) = 9; uint32 z = 1; } }`,
		`message B { option (sbe.template_id) = 7; uint64 y = 1; }`)

	mdA, mdB := fds[0].Messages().ByName("A"), fds[1].Messages().ByName("B")
	a := dynamicpb.NewMessage(mdA)
	a.Set(mdA.Fields().ByName("x"), protoreflect.ValueOfUint32(0xA))
	b := dynamicpb.NewMessage(mdB)
	b.Set(mdB.Fields().ByName("y"), protoreflect.ValueOfUint64(0xB))

	// Registration order does not matter: each schema's bytes reach their own
	// message whichever file came last.
	for _, order := range [][]protoreflect.FileDescriptor{fds, {fds[1], fds[0]}} {
		codec, err := sbe.NewCodec(order...)
		require.NoError(t, err, "two schemas may each use template 7")
		dataA, err := codec.Marshal(a)
		require.NoError(t, err)
		dataB, err := codec.Marshal(b)
		require.NoError(t, err)
		require.Equal(t, uint16(1), le16(dataA[4:]), "A's header names schema 1")
		require.Equal(t, uint16(2), le16(dataB[4:]), "B's header names schema 2")

		v, err := codec.View(dataA)
		require.NoError(t, err)
		require.Equal(t, uint64(0xA), v.Uint("x"))
		v, err = codec.View(dataB)
		require.NoError(t, err)
		require.Equal(t, uint64(0xB), v.Uint("y"))
	}

	// Template 9 exists in schema 1 only. A header naming it in schema 2 is
	// refused, where an id-only lookup would have found it.
	codec, err := sbe.NewCodec(fds...)
	require.NoError(t, err)
	data9, err := codec.Marshal(dynamicpb.NewMessage(mdA.Messages().ByName("Only1")))
	require.NoError(t, err)
	_, err = codec.View(data9)
	require.NoError(t, err)
	data9[4] = 2
	_, err = codec.View(data9)
	require.EqualError(t, err, "sbe: unknown template ID 9 in schema 2")
}
