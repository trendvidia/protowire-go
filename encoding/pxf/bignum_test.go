// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pb"
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
  map<string, pxf.BigFloat> floats = 3;
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

// setDecimal fills a pxf.Decimal message in place: value = unscaled x
// 10^(-scale), per pxf/bignum.proto.
func setDecimal(sub protoreflect.Message, unscaled *big.Int, scale int32, negative bool) {
	d := sub.Descriptor()
	sub.Set(d.Fields().ByName("unscaled"), protoreflect.ValueOfBytes(unscaled.Bytes()))
	sub.Set(d.Fields().ByName("scale"), protoreflect.ValueOfInt32(scale))
	sub.Set(d.Fields().ByName("negative"), protoreflect.ValueOfBool(negative))
}

// decimalDemo is a BigNumDemo whose decimal_field is the given Decimal.
func decimalDemo(t *testing.T, desc protoreflect.MessageDescriptor, unscaled *big.Int, scale int32, negative bool) *dynamicpb.Message {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	fd := desc.Fields().ByName("decimal_field")
	setDecimal(msg.Mutable(fd).Message(), unscaled, scale, negative)
	return msg
}

// pbReadsDecimal is what encoding/pb makes of the same Decimal: the
// message is marshalled by protobuf-go (the schema's own reading of
// bignum.proto) into field 1 of a struct with a *big.Rat there.
func pbReadsDecimal(t *testing.T, sub protoreflect.Message) *big.Rat {
	t.Helper()
	bytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(sub.Interface())
	require.NoError(t, err)
	var wire []byte
	wire = protowire.AppendTag(wire, 1, protowire.BytesType)
	wire = protowire.AppendBytes(wire, bytes)
	var got struct {
		Price *big.Rat `protowire:"1"`
	}
	got.Price = new(big.Rat)
	require.NoError(t, pb.Unmarshal(wire, &got))
	return got.Price
}

// TestDecimalNegativeScale: value = unscaled x 10^(-scale), so a negative
// scale means trailing zeros — unscaled 5, scale -2 is 500 — which is
// how encoding/pb's decoder and the @default carrier reader have read it
// since #92. The encoder wrote the digits alone, so 500 marshalled as
// `5` and read back as unscaled 5, scale 0 (#118).
//
// encoding/pxf never writes a negative scale itself (parseDecimal yields
// a fraction's digit count), so the messages here are built directly,
// the way bytes from another producer arrive.
func TestDecimalNegativeScale(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")

	cases := []struct {
		name     string
		unscaled *big.Int
		scale    int32
		negative bool
		want     string
	}{
		{"5 x 10^2", big.NewInt(5), -2, false, "500"},
		{"-5 x 10^2", big.NewInt(5), -2, true, "-500"},
		{"125 x 10^1", big.NewInt(125), -1, false, "1250"},
		{"0 x 10^2 is 0, not 000", big.NewInt(0), -2, false, "0"},
		{"scale 0 unchanged", big.NewInt(42), 0, false, "42"},
		{"positive scale unchanged", big.NewInt(12345), 2, false, "123.45"},
		{"positive scale pads", big.NewInt(5), 2, false, "0.05"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := decimalDemo(t, desc, tc.unscaled, tc.scale, tc.negative)
			out, err := pxf.Marshal(msg)
			require.NoError(t, err)
			assert.Equal(t, "decimal_field = "+tc.want+"\n", string(out))

			// The literal reads back as the same VALUE. The scale is not
			// preserved through text — 500 has no negative-scale spelling —
			// so compare values, not (unscaled, scale) pairs.
			back, err := pxf.UnmarshalDescriptor(out, desc)
			require.NoError(t, err)
			u, s, n := readDecimalFromMsg(back.ProtoReflect(), "decimal_field")
			got := new(big.Rat).SetFrac(u, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(s)), nil))
			if n {
				got.Neg(got)
			}
			want, ok := new(big.Rat).SetString(tc.want)
			require.True(t, ok)
			assert.Zero(t, want.Cmp(got), "round trip: want %s, got %s", want, got)

			// And it is the value encoding/pb reads off the same message:
			// the pb decoder and the PXF encoder agreeing, on both signs of
			// the scale.
			viaPB := pbReadsDecimal(t, msg.Get(desc.Fields().ByName("decimal_field")).Message())
			assert.Zero(t, want.Cmp(viaPB), "encoding/pb reads %s, the PXF encoder wrote %s", viaPB, tc.want)
		})
	}

	t.Run("in a list and as a map value", func(t *testing.T) {
		// The other two encoder paths render through the same function;
		// this pins that they reach it.
		msg := dynamicpb.NewMessage(desc)
		list := msg.Mutable(desc.Fields().ByName("repeated_decimal")).List()
		setDecimal(list.AppendMutable().Message(), big.NewInt(7), -3, false)
		out, err := pxf.Marshal(msg)
		require.NoError(t, err)
		assert.Equal(t, "repeated_decimal = [\n  7000\n]\n", string(out))

		maps := bigNumDesc(t, "BigNumMaps")
		mm := dynamicpb.NewMessage(maps)
		m := mm.Mutable(maps.Fields().ByName("rates")).Map()
		setDecimal(m.Mutable(protoreflect.ValueOfString("k").MapKey()).Message(), big.NewInt(7), -3, true)
		out, err = pxf.Marshal(mm)
		require.NoError(t, err)
		assert.Equal(t, "rates = {\n  k: -7000\n}\n", string(out))
	})
}

