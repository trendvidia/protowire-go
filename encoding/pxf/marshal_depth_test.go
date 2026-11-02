// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Marshal refuses a message deeper than a decoder at the same limit
// accepts, so nothing it writes is refused for depth when read back (#234).

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const marshalDepthProtoSrc = `
syntax = "proto3";
package mdepth.v1;

import "google/protobuf/any.proto";
import "google/protobuf/duration.proto";
import "pxf/annotations.proto";

message T {
  T next = 1;
  repeated T kids = 2;
  map<string, T> by_key = 3;
  map<string, int32> scalars = 4;
  repeated int32 ns = 5;
  int32 v = 6;
  repeated T keyed = 7 [(pxf.key) = "id"];
  string id = 8;
  google.protobuf.Any payload = 9;
  google.protobuf.Duration long = 10;
}

message Cell {
  Cell next = 1;
  repeated int32 must = 2 [(pxf.required) = true];
}
`

func marshalDepthSetup(t *testing.T) (protoreflect.MessageDescriptor, pxf.TypeResolver) {
	t.Helper()
	desc := compilePlacementSrc(t, marshalDepthProtoSrc, "mdepth.v1", "T")
	return desc, &testResolver{descs: map[string]protoreflect.MessageDescriptor{"mdepth.v1.T": desc}}
}

// deepT wraps m in n singular fields.
func deepT(desc protoreflect.MessageDescriptor, m *dynamicpb.Message, n int) *dynamicpb.Message {
	for range n {
		p := dynamicpb.NewMessage(desc)
		p.Set(desc.Fields().ByName("next"), protoreflect.ValueOfMessage(m))
		m = p
	}
	return m
}

func TestMarshal_DepthAgreesWithDecoderAndParser(t *testing.T) {
	desc, _ := marshalDepthSetup(t)
	f := desc.Fields()
	str := func(s string) protoreflect.MapKey { return protoreflect.ValueOfString(s).MapKey() }

	wraps := map[string]func(*dynamicpb.Message) *dynamicpb.Message{
		"singular": func(c *dynamicpb.Message) *dynamicpb.Message { return deepT(desc, c, 1) },
		"list": func(c *dynamicpb.Message) *dynamicpb.Message {
			p := dynamicpb.NewMessage(desc)
			p.Mutable(f.ByName("kids")).List().Append(protoreflect.ValueOfMessage(c))
			return p
		},
		"map": func(c *dynamicpb.Message) *dynamicpb.Message {
			p := dynamicpb.NewMessage(desc)
			p.Mutable(f.ByName("by_key")).Map().Set(str("k"), protoreflect.ValueOfMessage(c))
			return p
		},
		"keyed": func(c *dynamicpb.Message) *dynamicpb.Message {
			c.Set(f.ByName("id"), protoreflect.ValueOfString("x"))
			p := dynamicpb.NewMessage(desc)
			p.Mutable(f.ByName("keyed")).List().Append(protoreflect.ValueOfMessage(c))
			return p
		},
	}
	leaves := map[string]func(*dynamicpb.Message){
		"empty":  func(*dynamicpb.Message) {},
		"scalar": func(m *dynamicpb.Message) { m.Set(f.ByName("v"), protoreflect.ValueOfInt32(1)) },
		"list":   func(m *dynamicpb.Message) { m.Mutable(f.ByName("ns")).List().Append(protoreflect.ValueOfInt32(1)) },
		"scalar map": func(m *dynamicpb.Message) {
			m.Mutable(f.ByName("scalars")).Map().Set(str("a"), protoreflect.ValueOfInt32(1))
		},
		// A Duration past the literal's range is a block, not a literal.
		"duration block": func(m *dynamicpb.Message) {
			d := m.Mutable(f.ByName("long")).Message()
			d.Set(d.Descriptor().Fields().ByName("seconds"), protoreflect.ValueOfInt64(9500000000))
		},
	}

	for wn, wrap := range wraps {
		for ln, leaf := range leaves {
			written, refused := 0, 0
			for n := 1; n <= pxf.MaxNestingDepth+2; n++ {
				m := dynamicpb.NewMessage(desc)
				leaf(m)
				for range n {
					m = wrap(m)
				}
				out, err := pxf.Marshal(m)
				if err != nil {
					require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100", "%s/%s %d", wn, ln, n)
					refused++
					continue
				}
				written++
				back := dynamicpb.NewMessage(desc)
				require.NoError(t, pxf.Unmarshal(out, back), "%s/%s %d", wn, ln, n)
				require.True(t, proto.Equal(m, back), "%s/%s %d", wn, ln, n)
				_, err = pxf.Parse(out)
				require.NoError(t, err, "%s/%s %d", wn, ln, n)
			}
			assert.Positive(t, written, "%s/%s", wn, ln)
			assert.Positive(t, refused, "%s/%s", wn, ln)
		}
	}
}

