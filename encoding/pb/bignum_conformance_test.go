// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

// Conformance for the big-number codec against an INDEPENDENT oracle
// (#92).
//
// `pxf/bignum.proto` declares `Decimal.scale` and `BigFloat.exponent` as
// plain `int32`, which is a plain varint on the wire; zigzag is `sint32`,
// which neither field is. This package used zigzag for both, in all four
// places — two writers and two readers — so it round-tripped its own
// bytes perfectly and disagreed with every schema-conformant reader.
//
// Self-consistency is what hid it, and it hid it for the life of the
// package: the zigzag has been there since the initial public release
// (d137dd0, v0.70.0, 2026-05-06). Every other test in this package
// marshals with Marshal and unmarshals with Unmarshal, so none of them
// could ever have caught it, and none of them changed when it was fixed.
//
// The oracle here is protobuf-go over a descriptor compiled from
// bignum.proto itself: an implementation that follows the schema rather
// than this package's convention. Both directions are covered, because
// the defect was in both — bytes this package WRITES must be readable by
// a conformant decoder, and bytes a conformant encoder WRITES must be
// readable here.
//
// Only zero survives the difference. Over the full int32 range, zigzag
// and plain varint agree on exactly one of 4,294,967,296 values, so any
// non-zero scale or exponent was wrong on the wire — not merely the
// negative ones.

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

// bignumProtoSrc is pxf/bignum.proto. Kept here rather than shared with
// encoding/pxf's copy so this test states the contract it is checking:
// if the schema changes, this file has to be read.
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

// conformanceStruct is the shape encoding/pb marshals. Field numbers
// match the submessage extraction below.
type conformanceStruct struct {
	Price       *big.Rat   `protowire:"1"`
	Coefficient *big.Float `protowire:"2"`
}

func bignumDescriptors(t *testing.T) (decimal, bigFloat protoreflect.MessageDescriptor) {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(
					map[string]string{"pxf/bignum.proto": bignumProtoSrc},
				),
			},
		),
	}
	files, err := comp.Compile(context.Background(), "pxf/bignum.proto")
	require.NoError(t, err)
	msgs := files[0].Messages()
	d := msgs.ByName("Decimal")
	f := msgs.ByName("BigFloat")
	require.NotNil(t, d)
	require.NotNil(t, f)
	return d, f
}

// submessage pulls the length-delimited payload of field num out of a
// record encoding/pb produced.
func submessage(t *testing.T, b []byte, num protowire.Number) []byte {
	t.Helper()
	for len(b) > 0 {
		n, typ, tn := protowire.ConsumeTag(b)
		require.GreaterOrEqual(t, tn, 0, "corrupt tag")
		b = b[tn:]
		if n == num {
			require.Equal(t, protowire.BytesType, typ)
			v, vn := protowire.ConsumeBytes(b)
			require.GreaterOrEqual(t, vn, 0, "corrupt submessage")
			return v
		}
		vn := protowire.ConsumeFieldValue(n, typ, b)
		require.GreaterOrEqual(t, vn, 0, "corrupt field")
		b = b[vn:]
	}
	t.Fatalf("field %d not present", num)
	return nil
}

// intField reads an int32 field out of a message the ORACLE decoded, so
// the value reported is the schema's reading and not this package's.
func intField(msg protoreflect.Message, name string) int32 {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	return int32(msg.Get(fd).Int())
}