// TestDecimalScaleIsBoundedOnMarshal: both arms of readDecimalStr do
// work proportional to |scale| — the negative one materialises
// 10^|scale| — from four bytes the message's producer chose, so the
// bound comes before the value, as it does in the pb decoder and the
// carrier reader (#95, HARDENING.md § Arbitrary-precision magnitudes).
// The same limit, on both signs.
//
// The literal is bounded too (#119), and the two bounds disagree by
// one at the boundary: unscaled 5 at scale ±4096 is accepted on the pb
// wire and renders to 4097 digits — the leading zero of 0.000…5, or
// the 4096 trailing zeros after the 5 — which this port's decoder
// refuses. Pinned here as the divergence protowire#310 records; ±4095
// is the last scale with a readable literal.
func TestDecimalScaleIsBoundedOnMarshal(t *testing.T) {
	desc := bigNumDesc(t, "BigNumDemo")
	const max = pxf.MaxNumericLiteralDigits

	t.Run("last readable, positive", func(t *testing.T) {
		out, err := pxf.Marshal(decimalDemo(t, desc, big.NewInt(5), max-1, false))
		require.NoError(t, err)
		assert.Equal(t, "decimal_field = 0."+strings.Repeat("0", max-2)+"5\n", string(out))
		_, err = pxf.UnmarshalDescriptor(out, desc)
		require.NoError(t, err)
	})
	t.Run("last readable, negative", func(t *testing.T) {
		out, err := pxf.Marshal(decimalDemo(t, desc, big.NewInt(5), -(max - 1), false))
		require.NoError(t, err)
		assert.Equal(t, "decimal_field = 5"+strings.Repeat("0", max-1)+"\n", string(out))
		_, err = pxf.UnmarshalDescriptor(out, desc)
		require.NoError(t, err)
	})
	for _, scale := range []int32{max, -max} {
		t.Run("at the wire's bound, one digit past the literal's (protowire#310)", func(t *testing.T) {
			out, err := pxf.Marshal(decimalDemo(t, desc, big.NewInt(5), scale, false))
			require.Error(t, err, "scale %d", scale)
			assert.Nil(t, out)
			assert.Contains(t, err.Error(), "pxf.Decimal renders to 4097 digits; MaxNumericLiteralDigits=4096")
		})
	}
	for _, scale := range []int32{max + 1, -(max + 1), math.MaxInt32, math.MinInt32} {
		t.Run("past the bound", func(t *testing.T) {
			out, err := pxf.Marshal(decimalDemo(t, desc, big.NewInt(5), scale, false))
			require.Error(t, err, "scale %d must not render", scale)
			assert.Nil(t, out)
			assert.Contains(t, err.Error(), "MaxNumericLiteralDigits=4096")
			assert.Contains(t, err.Error(), "scale "+big.NewInt(int64(scale)).String())
		})
	}
}

