// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

const sbeAnnotationsSrc = `
syntax = "proto3";
package sbe;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FileOptions {
  uint32 schema_id = 50100;
  uint32 version = 50101;
}
extend google.protobuf.MessageOptions {
  uint32 template_id = 50200;
}
extend google.protobuf.FieldOptions {
  uint32 length = 50300;
  string encoding = 50301;
}
`

const testProtoSrc = `
syntax = "proto3";
package test.v1;
import "sbe/annotations.proto";

option (sbe.schema_id) = 1;
option (sbe.version) = 0;

enum Side {
  SIDE_BUY = 0;
  SIDE_SELL = 1;
}

message Order {
  option (sbe.template_id) = 1;
  uint64 order_id = 1;
  string symbol = 2 [(sbe.length) = 8];
  int64 price = 3;
  uint32 quantity = 4;
  Side side = 5;
  bool active = 6;
  double weight = 7;
  float score = 8;

  message Fill {
    int64 fill_price = 1;
    uint32 fill_qty = 2;
    uint64 fill_id = 3;
  }
  repeated Fill fills = 9;
}

message Simple {
  option (sbe.template_id) = 2;
  uint32 id = 1;
  int32 value = 2;
}

message WithComposite {
  option (sbe.template_id) = 3;
  uint64 id = 1;
  Inner inner = 2;
  int32 code = 3;
}

message Inner {
  int64 x = 1;
  int64 y = 2;
}

message WithNarrow {
  option (sbe.template_id) = 4;
  uint32 status = 1 [(sbe.encoding) = "uint8"];
  uint32 port = 2 [(sbe.encoding) = "uint16"];
  int32 delta = 3 [(sbe.encoding) = "int16"];
}
`

func compileTestProto(t testing.TB) protoreflect.FileDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"sbe/annotations.proto": sbeAnnotationsSrc,
					"test.proto":            testProtoSrc,
				}),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	for _, f := range result {
		if f.Path() == "test.proto" {
			return f
		}
	}
	t.Fatal("test.proto not found")
	return nil
}

func TestSimpleRoundTrip(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Simple")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint32(42))
	msg.Set(desc.Fields().ByName("value"), protoreflect.ValueOfInt32(-100))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	// Header(8) + id:uint32(4) + value:int32(4) = 16
	require.Equal(t, 16, len(data))

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, uint64(42), got.Get(desc.Fields().ByName("id")).Uint())
	require.Equal(t, int64(-100), got.Get(desc.Fields().ByName("value")).Int())
}

func TestOrderRoundTrip(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("order_id"), protoreflect.ValueOfUint64(1001))
	msg.Set(desc.Fields().ByName("symbol"), protoreflect.ValueOfString("AAPL"))
	msg.Set(desc.Fields().ByName("price"), protoreflect.ValueOfInt64(19150))
	msg.Set(desc.Fields().ByName("quantity"), protoreflect.ValueOfUint32(100))
	msg.Set(desc.Fields().ByName("side"), protoreflect.ValueOfEnum(1)) // SELL
	msg.Set(desc.Fields().ByName("active"), protoreflect.ValueOfBool(true))
	msg.Set(desc.Fields().ByName("weight"), protoreflect.ValueOfFloat64(0.85))
	msg.Set(desc.Fields().ByName("score"), protoreflect.ValueOfFloat32(3.14))

	// Add fills.
	fillsField := desc.Fields().ByName("fills")
	fillDesc := fillsField.Message()
	list := msg.Mutable(fillsField).List()

	fill1 := list.NewElement()
	fill1.Message().Set(fillDesc.Fields().ByName("fill_price"), protoreflect.ValueOfInt64(19155))
	fill1.Message().Set(fillDesc.Fields().ByName("fill_qty"), protoreflect.ValueOfUint32(25))
	fill1.Message().Set(fillDesc.Fields().ByName("fill_id"), protoreflect.ValueOfUint64(5001))
	list.Append(fill1)

	fill2 := list.NewElement()
	fill2.Message().Set(fillDesc.Fields().ByName("fill_price"), protoreflect.ValueOfInt64(19160))
	fill2.Message().Set(fillDesc.Fields().ByName("fill_qty"), protoreflect.ValueOfUint32(50))
	fill2.Message().Set(fillDesc.Fields().ByName("fill_id"), protoreflect.ValueOfUint64(5002))
	list.Append(fill2)

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	// Root: order_id(8)+symbol(8)+price(8)+quantity(4)+side(1)+active(1)+weight(8)+score(4) = 42
	// Header(8) + root(42) + group_header(4) + 2×fill(20) = 94
	require.Equal(t, 94, len(data))

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, uint64(1001), got.Get(desc.Fields().ByName("order_id")).Uint())
	require.Equal(t, "AAPL", got.Get(desc.Fields().ByName("symbol")).String())
	require.Equal(t, int64(19150), got.Get(desc.Fields().ByName("price")).Int())
	require.Equal(t, uint64(100), got.Get(desc.Fields().ByName("quantity")).Uint())
	require.Equal(t, protoreflect.EnumNumber(1), got.Get(desc.Fields().ByName("side")).Enum())
	require.True(t, got.Get(desc.Fields().ByName("active")).Bool())
	require.InDelta(t, 0.85, got.Get(desc.Fields().ByName("weight")).Float(), 1e-10)
	require.InDelta(t, 3.14, float64(float32(got.Get(desc.Fields().ByName("score")).Float())), 1e-6)

	gotList := got.Get(fillsField).List()
	require.Equal(t, 2, gotList.Len())

	gotFill1 := gotList.Get(0).Message()
	require.Equal(t, int64(19155), gotFill1.Get(fillDesc.Fields().ByName("fill_price")).Int())
	require.Equal(t, uint64(25), gotFill1.Get(fillDesc.Fields().ByName("fill_qty")).Uint())
	require.Equal(t, uint64(5001), gotFill1.Get(fillDesc.Fields().ByName("fill_id")).Uint())

	gotFill2 := gotList.Get(1).Message()
	require.Equal(t, int64(19160), gotFill2.Get(fillDesc.Fields().ByName("fill_price")).Int())
	require.Equal(t, uint64(50), gotFill2.Get(fillDesc.Fields().ByName("fill_qty")).Uint())
	require.Equal(t, uint64(5002), gotFill2.Get(fillDesc.Fields().ByName("fill_id")).Uint())
}

