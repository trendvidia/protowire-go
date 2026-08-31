// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Setting any member of a oneof clears the others, so the per-field
// reading of "absent" that (pxf.required) and (pxf.default) are otherwise
// defined against does not hold inside one: a member is absent precisely
// when a sibling was chosen. Before #72 postDecode tested presence per
// field with no notion of oneof membership, so a member's default was
// applied over a chosen sibling — destroying the value the document
// wrote, in all four ports that implement the annotations. Draft -01
// §annotation-extensions ("Oneof Members") is the rule these pin.
const oneofAnnotationProtoSrc = `
syntax = "proto3";
package oneofannotation_test.v1;

import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";

message OneDefault {
  oneof choice {
    string a = 1;
    string b = 2 [(pxf.default) = "bbb"];
  }
  string outside = 3 [(pxf.default) = "out"];
}

message NullableSibling {
  oneof choice {
    google.protobuf.StringValue w = 1;
    string b = 2 [(pxf.default) = "bbb"];
  }
}

message SyntheticOneof {
  optional string opt = 1 [(pxf.default) = "syn"];
}
`

// Schemas that must not bind. Separate files: ValidateFile walks the
// whole file, so a violating message would poison the conformant ones.
const oneofTwoDefaultsProtoSrc = `
syntax = "proto3";
package oneoftwodefaults_test.v1;
import "pxf/annotations.proto";

message TwoDefaults {
  oneof choice {
    string a = 1 [(pxf.default) = "aaa"];
    string b = 2 [(pxf.default) = "bbb"];
  }
}
`

const oneofRequiredProtoSrc = `
syntax = "proto3";
package oneofrequired_test.v1;
import "pxf/annotations.proto";

message RequiredMember {
  oneof choice {
    string a = 1 [(pxf.required) = true];
    string b = 2;
  }
}
`

func compileOneofSrc(t *testing.T, src, pkg, name string) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":            src,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName(pkg + "." + name))
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

func compileOneofAnnotation(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	return compileOneofSrc(t, oneofAnnotationProtoSrc, "oneofannotation_test.v1", name)
}

// The #72 repro: a document that chooses one arm keeps it. Before the
// fix this returned a="" b="bbb" case=b — the written value cleared, not
// shadowed.
func TestDecode_OneofDefaultDoesNotClobberChosenArm(t *testing.T) {
	desc := compileOneofAnnotation(t, "OneDefault")

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(`a = "written"`), desc)
	require.NoError(t, err)

	r := msg.ProtoReflect()
	fields := desc.Fields()
	assert.Equal(t, "written", r.Get(fields.ByName("a")).String())
	assert.Equal(t, "", r.Get(fields.ByName("b")).String(), "sibling default must not be applied")

	which := r.WhichOneof(desc.Oneofs().Get(0))
	require.NotNil(t, which)
	assert.Equal(t, protoreflect.Name("a"), which.Name())
}

// Choosing the annotated arm itself is likewise untouched.
func TestDecode_OneofDefaultSuppressedWhenItsOwnArmChosen(t *testing.T) {
	desc := compileOneofAnnotation(t, "OneDefault")

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(`b = "written"`), desc)
	require.NoError(t, err)

	r := msg.ProtoReflect()
	assert.Equal(t, "written", r.Get(desc.Fields().ByName("b")).String())
}

// With no member present the default still applies — the capability the
// rule deliberately preserves rather than forbidding outright.
func TestDecode_OneofDefaultAppliesWhenWholeOneofAbsent(t *testing.T) {
	desc := compileOneofAnnotation(t, "OneDefault")

	msg, res, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)

	r := msg.ProtoReflect()
	assert.Equal(t, "bbb", r.Get(desc.Fields().ByName("b")).String())
	assert.False(t, res.IsSet("b"), "a default is not an input-set field")

	which := r.WhichOneof(desc.Oneofs().Get(0))
	require.NotNil(t, which)
	assert.Equal(t, protoreflect.Name("b"), which.Name())
}

