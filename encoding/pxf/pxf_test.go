// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"
	"time"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const testProtoSrc = `
syntax = "proto3";
package test.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";

message AllTypes {
  string string_field = 1;
  int32 int32_field = 2;
  int64 int64_field = 3;
  uint32 uint32_field = 4;
  uint64 uint64_field = 5;
  float float_field = 6;
  double double_field = 7;
  bool bool_field = 8;
  bytes bytes_field = 9;
  Status enum_field = 10;
  Nested nested_field = 11;
  repeated string repeated_string = 12;
  repeated Nested repeated_nested = 13;
  map<string, string> string_map = 14;
  map<string, Nested> nested_map = 15;
  map<int32, string> int_map = 16;
  google.protobuf.Timestamp ts_field = 17;
  google.protobuf.Duration dur_field = 18;
  oneof choice {
    string text_choice = 19;
    int32 number_choice = 20;
  }
  google.protobuf.StringValue nullable_string = 21;
  google.protobuf.Int32Value nullable_int = 22;
  google.protobuf.BoolValue nullable_bool = 23;
}

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_ACTIVE = 1;
  STATUS_INACTIVE = 2;
}

message Nested {
  string name = 1;
  int32 value = 2;
}
`

func compileTestProto(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"test.proto": testProtoSrc,
				}),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	for _, f := range result {
		if f.Path() == "test.proto" {
			return f
		}
	}
	t.Fatal("test.proto not found")
	return nil
}

func msgDesc(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileTestProto(t)
	md := fd.Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md, "message %q not found", name)
	return md
}

func TestScalarFields(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
string_field = "hello"
int32_field = -42
int64_field = 1234567890
uint32_field = 100
uint64_field = 999999999
float_field = 3.14
double_field = 2.718
bool_field = true
bytes_field = b"AQID"
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	get := func(name string) protoreflect.Value {
		return msg.ProtoReflect().Get(desc.Fields().ByName(protoreflect.Name(name)))
	}

	assert.Equal(t, "hello", get("string_field").String())
	assert.Equal(t, int64(-42), get("int32_field").Int())
	assert.Equal(t, int64(1234567890), get("int64_field").Int())
	assert.Equal(t, uint64(100), get("uint32_field").Uint())
	assert.Equal(t, uint64(999999999), get("uint64_field").Uint())
	assert.InDelta(t, 3.14, get("float_field").Float(), 0.01)
	assert.InDelta(t, 2.718, get("double_field").Float(), 0.001)
	assert.True(t, get("bool_field").Bool())
	assert.Equal(t, []byte{1, 2, 3}, get("bytes_field").Bytes())
}

func TestBytesFieldURLSafeBase64(t *testing.T) {
	// RFC 4648 §5 URL-safe alphabet (draft §3.7): the lexer accepts it,
	// so both decode paths must too. 0xff 0xef encodes as "_-8=" in the
	// URL-safe alphabet ("/+8=" in standard).
	desc := msgDesc(t, "AllTypes")
	input := []byte(`bytes_field = b"_-8="`)

	msg, err := pxf.UnmarshalDescriptor(input, desc)
	require.NoError(t, err)
	got := msg.ProtoReflect().Get(desc.Fields().ByName("bytes_field")).Bytes()
	assert.Equal(t, []byte{0xff, 0xef}, got)

	doc, err := pxf.Parse(input)
	require.NoError(t, err)
	bv := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.BytesVal)
	assert.Equal(t, []byte{0xff, 0xef}, bv.Value)
}

func TestEnumField(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `enum_field = STATUS_ACTIVE`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("enum_field")
	assert.Equal(t, protoreflect.EnumNumber(1), msg.ProtoReflect().Get(fd).Enum())
}

func TestEnumByNumber(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `enum_field = 2`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("enum_field")
	assert.Equal(t, protoreflect.EnumNumber(2), msg.ProtoReflect().Get(fd).Enum())
}