func TestCompositeRoundTrip(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("WithComposite")
	require.NotNil(t, desc)

	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint64(99))

	innerField := desc.Fields().ByName("inner")
	innerDesc := innerField.Message()
	innerMsg := msg.Mutable(innerField).Message()
	innerMsg.Set(innerDesc.Fields().ByName("x"), protoreflect.ValueOfInt64(100))
	innerMsg.Set(innerDesc.Fields().ByName("y"), protoreflect.ValueOfInt64(-200))

	msg.Set(desc.Fields().ByName("code"), protoreflect.ValueOfInt32(42))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	// Header(8) + id:uint64(8) + inner:x(8)+y(8) + code:int32(4) = 36
	require.Equal(t, 36, len(data))

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, uint64(99), got.Get(desc.Fields().ByName("id")).Uint())
	gotInner := got.Get(innerField).Message()
	require.Equal(t, int64(100), gotInner.Get(innerDesc.Fields().ByName("x")).Int())
	require.Equal(t, int64(-200), gotInner.Get(innerDesc.Fields().ByName("y")).Int())
	require.Equal(t, int64(42), got.Get(desc.Fields().ByName("code")).Int())
}

func TestStringTruncation(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("symbol"), protoreflect.ValueOfString("LONGERTHAN8"))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	// Symbol has sbe.length=8, so truncated to 8 chars.
	require.Equal(t, "LONGERTH", got.Get(desc.Fields().ByName("symbol")).String())
}

func TestEmptyGroup(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("order_id"), protoreflect.ValueOfUint64(1))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	// Header(8) + root(42) + group_header(4) = 54
	require.Equal(t, 54, len(data))

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, uint64(1), got.Get(desc.Fields().ByName("order_id")).Uint())
	require.Equal(t, 0, got.Get(desc.Fields().ByName("fills")).List().Len())
}

func TestNarrowEncoding(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("WithNarrow")
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("status"), protoreflect.ValueOfUint32(200))
	msg.Set(desc.Fields().ByName("port"), protoreflect.ValueOfUint32(8080))
	msg.Set(desc.Fields().ByName("delta"), protoreflect.ValueOfInt32(-1234))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	// Header(8) + status:uint8(1) + port:uint16(2) + delta:int16(2) = 13
	require.Equal(t, 13, len(data))

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, uint64(200), got.Get(desc.Fields().ByName("status")).Uint())
	require.Equal(t, uint64(8080), got.Get(desc.Fields().ByName("port")).Uint())
	require.Equal(t, int64(-1234), got.Get(desc.Fields().ByName("delta")).Int())
}

func TestZeroValues(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Simple")
	msg := dynamicpb.NewMessage(desc) // all zero values

	data, err := codec.Marshal(msg)
	require.NoError(t, err)
	require.Equal(t, 16, len(data))

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, uint64(0), got.Get(desc.Fields().ByName("id")).Uint())
	require.Equal(t, int64(0), got.Get(desc.Fields().ByName("value")).Int())
}

