// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const nullTestProtoSrc = `
syntax = "proto3";
package nulltest.v1;

import "google/protobuf/field_mask.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";

message NullDemo {
  string name = 1;
  google.protobuf.StringValue role = 2;
  int32 age = 3;
  google.protobuf.StringValue email = 4;
  bool active = 5;
  Nested nested = 6;
  repeated string tags = 7;
  map<string, string> labels = 8;
  google.protobuf.StringValue nullable_string = 9;
  oneof choice {
    google.protobuf.StringValue text_choice = 10;
    int32 number_choice = 11;
  }
  optional int32 opt = 12;
  oneof scalar_choice {
    string scalar_text = 13;
    int32 scalar_number = 14;
  }
  google.protobuf.FieldMask _null = 15;
}

message Nested {
  google.protobuf.StringValue value = 1;
}

// A required field that may be null is a wrapper (#150).
message RequiredWrapper {
  google.protobuf.StringValue name = 1 [(pxf.required) = true];
}
`

// Annotations proto source to be compiled alongside test proto.
const annotationsProtoSrc = `
syntax = "proto3";
package pxf;

import "google/protobuf/descriptor.proto";

extend google.protobuf.FieldOptions {
  bool required = 1314;
  string default = 1315;
  string key = 1316;
}
`

// annotatedTestProtoSrc uses pxf annotations.
const annotatedTestProtoSrc = `
syntax = "proto3";
package annotated.v1;

import "google/protobuf/field_mask.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";

message Config {
  string name = 1 [(pxf.required) = true];
  string role = 2 [(pxf.default) = "viewer"];
  int32 priority = 3 [(pxf.default) = "5"];
  bool enabled = 4 [(pxf.default) = "true"];
  string email = 5;
  bytes token = 6 [(pxf.default) = "AQID"];
  double weight = 7 [(pxf.default) = "0.75"];
  google.protobuf.Timestamp created_at = 8 [(pxf.default) = "2024-01-15T10:30:00Z"];
  google.protobuf.Duration timeout = 9 [(pxf.default) = "30s"];
  google.protobuf.StringValue nickname = 10 [(pxf.default) = "anon"];
  Status status = 11 [(pxf.default) = "STATUS_ACTIVE"];
  Endpoint endpoint = 12;

  google.protobuf.FieldMask _null = 15;
}

message Endpoint {
  string host = 1 [(pxf.required) = true];
  int32 port = 2 [(pxf.default) = "8080"];
}

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_ACTIVE = 1;
  STATUS_INACTIVE = 2;
}
`

func compileNullTestProto(t *testing.T, protoName, protoSrc string) protoreflect.FileDescriptor {
	t.Helper()
	sources := map[string]string{
		protoName:               protoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
			},
		),
	}
	result, err := comp.Compile(context.Background(), protoName)
	require.NoError(t, err)
	for _, f := range result {
		if f.Path() == protoName {
			return f
		}
	}
	t.Fatalf("%s not found", protoName)
	return nil
}

func nullDemoDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileNullTestProto(t, "null_test.proto", nullTestProtoSrc)
	md := fd.Messages().ByName("NullDemo")
	require.NotNil(t, md)
	return md
}

func configDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileNullTestProto(t, "annotated_test.proto", annotatedTestProtoSrc)
	md := fd.Messages().ByName("Config")
	require.NotNil(t, md)
	return md
}

// --- Lexer / Parser / Formatter ---

func TestNullLexer(t *testing.T) {
	input := `name = null`
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)
	require.Len(t, doc.Entries, 1)
}

func TestNullParseAndFormat(t *testing.T) {
	input := `name = null
`
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)
	require.Len(t, doc.Entries, 1)

	out := pxf.FormatDocument(doc)
	assert.Equal(t, "name = null\n", string(out))
}

// --- UnmarshalFull: field presence tracking ---

func TestUnmarshalFullIsNullIsAbsentIsSet(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
email = null
age = 30
`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	assert.True(t, result.IsSet("name"), "name should be set")
	assert.False(t, result.IsNull("name"), "name should not be null")
	assert.False(t, result.IsAbsent("name"), "name should not be absent")

	assert.True(t, result.IsNull("email"), "email should be null")
	assert.False(t, result.IsSet("email"), "email should not be set")
	assert.False(t, result.IsAbsent("email"), "email should not be absent")

	assert.True(t, result.IsAbsent("role"), "role should be absent")
	assert.False(t, result.IsNull("role"), "role should not be null")
	assert.False(t, result.IsSet("role"), "role should not be set")

	assert.True(t, result.IsSet("age"), "age should be set")

	// Verify the proto message has the right values
	assert.Equal(t, "Alice", msg.ProtoReflect().Get(desc.Fields().ByName("name")).String())
	assert.Equal(t, int64(30), msg.ProtoReflect().Get(desc.Fields().ByName("age")).Int())
}