// setBigInt fills a pxf.BigInt message in place.
func setBigInt(sub protoreflect.Message, v *big.Int) {
	d := sub.Descriptor()
	sub.Set(d.Fields().ByName("abs"), protoreflect.ValueOfBytes(new(big.Int).Abs(v).Bytes()))
	sub.Set(d.Fields().ByName("negative"), protoreflect.ValueOfBool(v.Sign() < 0))
}

// setBigFloat fills a pxf.BigFloat message in place: value = mantissa x
// 2^exponent at prec bits, per pxf/bignum.proto.
func setBigFloat(sub protoreflect.Message, mantissa *big.Int, exponent int32, prec uint32) {
	d := sub.Descriptor()
	sub.Set(d.Fields().ByName("mantissa"), protoreflect.ValueOfBytes(mantissa.Bytes()))
	sub.Set(d.Fields().ByName("exponent"), protoreflect.ValueOfInt32(exponent))
	sub.Set(d.Fields().ByName("prec"), protoreflect.ValueOfUint32(prec))
}

// pow10 is 10^n.
func pow10(n int64) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil)
}

// allOnes is 2^bits − 1: a mantissa with every bit set, so that a
// BigFloat at prec bits renders with every significant digit its
// precision allows.
func allOnes(bits int64) *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(bits)), big.NewInt(1))
}