func TestNegativeInt(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("price"), protoreflect.ValueOfInt64(-99999))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	got := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, got)
	require.NoError(t, err)

	require.Equal(t, int64(-99999), got.Get(desc.Fields().ByName("price")).Int())
}

func TestViewScalars(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("order_id"), protoreflect.ValueOfUint64(1001))
	msg.Set(desc.Fields().ByName("symbol"), protoreflect.ValueOfString("AAPL"))
	msg.Set(desc.Fields().ByName("price"), protoreflect.ValueOfInt64(19150))
	msg.Set(desc.Fields().ByName("quantity"), protoreflect.ValueOfUint32(100))
	msg.Set(desc.Fields().ByName("side"), protoreflect.ValueOfEnum(1))
	msg.Set(desc.Fields().ByName("active"), protoreflect.ValueOfBool(true))
	msg.Set(desc.Fields().ByName("weight"), protoreflect.ValueOfFloat64(0.85))
	msg.Set(desc.Fields().ByName("score"), protoreflect.ValueOfFloat32(3.14))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	v, err := codec.View(data)
	require.NoError(t, err)

	require.Equal(t, uint64(1001), v.Uint("order_id"))
	require.Equal(t, "AAPL", v.String("symbol"))
	require.Equal(t, int64(19150), v.Int("price"))
	require.Equal(t, uint64(100), v.Uint("quantity"))
	require.Equal(t, 1, v.Enum("side"))
	require.True(t, v.Bool("active"))
	require.InDelta(t, 0.85, v.Float("weight"), 1e-10)
	require.InDelta(t, 3.14, v.Float("score"), 1e-6)
}

func TestViewGroup(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	fillDesc := desc.Fields().ByName("fills").Message()
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("order_id"), protoreflect.ValueOfUint64(1))

	list := msg.Mutable(desc.Fields().ByName("fills")).List()
	f1 := list.NewElement()
	f1.Message().Set(fillDesc.Fields().ByName("fill_price"), protoreflect.ValueOfInt64(100))
	f1.Message().Set(fillDesc.Fields().ByName("fill_qty"), protoreflect.ValueOfUint32(10))
	f1.Message().Set(fillDesc.Fields().ByName("fill_id"), protoreflect.ValueOfUint64(7))
	list.Append(f1)
	f2 := list.NewElement()
	f2.Message().Set(fillDesc.Fields().ByName("fill_price"), protoreflect.ValueOfInt64(200))
	f2.Message().Set(fillDesc.Fields().ByName("fill_qty"), protoreflect.ValueOfUint32(20))
	f2.Message().Set(fillDesc.Fields().ByName("fill_id"), protoreflect.ValueOfUint64(8))
	list.Append(f2)

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	v, err := codec.View(data)
	require.NoError(t, err)

	fills := v.Group("fills")
	require.Equal(t, 2, fills.Len())

	e0 := fills.Entry(0)
	require.Equal(t, int64(100), e0.Int("fill_price"))
	require.Equal(t, uint64(10), e0.Uint("fill_qty"))
	require.Equal(t, uint64(7), e0.Uint("fill_id"))

	e1 := fills.Entry(1)
	require.Equal(t, int64(200), e1.Int("fill_price"))
	require.Equal(t, uint64(20), e1.Uint("fill_qty"))
	require.Equal(t, uint64(8), e1.Uint("fill_id"))
}

func TestViewComposite(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("WithComposite")
	innerDesc := desc.Fields().ByName("inner").Message()
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint64(99))
	inner := msg.Mutable(desc.Fields().ByName("inner")).Message()
	inner.Set(innerDesc.Fields().ByName("x"), protoreflect.ValueOfInt64(100))
	inner.Set(innerDesc.Fields().ByName("y"), protoreflect.ValueOfInt64(-200))
	msg.Set(desc.Fields().ByName("code"), protoreflect.ValueOfInt32(42))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	v, err := codec.View(data)
	require.NoError(t, err)

	require.Equal(t, uint64(99), v.Uint("id"))
	require.Equal(t, int64(42), v.Int("code"))

	iv := v.Composite("inner")
	require.Equal(t, int64(100), iv.Int("x"))
	require.Equal(t, int64(-200), iv.Int("y"))
}

func TestViewEmptyGroup(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("order_id"), protoreflect.ValueOfUint64(1))

	data, err := codec.Marshal(msg)
	require.NoError(t, err)

	v, err := codec.View(data)
	require.NoError(t, err)

	fills := v.Group("fills")
	require.Equal(t, 0, fills.Len())
}