func TestNestedMessage(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
nested_field {
  name = "inner"
  value = 42
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("nested_field")
	sub := msg.ProtoReflect().Get(fd).Message()
	nestedDesc := fd.Message()
	assert.Equal(t, "inner", sub.Get(nestedDesc.Fields().ByName("name")).String())
	assert.Equal(t, int64(42), sub.Get(nestedDesc.Fields().ByName("value")).Int())
}

func TestRepeatedScalars(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	t.Run("with commas", func(t *testing.T) {
		input := `repeated_string = ["a", "b", "c"]`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)

		fd := desc.Fields().ByName("repeated_string")
		list := msg.ProtoReflect().Get(fd).List()
		assert.Equal(t, 3, list.Len())
		assert.Equal(t, "a", list.Get(0).String())
		assert.Equal(t, "c", list.Get(2).String())
	})

	t.Run("without commas", func(t *testing.T) {
		input := `
repeated_string = [
  "alpha"
  "beta"
  "gamma"
]`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)

		fd := desc.Fields().ByName("repeated_string")
		list := msg.ProtoReflect().Get(fd).List()
		assert.Equal(t, 3, list.Len())
		assert.Equal(t, "alpha", list.Get(0).String())
	})
}

func TestRepeatedMessages(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
repeated_nested = [
  {
    name = "first"
    value = 1
  }
  {
    name = "second"
    value = 2
  }
]`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("repeated_nested")
	list := msg.ProtoReflect().Get(fd).List()
	assert.Equal(t, 2, list.Len())

	nestedDesc := fd.Message()
	first := list.Get(0).Message()
	assert.Equal(t, "first", first.Get(nestedDesc.Fields().ByName("name")).String())
	assert.Equal(t, int64(1), first.Get(nestedDesc.Fields().ByName("value")).Int())
}

func TestStringMap(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
string_map = {
  env: "prod"
  team: "platform"
  "hello world": "value"
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("string_map")
	m := msg.ProtoReflect().Get(fd).Map()
	assert.Equal(t, 3, m.Len())
	assert.Equal(t, "prod", m.Get(protoreflect.ValueOfString("env").MapKey()).String())
	assert.Equal(t, "value", m.Get(protoreflect.ValueOfString("hello world").MapKey()).String())
}

func TestNestedMap(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
nested_map = {
  primary: {
    name = "node-1"
    value = 10
  }
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("nested_map")
	m := msg.ProtoReflect().Get(fd).Map()
	assert.Equal(t, 1, m.Len())

	entry := m.Get(protoreflect.ValueOfString("primary").MapKey())
	nestedDesc := fd.MapValue().Message()
	assert.Equal(t, "node-1", entry.Message().Get(nestedDesc.Fields().ByName("name")).String())
}

func TestIntMap(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
int_map = {
  404: "Not Found"
  500: "Error"
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("int_map")
	m := msg.ProtoReflect().Get(fd).Map()
	assert.Equal(t, 2, m.Len())
	assert.Equal(t, "Not Found", m.Get(protoreflect.ValueOfInt32(404).MapKey()).String())
}

func TestTimestamp(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `ts_field = 2024-01-15T10:30:00Z`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("ts_field")
	sub := msg.ProtoReflect().Get(fd).Message()
	tsDesc := fd.Message()
	secs := sub.Get(tsDesc.Fields().ByName("seconds")).Int()
	expected := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	assert.Equal(t, expected.Unix(), secs)
}

func TestDuration(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `dur_field = 1h30m45s`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("dur_field")
	sub := msg.ProtoReflect().Get(fd).Message()
	durDesc := fd.Message()
	secs := sub.Get(durDesc.Fields().ByName("seconds")).Int()
	assert.Equal(t, int64(5445), secs) // 1*3600 + 30*60 + 45
}

func TestOneof(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	t.Run("text choice", func(t *testing.T) {
		input := `text_choice = "hello"`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)

		fd := desc.Fields().ByName("text_choice")
		assert.Equal(t, "hello", msg.ProtoReflect().Get(fd).String())
	})

	t.Run("number choice", func(t *testing.T) {
		input := `number_choice = 42`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)

		fd := desc.Fields().ByName("number_choice")
		assert.Equal(t, int64(42), msg.ProtoReflect().Get(fd).Int())
	})

	t.Run("conflict", func(t *testing.T) {
		input := `
text_choice = "hello"
number_choice = 42
`
		_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "oneof")
	})
}

func TestWrapperTypes(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
nullable_string = "present"
nullable_int = 42
nullable_bool = true
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	// StringValue
	fd := desc.Fields().ByName("nullable_string")
	sub := msg.ProtoReflect().Get(fd).Message()
	assert.Equal(t, "present", sub.Get(fd.Message().Fields().ByName("value")).String())

	// Int32Value
	fd = desc.Fields().ByName("nullable_int")
	sub = msg.ProtoReflect().Get(fd).Message()
	assert.Equal(t, int64(42), sub.Get(fd.Message().Fields().ByName("value")).Int())

	// BoolValue
	fd = desc.Fields().ByName("nullable_bool")
	sub = msg.ProtoReflect().Get(fd).Message()
	assert.True(t, sub.Get(fd.Message().Fields().ByName("value")).Bool())
}

func TestMultiLineString(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := "string_field = \"\"\"\n  hello\n  world\n  \"\"\""
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("string_field")
	assert.Equal(t, "hello\nworld", msg.ProtoReflect().Get(fd).String())
}

func TestComments(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
# hash comment
string_field = "hello" // line comment
/* block
   comment */
int32_field = 42
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("string_field")
	assert.Equal(t, "hello", msg.ProtoReflect().Get(fd).String())
	fd = desc.Fields().ByName("int32_field")
	assert.Equal(t, int64(42), msg.ProtoReflect().Get(fd).Int())
}

func TestTypeDirective(t *testing.T) {
	input := `@type test.v1.AllTypes
string_field = "hello"
`
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "test.v1.AllTypes", doc.TypeURL)
	assert.Len(t, doc.Entries, 1)
}

func TestErrorUnknownField(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `nonexistent = "value"`
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestErrorWrongOperatorInMap(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
string_map = {
  key = "value"
}
`
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "use ':'")
}

