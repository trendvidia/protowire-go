// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const anyProtoSrc = `
syntax = "proto3";
package any_test.v1;

import "google/protobuf/any.proto";

message Container {
  string name = 1;
  google.protobuf.Any payload = 2;
}

message Detail {
  int32 code = 1;
  string reason = 2;
}
`

type testResolver struct {
	descs map[string]protoreflect.MessageDescriptor
}

func (r *testResolver) FindMessageByURL(url string) (protoreflect.MessageDescriptor, error) {
	if d, ok := r.descs[url]; ok {
		return d, nil
	}
	return nil, &pxf.Error{Msg: "type not found: " + url}
}

func compileAnyProto(t *testing.T) (protoreflect.FileDescriptor, *testResolver) {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"test.proto": anyProtoSrc,
				}),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	var fd protoreflect.FileDescriptor
	for _, f := range result {
		if f.Path() == "test.proto" {
			fd = f
			break
		}
	}
	require.NotNil(t, fd)

	resolver := &testResolver{descs: map[string]protoreflect.MessageDescriptor{
		"any_test.v1.Detail": fd.Messages().ByName("Detail"),
	}}
	return fd, resolver
}

func TestAnyDecode(t *testing.T) {
	fd, resolver := compileAnyProto(t)
	containerDesc := fd.Messages().ByName("Container")
	detailDesc := fd.Messages().ByName("Detail")

	input := `
name = "test"
payload {
  @type = "any_test.v1.Detail"
  code = 42
  reason = "not found"
}
`
	opts := pxf.UnmarshalOptions{TypeResolver: resolver}
	msg, err := opts.UnmarshalDescriptor([]byte(input), containerDesc)
	require.NoError(t, err)

	assert.Equal(t, "test", msg.ProtoReflect().Get(containerDesc.Fields().ByName("name")).String())

	// Verify the Any field
	anyFd := containerDesc.Fields().ByName("payload")
	anyMsg := msg.ProtoReflect().Get(anyFd).Message()
	anyDesc := anyFd.Message()
	typeURL := anyMsg.Get(anyDesc.Fields().ByName("type_url")).String()
	valueBytes := anyMsg.Get(anyDesc.Fields().ByName("value")).Bytes()

	assert.Equal(t, "any_test.v1.Detail", typeURL)

	// Unmarshal the inner value
	inner := dynamicpb.NewMessage(detailDesc)
	require.NoError(t, proto.Unmarshal(valueBytes, inner))
	assert.Equal(t, int64(42), inner.ProtoReflect().Get(detailDesc.Fields().ByName("code")).Int())
	assert.Equal(t, "not found", inner.ProtoReflect().Get(detailDesc.Fields().ByName("reason")).String())
}

func TestAnyEncode(t *testing.T) {
	fd, resolver := compileAnyProto(t)
	containerDesc := fd.Messages().ByName("Container")
	detailDesc := fd.Messages().ByName("Detail")

	// Build a Container with an Any payload
	container := dynamicpb.NewMessage(containerDesc)
	container.Set(containerDesc.Fields().ByName("name"), protoreflect.ValueOfString("test"))

	detail := dynamicpb.NewMessage(detailDesc)
	detail.Set(detailDesc.Fields().ByName("code"), protoreflect.ValueOfInt32(42))
	detail.Set(detailDesc.Fields().ByName("reason"), protoreflect.ValueOfString("not found"))

	packed, err := proto.Marshal(detail)
	require.NoError(t, err)

	anyFd := containerDesc.Fields().ByName("payload")
	anyMsg := container.Mutable(anyFd).Message()
	anyDesc := anyFd.Message()
	anyMsg.Set(anyDesc.Fields().ByName("type_url"), protoreflect.ValueOfString("any_test.v1.Detail"))
	anyMsg.Set(anyDesc.Fields().ByName("value"), protoreflect.ValueOfBytes(packed))

	out, err := pxf.MarshalOptions{TypeResolver: resolver}.Marshal(container)
	require.NoError(t, err)

	output := string(out)
	assert.Contains(t, output, `@type = "any_test.v1.Detail"`)
	assert.Contains(t, output, `code = 42`)
	assert.Contains(t, output, `reason = "not found"`)
}

func TestDiscardUnknown(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	input := `
string_field = "hello"
unknown_field = "skip me"
int32_field = 42
unknown_block {
  nested = "also skipped"
}
bool_field = true
`
	// Without DiscardUnknown — should fail
	_, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")

	// With DiscardUnknown — should succeed, skipping unknowns
	opts := pxf.UnmarshalOptions{DiscardUnknown: true}
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	assert.Equal(t, "hello", msg.ProtoReflect().Get(desc.Fields().ByName("string_field")).String())
	assert.Equal(t, int64(42), msg.ProtoReflect().Get(desc.Fields().ByName("int32_field")).Int())
	assert.True(t, msg.ProtoReflect().Get(desc.Fields().ByName("bool_field")).Bool())
}

func TestCommentPreservation(t *testing.T) {
	input := `# File header comment
