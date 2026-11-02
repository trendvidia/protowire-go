// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Marshal writes an Any as `@type` and a body only when reading that body
// gives back the Any's value, byte for byte. Anything else stays as the
// Any's own fields, type_url and value (#232). A reader compares an Any by
// its bytes, so a body that drops or reorders them changes the message.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const anyReadBackProtoSrc = `
syntax = "proto3";
package anyback.v1;

import "google/protobuf/any.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";

message Holder {
  google.protobuf.Any payload = 1;
  Holder next = 2;
}

message Leaf {
  string tag = 1;
  int32 n = 2;
}

message Rich {
  google.protobuf.Timestamp ts = 1;
  google.protobuf.Int32Value w = 2;
  Leaf leaf = 3;
  map<string, int32> m = 4;
  repeated int32 r = 5;
  optional int32 o = 6;
  google.protobuf.Duration d = 7;
  repeated Leaf leaves = 8;
}

message Defaulted {
  string label = 1 [(pxf.default) = "dflt"];
  int32 n = 2;
}

message Required {
  int32 n = 1 [(pxf.required) = true];
  string s = 2;
}
`

func anyReadBackSetup(t *testing.T) (protoreflect.MessageDescriptor, pxf.TypeResolver) {
	t.Helper()
	desc := compilePlacementSrc(t, anyReadBackProtoSrc, "anyback.v1", "Holder")
	res := &testResolver{descs: map[string]protoreflect.MessageDescriptor{}}
	msgs := desc.ParentFile().Messages()
	for i := range msgs.Len() {
		res.descs[string(msgs.Get(i).FullName())] = msgs.Get(i)
	}
	return desc, res
}

// anyHolder builds a Holder whose payload is typeName with value verbatim.
func anyHolder(desc protoreflect.MessageDescriptor, typeName string, value []byte) *dynamicpb.Message {
	m := dynamicpb.NewMessage(desc)
	fd := desc.Fields().ByName("payload")
	a := m.Mutable(fd).Message()
	f := a.Descriptor().Fields()
	a.Set(f.ByName("type_url"), protoreflect.ValueOfString(typeName))
	a.Set(f.ByName("value"), protoreflect.ValueOfBytes(value))
	return m
}

// assertReadsBack marshals m and reads it with both decoders. It returns
// the document.
func assertReadsBack(t *testing.T, desc protoreflect.MessageDescriptor, res pxf.TypeResolver, m proto.Message) string {
	t.Helper()
	out, err := pxf.MarshalOptions{TypeResolver: res}.Marshal(m)
	require.NoError(t, err)
	opts := pxf.UnmarshalOptions{TypeResolver: res}

	back := dynamicpb.NewMessage(desc)
	if assert.NoError(t, opts.Unmarshal(out, back), "Unmarshal:\n%.2000s", out) {
		assert.True(t, proto.Equal(m, back), "Unmarshal changed the message:\n%.2000s", out)
	}
	full := dynamicpb.NewMessage(desc)
	if _, err := opts.UnmarshalFull(out, full); assert.NoError(t, err, "UnmarshalFull:\n%.2000s", out) {
		assert.True(t, proto.Equal(m, full), "UnmarshalFull changed the message:\n%.2000s", out)
	}
	return string(out)
}

func varintField(num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, num, protowire.VarintType), v)
}

func lenField(num protowire.Number, body []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), body)
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestAnyBody_WrittenOnlyWhenItReadsBack(t *testing.T) {
	desc, res := anyReadBackSetup(t)

	tagT := lenField(1, []byte("t"))
	n1 := varintField(2, 1)
	unknown := varintField(101, 1)
	entry := func(k string, v uint64) []byte {
		return lenField(4, cat(lenField(1, []byte(k)), varintField(2, v)))
	}

	for _, tc := range []struct {
		name     string
		typeName string
		value    []byte
		body     bool
	}{
		// What a body can say keeps the body form.
		{"canonical", "anyback.v1.Leaf", cat(tagT, n1), true},
		{"empty", "anyback.v1.Leaf", nil, true},
		{"timestamp", "anyback.v1.Rich", lenField(1, varintField(1, 1700000000)), true},
		{"wrapper", "anyback.v1.Rich", lenField(2, varintField(1, 7)), true},
		{"wrapper holding zero", "anyback.v1.Rich", lenField(2, nil), true},
		{"sorted map", "anyback.v1.Rich", cat(entry("a", 1), entry("b", 2)), true},
		{"packed list", "anyback.v1.Rich", lenField(5, []byte{1, 2}), true},
		{"optional zero", "anyback.v1.Rich", varintField(6, 0), true},
		{"default field set", "anyback.v1.Defaulted", lenField(1, []byte("x")), true},
		{"required field set", "anyback.v1.Required", varintField(1, 3), true},

		// The issue's cases.
		{"unknown field", "anyback.v1.Leaf", cat(tagT, unknown), false},
		{"fields out of order", "anyback.v1.Leaf", cat(n1, tagT), false},

		// The same loss one level down, where re-encoding the parsed
		// value gives the same bytes.
		{"unknown in submessage", "anyback.v1.Rich", lenField(3, cat(tagT, unknown)), false},
		{"unknown in list element", "anyback.v1.Rich", lenField(8, cat(tagT, unknown)), false},
		{"unknown in wrapper", "anyback.v1.Rich", lenField(2, cat(varintField(1, 7), unknown)), false},
		{"unknown in timestamp", "anyback.v1.Rich", lenField(1, cat(varintField(1, 5), unknown)), false},

		// Bytes a decoder would not write back.
		{"explicit zero", "anyback.v1.Leaf", varintField(2, 0), false},
		{"long varint", "anyback.v1.Leaf", []byte{0x10, 0x81, 0x00}, false},
		{"unpacked list", "anyback.v1.Rich", cat(varintField(5, 1), varintField(5, 2)), false},
		{"map out of order", "anyback.v1.Rich", cat(entry("b", 2), entry("a", 1)), false},
		{"repeated singular field", "anyback.v1.Leaf", cat(varintField(2, 1), varintField(2, 2)), false},

		// A literal cannot hold it.
		{"timestamp past year 9999", "anyback.v1.Rich", lenField(1, varintField(1, 1<<40)), false},

		// A reader's annotation rules would change or refuse the body.
		{"default unset", "anyback.v1.Defaulted", varintField(2, 1), false},
		{"required unset", "anyback.v1.Required", lenField(2, []byte("s")), false},

		// Never unpacked at all.
		{"unresolved", "anyback.v1.Nope", tagT, false},
		{"unparsable", "anyback.v1.Leaf", []byte{0xff}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := assertReadsBack(t, desc, res, anyHolder(desc, tc.typeName, tc.value))
			assert.Equal(t, tc.body, strings.Contains(out, "@type"), "%s", out)
		})
	}
}

// holderChain nests n Anys, each holding a Holder, around leaf bytes.
func holderChain(desc protoreflect.MessageDescriptor, n int) *dynamicpb.Message {
	m := anyHolder(desc, "anyback.v1.Leaf", lenField(1, []byte("t")))
	for range n - 1 {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if err != nil {
			panic(err)
		}
		m = anyHolder(desc, "anyback.v1.Holder", b)
	}
	return m
}

// A chain of Anys costs its own bytes, not MaxNestingDepth, on the wire:
// without a limit a long one wrote a document no reader accepts.
func TestAnyBody_ChainStopsAtTheDepthLimit(t *testing.T) {
	desc, res := anyReadBackSetup(t)

	for _, tc := range []struct {
		n, bodies int
	}{
		{1, 1},
		{99, 99},
		{100, 100},
		{101, 99}, // the 100th Any's body needs the 101st's raw block
		{300, 99},
	} {
		out := assertReadsBack(t, desc, res, holderChain(desc, tc.n))
		assert.Equal(t, tc.bodies, strings.Count(out, "@type"), "%d Anys", tc.n)
	}
}

type countingResolver struct {
	pxf.TypeResolver
	calls int
}

func (r *countingResolver) FindMessageByURL(url string) (protoreflect.MessageDescriptor, error) {
	r.calls++
	return r.TypeResolver.FindMessageByURL(url)
}

// The encoder stops unpacking at the limit, so a longer chain costs it no
// more lookups. Without the stop it walked the whole chain, at a cost of
// every remaining byte per Any.
func TestAnyBody_ChainPastTheLimitIsNotWalked(t *testing.T) {
	desc, res := anyReadBackSetup(t)
	lookups := func(n int) int {
		r := &countingResolver{TypeResolver: res}
		_, err := pxf.MarshalOptions{TypeResolver: r}.Marshal(holderChain(desc, n))
		require.NoError(t, err)
		return r.calls
	}
	assert.Equal(t, lookups(300), lookups(2000))
}

// A body is measured where it lands: the same value that fits near the
// root does not fit near the limit, and only the Any there stays raw.
func TestAnyBody_SubmessageDepthCounts(t *testing.T) {
	desc, res := anyReadBackSetup(t)
	nest := func(levels int) *dynamicpb.Message {
		m := dynamicpb.NewMessage(desc)
		a := m.Mutable(desc.Fields().ByName("payload")).Message()
		f := a.Descriptor().Fields()
		a.Set(f.ByName("type_url"), protoreflect.ValueOfString("anyback.v1.Rich"))
		a.Set(f.ByName("value"), protoreflect.ValueOfBytes(lenField(3, lenField(1, []byte("t")))))
		for range levels {
			parent := dynamicpb.NewMessage(desc)
			parent.Set(desc.Fields().ByName("next"), protoreflect.ValueOfMessage(m))
			m = parent
		}
		return m
	}

	for _, tc := range []struct {
		levels int
		body   bool
	}{{0, true}, {98, true}, {99, false}} {
		out := assertReadsBack(t, desc, res, nest(tc.levels))
		assert.Equal(t, tc.body, strings.Contains(out, "@type"), "%d levels", tc.levels)
	}
}

// EmitDefaults writes an unset optional field out, which sets it for a
// reader, so a body that has one stays raw.
func TestAnyBody_EmitDefaults(t *testing.T) {
	desc, res := anyReadBackSetup(t)
	m := anyHolder(desc, "anyback.v1.Rich", lenField(3, nil))
	out, err := pxf.MarshalOptions{TypeResolver: res, EmitDefaults: true}.Marshal(m)
	require.NoError(t, err)
	back := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.UnmarshalOptions{TypeResolver: res}.Unmarshal(out, back), "%s", out)
	assert.True(t, proto.Equal(m, back), "%s", out)
}
