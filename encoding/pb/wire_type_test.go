// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

// A tag's wire type, against protobuf-go (#304, protowire#614).
//
// A PB decoder does what protobuf does with a tag's wire type: it rejects
// wire type 6 or 7 on any field, and handles a known field whose wire type
// does not match its type as an unknown field, skipped by its wire type
// with the field left as it was. Unmarshal used to read a known field by its
// Go kind whatever the wire type said, so `0a01` on an int64 read the length
// byte as the value 1, and `0e01` read wire type 6 as a varint.
//
// The oracle is protobuf-go over conf.proto (conformance_test.go). Every
// field of confAll is sent one record of every wire type, well formed and
// cut short, at the top level and inside each message and map-entry field.
// Each input must get protobuf-go's verdict, and on accept the same values:
// the oracle's message, read without its unknown fields and re-encoded, must decode
// here to what the input decoded to.

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

// wireRecords returns single records for field num: every wire type, each
// with a well-formed value and a value cut short.
func wireRecords(num protowire.Number) [][]byte {
	tag := func(typ protowire.Type) []byte { return protowire.AppendTag(nil, num, typ) }
	cat := func(parts ...[]byte) []byte {
		var b []byte
		for _, p := range parts {
			b = append(b, p...)
		}
		return b
	}
	return [][]byte{
		cat(tag(protowire.VarintType), []byte{0x01}),
		cat(tag(protowire.VarintType), []byte{0x80}), // cut short
		cat(tag(protowire.Fixed64Type), []byte{1, 0, 0, 0, 0, 0, 0, 0}),
		cat(tag(protowire.Fixed64Type), []byte{1, 0, 0}), // cut short
		cat(tag(protowire.BytesType), []byte{0x00}),
		cat(tag(protowire.BytesType), []byte{0x01, 0x01}),
		cat(tag(protowire.BytesType), []byte{0x01}), // cut short
		cat(tag(protowire.StartGroupType), tag(protowire.EndGroupType)),
		tag(protowire.StartGroupType), // never ended
		tag(protowire.EndGroupType),   // ends nothing
		cat(tag(protowire.Fixed32Type), []byte{1, 0, 0, 0}),
		cat(tag(protowire.Fixed32Type), []byte{1, 0}), // cut short
		cat(protowire.AppendVarint(nil, uint64(num)<<3|6), []byte{0x01}),
		cat(protowire.AppendVarint(nil, uint64(num)<<3|7), []byte{0x01}),
	}
}

