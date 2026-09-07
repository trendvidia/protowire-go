// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// staleOptions is the unknown bytes to put on each Options message of a
// synthetic file. nil means no options at all.
type staleOptions struct {
	file, msg, field, oneof, enum, enumValue []byte
}

func varintOpt(num protowire.Number, v uint64) []byte {
	b := protowire.AppendTag(nil, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func bytesOpt(num protowire.Number, v []byte) []byte {
	b := protowire.AppendTag(nil, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

// staleFile builds a one-message file through protodesc — the path a
// descriptor set loaded from disk takes — with the given imports and
// unknown option bytes. Imports are left unresolved on purpose: a set
// compiled without --include_imports still names them, and the name is
// what the gate reads.
func staleFile(t *testing.T, deps []string, o staleOptions) protoreflect.FileDescriptor {
	t.Helper()
	unknown := func(m proto.Message, b []byte) {
		m.ProtoReflect().SetUnknown(b)
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("stale.proto"),
		Package:    proto.String("stale.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: deps,
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:       proto.String("x"),
				Number:     proto.Int32(1),
				Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:       descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName:   proto.String("x"),
				OneofIndex: proto.Int32(0),
			}},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("o")}},
		}},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:  proto.String("E"),
			Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("E_ZERO"), Number: proto.Int32(0)}},
		}},
	}
	if o.file != nil {
		fdp.Options = &descriptorpb.FileOptions{}
		unknown(fdp.Options, o.file)
	}
	if o.msg != nil {
		fdp.MessageType[0].Options = &descriptorpb.MessageOptions{}
		unknown(fdp.MessageType[0].Options, o.msg)
	}
	if o.field != nil {
		fdp.MessageType[0].Field[0].Options = &descriptorpb.FieldOptions{}
		unknown(fdp.MessageType[0].Field[0].Options, o.field)
	}
	if o.oneof != nil {
		fdp.MessageType[0].OneofDecl[0].Options = &descriptorpb.OneofOptions{}
		unknown(fdp.MessageType[0].OneofDecl[0].Options, o.oneof)
	}
	if o.enum != nil {
		fdp.EnumType[0].Options = &descriptorpb.EnumOptions{}
		unknown(fdp.EnumType[0].Options, o.enum)
	}
	if o.enumValue != nil {
		fdp.EnumType[0].Value[0].Options = &descriptorpb.EnumValueOptions{}
		unknown(fdp.EnumType[0].Value[0].Options, o.enumValue)
	}
	fd, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdp, new(protoregistry.Files))
	require.NoError(t, err)
	return fd
}

