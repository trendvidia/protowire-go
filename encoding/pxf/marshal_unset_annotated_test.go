// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Marshal and an unset annotated field (#259). A reader that applies
// (pxf.default) to an absent field and checks (pxf.required) (#161) would
// not read a left-out annotated field back as unset, so Marshal writes
// such a field whenever text can say what it holds. The draft requires
// decode_PXF(encode_PXF(v)) == v (Common Schema Layer).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const unsetAnnotatedProtoSrc = `
syntax = "proto3";
package unsetann.v1;

import "google/protobuf/wrappers.proto";
import "google/protobuf/timestamp.proto";
import "pxf/annotations.proto";

message M {
  int32 n = 1 [(pxf.default) = "5"];
  string s = 2 [(pxf.default) = "\"dflt\""];
  bool b = 3 [(pxf.default) = "true"];
  int32 req = 4 [(pxf.required) = true];
  repeated int32 rl = 5 [(pxf.required) = true];
  map<string, int32> rm = 6 [(pxf.required) = true];
  google.protobuf.Int32Value w = 7 [(pxf.default) = "7"];
  google.protobuf.Timestamp ts = 8 [(pxf.default) = "2024-01-01T00:00:00Z"];
  oneof o {
    int32 a = 9 [(pxf.default) = "8"];
    string other = 10;
  }
  int32 plain = 11;
  Sub sub = 12;
}

message Sub {
  int32 k = 1 [(pxf.default) = "3"];
  int32 plain = 2;
}

message Opt {
  optional int32 o = 1 [(pxf.default) = "6"];
}

message ReqOpt {
  optional int32 o = 1 [(pxf.required) = true];
  int32 plain = 2;
}
`

func unsetAnnotatedDesc(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	return compilePlacementSrc(t, unsetAnnotatedProtoSrc, "unsetann.v1", name)
}

// fullReadBack marshals m and reads the text back with defaults applied
// and required fields checked, returning the text and what it read.
func fullReadBack(t *testing.T, m *dynamicpb.Message) (string, *dynamicpb.Message) {
	t.Helper()
	out, err := pxf.Marshal(m)
	require.NoError(t, err)
	back, _, err := pxf.UnmarshalFullDescriptor(out, m.Descriptor())
	require.NoError(t, err, "%s", out)
	return string(out), back
}

// The issue's case: a zero proto3 scalar with a default reads back zero.
func TestMarshal_ZeroDefaultedScalarReadsBack(t *testing.T) {
	desc := unsetAnnotatedDesc(t, "Sub")
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("plain"), protoreflect.ValueOfInt32(1))

	out, back := fullReadBack(t, m)
	assert.Equal(t, "k = 0\nplain = 1\n", out)
	assert.True(t, proto.Equal(m, back), "%s read back as %v", out, back)
}

// Every unset annotated shape at once, top level and one level down, with
// nothing but plain fields set.
func TestMarshal_UnsetAnnotatedFieldsReadBack(t *testing.T) {
	desc := unsetAnnotatedDesc(t, "M")
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("plain"), protoreflect.ValueOfInt32(1))
	sub := m.Mutable(desc.Fields().ByName("sub")).Message()
	sub.Set(sub.Descriptor().Fields().ByName("plain"), protoreflect.ValueOfInt32(2))

	out, back := fullReadBack(t, m)
	assert.Equal(t, `n = 0
s = ""
b = false
req = 0
rl = []
rm = {}
w = null
ts = null
plain = 1
sub {
  k = 0
  plain = 2
}
`, out)

	// The oneof member with a default is the one field that reads back
	// as its default: Marshal leaves it out rather than write null (#267, owner's choice).
	a := desc.Fields().ByName("a")
	assert.Equal(t, int64(8), back.Get(a).Int())
	back.Clear(a)
	assert.True(t, proto.Equal(m, back), "%s read back as %v", out, back)
}

// A set oneof member keeps its sibling's default off, so the sibling is
// not written.
func TestMarshal_OneofSiblingKeepsDefaultOff(t *testing.T) {
	desc := unsetAnnotatedDesc(t, "M")
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("other"), protoreflect.ValueOfString("x"))

	out, back := fullReadBack(t, m)
	assert.NotContains(t, out, "a = ")
	assert.True(t, proto.Equal(m, back), "%s read back as %v", out, back)
}

// An unset `optional` scalar with a default is left out and reads back
// as its default: the owner chose this in #267, and draft -01 § Common
// Schema Layer names it as an exception to the round trip. `o = null`
// would read back unset (trendvidia/protowire#339 option 3).
func TestMarshal_UnsetOptionalDefaultedReadsBackDefault(t *testing.T) {
	desc := unsetAnnotatedDesc(t, "Opt")
	out, back := fullReadBack(t, dynamicpb.NewMessage(desc))
	assert.Empty(t, out)
	assert.Equal(t, int64(6), back.Get(desc.Fields().ByName("o")).Int())

	back, _, err := pxf.UnmarshalFullDescriptor([]byte("o = null\n"), desc)
	require.NoError(t, err)
	assert.False(t, back.Has(desc.Fields().ByName("o")), "null is the text that keeps it unset")
}

// Pinned limit (#267): in a message with a _null mask, every null read is
// recorded in the mask, so an unset defaulted field has no text that
// reads back as "unset and not null". Written `null`, as Marshal writes a
// message-typed one, it reads back with the mask naming it; left out, it
// reads back as its default. Neither equals the message. No writer can
// avoid this.
func TestMarshal_UnsetDefaultedInNullMaskMessageHasNoText(t *testing.T) {
	desc := compilePlacementSrc(t, `
syntax = "proto3";
package unsetmask.v1;
import "google/protobuf/field_mask.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";
message N {
  google.protobuf.Int32Value w = 1 [(pxf.default) = "7"];
  int32 plain = 2;
  google.protobuf.FieldMask _null = 3;
}`, "unsetmask.v1", "N")
	w, mask := desc.Fields().ByName("w"), desc.Fields().ByName("_null")
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("plain"), protoreflect.ValueOfInt32(1))

	out, back := fullReadBack(t, m)
	assert.Equal(t, "w = null\nplain = 1\n", out)
	assert.False(t, back.Has(w))
	assert.Equal(t, []string{"w"}, readMaskPaths(back))
	assert.False(t, proto.Equal(m, back), "the mask names w")

	back, _, err := pxf.UnmarshalFullDescriptor([]byte("plain = 1\n"), desc)
	require.NoError(t, err)
	assert.Equal(t, int64(7), back.Get(w).Message().Get(w.Message().Fields().ByName("value")).Int())
	assert.False(t, back.Has(mask))
}

// An unset required field with presence has no text a reader accepts:
// absent is rejected, and so is null (#268). So Marshal refuses it. With
// EmitDefaults it is written as zero, which a reader accepts, as before.
func TestMarshal_UnsetRequiredOptionalRefused(t *testing.T) {
	desc := unsetAnnotatedDesc(t, "ReqOpt")
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("plain"), protoreflect.ValueOfInt32(1))

	_, err := pxf.Marshal(m)
	require.EqualError(t, err, "pxf: unsetann.v1.ReqOpt.o is unset, and the field is (pxf.required)")

	out, err := pxf.MarshalOptions{EmitDefaults: true}.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, "o = 0\nplain = 1\n", string(out))

	m.Set(desc.Fields().ByName("o"), protoreflect.ValueOfInt32(0))
	_, back := fullReadBack(t, m)
	assert.True(t, proto.Equal(m, back))
}