// wireTypeInputs is every record for every field of confAll, alone and
// carried inside each message field (12, 13, 22: confInner's fields 1, 2
// and an unknown 3) and map field (18, 19, 25: key 1, value 2, unknown 3).
func wireTypeInputs() [][]byte {
	var out [][]byte
	for num := protowire.Number(1); num <= 25; num++ {
		out = append(out, wireRecords(num)...)
	}
	for _, outer := range []protowire.Number{12, 13, 18, 19, 22, 25} {
		for inner := protowire.Number(1); inner <= 3; inner++ {
			for _, rec := range wireRecords(inner) {
				b := protowire.AppendTag(nil, outer, protowire.BytesType)
				out = append(out, protowire.AppendBytes(b, rec))
			}
		}
	}
	// A matching record after a mismatched one: the mismatch leaves the
	// field as it was, and the match still sets it.
	out = append(out,
		mustHex("0a0161"+"0801"),     // s: "a", then s as a varint
		mustHex("1007"+"1501000000"), // i64: 7, then i64 as fixed32
		mustHex("720101"+"7001"),       // nums packed [1], then nums as a varint
		mustHex("7001"+"7501000000"), // nums [1], then nums as fixed32
	)
	return out
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestUnmarshal_WireTypeMatchesProtobuf(t *testing.T) {
	md := conformanceDesc(t)
	inputs := wireTypeInputs()
	var accepted, rejected int
	for _, in := range inputs {
		oracle := dynamicpb.NewMessage(md)
		oracleErr := proto.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(in, oracle)
		var got confAll
		err := pb.Unmarshal(in, &got)
		if oracleErr != nil {
			rejected++
			assert.Error(t, err, "%x: protobuf-go rejects it (%v)", in, oracleErr)
			continue
		}
		accepted++
		if !assert.NoError(t, err, "%x: protobuf-go accepts it", in) {
			continue
		}
		known, err := proto.MarshalOptions{Deterministic: true}.Marshal(oracle)
		require.NoError(t, err)
		var want confAll
		require.NoError(t, pb.Unmarshal(known, &want), "%x", known)
		emptyMapMessages(&got)
		assert.Equal(t, want, got, "%x: values differ from protobuf-go's", in)
	}
	// A positive marker that both verdicts were exercised, and not only one.
	t.Logf("%d inputs: protobuf-go accepts %d, rejects %d", len(inputs), accepted, rejected)
	assert.Greater(t, accepted, 100)
	assert.Greater(t, rejected, 100)
}

// emptyMapMessages stands a nil message in MM for the empty message
// protobuf-go reads from a map entry with no value. That difference is #307,
// not a wire type's, and this goes when #307 is fixed.
func emptyMapMessages(m *confAll) {
	for k, v := range m.MM {
		if v == nil {
			m.MM[k] = &confInner{}
		}
	}
}

// The rows of #304's table, by name, on a struct of their own.
func TestUnmarshal_WireTypeRows(t *testing.T) {
	type i64Holder struct {
		V int64 `protowire:"1"`
	}
	type strHolder struct {
		V string `protowire:"1"`
	}
	for _, tc := range []struct {
		name, hex string
		str       bool
		reject    bool
	}{
		{"int64, wire type 6", "0e01", false, true},
		{"string, wire type 7", "0f0161", true, true},
		{"string, wire type 6", "0e0161", true, true},
		{"int64 sent as wire type 2", "0a0101", false, false},
		{"int64 sent as wire type 5", "0d01000000", false, false},
		{"string sent as wire type 0", "0801", true, false},
		{"int64 as wire type 2, cut short", "0a01", false, true},
		{"int64 as wire type 5, cut short", "0d01", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.str {
				var m strHolder
				err = pb.Unmarshal(mustHex(tc.hex), &m)
				assert.Empty(t, m.V)
			} else {
				var m i64Holder
				err = pb.Unmarshal(mustHex(tc.hex), &m)
				assert.Zero(t, m.V)
			}
			if tc.reject {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// A skipped field is not touched: a pointer stays nil and a list gets no
// element.
func TestUnmarshal_WireTypeMismatchLeavesFieldAlone(t *testing.T) {
	type holder struct {
		P    *int64   `protowire:"1"`
		L    []string `protowire:"2"`
		Kept string   `protowire:"3"`
	}
	var m holder
	require.NoError(t, pb.Unmarshal(mustHex("0d01000000"+"1001"+"1a0161"), &m))
	assert.Nil(t, m.P)
	assert.Nil(t, m.L)
	assert.Equal(t, "a", m.Kept)
}

// The big-number messages are read by the same rule: a field whose wire
// type does not match is skipped, and wire types 6 and 7 are refused.
func TestUnmarshal_WireTypeBigNumbers(t *testing.T) {
	type holder struct {
		I *big.Int   `protowire:"1"`
		R *big.Rat   `protowire:"2"`
		F *big.Float `protowire:"3"`
	}
	for _, tc := range []struct {
		name, hex string
		check     func(t *testing.T, m holder)
		reject    bool
	}{
		// BigInt{abs: 5, negative sent as wire type 2}: negative is skipped.
		{"BigInt negative as bytes", "0a06" + "0a0105" + "120101", func(t *testing.T, m holder) {
			assert.Equal(t, "5", m.I.String())
		}, false},
		// BigInt{abs sent as a varint}: abs is skipped, the value is 0.
		{"BigInt abs as varint", "0a02" + "0805", func(t *testing.T, m holder) {
			assert.Equal(t, "0", m.I.String())
		}, false},
		// Decimal{unscaled: 15, scale sent as fixed32}: scale stays 0.
		{"Decimal scale as fixed32", "1208" + "0a010f" + "1501000000", func(t *testing.T, m holder) {
			assert.Equal(t, "15/1", m.R.String())
		}, false},
		// BigFloat{mantissa: 1, exponent sent as bytes, prec 53}: exponent stays 0.
		{"BigFloat exponent as bytes", "1a08" + "0a0101" + "120101" + "1835", func(t *testing.T, m holder) {
			assert.Equal(t, "1", m.F.Text('g', 10))
		}, false},
		{"BigInt, wire type 6", "0a02" + "0e01", nil, true},
		{"Decimal, wire type 7", "1203" + "1f0101", nil, true},
		{"BigFloat, wire type 6", "1a02" + "2601", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m holder
			err := pb.Unmarshal(mustHex(tc.hex), &m)
			if tc.reject {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tc.check(t, m)
		})
	}
}
