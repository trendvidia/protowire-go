// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

func varintUnknown(num protowire.Number, v uint64) []byte {
	b := protowire.AppendTag(nil, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

// staleSBEFile builds a file with one message through protodesc, with
// the given unknown bytes on the file's and the message's options — the
// shape of a descriptor set compiled before protowire v1.12.0 and
// loaded from disk.
func staleSBEFile(t *testing.T, fileUnknown, msgUnknown []byte) protoreflect.FileDescriptor {
	t.Helper()
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("stale.proto"),
		Package:    proto.String("stale.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"sbe/annotations.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Order"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("id"), Number: proto.Int32(1), JsonName: proto.String("id"),
				Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:  descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum(),
			}},
		}},
	}
	if fileUnknown != nil {
		fdp.Options = &descriptorpb.FileOptions{}
		fdp.Options.ProtoReflect().SetUnknown(fileUnknown)
	}
	if msgUnknown != nil {
		fdp.MessageType[0].Options = &descriptorpb.MessageOptions{}
		fdp.MessageType[0].Options.ProtoReflect().SetUnknown(msgUnknown)
	}
	fd, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdp, new(protoregistry.Files))
	require.NoError(t, err)
	return fd
}

// TestNewCodec_RetiredNumbersAreDiagnosed pins #98's SBE half: a file
// whose (sbe.schema_id) sits at 50100, or a message whose
// (sbe.template_id) sits at 50200, is reported as predating the
// registered block — not as "missing (sbe.schema_id)", which sent the
// reader to a .proto that does declare it.
func TestNewCodec_RetiredNumbersAreDiagnosed(t *testing.T) {
	t.Run("schema_id at 50100", func(t *testing.T) {
		fd := staleSBEFile(t, varintUnknown(50100, 9001), nil)
		_, err := sbe.NewCodec(fd)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retired option number 50100")
		assert.Contains(t, err.Error(), "registered as 1319")
		assert.Contains(t, err.Error(), "must be recompiled")
		assert.NotContains(t, err.Error(), "missing")
	})
	t.Run("template_id at 50200", func(t *testing.T) {
		// schema_id present at its registered number, as unknown bytes
		// (the reader falls back to them), and a message left at 50200.
		fd := staleSBEFile(t, varintUnknown(1319, 9001), varintUnknown(50200, 7))
		_, err := sbe.NewCodec(fd)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stale.v1.Order")
		assert.Contains(t, err.Error(), "retired option number 50200")
		assert.Contains(t, err.Error(), "registered as 1321")
	})
	t.Run("genuinely missing still says missing", func(t *testing.T) {
		fd := staleSBEFile(t, nil, nil)
		_, err := sbe.NewCodec(fd)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing (sbe.schema_id)")
	})
	t.Run("a message without template_id is still skipped", func(t *testing.T) {
		fd := staleSBEFile(t, varintUnknown(1319, 9001), nil)
		_, err := sbe.NewCodec(fd)
		require.NoError(t, err)
	})
}