func TestUnmarshalFullNullFields(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
nested = null
email = null
`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	nulls := result.NullFields()
	assert.Len(t, nulls, 2)
	assert.Contains(t, nulls, "nested")
	assert.Contains(t, nulls, "email")
}

// --- Null mask wire round-trip ---

func TestNullMaskPopulation(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
email = null
role = null
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// The _null FieldMask should contain email and role
	nullFd := desc.Fields().ByName("_null")
	require.NotNil(t, nullFd)
	assert.True(t, msg.ProtoReflect().Has(nullFd), "_null field should be set")

	fmMsg := msg.ProtoReflect().Get(nullFd).Message()
	pathsFd := fmMsg.Descriptor().Fields().ByName("paths")
	list := fmMsg.Get(pathsFd).List()
	paths := make([]string, list.Len())
	for i := range list.Len() {
		paths[i] = list.Get(i).String()
	}
	assert.Contains(t, paths, "email")
	assert.Contains(t, paths, "role")
	assert.NotContains(t, paths, "name")
}

func TestNullMaskBinaryRoundTrip(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
email = null
age = 30
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// Marshal to protobuf binary
	bin, err := proto.Marshal(msg)
	require.NoError(t, err)

	// Unmarshal from binary
	msg2 := dynamicpb.NewMessage(desc)
	require.NoError(t, proto.Unmarshal(bin, msg2))

	// Marshal back to PXF — email should be null, not absent
	out, err := pxf.Marshal(msg2)
	require.NoError(t, err)

	assert.Contains(t, string(out), "email = null")
	assert.Contains(t, string(out), `name = "Alice"`)
	assert.Contains(t, string(out), "age = 30")
}

// --- Required field validation ---

func TestRequiredFieldPresent(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `name = "Alice"`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	assert.Equal(t, "Alice", msg.ProtoReflect().Get(desc.Fields().ByName("name")).String())
}

func TestRequiredFieldNull(t *testing.T) {
	fd := compileNullTestProto(t, "null_test.proto", nullTestProtoSrc)
	desc := fd.Messages().ByName("RequiredWrapper")
	msg := dynamicpb.NewMessage(desc)

	// null counts as present for required
	input := `name = null`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
}

func TestRequiredFieldAbsent(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `role = "admin"`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
	assert.Contains(t, err.Error(), "name")
}

// --- Default values ---

func TestDefaultAppliedOnAbsent(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `name = "Alice"`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	r := msg.ProtoReflect()

	// string
	assert.Equal(t, "viewer", r.Get(desc.Fields().ByName("role")).String())
	assert.True(t, result.IsAbsent("role"))

	// int32
	assert.Equal(t, int64(5), r.Get(desc.Fields().ByName("priority")).Int())

	// bool
	assert.True(t, r.Get(desc.Fields().ByName("enabled")).Bool())

	// double
	assert.InDelta(t, 0.75, r.Get(desc.Fields().ByName("weight")).Float(), 0.001)

	// bytes (base64 "AQID" = []byte{1,2,3})
	assert.Equal(t, []byte{1, 2, 3}, r.Get(desc.Fields().ByName("token")).Bytes())

	// Timestamp
	tsFd := desc.Fields().ByName("created_at")
	assert.True(t, r.Has(tsFd), "created_at should be set")
	tsMsg := r.Get(tsFd).Message()
	secs := tsMsg.Get(tsMsg.Descriptor().Fields().ByName("seconds")).Int()
	assert.Equal(t, int64(1705314600), secs)

	// Duration (30s)
	durFd := desc.Fields().ByName("timeout")
	assert.True(t, r.Has(durFd), "timeout should be set")
	durMsg := r.Get(durFd).Message()
	durSecs := durMsg.Get(durMsg.Descriptor().Fields().ByName("seconds")).Int()
	assert.Equal(t, int64(30), durSecs)

	// Wrapper type (StringValue)
	nickFd := desc.Fields().ByName("nickname")
	assert.True(t, r.Has(nickFd), "nickname should be set")
	nickMsg := r.Get(nickFd).Message()
	assert.Equal(t, "anon", nickMsg.Get(nickMsg.Descriptor().Fields().ByName("value")).String())

	// Enum
	assert.Equal(t, protoreflect.EnumNumber(1), r.Get(desc.Fields().ByName("status")).Enum())
}

func TestDefaultNotAppliedOnNull(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
nickname = null
`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// nickname should NOT get its default because it was explicitly null
	assert.True(t, result.IsNull("nickname"))
	assert.False(t, msg.ProtoReflect().Has(desc.Fields().ByName("nickname")))
}