// TestBignumConformance_WrittenBytesAreReadableBySchema is the write
// half: what this package emits must mean the same thing to protobuf-go.
//
// 3.1415 gives scale 4. Under zigzag that was written as 8, so a
// conformant reader saw 31415 x 10^-8 — the right digits, wrong by four
// orders of magnitude, with nothing to signal it.
func TestBignumConformance_WrittenBytesAreReadableBySchema(t *testing.T) {
	decimalDesc, bigFloatDesc := bignumDescriptors(t)

	for _, tc := range []struct {
		name      string
		rat       *big.Rat
		wantScale int32
	}{
		{"3.1415", new(big.Rat).SetFrac64(31415, 10000), 4},
		{"1.5", new(big.Rat).SetFrac64(3, 2), 1},
		{"0.001", new(big.Rat).SetFrac64(1, 1000), 3},
		{"-2.25", new(big.Rat).SetFrac64(-9, 4), 2},
	} {
		t.Run("decimal/"+tc.name, func(t *testing.T) {
			data, err := pb.Marshal(&conformanceStruct{Price: tc.rat})
			require.NoError(t, err)

			oracle := dynamicpb.NewMessage(decimalDesc)
			require.NoError(t, proto.Unmarshal(submessage(t, data, 1), oracle),
				"a schema-conformant decoder must accept what this package writes")

			assert.Equal(t, tc.wantScale, intField(oracle, "scale"),
				"the scale a conformant reader sees must be the scale that was meant")

			// And the value it reconstructs from those fields.
			unscaled := new(big.Int).SetBytes(
				oracle.Get(decimalDesc.Fields().ByName("unscaled")).Bytes())
			negative := oracle.Get(decimalDesc.Fields().ByName("negative")).Bool()
			got := new(big.Rat).SetFrac(unscaled,
				new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tc.wantScale)), nil))
			if negative {
				got.Neg(got)
			}
			assert.Zero(t, tc.rat.Cmp(got), "want %s, oracle reconstructed %s", tc.rat, got)
		})
	}

	// BigFloat.exponent is adjExp = exp - prec, negative for essentially
	// every value, which is where #92 bit hardest: 1.0 at prec 53 wrote
	// an exponent a conformant reader saw as 103 rather than -52, a
	// factor of 2^155.
	for _, tc := range []struct {
		name string
		text string
		prec uint
	}{
		{"1.0", "1", 53},
		{"1.5", "1.5", 53},
		{"1e10", "1e10", 53},
		{"-1.23e-45", "-1.23e-45", 64},
		{"6.02214076e23", "6.02214076e23", 128},
	} {
		t.Run("bigfloat/"+tc.name, func(t *testing.T) {
			bf, _, err := big.ParseFloat(tc.text, 10, tc.prec, big.ToNearestEven)
			require.NoError(t, err)

			data, err := pb.Marshal(&conformanceStruct{Coefficient: bf})
			require.NoError(t, err)

			oracle := dynamicpb.NewMessage(bigFloatDesc)
			require.NoError(t, proto.Unmarshal(submessage(t, data, 2), oracle))

			mant := new(big.Int).SetBytes(
				oracle.Get(bigFloatDesc.Fields().ByName("mantissa")).Bytes())
			exp := intField(oracle, "exponent")
			prec := uint32(oracle.Get(bigFloatDesc.Fields().ByName("prec")).Uint())
			negative := oracle.Get(bigFloatDesc.Fields().ByName("negative")).Bool()

			// value = mantissa x 2^exponent, per the schema.
			got := new(big.Float).SetPrec(uint(prec)).SetInt(mant)
			got.SetMantExp(got, int(exp))
			if negative {
				got.Neg(got)
			}
			assert.Zero(t, bf.Cmp(got),
				"want %s, oracle reconstructed %s (exponent %d)",
				bf.Text('g', 20), got.Text('g', 20), exp)
		})
	}
}