@type test.v1.AllTypes

# Field comment
string_field = "hello"

# Another comment
// Double slash comment
int32_field = 42
`
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "test.v1.AllTypes", doc.TypeURL)
	require.Len(t, doc.Entries, 2)

	// First entry should have leading comment
	a1 := doc.Entries[0].(*pxf.Assignment)
	assert.Equal(t, "string_field", a1.Key)
	require.Len(t, a1.LeadingComments, 1)
	assert.Contains(t, a1.LeadingComments[0].Text, "Field comment")

	// Second entry should have two leading comments
	a2 := doc.Entries[1].(*pxf.Assignment)
	assert.Equal(t, "int32_field", a2.Key)
	require.Len(t, a2.LeadingComments, 2)
	assert.Contains(t, a2.LeadingComments[0].Text, "Another comment")
	assert.Contains(t, a2.LeadingComments[1].Text, "Double slash")
}

func TestInlineTrailingComment(t *testing.T) {
	t.Run("last entry in block before closing brace", func(t *testing.T) {
		src := []byte("capabilities {\n  restrict = true # deny-by-default\n}\n")
		doc, err := pxf.Parse(src)
		require.NoError(t, err)
		require.Len(t, doc.Entries, 1)
		block := doc.Entries[0].(*pxf.Block)
		require.Len(t, block.Entries, 1)
		a := block.Entries[0].(*pxf.Assignment)
		assert.Equal(t, "restrict", a.Key)
		assert.Equal(t, "# deny-by-default", a.TrailingComment)
		// The comment round-trips instead of being dropped.
		assert.Equal(t, string(src), string(pxf.FormatDocument(doc)))
	})

	t.Run("last entry at top level before EOF", func(t *testing.T) {
		src := []byte(`theme_variant = "dark" # matches my terminal` + "\n")
		doc, err := pxf.Parse(src)
		require.NoError(t, err)
		require.Len(t, doc.Entries, 1)
		a := doc.Entries[0].(*pxf.Assignment)
		assert.Equal(t, "theme_variant", a.Key)
		assert.Equal(t, "# matches my terminal", a.TrailingComment)
		assert.Equal(t, string(src), string(pxf.FormatDocument(doc)))
	})

	t.Run("entry with a following entry attaches to itself not the next", func(t *testing.T) {
		src := []byte("host = \"prod\" # primary\nport = 8080\n")
		doc, err := pxf.Parse(src)
		require.NoError(t, err)
		require.Len(t, doc.Entries, 2)
		host := doc.Entries[0].(*pxf.Assignment)
		port := doc.Entries[1].(*pxf.Assignment)
		assert.Equal(t, "# primary", host.TrailingComment)
		assert.Empty(t, port.LeadingComments, "inline comment must not migrate to the next entry's leading comments")
		assert.Empty(t, port.TrailingComment)
	})

	t.Run("map entry trailing comment", func(t *testing.T) {
		src := []byte("labels {\n  team: \"core\" # owning team\n}\n")
		doc, err := pxf.Parse(src)
		require.NoError(t, err)
		block := doc.Entries[0].(*pxf.Block)
		require.Len(t, block.Entries, 1)
		m := block.Entries[0].(*pxf.MapEntry)
		assert.Equal(t, "team", m.Key)
		assert.Equal(t, "# owning team", m.TrailingComment)
		assert.Equal(t, string(src), string(pxf.FormatDocument(doc)))
	})

	t.Run("own-line comment stays leading, not trailing", func(t *testing.T) {
		src := []byte("a = 1\n# not a's trailer\nb = 2\n")
		doc, err := pxf.Parse(src)
		require.NoError(t, err)
		require.Len(t, doc.Entries, 2)
		a := doc.Entries[0].(*pxf.Assignment)
		b := doc.Entries[1].(*pxf.Assignment)
		assert.Empty(t, a.TrailingComment)
		require.Len(t, b.LeadingComments, 1)
		assert.Contains(t, b.LeadingComments[0].Text, "not a's trailer")
	})
}

func TestFormatDocument(t *testing.T) {
	input := `# Header
@type test.v1.AllTypes

# Name field
string_field = "hello"
int32_field = 42
`
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)

	output := string(pxf.FormatDocument(doc))
	assert.Contains(t, output, "@type test.v1.AllTypes")
	assert.Contains(t, output, "# Name field")
	assert.Contains(t, output, `string_field = "hello"`)
	assert.Contains(t, output, "int32_field = 42")
}