func TestDefaultNotAppliedOnSet(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
role = "admin"
`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	assert.True(t, result.IsSet("role"))
	assert.Equal(t, "admin", msg.ProtoReflect().Get(desc.Fields().ByName("role")).String())
}

// --- Nested required and default ---

func TestNestedRequiredPresent(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
endpoint {
  host = "localhost"
}
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// port should get default 8080
	epFd := desc.Fields().ByName("endpoint")
	epMsg := msg.ProtoReflect().Get(epFd).Message()
	assert.Equal(t, int64(8080), epMsg.Get(epMsg.Descriptor().Fields().ByName("port")).Int())
}

func TestNestedRequiredAbsent(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	// endpoint is present but host (required) is missing inside it
	input := `
name = "Alice"
endpoint {
  port = 9090
}
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
	assert.Contains(t, err.Error(), "endpoint.host")
}

func TestNestedDefaultApplied(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
endpoint {
  host = "localhost"
}
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	epFd := desc.Fields().ByName("endpoint")
	epMsg := msg.ProtoReflect().Get(epFd).Message()
	portFd := epMsg.Descriptor().Fields().ByName("port")
	assert.Equal(t, int64(8080), epMsg.Get(portFd).Int())
}

func TestNestedAbsentMessageNotValidated(t *testing.T) {
	desc := configDesc(t)
	msg := dynamicpb.NewMessage(desc)

	// endpoint is absent entirely — its inner required fields should NOT trigger errors
	input := `name = "Alice"`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
}

// --- Null in lists and maps (errors) ---

func TestNullInListError(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `tags = ["a", null, "b"]`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
	assert.Contains(t, err.Error(), "repeated")
}

func TestNullInMapError(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `labels = { key: null }`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
	assert.Contains(t, err.Error(), "map")
}

// --- Null on oneof ---

func TestNullOnOneof(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `text_choice = null`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	assert.True(t, result.IsNull("text_choice"))
}

func TestNullOneofConflict(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
text_choice = null
number_choice = 42
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oneof")
}

// --- Null on nested message ---

func TestNullNestedMessage(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `nested = null`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	assert.True(t, result.IsNull("nested"))
	assert.False(t, msg.ProtoReflect().Has(desc.Fields().ByName("nested")))
}

func TestNullInsideNestedMessage(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
nested {
  value = null
}
`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// Nested is present (set), but its inner field is null
	assert.True(t, result.IsSet("nested"), "nested should be set")
	assert.True(t, result.IsNull("nested.value"), "nested.value should be null")
	assert.False(t, result.IsAbsent("nested.value"), "nested.value should not be absent")

	// The _null FieldMask should contain dotted path
	nullFd := desc.Fields().ByName("_null")
	require.NotNil(t, nullFd)
	assert.True(t, msg.ProtoReflect().Has(nullFd))

	fmMsg := msg.ProtoReflect().Get(nullFd).Message()
	pathsFd := fmMsg.Descriptor().Fields().ByName("paths")
	list := fmMsg.Get(pathsFd).List()
	paths := make([]string, list.Len())
	for i := range list.Len() {
		paths[i] = list.Get(i).String()
	}
	assert.Contains(t, paths, "nested.value")
}

func TestNullInsideNestedBinaryRoundTrip(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
nested {
  value = null
}
`
	_, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// Binary round-trip
	bin, err := proto.Marshal(msg)
	require.NoError(t, err)

	msg2 := dynamicpb.NewMessage(desc)
	require.NoError(t, proto.Unmarshal(bin, msg2))

	out, err := pxf.Marshal(msg2)
	require.NoError(t, err)

	assert.Contains(t, string(out), "value = null")
	assert.Contains(t, string(out), `name = "Alice"`)
}