// TestRetiredNumbersAreDiagnosed pins #98: every retired number, on the
// Options kind it was allocated on, in a file that imports protowire's
// annotations, is a bind-time violation naming the number, what it was,
// what it is now, and that the descriptor must be recompiled.
func TestRetiredNumbersAreDiagnosed(t *testing.T) {
	deps := []string{"pxf/annotations.proto"}
	carrier := bytesOpt(50400, nil)
	fd := staleFile(t, deps, staleOptions{
		file:      append(append(varintOpt(50100, 9001), varintOpt(50101, 0)...), bytesOpt(50404, nil)...),
		msg:       append(varintOpt(50200, 7), carrier...),
		field:     append(append(varintOpt(50000, 1), bytesOpt(50001, []byte("42"))...), append(carrier, varintOpt(51000, 1)...)...),
		oneof:     carrier,
		enum:      carrier,
		enumValue: carrier,
	})

	vs := pxf.ValidateFile(fd)
	byNumber := map[string]pxf.Violation{}
	for _, v := range vs {
		require.Equal(t, pxf.ViolationRetiredNumber, v.Kind)
		byNumber[v.Element+"#"+v.Name] = v
	}
	want := map[string]string{
		"stale.proto#50100":     "(sbe.schema_id) before protowire v1.12.0 and is 1319 now",
		"stale.proto#50101":     "(sbe.version) before protowire v1.12.0 and is 1320 now",
		"stale.proto#50404":     "source_map carrier before protowire v1.12.0 and is 1331 now",
		"stale.v1.M#50200":      "(sbe.template_id) before protowire v1.12.0 and is 1321 now",
		"stale.v1.M#50400":      "message_annotations carrier before protowire v1.12.0 and is 1327 now",
		"stale.v1.M.x#50000":    "(pxf.required) before protowire v1.12.0 and is 1314 now",
		"stale.v1.M.x#50001":    "(pxf.default) before protowire v1.12.0 and is 1315 now",
		"stale.v1.M.x#50400":    "field_annotations carrier before protowire v1.12.0 and is 1327 now",
		"stale.v1.M.x#51000":    "protocheck field constraint before protowire v1.12.0 and is 1347 now",
		"stale.v1.M.o#50400":    "oneof_annotations carrier",
		"stale.v1.E#50400":      "*_annotations carrier",
		"stale.v1.E_ZERO#50400": "*_annotations carrier",
	}
	for key, detail := range want {
		v, ok := byNumber[key]
		if !assert.True(t, ok, "expected a violation for %s; got %v", key, keys(byNumber)) {
			continue
		}
		assert.Contains(t, v.Detail, detail)
		assert.Contains(t, v.Detail, "must be recompiled")
		assert.Contains(t, v.String(), "STABILITY.md promise 3")
	}
	assert.Len(t, vs, len(want), "one violation per retired number, no more: %v", vs)

	// The violation is a bind failure, which is the point: the
	// alternative was a schema that silently stopped enforcing
	// (pxf.required).
	_, err := pxf.UnmarshalDescriptor([]byte(`x = "v"`), fd.Messages().ByName("M"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "50000")
}

func keys(m map[string]pxf.Violation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRetiredNumbersNeedProtowireImport pins the gate: the same bytes in
// a file that imports none of protowire's annotation files are a third
// party's options in the range everybody squats, and are left alone.
// Likewise a protowire-importing file carrying a number in the range
// that was never allocated, and a clean file, which is the "no behaviour
// change" half of #98.
func TestRetiredNumbersNeedProtowireImport(t *testing.T) {
	stale := staleOptions{file: varintOpt(50100, 9001), field: varintOpt(50000, 1), msg: bytesOpt(50400, nil)}
	cases := []struct {
		name string
		deps []string
		opts staleOptions
	}{
		{"no protowire import", []string{"google/protobuf/timestamp.proto"}, stale},
		{"no import at all", nil, stale},
		{"unallocated numbers", []string{"pxf/annotations.proto"}, staleOptions{field: varintOpt(50050, 1), file: varintOpt(50999, 1)}},
		{"registered numbers as unknown bytes", []string{"pxf/annotations.proto"}, staleOptions{field: varintOpt(1314, 1)}},
		{"clean", []string{"pxf/annotations.proto"}, staleOptions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := staleFile(t, tc.deps, tc.opts)
			var retired []string
			for _, v := range pxf.ValidateFile(fd) {
				if v.Kind == pxf.ViolationRetiredNumber {
					retired = append(retired, v.String())
				}
			}
			assert.Empty(t, retired, strings.Join(retired, "\n"))
		})
	}
}

// TestRetiredNumbersInAnImport: the check runs over the import closure
// like every other bind-time check, so a stale imported file is
// diagnosed through the file that imports it.
func TestRetiredNumbersInAnImport(t *testing.T) {
	stale := staleFile(t, []string{"pxf/annotations.proto"}, staleOptions{field: varintOpt(50000, 1)})
	files := new(protoregistry.Files)
	require.NoError(t, files.RegisterFile(stale))
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("uses.proto"),
		Package:    proto.String("uses.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"stale.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("U"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("m"), Number: proto.Int32(1), JsonName: proto.String("m"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				TypeName: proto.String(".stale.v1.M"),
			}},
		}},
	}
	uses, err := protodesc.FileOptions{AllowUnresolvable: true}.New(fdp, files)
	require.NoError(t, err)
	vs := pxf.ValidateDescriptor(uses.Messages().ByName("U"))
	require.Len(t, vs, 1)
	assert.Equal(t, "stale.proto", vs[0].File)
	assert.Equal(t, "50000", vs[0].Name)
}
