// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// An empty (pxf.required) repeated or map field used to reach the scalar
// writer, which panicked, wrote the list's address into the row, or
// returned "unsupported kind" (#219).
const requiredListProtoSrc = `
syntax = "proto3";
package dswl.v1;

import "pxf/annotations.proto";

enum E { E_ZERO = 0; }
message Sub { int32 x = 1; }
message Key { string id = 1; }

message Ints { repeated int32 f = 1 [(pxf.required) = true]; }
message Strs { repeated string f = 1 [(pxf.required) = true]; }
message Bools { repeated bool f = 1 [(pxf.required) = true]; }
message Bys { repeated bytes f = 1 [(pxf.required) = true]; }
message Dbls { repeated double f = 1 [(pxf.required) = true]; }
message Flts { repeated float f = 1 [(pxf.required) = true]; }
message Enums { repeated E f = 1 [(pxf.required) = true]; }
message Msgs { repeated Sub f = 1 [(pxf.required) = true]; }
message Keyed { repeated Key f = 1 [(pxf.required) = true, (pxf.key) = "id"]; }
message Maps { map<string, int32> f = 1 [(pxf.required) = true]; }
message MsgMaps { map<string, Sub> f = 1 [(pxf.required) = true]; }

message Outer {
  Ints ints = 1;
  Maps maps = 2;
  repeated Ints list = 3;
  map<string, Maps> by_key = 4;
}
`

func compileRequiredList(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	return compileProtoSources(t, "dswl.proto", map[string]string{
		"dswl.proto":            requiredListProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	})
}

func TestAppendDatasetRow_EmptyRequiredList(t *testing.T) {
	fd := compileRequiredList(t)
	for name, want := range map[protoreflect.Name]string{
		"Ints": "([])\n", "Strs": "([])\n", "Bools": "([])\n", "Bys": "([])\n",
		"Dbls": "([])\n", "Flts": "([])\n", "Enums": "([])\n", "Msgs": "([])\n",
		"Keyed": "([])\n", "Maps": "({})\n", "MsgMaps": "({})\n",
	} {
		t.Run(string(name), func(t *testing.T) {
			md := fd.Messages().ByName(name)
			require.NotNil(t, md)
			msg := dynamicpb.NewMessage(md)
			columns := []string{"f"}

			row, err := pxf.AppendDatasetRow(nil, msg, columns)
			require.NoError(t, err)
			assert.Equal(t, want, string(row))

			head, err := pxf.AppendDatasetHeader(nil, md, columns)
			require.NoError(t, err)
			got, err := bindRows(t, md, string(head)+string(row))
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.True(t, proto.Equal(msg, got[0]))
		})
	}
}

// The reader checks (pxf.required) in every body of a cell and through a
// dotted column, so the rule holds there too. Inside a body the field used
// to be left out, and the row came back refused.
func TestAppendDatasetRow_EmptyRequiredListNested(t *testing.T) {
	fd := compileRequiredList(t)
	md := fd.Messages().ByName("Outer")
	sub := func(name protoreflect.Name) protoreflect.Value {
		return protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Messages().ByName(name)))
	}
	fields := md.Fields()

	for _, tc := range []struct {
		name    string
		columns []string
		build   func(m *dynamicpb.Message)
		want    string
	}{
		{"list in a block cell", []string{"ints"},
			func(m *dynamicpb.Message) { m.Set(fields.ByName("ints"), sub("Ints")) },
			"({ f = [] })\n"},
		{"map in a block cell", []string{"maps"},
			func(m *dynamicpb.Message) { m.Set(fields.ByName("maps"), sub("Maps")) },
			"({ f = {} })\n"},
		{"list in a list element", []string{"list"},
			func(m *dynamicpb.Message) {
				l := m.Mutable(fields.ByName("list")).List()
				l.Append(sub("Ints"))
			},
			"([ { f = [] } ])\n"},
		{"map in a map value", []string{"by_key"},
			func(m *dynamicpb.Message) {
				mp := m.Mutable(fields.ByName("by_key")).Map()
				mp.Set(protoreflect.ValueOfString("a").MapKey(), sub("Maps"))
			},
			"({ a: { f = {} } })\n"},
		{"dotted list", []string{"ints.f"},
			func(m *dynamicpb.Message) { m.Set(fields.ByName("ints"), sub("Ints")) },
			"([])\n"},
		{"dotted map", []string{"maps.f"},
			func(m *dynamicpb.Message) { m.Set(fields.ByName("maps"), sub("Maps")) },
			"({})\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := dynamicpb.NewMessage(md)
			tc.build(msg)

			row, err := pxf.AppendDatasetRow(nil, msg, tc.columns)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(row))

			head, err := pxf.AppendDatasetHeader(nil, md, tc.columns)
			require.NoError(t, err)
			got, err := bindRows(t, md, string(head)+string(row))
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.True(t, proto.Equal(msg, got[0]), "read back %v", got[0])
		})
	}
}

// A list with elements is untouched, and an unannotated empty list stays
// an empty cell: only (pxf.required) changes what is written.
func TestAppendDatasetRow_RequiredListWithElements(t *testing.T) {
	fd := compileRequiredList(t)
	md := fd.Messages().ByName("Ints")
	msg := dynamicpb.NewMessage(md)
	msg.Mutable(md.Fields().ByName("f")).List().Append(protoreflect.ValueOfInt32(7))
	row, err := pxf.AppendDatasetRow(nil, msg, []string{"f"})
	require.NoError(t, err)
	assert.Equal(t, "([ 7 ])\n", string(row))

	outer := fd.Messages().ByName("Outer")
	row, err = pxf.AppendDatasetRow(nil, dynamicpb.NewMessage(outer), []string{"list"})
	require.NoError(t, err)
	assert.Equal(t, "()\n", string(row))
}
