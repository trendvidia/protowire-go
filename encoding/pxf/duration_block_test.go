// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pxf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// durationHolder is a message with a Duration in a singular field, a list
// and a map.
func durationHolder(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	dur := ".google.protobuf.Duration"
	field := func(name string, num int32, label descriptorpb.FieldDescriptorProto_Label, typ string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(num), Label: label.Enum(),
			Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(typ),
		}
	}
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	key := field("key", 1, optional, "")
	key.Type, key.TypeName = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), nil
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("durblock.proto"), Package: proto.String("durblock"), Syntax: proto.String("proto3"),
		Dependency: []string{"google/protobuf/duration.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Holder"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("dur_field", 1, optional, dur),
				field("list", 2, descriptorpb.FieldDescriptorProto_LABEL_REPEATED, dur),
				field("by_name", 3, descriptorpb.FieldDescriptorProto_LABEL_REPEATED, ".durblock.Holder.ByNameEntry"),
			},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name:    proto.String("ByNameEntry"),
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field:   []*descriptorpb.FieldDescriptorProto{key, field("value", 2, optional, dur)},
			}},
		}},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)
	return file.Messages().Get(0)
}

// A literal holds 64 bits of nanoseconds, about 292 years. Past that a
// valid Duration is written as a block with its exact value (#225). The
// table is the same as protowire-java's DurationReadBackTest, so the two
// ports agree on the text.
func TestDurationAroundTheLiteralLimit(t *testing.T) {
	desc := durationHolder(t)
	fd := desc.Fields().ByName("dur_field")
	for _, c := range []struct {
		seconds int64
		nanos   int32
		text    string
	}{
		{9223372036, 854775807, "dur_field = 2562047h47m16.854775807s\n"},
		{-9223372036, -854775808, "dur_field = -2562047h47m16.854775808s\n"},
		{9223372036, 854775808, "dur_field {\n  seconds = 9223372036\n  nanos = 854775808\n}\n"},
		{-9223372036, -854775809, "dur_field {\n  seconds = -9223372036\n  nanos = -854775809\n}\n"},
		{9500000000, 0, "dur_field {\n  seconds = 9500000000\n}\n"},
		{-9500000000, -5, "dur_field {\n  seconds = -9500000000\n  nanos = -5\n}\n"},
		{315576000000, 999999999, "dur_field {\n  seconds = 315576000000\n  nanos = 999999999\n}\n"},
	} {
		t.Run(c.text, func(t *testing.T) {
			m := dynamicpb.NewMessage(desc)
			m.Set(fd, protoreflect.ValueOfMessage((&durationpb.Duration{Seconds: c.seconds, Nanos: c.nanos}).ProtoReflect()))
			text, err := pxf.Marshal(m)
			require.NoError(t, err)
			assert.Equal(t, c.text, string(text))

			back := dynamicpb.NewMessage(desc)
			require.NoError(t, pxf.Unmarshal(text, back))
			got := back.Get(fd).Message()
			assert.Equal(t, c.seconds, got.Get(got.Descriptor().Fields().ByName("seconds")).Int())
			assert.Equal(t, int64(c.nanos), got.Get(got.Descriptor().Fields().ByName("nanos")).Int())
		})
	}
}

func TestDurationPastTheLiteralLimitInListAndMap(t *testing.T) {
	desc := durationHolder(t)
	big := protoreflect.ValueOfMessage((&durationpb.Duration{Seconds: 9500000000, Nanos: 1}).ProtoReflect())
	small := protoreflect.ValueOfMessage((&durationpb.Duration{Seconds: 5400}).ProtoReflect())
	m := dynamicpb.NewMessage(desc)
	list := m.Mutable(desc.Fields().ByName("list")).List()
	list.Append(small)
	list.Append(big)
	byName := m.Mutable(desc.Fields().ByName("by_name")).Map()
	byName.Set(protoreflect.ValueOfString("big").MapKey(), big)
	byName.Set(protoreflect.ValueOfString("small").MapKey(), small)

	text, err := pxf.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, `list = [
  1h30m0s,
  {
    seconds = 9500000000
    nanos = 1
  }
]
by_name = {
  big: {
    seconds = 9500000000
    nanos = 1
  }
  small: 1h30m0s
}
`, string(text))

	back := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal(text, back))
	assert.True(t, proto.Equal(m, back), "text:\n%s", text)
}

func TestDurationPastTheLiteralLimitInADatasetCell(t *testing.T) {
	desc := durationHolder(t)
	m := dynamicpb.NewMessage(desc)
	m.Set(desc.Fields().ByName("dur_field"),
		protoreflect.ValueOfMessage((&durationpb.Duration{Seconds: 9500000000, Nanos: 1}).ProtoReflect()))
	columns := []string{"dur_field"}
	table := writeTable(t, desc, columns, []*dynamicpb.Message{m})
	assertTableReadsBack(t, desc, table, []*dynamicpb.Message{m})
}
