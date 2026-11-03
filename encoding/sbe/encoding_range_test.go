// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"encoding/binary"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// rangeCodec binds one message M whose only field v is declared by decl.
func rangeCodec(t *testing.T, decl string) (*sbe.Codec, protoreflect.MessageDescriptor, protoreflect.FieldDescriptor) {
	t.Helper()
	fd := boundsFile(t, "1", "0", fmt.Sprintf(`enum K { K0 = 0; } message M { option (sbe.template_id) = 1; %s; }`, decl))
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	md := fd.Messages().ByName("M")
	return codec, md, md.Fields().ByName("v")
}

// TestMarshalEncodingRange: draft -01 § SBE Annotations, "Emitters MUST
// reject values that fall outside the narrowed type's range". Before the fix
// Marshal kept the low bits, so int8 300 was written as 44 and read back as
// 44 with no error (issue #252). Every refused value is paired with the value
// at the bound, which must round-trip exactly.
func TestMarshalEncodingRange(t *testing.T) {
	type row struct {
		decl   string
		ok     []protoreflect.Value
		refuse []protoreflect.Value
		want   string // the refusal's range, as the error states it
	}
	i32, i64 := protoreflect.ValueOfInt32, protoreflect.ValueOfInt64
	u32, u64 := protoreflect.ValueOfUint32, protoreflect.ValueOfUint64
	enum := func(n int32) protoreflect.Value { return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)) }
	rows := []row{
		{`int32 v = 1 [(sbe.encoding) = "int8"]`, []protoreflect.Value{i32(127), i32(-128)}, []protoreflect.Value{i32(128), i32(-129), i32(300)}, "int8, [-128, 127]"},
		{`int32 v = 1 [(sbe.encoding) = "int16"]`, []protoreflect.Value{i32(32767), i32(-32768)}, []protoreflect.Value{i32(32768), i32(-32769)}, "int16, [-32768, 32767]"},
		{`int64 v = 1 [(sbe.encoding) = "int32"]`, []protoreflect.Value{i64(math.MaxInt32), i64(math.MinInt32)}, []protoreflect.Value{i64(math.MaxInt32 + 1), i64(math.MinInt32 - 1)}, "int32, [-2147483648, 2147483647]"},
		{`uint64 v = 1 [(sbe.encoding) = "int64"]`, []protoreflect.Value{u64(math.MaxInt64)}, []protoreflect.Value{u64(math.MaxInt64 + 1)}, "int64, [-9223372036854775808, 9223372036854775807]"},
		{`uint32 v = 1 [(sbe.encoding) = "uint8"]`, []protoreflect.Value{u32(255)}, []protoreflect.Value{u32(256)}, "uint8, [0, 255]"},
		{`uint32 v = 1 [(sbe.encoding) = "uint16"]`, []protoreflect.Value{u32(65535)}, []protoreflect.Value{u32(65536), u32(70000)}, "uint16, [0, 65535]"},
		{`uint64 v = 1 [(sbe.encoding) = "uint32"]`, []protoreflect.Value{u64(math.MaxUint32)}, []protoreflect.Value{u64(math.MaxUint32 + 1)}, "uint32, [0, 4294967295]"},
		{`int64 v = 1 [(sbe.encoding) = "uint64"]`, []protoreflect.Value{i64(0), i64(math.MaxInt64)}, []protoreflect.Value{i64(-1)}, "uint64, [0, 18446744073709551615]"},
		{`sint32 v = 1 [(sbe.encoding) = "uint8"]`, []protoreflect.Value{i32(0), i32(255)}, []protoreflect.Value{i32(-1), i32(256)}, "uint8, [0, 255]"},
		{`uint64 v = 1 [(sbe.encoding) = "int8"]`, []protoreflect.Value{u64(127)}, []protoreflect.Value{u64(128)}, "int8, [-128, 127]"},
		// An enum with no annotation is laid out as uint8.
		{`K v = 1`, []protoreflect.Value{enum(0), enum(255)}, []protoreflect.Value{enum(256), enum(-1)}, "uint8, [0, 255]"},
		{`K v = 1 [(sbe.encoding) = "int16"]`, []protoreflect.Value{enum(-32768), enum(32767)}, []protoreflect.Value{enum(32768)}, "int16, [-32768, 32767]"},
	}
	for _, r := range rows {
		t.Run(r.decl, func(t *testing.T) {
			codec, md, f := rangeCodec(t, r.decl)
			for _, v := range r.ok {
				msg := dynamicpb.NewMessage(md)
				msg.Set(f, v)
				data, err := codec.Marshal(msg)
				require.NoError(t, err, "value %v", v)
				back, err := codec.UnmarshalDescriptor(data, md)
				require.NoError(t, err, "value %v", v)
				require.Equal(t, v.Interface(), back.Get(f).Interface())
			}
			for _, v := range r.refuse {
				msg := dynamicpb.NewMessage(md)
				msg.Set(f, v)
				_, err := codec.Marshal(msg)
				require.Error(t, err, "value %v", v)
				require.Equal(t, fmt.Sprintf("sbe: field bounds.v1.M.v: value %v is outside the range of %s", v.Interface(), r.want), err.Error())
			}
		})
	}
}