// --- Null on wrapper type ---

func TestNullWrapperType(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `nullable_string = null`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	assert.True(t, result.IsNull("nullable_string"))
	assert.False(t, msg.ProtoReflect().Has(desc.Fields().ByName("nullable_string")))
}

// --- Backward compatibility ---

func TestBackwardCompatPlainUnmarshalWithNull(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	// Plain Unmarshal (not Full) should accept null without error
	input := `
name = "Alice"
email = null
`
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	// email should simply be unset
	assert.False(t, msg.ProtoReflect().Has(desc.Fields().ByName("email")))
}

// --- Null in list/map via plain Unmarshal ---

func TestPlainUnmarshalNullInListError(t *testing.T) {
	desc := nullDemoDesc(t)

	input := `tags = ["a", null]`
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
}

func TestPlainUnmarshalNullInMapError(t *testing.T) {
	desc := nullDemoDesc(t)

	input := `labels = { key: null }`
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
}

// --- Encode with NullFields Result ---

func TestMarshalWithNullFieldsResult(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)

	input := `
name = "Alice"
email = null
age = 30
`
	result, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)

	// Marshal using NullFields result (not null_mask on message)
	// First clear the null mask to test the Result path
	nullFd := desc.Fields().ByName("_null")
	msg.ProtoReflect().Clear(nullFd)

	out, err := pxf.MarshalOptions{NullFields: result}.Marshal(msg)
	require.NoError(t, err)

	assert.Contains(t, string(out), "email = null")
	assert.Contains(t, string(out), `name = "Alice"`)
}

// --- Null where the field cannot hold it (#150, trendvidia/protowire#339) ---

// null is a decode error on a field that is not a singular message
// field: a plain scalar, one with explicit presence (proto3 optional, a
// oneof member), a repeated field and a map field. Every decode path
// rejects it, at the null, naming the field.
func TestNullRejectedWhereNotNullable(t *testing.T) {
	desc := nullDemoDesc(t)
	for _, tc := range []struct{ doc, want string }{
		{`name = null`, `1:8: null is not allowed on scalar field "name": declare it as a wrapper type (google.protobuf.StringValue`},
		{`age = null`, `1:7: null is not allowed on scalar field "age": declare it as a wrapper type (google.protobuf.Int32Value`},
		{`active = null`, `google.protobuf.BoolValue`},
		{`opt = null`, `null is not allowed on scalar field "opt"`},
		{`scalar_text = null`, `null is not allowed on scalar field "scalar_text"`},
		{`tags = null`, `null is not allowed on repeated field "tags"`},
		{`labels = null`, `null is not allowed on map field "labels"`},
		{"nested {\n  value = \"v\"\n}\nage = null", `4:7: null is not allowed on scalar field "age"`},
	} {
		for name, decode := range map[string]func() error{
			"Unmarshal":     func() error { return pxf.Unmarshal([]byte(tc.doc), dynamicpb.NewMessage(desc)) },
			"UnmarshalFull": func() error { _, err := pxf.UnmarshalFull([]byte(tc.doc), dynamicpb.NewMessage(desc)); return err },
		} {
			err := decode()
			require.Error(t, err, "%s %q", name, tc.doc)
			assert.Contains(t, err.Error(), tc.want, "%s %q", name, tc.doc)
		}
	}
}

// Marshal writes `field = null` only for a field that may hold it. A
// scalar named in a _null mask (one from another producer, or from
// before #150) is written as its value, so Marshal's output decodes
// (#151).
func TestMarshalNullOnlyWhereNullable(t *testing.T) {
	desc := nullDemoDesc(t)
	msg := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(`name = "Alice" age = 3`), msg))
	mask := msg.Mutable(desc.Fields().ByName("_null")).Message()
	paths := mask.Mutable(mask.Descriptor().Fields().ByName("paths")).List()
	for _, p := range []string{"email", "name", "age", "tags"} {
		paths.Append(protoreflect.ValueOfString(p))
	}
	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "email = null")
	assert.Contains(t, string(out), `name = "Alice"`)
	assert.Contains(t, string(out), "age = 3")
	assert.NotContains(t, string(out), "tags = null")

	back := dynamicpb.NewMessage(desc)
	_, err = pxf.UnmarshalFull(out, back)
	require.NoError(t, err, "Marshal's output decodes")
}
