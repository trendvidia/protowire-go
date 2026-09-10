// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package protovalidate_test

import (
	"context"
	_ "embed"
	"errors"
	"testing"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/check"
	pvcheck "github.com/trendvidia/protowire-go/check/protovalidate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const userProtoSrc = `syntax = "proto3";

import "buf/validate/validate.proto";

message User {
  string email = 1 [(buf.validate.field).string.min_len = 3];
  uint32 age = 2 [(buf.validate.field).uint32.lt = 150];
}
`

// validateProtoSrc is buf/validate/validate.proto at protovalidate v1.2.2,
// which matches the buf.build/gen stub this module links (see
// testdata/README.md for the exact revision). The fork
// compiles source only; a descriptor handed through SearchResult.Desc is
// rendered back to source first, and that renderer writes proto2 oneof
// members with an `optional` label its own parser rejects
// (trendvidia/protocompile#220). validate.proto is proto2 and FieldRules
// is a oneof, so the file is served as source instead.
// TestVendoredValidateProtoMatchesLinkedStub keeps the two in step.
//
//go:embed testdata/buf/validate/validate.proto
var validateProtoSrc string

func newResolver() protocompile.Resolver {
	return protocompile.WithStandardImports(&protocompile.SourceResolver{
		Accessor: protocompile.SourceAccessorFromMap(map[string]string{
			"user.proto":                  userProtoSrc,
			"buf/validate/validate.proto": validateProtoSrc,
		}),
	})
}

func compileUser(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	comp := protocompile.Compiler{Resolver: newResolver()}
	files, err := comp.Compile(context.Background(), "user.proto")
	require.NoError(t, err)
	desc := files[0].Messages().ByName("User")
	require.NotNil(t, desc)
	return desc
}

// TestVendoredValidateProtoMatchesLinkedStub pins that the vendored
// validate.proto source and the generated Go stub describe the same file.
// The rules a test compiles against come from the source; the extension
// types protovalidate reads them through come from the stub. If the two
// drift, a rule could compile here and not resolve there.
func TestVendoredValidateProtoMatchesLinkedStub(t *testing.T) {
	comp := protocompile.Compiler{Resolver: newResolver()}
	files, err := comp.Compile(context.Background(), "buf/validate/validate.proto")
	require.NoError(t, err)

	fromSource := normalizeFile(t, protodesc.ToFileDescriptorProto(files[0]))
	fromStub := normalizeFile(t, protodesc.ToFileDescriptorProto(validate.File_buf_validate_validate_proto))

	if !proto.Equal(fromSource, fromStub) {
		got := prototext.Format(fromSource)
		want := prototext.Format(fromStub)
		assert.Equal(t, want, got, "vendored validate.proto and the linked stub disagree")
	}
}

// normalizeFile drops source info and re-reads the descriptor through the
// global type registry. The compiler resolves validate.proto's own options
// — (buf.validate.predefined) on its rule fields — against the extension
// types it just built, and a proto handed out of it carries them as
// unknown bytes rather than as the stub's generated extension. proto.Equal
// tells those apart; a round trip through the registry does not.
func normalizeFile(t *testing.T, fdp *descriptorpb.FileDescriptorProto) *descriptorpb.FileDescriptorProto {
	t.Helper()
	fdp.SourceCodeInfo = nil
	wire, err := proto.MarshalOptions{Deterministic: true}.Marshal(fdp)
	require.NoError(t, err)
	out := &descriptorpb.FileDescriptorProto{}
	require.NoError(t, proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}.Unmarshal(wire, out))
	return out
}

func newUser(t *testing.T, desc protoreflect.MessageDescriptor, email string, age uint32) *dynamicpb.Message {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("email"), protoreflect.ValueOfString(email))
	msg.Set(desc.Fields().ByName("age"), protoreflect.ValueOfUint32(age))
	return msg
}

func TestValidateMapsViolations(t *testing.T) {
	desc := compileUser(t)
	v, err := pvcheck.New()
	require.NoError(t, err)

	rep, err := v.Validate(newUser(t, desc, "x", 200))
	require.NoError(t, err, "violations are report content, not an engine error")
	require.NotNil(t, rep)
	require.Len(t, rep.Violations, 2)

	byPath := map[string]check.Violation{}
	for _, viol := range rep.Violations {
		byPath[viol.Path] = viol
	}
	email, ok := byPath["email"]
	require.True(t, ok, "expected a violation for email, got %v", rep.Violations)
	assert.Equal(t, "buf.validate.string.min_len", email.RuleID)
	assert.NotEmpty(t, email.Message)

	age, ok := byPath["age"]
	require.True(t, ok, "expected a violation for age, got %v", rep.Violations)
	assert.Equal(t, "buf.validate.uint32.lt", age.RuleID)
}

func TestValidateCleanPass(t *testing.T) {
	desc := compileUser(t)
	v, err := pvcheck.New()
	require.NoError(t, err)

	rep, err := v.Validate(newUser(t, desc, "a@b.co", 30))
	require.NoError(t, err)
	assert.True(t, rep.OK())
}

func TestValidateNonProtoValue(t *testing.T) {
	v, err := pvcheck.New()
	require.NoError(t, err)

	rep, err := v.Validate(struct{ Name string }{"x"})
	assert.Nil(t, rep)
	require.Error(t, err)
	var ce *check.Error
	assert.False(t, errors.As(err, &ce), "engine errors must not be check.Error")
}

func TestPXFDecodeWithProtovalidate(t *testing.T) {
	desc := compileUser(t)
	v, err := pvcheck.New()
	require.NoError(t, err)

	opts := pxf.UnmarshalOptions{Validator: v}

	// Violating document fails the decode with a *check.Error carrying
	// the mapped report.
	_, _, err = opts.UnmarshalFullDescriptor([]byte(`email = "x"`+"\n"+`age = 200`), desc)
	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Len(t, ce.Report.Violations, 2)

	// Valid document decodes cleanly.
	msg, result, err := opts.UnmarshalFullDescriptor([]byte(`email = "a@b.co"`+"\n"+`age = 30`), desc)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.True(t, result.Report().OK())
}