// A field outside the oneof is unaffected by any of this.
func TestDecode_OneofRuleDoesNotAffectOrdinaryFields(t *testing.T) {
	desc := compileOneofAnnotation(t, "OneDefault")

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(`a = "written"`), desc)
	require.NoError(t, err)
	assert.Equal(t, "out", msg.ProtoReflect().Get(desc.Fields().ByName("outside")).String())
}

// A member bound to null counts as present, so it suppresses a sibling's
// default — matching the long-standing rule that null suppresses a
// field's own default because null is intentional.
func TestDecode_NullOneofMemberSuppressesSiblingDefault(t *testing.T) {
	desc := compileOneofSrc(t, oneofAnnotationProtoSrc, "oneofannotation_test.v1", "NullableSibling")

	msg, res, err := pxf.UnmarshalFullDescriptor([]byte(`w = null`), desc)
	require.NoError(t, err)

	assert.True(t, res.IsNull("w"))
	assert.Equal(t, "", msg.ProtoReflect().Get(desc.Fields().ByName("b")).String(),
		"a null sibling is present, so the default must not fire")
}

// proto3 `optional` puts the field in a synthetic single-member oneof.
// Nothing can clear it, so it keeps plain per-field presence and its
// default must still apply — the regression guard for realOneof.
func TestDecode_SyntheticOneofStillTakesItsDefault(t *testing.T) {
	desc := compileOneofAnnotation(t, "SyntheticOneof")

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)
	assert.Equal(t, "syn", msg.ProtoReflect().Get(desc.Fields().ByName("opt")).String())
}

// Two (pxf.default) in one oneof is a bind-time violation, reported on
// every offending member so the author sees each annotation to remove.
func TestValidateDescriptor_TwoDefaultsInOneOneof(t *testing.T) {
	desc := compileOneofSrc(t, oneofTwoDefaultsProtoSrc, "oneoftwodefaults_test.v1", "TwoDefaults")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 2, "one violation per annotated member: %v", vs)

	for _, v := range vs {
		assert.Equal(t, pxf.ViolationDefaultOption, v.Kind)
		assert.Contains(t, v.Detail, `at most one member of oneof "choice" may carry (pxf.default)`)
		assert.Contains(t, v.Detail, "2 do (a, b)")
	}
	assert.Equal(t, "oneoftwodefaults_test.v1.TwoDefaults.a", vs[0].Element)
	assert.Equal(t, "aaa", vs[0].Name)
	assert.Equal(t, "oneoftwodefaults_test.v1.TwoDefaults.b", vs[1].Element)
	assert.Equal(t, "bbb", vs[1].Name)

	_, _, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most one member of oneof")
}

// One default in a oneof is fine — the cap is two, not one.
func TestValidateDescriptor_OneDefaultInOneofIsClean(t *testing.T) {
	desc := compileOneofAnnotation(t, "OneDefault")
	assert.Empty(t, pxf.ValidateDescriptor(desc))
}

