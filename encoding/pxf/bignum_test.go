// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const bignumProtoSrc = `
syntax = "proto3";
package pxf;

message BigInt {
  bytes abs = 1;
  bool negative = 2;
}

message Decimal {
  bytes unscaled = 1;
  int32 scale = 2;
  bool negative = 3;
}

message BigFloat {
  bytes mantissa = 1;
  int32 exponent = 2;
  uint32 prec = 3;
  bool negative = 4;
}
`

const bignumTestProtoSrc = `
syntax = "proto3";
package bignum_test.v1;

import "pxf/bignum.proto";
import "pxf/annotations.proto";

message BigNumDemo {
  pxf.BigInt big_int_field = 1;
  pxf.Decimal decimal_field = 2;
  pxf.BigFloat big_float_field = 3;
  repeated pxf.BigInt repeated_big_int = 4;
  repeated pxf.Decimal repeated_decimal = 5;
  repeated pxf.BigFloat repeated_big_float = 6;
}

message BigNumDefaults {
  pxf.BigInt int_with_default = 1 [(pxf.default) = "42"];
  pxf.Decimal dec_with_default = 2 [(pxf.default) = "3.14"];
  pxf.BigFloat float_with_default = 3 [(pxf.default) = "2.718"];
}

message BigNumMaps {
  map<string, pxf.BigInt> weights = 1;
  map<string, pxf.Decimal> rates = 2;
}
`

func compileBigNumProto(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":            bignumTestProtoSrc,
		"pxf/bignum.proto":      bignumProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
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

func bigNumDesc(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileBigNumProto(t)
	md := fd.Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md, "message %q not found", name)
	return md
}

// readBigIntFromMsg extracts the big.Int value from a pxf.BigInt message field.
func readBigIntFromMsg(msg protoreflect.Message, fieldName string) *big.Int {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	sub := msg.Get(fd).Message()
	d := sub.Descriptor()
	absBytes := sub.Get(d.Fields().ByName("abs")).Bytes()
	negative := sub.Get(d.Fields().ByName("negative")).Bool()
	v := new(big.Int).SetBytes(absBytes)
	if negative {
		v.Neg(v)
	}
	return v
}

// readDecimalFromMsg extracts scale and unscaled from a pxf.Decimal message field.
func readDecimalFromMsg(msg protoreflect.Message, fieldName string) (unscaled *big.Int, scale int32, negative bool) {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	sub := msg.Get(fd).Message()
	d := sub.Descriptor()
	unscaledBytes := sub.Get(d.Fields().ByName("unscaled")).Bytes()
	scale = int32(sub.Get(d.Fields().ByName("scale")).Int())
	negative = sub.Get(d.Fields().ByName("negative")).Bool()
	unscaled = new(big.Int).SetBytes(unscaledBytes)
	return
}

func TestBigIntMapValueScalarShorthand(t *testing.T) {
	desc := bigNumDesc(t, "BigNumMaps")
	input := `weights = {
  "alpha": 100
  "beta": -7
}`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("weights")
	m := msg.ProtoReflect().Get(fd).Map()
	require.Equal(t, 2, m.Len())

	read := func(key string) *big.Int {
		sub := m.Get(protoreflect.ValueOfString(key).MapKey()).Message()
		d := sub.Descriptor()
		absBytes := sub.Get(d.Fields().ByName("abs")).Bytes()
		negative := sub.Get(d.Fields().ByName("negative")).Bool()
		v := new(big.Int).SetBytes(absBytes)
		if negative {
			v.Neg(v)
		}
		return v
	}
	assert.Equal(t, big.NewInt(100), read("alpha"))
	assert.Equal(t, big.NewInt(-7), read("beta"))

	// Encode round-trip emits scalar shorthand, not block form.
	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, "alpha: 100")
	assert.Contains(t, s, "beta: -7")
	assert.NotContains(t, s, "alpha: {")
}

func TestDecimalMapValueScalarShorthand(t *testing.T) {
	desc := bigNumDesc(t, "BigNumMaps")
	input := `rates = {
  "usd": 1.00
  "eur": 0.92
}`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("rates")
	m := msg.ProtoReflect().Get(fd).Map()
	require.Equal(t, 2, m.Len())
}

func TestBigIntBasic(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	// uint256 max
	input := `big_int_field = 115792089237316195423570985008687907853269984665640564039457584007913129639935`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	expected, _ := new(big.Int).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639935", 10)
	actual := readBigIntFromMsg(msg.ProtoReflect(), "big_int_field")
	assert.Equal(t, expected, actual)
}

func TestBigIntNegative(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `big_int_field = -42`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	actual := readBigIntFromMsg(msg.ProtoReflect(), "big_int_field")
	assert.Equal(t, big.NewInt(-42), actual)
}

func TestBigIntZero(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `big_int_field = 0`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	actual := readBigIntFromMsg(msg.ProtoReflect(), "big_int_field")
	assert.Equal(t, big.NewInt(0), actual)
}

func TestDecimalBasic(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `decimal_field = 79228.162514264337593543950335`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	unscaled, scale, negative := readDecimalFromMsg(msg.ProtoReflect(), "decimal_field")
	expectedUnscaled, _ := new(big.Int).SetString("79228162514264337593543950335", 10)
	assert.Equal(t, expectedUnscaled, unscaled)
	assert.Equal(t, int32(24), scale)
	assert.False(t, negative)
}

func TestDecimalPrecisionPreservation(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")

	t.Run("1.0 has scale 1", func(t *testing.T) {
		input := `decimal_field = 1.0`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)
		_, scale, _ := readDecimalFromMsg(msg.ProtoReflect(), "decimal_field")
		assert.Equal(t, int32(1), scale)
	})

	t.Run("1.00 has scale 2", func(t *testing.T) {
		input := `decimal_field = 1.00`
		msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
		require.NoError(t, err)
		_, scale, _ := readDecimalFromMsg(msg.ProtoReflect(), "decimal_field")
		assert.Equal(t, int32(2), scale)
	})
}

func TestDecimalInteger(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `decimal_field = 42`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	unscaled, scale, negative := readDecimalFromMsg(msg.ProtoReflect(), "decimal_field")
	assert.Equal(t, big.NewInt(42), unscaled)
	assert.Equal(t, int32(0), scale)
	assert.False(t, negative)
}

func TestDecimalNegative(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `decimal_field = -123.45`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	unscaled, scale, negative := readDecimalFromMsg(msg.ProtoReflect(), "decimal_field")
	assert.Equal(t, big.NewInt(12345), unscaled)
	assert.Equal(t, int32(2), scale)
	assert.True(t, negative)
}

func TestBigFloatBasic(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `big_float_field = 6.02214076e+23`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("big_float_field")
	sub := msg.ProtoReflect().Get(fd).Message()
	prec := sub.Get(sub.Descriptor().Fields().ByName("prec")).Uint()
	assert.True(t, prec > 0, "prec should be set")
}

func TestBigFloatInteger(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `big_float_field = 42`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("big_float_field")
	sub := msg.ProtoReflect().Get(fd).Message()
	prec := sub.Get(sub.Descriptor().Fields().ByName("prec")).Uint()
	assert.True(t, prec > 0, "prec should be set")
}

func TestBigIntRoundTrip(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `big_int_field = -115792089237316195423570985008687907853269984665640564039457584007913129639935`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	output, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(output), "-115792089237316195423570985008687907853269984665640564039457584007913129639935")

	// Unmarshal again and verify
	msg2, err := pxf.UnmarshalDescriptor(output, desc)
	require.NoError(t, err)
	v1 := readBigIntFromMsg(msg.ProtoReflect(), "big_int_field")
	v2 := readBigIntFromMsg(msg2.ProtoReflect(), "big_int_field")
	assert.Equal(t, v1, v2)
}

func TestDecimalRoundTrip(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `decimal_field = -123.456789`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	output, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(output), "-123.456789")

	msg2, err := pxf.UnmarshalDescriptor(output, desc)
	require.NoError(t, err)
	u1, s1, n1 := readDecimalFromMsg(msg.ProtoReflect(), "decimal_field")
	u2, s2, n2 := readDecimalFromMsg(msg2.ProtoReflect(), "decimal_field")
	assert.Equal(t, u1, u2)
	assert.Equal(t, s1, s2)
	assert.Equal(t, n1, n2)
}

func TestBigFloatRoundTrip(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `big_float_field = 3.14159265358979323846`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	output, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(output), "3.14159265358979323846") ||
		strings.Contains(string(output), "3.1415926535897932384"), // precision-dependent rounding
		"output should contain pi digits: %s", string(output))
}

func TestRepeatedBigInt(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `repeated_big_int = [1, -2, 115792089237316195423570985008687907853269984665640564039457584007913129639935]`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("repeated_big_int")
	list := msg.ProtoReflect().Get(fd).List()
	assert.Equal(t, 3, list.Len())
}

func TestRepeatedDecimal(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `repeated_decimal = [1.0, 2.50, -0.001]`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("repeated_decimal")
	list := msg.ProtoReflect().Get(fd).List()
	assert.Equal(t, 3, list.Len())
}

func TestRepeatedBigFloat(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	input := `repeated_big_float = [1e100, -3.14159]`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	fd := desc.Fields().ByName("repeated_big_float")
	list := msg.ProtoReflect().Get(fd).List()
	assert.Equal(t, 2, list.Len())
}

func TestBigNumDefaults(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDefaults")
	input := `` // empty input, defaults should apply
	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)

	bi := readBigIntFromMsg(msg.ProtoReflect(), "int_with_default")
	assert.Equal(t, big.NewInt(42), bi)

	u, s, neg := readDecimalFromMsg(msg.ProtoReflect(), "dec_with_default")
	assert.Equal(t, big.NewInt(314), u)
	assert.Equal(t, int32(2), s)
	assert.False(t, neg)
}