func TestErrorWrongOperatorInMessage(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `string_field: "value"`
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "use '='")
}

func TestRoundTrip(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `
string_field = "round trip"
int32_field = 123
bool_field = true
enum_field = STATUS_ACTIVE
nested_field {
  name = "nested"
  value = 7
}
repeated_string = ["x", "y"]
string_map = {
  a: "1"
}
ts_field = 2024-06-15T12:00:00Z
dur_field = 5m30s
nullable_string = "wrapped"
`
	msg1, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	encoded, err := pxf.Marshal(msg1)
	require.NoError(t, err)

	msg2, err := pxf.UnmarshalDescriptor(encoded, desc)
	require.NoError(t, err)

	// Compare field values
	get := func(msg protoreflect.Message, name string) protoreflect.Value {
		return msg.Get(desc.Fields().ByName(protoreflect.Name(name)))
	}

	assert.Equal(t, get(msg1.ProtoReflect(), "string_field").String(), get(msg2.ProtoReflect(), "string_field").String())
	assert.Equal(t, get(msg1.ProtoReflect(), "int32_field").Int(), get(msg2.ProtoReflect(), "int32_field").Int())
	assert.Equal(t, get(msg1.ProtoReflect(), "bool_field").Bool(), get(msg2.ProtoReflect(), "bool_field").Bool())
	assert.Equal(t, get(msg1.ProtoReflect(), "enum_field").Enum(), get(msg2.ProtoReflect(), "enum_field").Enum())
}

func TestEmptyContainers(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	t.Run("empty list", func(t *testing.T) {
		input := `repeated_string = []`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)
		fd := desc.Fields().ByName("repeated_string")
		assert.Equal(t, 0, msg.ProtoReflect().Get(fd).List().Len())
	})

	t.Run("empty map", func(t *testing.T) {
		input := `string_map = {}`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)
		fd := desc.Fields().ByName("string_map")
		assert.Equal(t, 0, msg.ProtoReflect().Get(fd).Map().Len())
	})

	t.Run("empty nested", func(t *testing.T) {
		input := `nested_field {}`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)
		fd := desc.Fields().ByName("nested_field")
		assert.True(t, msg.ProtoReflect().Has(fd))
	})
}

func TestNegativeDuration(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	input := `dur_field = -30s`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("dur_field")
	sub := msg.ProtoReflect().Get(fd).Message()
	durDesc := fd.Message()
	secs := sub.Get(durDesc.Fields().ByName("seconds")).Int()
	assert.Equal(t, int64(-30), secs)
}