// TestBignumConformance_SchemaBytesAreReadableHere is the read half, and
// the one the done-when asks for by name: a NEGATIVE scale and a
// negative exponent, written by the oracle.
//
// encoding/pb never produces a negative scale itself — ratToDecimal
// returns max(twos, fives) or a digit count, both non-negative — so this
// direction is the only way to reach that arm at all. A conformant
// producer can write one, and encoding/pxf's carrier reader already
// handles it ("a negative scale means trailing zeros"), so this package
// has to as well.
func TestBignumConformance_SchemaBytesAreReadableHere(t *testing.T) {
	decimalDesc, bigFloatDesc := bignumDescriptors(t)

	t.Run("negative scale", func(t *testing.T) {
		// 25 x 10^-(-2) = 2500
		oracle := dynamicpb.NewMessage(decimalDesc)
		oracle.Set(decimalDesc.Fields().ByName("unscaled"),
			protoreflect.ValueOfBytes(big.NewInt(25).Bytes()))
		oracle.Set(decimalDesc.Fields().ByName("scale"), protoreflect.ValueOfInt32(-2))
		sub, err := proto.Marshal(oracle)
		require.NoError(t, err)

		got := &conformanceStruct{Price: new(big.Rat), Coefficient: new(big.Float)}
		require.NoError(t, pb.Unmarshal(wrap(1, sub), got))
		assert.Zero(t, new(big.Rat).SetInt64(2500).Cmp(got.Price),
			"a negative scale from a conformant producer must mean trailing zeros, got %s", got.Price)
	})

	t.Run("positive scale", func(t *testing.T) {
		// 31415 x 10^-4 = 3.1415
		oracle := dynamicpb.NewMessage(decimalDesc)
		oracle.Set(decimalDesc.Fields().ByName("unscaled"),
			protoreflect.ValueOfBytes(big.NewInt(31415).Bytes()))
		oracle.Set(decimalDesc.Fields().ByName("scale"), protoreflect.ValueOfInt32(4))
		sub, err := proto.Marshal(oracle)
		require.NoError(t, err)

		got := &conformanceStruct{Price: new(big.Rat), Coefficient: new(big.Float)}
		require.NoError(t, pb.Unmarshal(wrap(1, sub), got))
		assert.Zero(t, new(big.Rat).SetFrac64(31415, 10000).Cmp(got.Price),
			"got %s", got.Price)
	})

	t.Run("negative exponent", func(t *testing.T) {
		// mantissa 2^52 x 2^-52 = 1.0, at prec 53.
		mant := new(big.Int).Lsh(big.NewInt(1), 52)
		oracle := dynamicpb.NewMessage(bigFloatDesc)
		oracle.Set(bigFloatDesc.Fields().ByName("mantissa"),
			protoreflect.ValueOfBytes(mant.Bytes()))
		oracle.Set(bigFloatDesc.Fields().ByName("exponent"), protoreflect.ValueOfInt32(-52))
		oracle.Set(bigFloatDesc.Fields().ByName("prec"), protoreflect.ValueOfUint32(53))
		sub, err := proto.Marshal(oracle)
		require.NoError(t, err)

		got := &conformanceStruct{Price: new(big.Rat), Coefficient: new(big.Float)}
		require.NoError(t, pb.Unmarshal(wrap(2, sub), got))
		assert.Zero(t, big.NewFloat(1).Cmp(got.Coefficient),
			"got %s", got.Coefficient.Text('g', 20))
	})
}

// TestBignumConformance_ByteForByte pins the encoding itself, so a
// future change to either side has to look at the bytes rather than at
// a round trip that agrees with itself.
func TestBignumConformance_ByteForByte(t *testing.T) {
	decimalDesc, _ := bignumDescriptors(t)

	data, err := pb.Marshal(&conformanceStruct{Price: new(big.Rat).SetFrac64(31415, 10000)})
	require.NoError(t, err)
	ours := submessage(t, data, 1)

	oracle := dynamicpb.NewMessage(decimalDesc)
	oracle.Set(decimalDesc.Fields().ByName("unscaled"),
		protoreflect.ValueOfBytes(big.NewInt(31415).Bytes()))
	oracle.Set(decimalDesc.Fields().ByName("scale"), protoreflect.ValueOfInt32(4))
	theirs, err := proto.Marshal(oracle)
	require.NoError(t, err)

	assert.Equal(t, theirs, ours,
		"this package's bytes must be byte-identical to a conformant encoder's")

	// The specific byte the bug lived in: scale 4 is varint 0x04, not
	// zigzag's 0x08.
	assert.Contains(t, string(ours), string([]byte{0x10, 0x04}),
		"field 2 varint must be 4 (plain), not 8 (zigzag)")
}

// wrap builds the outer record encoding/pb expects: one length-delimited
// field carrying sub.
func wrap(num protowire.Number, sub []byte) []byte {
	var b []byte
	b = protowire.AppendTag(b, num, protowire.BytesType)
	b = protowire.AppendBytes(b, sub)
	return b
}