// TestMarshalFloatRange: a double narrowed to float must be within ±MaxFloat32.
// The infinities and NaN are float values and pass; a finite double past the
// bound is refused even when it would round down to it.
func TestMarshalFloatRange(t *testing.T) {
	codec, md, f := rangeCodec(t, `double v = 1 [(sbe.encoding) = "float"]`)
	past := math.Nextafter(math.MaxFloat32, math.Inf(1))
	for _, v := range []float64{math.MaxFloat32, -math.MaxFloat32, math.Inf(1), math.Inf(-1), math.NaN(), 0.1} {
		msg := dynamicpb.NewMessage(md)
		msg.Set(f, protoreflect.ValueOfFloat64(v))
		data, err := codec.Marshal(msg)
		require.NoError(t, err, "value %g", v)
		back, err := codec.UnmarshalDescriptor(data, md)
		require.NoError(t, err)
		got := back.Get(f).Float()
		if math.IsNaN(v) {
			require.True(t, math.IsNaN(got))
		} else {
			require.Equal(t, float64(float32(v)), got)
		}
	}
	for _, v := range []float64{past, -past, 1e39} {
		msg := dynamicpb.NewMessage(md)
		msg.Set(f, protoreflect.ValueOfFloat64(v))
		_, err := codec.Marshal(msg)
		require.Error(t, err, "value %g", v)
		require.Contains(t, err.Error(), "sbe: field bounds.v1.M.v: value")
		require.Contains(t, err.Error(), "is outside the range of float")
	}
}

// TestMarshalEncodingRangeNested: the refusal holds in a composite and in a
// group entry, and names the field there.
func TestMarshalEncodingRangeNested(t *testing.T) {
	fd := boundsFile(t, "1", "0", `
message C { int32 c = 1 [(sbe.encoding) = "int8"]; }
message E { uint32 e = 1 [(sbe.encoding) = "uint8"]; }
message M { option (sbe.template_id) = 1; C comp = 1; repeated E grp = 2; }`)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	md := fd.Messages().ByName("M")
	cfd, efd := fd.Messages().ByName("C").Fields().ByName("c"), fd.Messages().ByName("E").Fields().ByName("e")

	build := func(c int32, e uint32) *dynamicpb.Message {
		msg := dynamicpb.NewMessage(md)
		msg.Mutable(md.Fields().ByName("comp")).Message().Set(cfd, protoreflect.ValueOfInt32(c))
		list := msg.Mutable(md.Fields().ByName("grp")).List()
		el := list.NewElement()
		el.Message().Set(efd, protoreflect.ValueOfUint32(e))
		list.Append(el)
		return msg
	}
	_, err = codec.Marshal(build(127, 255))
	require.NoError(t, err)
	_, err = codec.Marshal(build(128, 255))
	require.EqualError(t, err, "sbe: field bounds.v1.C.c: value 128 is outside the range of int8, [-128, 127]")
	_, err = codec.Marshal(build(127, 256))
	require.EqualError(t, err, "sbe: field bounds.v1.E.e: value 256 is outside the range of uint8, [0, 255]")
}