// (pxf.required) on a oneof member never has a coherent reading, so it
// is a bind-time violation regardless of the document.
func TestValidateDescriptor_RequiredOnOneofMember(t *testing.T) {
	desc := compileOneofSrc(t, oneofRequiredProtoSrc, "oneofrequired_test.v1", "RequiredMember")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 1)
	assert.Equal(t, pxf.ViolationRequiredOption, vs[0].Kind)
	assert.Equal(t, "oneofrequired_test.v1.RequiredMember.a", vs[0].Element)
	assert.Equal(t, "choice", vs[0].Name)
	assert.Contains(t, vs[0].Detail, "(pxf.required) is not valid on a member of oneof")
	assert.Contains(t, vs[0].String(), "invalid (pxf.required)")
	assert.Contains(t, vs[0].String(), "draft -01 §annotation-extensions")

	// The pre-#72 symptom: a document choosing a valid arm was rejected
	// for the absence of a different one. Now the schema is rejected
	// instead, so the document never gets that far.
	_, _, err := pxf.UnmarshalFullDescriptor([]byte(`b = "written"`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(pxf.required) is not valid on a member of oneof")
	assert.NotContains(t, err.Error(), `required field "a" is absent`)
}

func TestViolationKind_RequiredOptionString(t *testing.T) {
	assert.Equal(t, "required field option", pxf.ViolationRequiredOption.String())
}

// requiredFromUnknownBytes is a FieldOptions carrying only
// (pxf.required) = true as raw wire bytes — the shape a protoc-produced
// FileDescriptorSet has, where nothing resolved the extension into a
// known field.
func requiredFromUnknownBytes() *descriptorpb.FieldOptions {
	raw := protowire.AppendVarint(protowire.AppendTag(nil, 1314, protowire.VarintType), 1)
	opts := &descriptorpb.FieldOptions{}
	opts.ProtoReflect().SetUnknown(protoreflect.RawFields(raw))
	return opts
}

// Every other (pxf.required) test compiles with protocompile, which
// resolves the extension into a known field — so only pxfFieldOptions'
// Range arm runs. protoc-produced descriptors reach the same annotation
// through the unknown-bytes varint arm instead, which is the path a real
// FileDescriptorSet takes. Without this the arm could be inverted,
// mis-numbered or absent and the whole package would still pass.
func TestValidateFile_RequiredOnOneofMemberFromUnknownBytes(t *testing.T) {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("unknownrequired.proto"),
		Package: proto.String("unknownrequired_test.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:      proto.String("RequiredMember"),
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("choice")}},
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:       proto.String("a"),
				Number:     proto.Int32(1),
				JsonName:   proto.String("a"),
				Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:       descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				OneofIndex: proto.Int32(0),
				Options:    requiredFromUnknownBytes(),
			}, {
				Name:       proto.String("b"),
				Number:     proto.Int32(2),
				JsonName:   proto.String("b"),
				Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:       descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				OneofIndex: proto.Int32(0),
			}},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)

	vs := pxf.ValidateFile(fd)
	require.Len(t, vs, 1, "%v", vs)
	assert.Equal(t, pxf.ViolationRequiredOption, vs[0].Kind)
	assert.Equal(t, "unknownrequired_test.v1.RequiredMember.a", vs[0].Element)
	assert.Equal(t, "choice", vs[0].Name)
	assert.Contains(t, vs[0].Detail, `(pxf.required) is not valid on a member of oneof "choice"`)
}

// The synthetic oneof a proto3 `optional` field sits in is excluded from
// the oneof rules for (pxf.required) exactly as it is for
// (pxf.default): nothing else can clear the field, so the per-field
// reading of "absent" still holds and the annotation stays legal.
// Reached through the unknown-bytes arm, so realOneof's IsSynthetic
// guard is pinned on both option-reading paths.
func TestValidateFile_RequiredOnProto3OptionalIsClean(t *testing.T) {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("syntheticrequired.proto"),
		Package: proto.String("syntheticrequired_test.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:      proto.String("OptionalMember"),
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_opt")}},
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:           proto.String("opt"),
				Number:         proto.Int32(1),
				JsonName:       proto.String("opt"),
				Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				OneofIndex:     proto.Int32(0),
				Proto3Optional: proto.Bool(true),
				Options:        requiredFromUnknownBytes(),
			}},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)
	require.True(t, fd.Messages().Get(0).Oneofs().Get(0).IsSynthetic(), "fixture must be a synthetic oneof")

	assert.Empty(t, pxf.ValidateFile(fd))
	assert.True(t, pxf.IsRequired(fd.Messages().Get(0).Fields().Get(0)),
		"the annotation is still read — it is the oneof rule that does not apply")
}

// SkipValidate bypasses the bind-time cap, so an invalid two-default
// oneof reaches the decoder. The runtime rule still protects written
// input — which is the property that matters, since the data loss is
// what #72 is about.
func TestDecode_SkipValidateStillProtectsWrittenOneofArm(t *testing.T) {
	desc := compileOneofSrc(t, oneofTwoDefaultsProtoSrc, "oneoftwodefaults_test.v1", "TwoDefaults")
	opts := pxf.UnmarshalOptions{SkipValidate: true}

	msg, _, err := opts.UnmarshalFullDescriptor([]byte(`a = "written"`), desc)
	require.NoError(t, err)

	r := msg.ProtoReflect()
	assert.Equal(t, "written", r.Get(desc.Fields().ByName("a")).String())
	assert.Equal(t, "", r.Get(desc.Fields().ByName("b")).String())
}