// The largest shape that fits is written: refusal is not a level early.
func TestMarshal_DepthBound(t *testing.T) {
	desc, _ := marshalDepthSetup(t)
	out, err := pxf.Marshal(deepT(desc, dynamicpb.NewMessage(desc), pxf.MaxNestingDepth))
	require.NoError(t, err)
	require.NoError(t, pxf.Unmarshal(out, dynamicpb.NewMessage(desc)))
	_, err = pxf.Marshal(deepT(desc, dynamicpb.NewMessage(desc), pxf.MaxNestingDepth+1))
	require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100")
}

func TestMarshalOptions_MaxNestingDepth(t *testing.T) {
	desc, _ := marshalDepthSetup(t)
	m := deepT(desc, dynamicpb.NewMessage(desc), 150)
	out, err := pxf.MarshalOptions{MaxNestingDepth: 150}.Marshal(m)
	require.NoError(t, err)
	back := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.UnmarshalOptions{MaxNestingDepth: 150}.Unmarshal(out, back))
	assert.True(t, proto.Equal(m, back))

	_, err = pxf.MarshalOptions{MaxNestingDepth: 149}.Marshal(m)
	require.ErrorContains(t, err, "MaxNestingDepth=149")
	_, err = pxf.MarshalOptions{MaxNestingDepth: 2}.Marshal(deepT(desc, dynamicpb.NewMessage(desc), 3))
	require.ErrorContains(t, err, "MaxNestingDepth=2")
}

// tAny returns a T whose payload holds value as a T.
func tAny(t *testing.T, desc protoreflect.MessageDescriptor, value proto.Message) *dynamicpb.Message {
	t.Helper()
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(value)
	require.NoError(t, err)
	m := dynamicpb.NewMessage(desc)
	a := m.Mutable(desc.Fields().ByName("payload")).Message()
	f := a.Descriptor().Fields()
	a.Set(f.ByName("type_url"), protoreflect.ValueOfString("mdepth.v1.T"))
	a.Set(f.ByName("value"), protoreflect.ValueOfBytes(b))
	return m
}

// An Any is not refused for what its bytes hold: when a body would be too
// deep, the Any stays raw, at any limit.
func TestMarshal_AnyTooDeepStaysRaw(t *testing.T) {
	desc, res := marshalDepthSetup(t)
	m := tAny(t, desc, deepT(desc, dynamicpb.NewMessage(desc), 150))

	out, err := pxf.MarshalOptions{TypeResolver: res}.Marshal(m)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "@type")
	back := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.UnmarshalOptions{TypeResolver: res}.Unmarshal(out, back))
	assert.True(t, proto.Equal(m, back))

	// The body fits under a raised limit, on a reader raised to match.
	out, err = pxf.MarshalOptions{TypeResolver: res, MaxNestingDepth: 200}.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(out), "@type")
	back = dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.UnmarshalOptions{TypeResolver: res, MaxNestingDepth: 200}.Unmarshal(out, back))
	assert.True(t, proto.Equal(m, back))
}

// An Any that sits past the limit itself has no room even for its raw block.
func TestMarshal_AnyPastTheLimitIsRefused(t *testing.T) {
	desc, res := marshalDepthSetup(t)
	inner := dynamicpb.NewMessage(desc)
	inner.Set(desc.Fields().ByName("v"), protoreflect.ValueOfInt32(1))
	_, err := pxf.MarshalOptions{TypeResolver: res}.Marshal(deepT(desc, tAny(t, desc, inner), pxf.MaxNestingDepth))
	require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100")
}

// A @dataset cell obeys the same limit, as the reader does.
func TestAppendDatasetRow_Depth(t *testing.T) {
	desc, _ := marshalDepthSetup(t)
	row := func(n int) *dynamicpb.Message { return deepT(desc, dynamicpb.NewMessage(desc), n) }

	dst, err := pxf.AppendDatasetRow(nil, row(pxf.MaxNestingDepth), []string{"next"})
	require.NoError(t, err)
	require.NotEmpty(t, dst)
	_, err = pxf.AppendDatasetRow(nil, row(pxf.MaxNestingDepth+1), []string{"next"})
	require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100")
}

// In a cell, an unset required list is written as `[]`, which is a descent
// of its own at the bottom of the row.
func TestAppendDatasetRow_DepthCountsAnEmptyCollection(t *testing.T) {
	desc, _ := marshalDepthSetup(t)
	cell := desc.ParentFile().Messages().ByName("Cell")
	row := func(n int) *dynamicpb.Message {
		m := dynamicpb.NewMessage(cell)
		for range n {
			p := dynamicpb.NewMessage(cell)
			p.Set(cell.Fields().ByName("next"), protoreflect.ValueOfMessage(m))
			m = p
		}
		return m
	}
	columns := []string{"next", "must"}
	head, err := pxf.AppendDatasetHeader(nil, cell, columns)
	require.NoError(t, err)

	dst, err := pxf.AppendDatasetRow(append([]byte(nil), head...), row(pxf.MaxNestingDepth-1), columns)
	require.NoError(t, err)
	tr, err := pxf.NewDatasetReader(bytes.NewReader(dst))
	require.NoError(t, err, "%s", dst)
	require.NoError(t, tr.Scan(dynamicpb.NewMessage(cell)), "%s", dst)

	_, err = pxf.AppendDatasetRow(nil, row(pxf.MaxNestingDepth), columns)
	require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100")
}