// TestUnmarshalEncodingRange: a primitive whose values the field cannot all
// hold — a wider one, or one of the other signedness — is refused on read
// when the value does not fit, rather than cut to the field's low bits. Each
// refusal is paired with the wire value at the bound. The bytes are what
// another emitter could write; this codec's Marshal never does.
func TestUnmarshalEncodingRange(t *testing.T) {
	rows := []struct {
		decl   string
		ok     []uint64 // primitive bits, little-endian in the block
		refuse []uint64
	}{
		{`int32 v = 1 [(sbe.encoding) = "int64"]`, []uint64{math.MaxInt32, neg(math.MinInt32)}, []uint64{math.MaxInt32 + 1, neg(math.MinInt32 - 1)}},
		{`int32 v = 1 [(sbe.encoding) = "uint32"]`, []uint64{math.MaxInt32}, []uint64{math.MaxInt32 + 1, math.MaxUint32}},
		{`uint32 v = 1 [(sbe.encoding) = "int8"]`, []uint64{0x7f, 0}, []uint64{0xff, 0x80}},
		{`uint32 v = 1 [(sbe.encoding) = "uint64"]`, []uint64{math.MaxUint32}, []uint64{math.MaxUint32 + 1}},
		{`uint64 v = 1 [(sbe.encoding) = "int64"]`, []uint64{math.MaxInt64}, []uint64{neg(-1)}},
		{`int64 v = 1 [(sbe.encoding) = "uint64"]`, []uint64{math.MaxInt64}, []uint64{math.MaxInt64 + 1}},
		{`K v = 1 [(sbe.encoding) = "uint32"]`, []uint64{math.MaxInt32}, []uint64{math.MaxInt32 + 1}},
		{`float v = 1 [(sbe.encoding) = "double"]`, []uint64{math.Float64bits(math.MaxFloat32), math.Float64bits(math.Inf(-1))}, []uint64{math.Float64bits(1e39), math.Float64bits(-1e39)}},
	}
	for _, r := range rows {
		t.Run(r.decl, func(t *testing.T) {
			codec, md, f := rangeCodec(t, r.decl)
			data, err := codec.Marshal(dynamicpb.NewMessage(md))
			require.NoError(t, err)
			size := len(data) - 8
			put := func(bits uint64) []byte {
				b := append([]byte(nil), data...)
				var w [8]byte
				binary.LittleEndian.PutUint64(w[:], bits)
				copy(b[8:], w[:size])
				return b
			}
			for _, bits := range r.ok {
				back, err := codec.UnmarshalDescriptor(put(bits), md)
				require.NoError(t, err, "bits %#x", bits)
				// What came back is what Marshal writes again.
				again, err := codec.Marshal(back)
				require.NoError(t, err)
				require.Equal(t, put(bits), again, "field %v", back.Get(f).Interface())
			}
			for _, bits := range r.refuse {
				_, err := codec.UnmarshalDescriptor(put(bits), md)
				require.Error(t, err, "bits %#x", bits)
				require.Contains(t, err.Error(), "sbe: field bounds.v1.M.v: wire value")
				require.Contains(t, err.Error(), "does not fit a")
			}
		})
	}
}

func neg(v int64) uint64 { return uint64(v) }