// TestMarshalBoundsBigNumberLiterals (#119): a big-number literal this
// port's own decoder would refuse under MaxNumericLiteralDigits is a
// Marshal error, not a document nobody can read. HARDENING.md
// § Arbitrary-precision magnitudes bounds a marshaller's literal like
// any other, and the pb marshaller already refuses a Decimal.scale past
// the same constant (#95). One accept row at the bound and one reject
// row past it per renderer, the sign not counting, and each reject
// through the field, list and map paths.
//
// The decoder parses every BigFloat literal at 256 bits, so no message
// this package decoded reaches the BigFloat rows; a wire prec is four
// bytes another producer chose, and the digit count it implies is
// refused before Text is asked for them.
func TestMarshalBoundsBigNumberLiterals(t *testing.T) {
	demo := bigNumDesc(t, "BigNumDemo")
	maps := bigNumDesc(t, "BigNumMaps")
	const max = pxf.MaxNumericLiteralDigits

	nines := new(big.Int).Sub(pow10(max), big.NewInt(1)) // max digits
	tenPow := pow10(max)                                 // max+1 digits

	t.Run("BigInt at the bound", func(t *testing.T) {
		for _, v := range []*big.Int{nines, new(big.Int).Neg(nines)} {
			msg := dynamicpb.NewMessage(demo)
			setBigInt(msg.Mutable(demo.Fields().ByName("big_int_field")).Message(), v)
			out, err := pxf.Marshal(msg)
			require.NoError(t, err)
			assert.Equal(t, "big_int_field = "+v.Text(10)+"\n", string(out))
			back, err := pxf.UnmarshalDescriptor(out, demo)
			require.NoError(t, err, "the decoder takes what the encoder wrote at the bound")
			assert.Zero(t, v.Cmp(readBigIntFromMsg(back.ProtoReflect(), "big_int_field")))
		}
	})
	t.Run("BigInt past the bound", func(t *testing.T) {
		const want = "pxf.BigInt renders to 4097 digits; MaxNumericLiteralDigits=4096"

		msg := dynamicpb.NewMessage(demo)
		setBigInt(msg.Mutable(demo.Fields().ByName("big_int_field")).Message(), tenPow)
		_, err := pxf.Marshal(msg)
		require.ErrorContains(t, err, want, "field")

		msg = dynamicpb.NewMessage(demo)
		setBigInt(msg.Mutable(demo.Fields().ByName("repeated_big_int")).List().AppendMutable().Message(), tenPow)
		_, err = pxf.Marshal(msg)
		require.ErrorContains(t, err, want, "list")

		mm := dynamicpb.NewMessage(maps)
		setBigInt(mm.Mutable(maps.Fields().ByName("weights")).Map().Mutable(protoreflect.ValueOfString("k").MapKey()).Message(), tenPow)
		_, err = pxf.Marshal(mm)
		require.ErrorContains(t, err, want, "map")
	})

	// 'g' prints positionally while the decimal exponent is below the
	// digit count and in exponent form above it, so the digits of an
	// exponent count only for a very large or a very small value.
	// Measured: 13600 bits of all-ones at exponent 0 is 4095 digits,
	// positional, under the cap; the same mantissa at 2^100000 is a
	// 4095-digit mantissa and a five-digit exponent, 4100 — the
	// post-render count, since the pre-render one passes at 4095.
	// 13610 bits implies 4098 digits and 2^32−1 bits 1.3e9, both refused
	// before Text is asked for a single one.
	t.Run("BigFloat at the bound", func(t *testing.T) {
		msg := dynamicpb.NewMessage(demo)
		setBigFloat(msg.Mutable(demo.Fields().ByName("big_float_field")).Message(), allOnes(13600), 0, 13600)
		out, err := pxf.Marshal(msg)
		require.NoError(t, err)
		assert.Len(t, out, len("big_float_field = \n")+4095)
		_, err = pxf.UnmarshalDescriptor(out, demo)
		require.NoError(t, err, "the decoder takes what the encoder wrote at the bound: %.40s…", out)
	})
	t.Run("BigFloat past the bound, rendered", func(t *testing.T) {
		const want = "pxf.BigFloat renders to 4100 digits; MaxNumericLiteralDigits=4096"

		msg := dynamicpb.NewMessage(demo)
		setBigFloat(msg.Mutable(demo.Fields().ByName("big_float_field")).Message(), allOnes(13600), 100000, 13600)
		_, err := pxf.Marshal(msg)
		require.ErrorContains(t, err, want, "field")

		msg = dynamicpb.NewMessage(demo)
		setBigFloat(msg.Mutable(demo.Fields().ByName("repeated_big_float")).List().AppendMutable().Message(), allOnes(13600), 100000, 13600)
		_, err = pxf.Marshal(msg)
		require.ErrorContains(t, err, want, "list")

		mm := dynamicpb.NewMessage(maps)
		setBigFloat(mm.Mutable(maps.Fields().ByName("floats")).Map().Mutable(protoreflect.ValueOfString("k").MapKey()).Message(), allOnes(13600), 100000, 13600)
		_, err = pxf.Marshal(mm)
		require.ErrorContains(t, err, want, "map")
	})
	t.Run("BigFloat past the bound, before rendering", func(t *testing.T) {
		for _, tc := range []struct {
			prec uint32
			want string
		}{
			{13610, "pxf.BigFloat prec 13610 bits renders to 4098 digits; MaxNumericLiteralDigits=4096"},
			{math.MaxUint32, "pxf.BigFloat prec 4294967295 bits renders to 1292914005 digits; MaxNumericLiteralDigits=4096"},
		} {
			msg := dynamicpb.NewMessage(demo)
			setBigFloat(msg.Mutable(demo.Fields().ByName("big_float_field")).Message(), big.NewInt(1), 0, tc.prec)
			_, err := pxf.Marshal(msg)
			require.ErrorContains(t, err, tc.want)
		}
	})
}
