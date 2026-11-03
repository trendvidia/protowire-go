// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Draft -01 § Numeric Literals: a decoder reading "nan" stores the canonical
// quiet NaN, 0x7FF8000000000000 for a double and 0x7FC00000 for a float
// (protowire-java#161). strconv.ParseFloat gives math.NaN(), whose bits are
// 0x7FF8000000000001, so before this every double NaN Go read from text was
// written with different bytes from every other port.

const (
	canonicalNaN64 = 0x7FF8000000000000
	canonicalNaN32 = 0x7FC00000
)

func TestNaN_DecodedCanonically(t *testing.T) {
	fd := compileFiles(t, map[string]string{
		"nan.proto": `
syntax = "proto3";
package nan.v1;
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";
message M {
  double d = 1;
  float f = 2;
  repeated double ds = 3;
  repeated float fs = 4;
  google.protobuf.DoubleValue dw = 5;
  google.protobuf.FloatValue fw = 6;
  optional double d_default = 7 [(pxf.default) = "nan"];
  optional float f_default = 8 [(pxf.default) = "nan"];
}
`,
		"pxf/annotations.proto": annotationsProtoSrc,
	})
	desc := findMsg(t, fd, "M")
	field := func(name string) protoreflect.FieldDescriptor { return desc.Fields().ByName(protoreflect.Name(name)) }

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte("d = nan\nf = nan\nds = [nan, 1]\nfs = [nan]\ndw = nan\nfw = nan\n"), desc)
	require.NoError(t, err)

	double := func(name string, v float64) {
		t.Helper()
		assert.Equal(t, uint64(canonicalNaN64), math.Float64bits(v), "%s: %#x", name, math.Float64bits(v))
	}
	float := func(name string, v float64) {
		t.Helper()
		assert.Equal(t, uint32(canonicalNaN32), math.Float32bits(float32(v)), "%s: %#x", name, math.Float32bits(float32(v)))
	}
	double("d", msg.Get(field("d")).Float())
	float("f", msg.Get(field("f")).Float())
	double("ds[0]", msg.Get(field("ds")).List().Get(0).Float())
	float("fs[0]", msg.Get(field("fs")).List().Get(0).Float())
	inner := func(name string) float64 {
		w := msg.Get(field(name)).Message()
		return w.Get(w.Descriptor().Fields().ByName("value")).Float()
	}
	double("dw", inner("dw"))
	float("fw", inner("fw"))
	double("d_default", msg.Get(field("d_default")).Float())
	float("f_default", msg.Get(field("f_default")).Float())

	// And so the bytes: field 1, fixed64, 00 00 00 00 00 00 f8 7f.
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	require.NoError(t, err)
	assert.Equal(t, "09000000000000f87f", hexOf(out[:9]))
}

// A message's fields are written as they are: a NaN that arrives in PB with a
// payload keeps it. The rule above is about what a decoder chooses, never
// about bits it was given.
func TestNaN_PayloadFromWireIsKept(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	fd := desc.Fields().ByName("double_field")
	in := dynamicpb.NewMessage(desc)
	in.Set(fd, protoreflect.ValueOfFloat64(math.Float64frombits(0x7FF8000000000001)))
	wire, err := proto.Marshal(in)
	require.NoError(t, err)

	back := dynamicpb.NewMessage(desc)
	require.NoError(t, proto.Unmarshal(wire, back))
	again, err := proto.Marshal(back)
	require.NoError(t, err)
	assert.Equal(t, wire, again)
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