// TestEncodingKindBind: (sbe.encoding) narrows a numeric field. A string or
// bytes field, and a crossing between integers and floating point, are
// refused when the codec is built; before the fix each one bound and then
// panicked in Marshal on every value (issue #252).
func TestEncodingKindBind(t *testing.T) {
	for decl, want := range map[string]string{
		`int32 v = 1 [(sbe.encoding) = "float"]`:                    `field bounds.v1.M.v: (sbe.encoding) = "float" is a floating-point primitive and the field is int32`,
		`K v = 1 [(sbe.encoding) = "double"]`:                       `field bounds.v1.M.v: (sbe.encoding) = "double" is a floating-point primitive and the field is enum`,
		`bool v = 1 [(sbe.encoding) = "float"]`:                     `field bounds.v1.M.v: (sbe.encoding) = "float" is a floating-point primitive and the field is bool`,
		`double v = 1 [(sbe.encoding) = "int32"]`:                   `field bounds.v1.M.v: (sbe.encoding) = "int32" is an integer primitive and the field is double`,
		`float v = 1 [(sbe.encoding) = "uint8"]`:                    `field bounds.v1.M.v: (sbe.encoding) = "uint8" is an integer primitive and the field is float`,
		`string v = 1 [(sbe.length) = 4, (sbe.encoding) = "uint8"]`: `field bounds.v1.M.v: (sbe.encoding) = "uint8" on a string field`,
		`bytes v = 1 [(sbe.length) = 4, (sbe.encoding) = "int32"]`:  `field bounds.v1.M.v: (sbe.encoding) = "int32" on a bytes field`,
		`double v = 1 [(sbe.encoding) = "char"]`:                    `field bounds.v1.M.v: unknown encoding "char"`,
	} {
		t.Run(decl, func(t *testing.T) {
			fd := boundsFile(t, "1", "0", fmt.Sprintf(`enum K { K0 = 0; } message M { option (sbe.template_id) = 1; %s; }`, decl))
			_, err := sbe.NewCodec(fd)
			require.Error(t, err)
			require.Contains(t, err.Error(), want)
		})
	}
}

// TestEncodingMatrix pins every pairing of an integer, bool, enum or floating
// field with every (sbe.encoding): it is refused when the codec is built, or
// the value 1 round-trips. Before the fix 64 of these 100 pairings bound and
// then panicked in Marshal; 32 of those now bind and 32 are refused.
func TestEncodingMatrix(t *testing.T) {
	kinds := []string{"int32", "int64", "uint32", "uint64", "sint32", "fixed64", "float", "double", "bool", "K"}
	encs := []string{"int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64", "float", "double"}
	bound, refused := 0, 0
	for _, k := range kinds {
		for _, e := range encs {
			fd := boundsFile(t, "1", "0", fmt.Sprintf(`enum K { K0 = 0; K1 = 1; } message M { option (sbe.template_id) = 1; %s v = 1 [(sbe.encoding) = %q]; }`, k, e))
			codec, err := sbe.NewCodec(fd)
			isFloatKind := k == "float" || k == "double"
			isFloatEnc := e == "float" || e == "double"
			if isFloatKind != isFloatEnc {
				require.Error(t, err, "%s/%s", k, e)
				refused++
				continue
			}
			require.NoError(t, err, "%s/%s", k, e)
			bound++
			md := fd.Messages().ByName("M")
			f := md.Fields().ByName("v")
			one := map[protoreflect.Kind]protoreflect.Value{
				protoreflect.Int32Kind: protoreflect.ValueOfInt32(1), protoreflect.Sint32Kind: protoreflect.ValueOfInt32(1),
				protoreflect.Int64Kind: protoreflect.ValueOfInt64(1), protoreflect.Uint32Kind: protoreflect.ValueOfUint32(1),
				protoreflect.Uint64Kind: protoreflect.ValueOfUint64(1), protoreflect.Fixed64Kind: protoreflect.ValueOfUint64(1),
				protoreflect.FloatKind: protoreflect.ValueOfFloat32(1), protoreflect.DoubleKind: protoreflect.ValueOfFloat64(1),
				protoreflect.BoolKind: protoreflect.ValueOfBool(true), protoreflect.EnumKind: protoreflect.ValueOfEnum(1),
			}[f.Kind()]
			msg := dynamicpb.NewMessage(md)
			msg.Set(f, one)
			data, err := codec.Marshal(msg)
			require.NoError(t, err, "%s/%s", k, e)
			back, err := codec.UnmarshalDescriptor(data, md)
			require.NoError(t, err, "%s/%s", k, e)
			require.Equal(t, one.Interface(), back.Get(f).Interface(), "%s/%s", k, e)
		}
	}
	require.Equal(t, 68, bound)
	require.Equal(t, 32, refused)
}
