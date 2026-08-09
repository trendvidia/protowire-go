// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// (pxf.default) is a singular-field annotation: applyDefaultImpl parses
// one PXF literal and Sets it, which protoreflect rejects on a list or
// map field. Before #66 the four shapes below panicked (lists) or
// reported a confusing MEntry message-type error (maps) — they now all
// return one error naming the placement. One message per shape so
// postDecode's first-error return exercises each independently.
const defaultPlacementProtoSrc = `
syntax = "proto3";
package defaultplacement_test.v1;

import "google/protobuf/duration.proto";
import "pxf/annotations.proto";

message ListScalar {
  repeated string tags = 1 [(pxf.default) = "ignored"];
}

message ListMessage {
  repeated google.protobuf.Duration ds = 1 [(pxf.default) = "5s"];
}

message MapScalar {
  map<string, string> m = 1 [(pxf.default) = "ignored"];
}

message MapMessage {
  map<string, google.protobuf.Duration> m = 1 [(pxf.default) = "5s"];
}

message Control {
  repeated string tags = 1;
  map<string, string> m = 2;
  string role = 3 [(pxf.default) = "viewer"];
}
`

func compileDefaultPlacement(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":            defaultPlacementProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName("defaultplacement_test.v1." + name))
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// defaultPlacementCases is the four-shape matrix from #66: the two list
// shapes panicked inside protoreflect's Set, the two map shapes reported
// the synthetic entry message's name instead of the placement.
var defaultPlacementCases = []struct {
	msg   string
	field string
	def   string
	want  string
}{
	{"ListScalar", "tags", "ignored", `default values not supported for repeated field "tags"`},
	{"ListMessage", "ds", "5s", `default values not supported for repeated field "ds"`},
	{"MapScalar", "m", "ignored", `default values not supported for map field "m"`},
	{"MapMessage", "m", "5s", `default values not supported for map field "m"`},
}

// TestApplyDefault_RepeatedAndMapFieldsError covers the exported entry
// point, which layered-config consumers (chameleon) call directly with
// descriptors the decoder never sees.
func TestApplyDefault_RepeatedAndMapFieldsError(t *testing.T) {
	for _, c := range defaultPlacementCases {
		c := c
		t.Run(c.msg, func(t *testing.T) {
			desc := compileDefaultPlacement(t, c.msg)
			msg := dynamicpb.NewMessage(desc)
			fd := desc.Fields().ByName(protoreflect.Name(c.field))
			require.NotNil(t, fd)

			var err error
			require.NotPanics(t, func() {
				err = pxf.ApplyDefault(msg, fd, c.def)
			})
			require.Error(t, err)
			assert.Equal(t, c.want, err.Error())
			if fd.IsMap() {
				assert.Equal(t, 0, msg.Get(fd).Map().Len(), "field left untouched")
			} else {
				assert.Equal(t, 0, msg.Get(fd).List().Len(), "field left untouched")
			}
		})
	}
}

// TestDecode_DefaultOnRepeatedOrMapFieldErrors covers the decode path:
// the default only fires for an absent field, so empty input is the
// repro from #66.
func TestDecode_DefaultOnRepeatedOrMapFieldErrors(t *testing.T) {
	for _, c := range defaultPlacementCases {
		c := c
		t.Run(c.msg, func(t *testing.T) {
			desc := compileDefaultPlacement(t, c.msg)

			var err error
			require.NotPanics(t, func() {
				_, _, err = pxf.UnmarshalFullDescriptor([]byte(``), desc)
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// A present field suppresses the default, so a document that sets the
// annotated field still decodes — the error is reachable only through
// the absent-field path.
func TestDecode_DefaultOnRepeatedFieldPresentStillDecodes(t *testing.T) {
	desc := compileDefaultPlacement(t, "ListScalar")
	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(`tags = ["a", "b"]`), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("tags")
	list := msg.ProtoReflect().Get(fd).List()
	require.Equal(t, 2, list.Len())
	assert.Equal(t, "a", list.Get(0).String())
	assert.Equal(t, "b", list.Get(1).String())
}

// Unannotated repeated/map fields alongside an annotated singular field
// are unaffected: the singular default still applies.
func TestDecode_DefaultOnSingularFieldUnaffected(t *testing.T) {
	desc := compileDefaultPlacement(t, "Control")
	msg, res, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)

	fields := desc.Fields()
	assert.Equal(t, "viewer", msg.ProtoReflect().Get(fields.ByName("role")).String())
	assert.Equal(t, 0, msg.ProtoReflect().Get(fields.ByName("tags")).List().Len())
	assert.Equal(t, 0, msg.ProtoReflect().Get(fields.ByName("m")).Map().Len())
	assert.False(t, res.IsSet("role"), "a default is not an input-set field")
}

// The non-Full entry points never call postDecode, so they never apply
// defaults and never reach the guard — #66 was reachable only through
// UnmarshalFull* and the exported ApplyDefault. Pins that blast radius,
// which the CHANGELOG and README both state.
func TestUnmarshalDescriptor_NeverAppliesDefaults(t *testing.T) {
	t.Run("misplaced default is unreachable", func(t *testing.T) {
		desc := compileDefaultPlacement(t, "ListScalar")

		var msg protoreflect.ProtoMessage
		var err error
		require.NotPanics(t, func() {
			msg, err = pxf.UnmarshalDescriptor([]byte(``), desc)
		})
		require.NoError(t, err)
		assert.Equal(t, 0, msg.ProtoReflect().Get(desc.Fields().ByName("tags")).List().Len())
	})

	t.Run("singular default is not applied either", func(t *testing.T) {
		desc := compileDefaultPlacement(t, "Control")
		msg, err := pxf.UnmarshalDescriptor([]byte(``), desc)
		require.NoError(t, err)
		assert.Equal(t, "", msg.ProtoReflect().Get(desc.Fields().ByName("role")).String())
	})
}
