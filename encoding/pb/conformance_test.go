// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

// Conformance for the pb codec against an INDEPENDENT oracle, beyond
// the big-number messages (#99).
//
// Every other test in this package marshals with Marshal and
// unmarshals with Unmarshal, so a wrong encoding passes as long as it
// is wrong consistently — which is how #92 shipped and survived four
// months. The oracle here is protobuf-go over a descriptor compiled
// from a schema written for the purpose, and every path is checked in
// BOTH directions, because a codec can be wrong in only one:
//
//   - write: bytes this package emits, read by protobuf-go, must equal
//     the message protobuf-go builds from the same values — and record
//     by record they must be the bytes protobuf-go itself would write;
//   - read: bytes protobuf-go emits must decode here to the same values.
//
// The oracle is schema-compiled rather than hand-built: hand-built
// bytes encode the same assumption the code does.
//
// What the pb type model cannot express is stated rather than skipped
// silently: there is no tag for fixed32/fixed64/sfixed32/sfixed64 (only
// floats use fixed-width wire types), no enum, and no oneof — a struct
// whose fields mirror a oneof's members is just fields, which is why
// the oneof cases below set exactly one member, and one case pins what
// happens when two are set.

import (
	"context"
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pb"
)

// oracleMarshal is the one way tests in this package should ask
// protobuf-go for bytes. Deterministic is required: without it
// protobuf-go does not promise field order, and a byte-for-byte
// comparison then fails on a fraction of runs — measured at ~11-13% for
// one dynamicpb message (#100), which is how #96's test flaked on one CI
// leg of nine. Map entries are additionally sorted by key under
// Deterministic, which this package does not do; compare maps by
// record, not by buffer (see recordsByField).
func oracleMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	require.NoError(t, err)
	return b
}

const conformanceProtoSrc = `
syntax = "proto3";
package conf;

message Inner {
  string name = 1;
  int64 n = 2;
}

message All {
  string s = 1;
  int64 i64 = 2;
  int32 i32 = 3;
  uint64 u64 = 4;
  uint32 u32 = 5;
  sint64 z64 = 6;
  sint32 z32 = 7;
  bool b = 8;
  double f64 = 9;
  float f32 = 10;
  bytes buf = 11;
  Inner inner = 12;
  repeated Inner list = 13;
  repeated uint32 nums = 14;
  repeated sint64 zigs = 15;
  repeated float reals = 16;
  repeated string strs = 17;
  map<string, int64> m = 18;
  map<int32, Inner> mm = 19;
  oneof choice {
    string a = 20;
    int64 c = 21;
    Inner d = 22;
  }
  repeated bool flags = 23;
  repeated int64 negs = 24;
}
`

type confInner struct {
	Name string `protowire:"1"`
	N    int64  `protowire:"2"`
}

type confAll struct {
	S     string               `protowire:"1"`
	I64   int64                `protowire:"2"`
	I32   int32                `protowire:"3"`
	U64   uint64               `protowire:"4"`
	U32   uint32               `protowire:"5"`
	Z64   int64                `protowire:"6,zigzag"`
	Z32   int32                `protowire:"7,zigzag"`
	B     bool                 `protowire:"8"`
	F64   float64              `protowire:"9"`
	F32   float32              `protowire:"10"`
	Buf   []byte               `protowire:"11"`
	Inner *confInner           `protowire:"12"`
	List  []*confInner         `protowire:"13"`
	Nums  []uint32             `protowire:"14"`
	Zigs  []int64              `protowire:"15,zigzag"`
	Reals []float32            `protowire:"16"`
	Strs  []string             `protowire:"17"`
	M     map[string]int64     `protowire:"18"`
	MM    map[int32]*confInner `protowire:"19"`
	A     string               `protowire:"20"`
	C     int64                `protowire:"21"`
	D     *confInner           `protowire:"22"`
	Flags []bool               `protowire:"23"`
	Negs  []int64              `protowire:"24"`
}

func conformanceDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(
					map[string]string{"conf.proto": conformanceProtoSrc},
				),
			},
		),
	}
	files, err := comp.Compile(context.Background(), "conf.proto")
	require.NoError(t, err)
	md := files[0].Messages().ByName("All")
	require.NotNil(t, md)
	return md
}

// oracleMessage builds the oracle's message from prototext, so the
// expected values are written in the schema's own terms and never
// through this package.
func oracleMessage(t *testing.T, md protoreflect.MessageDescriptor, text string) *dynamicpb.Message {
	t.Helper()
	m := dynamicpb.NewMessage(md)
	require.NoError(t, prototext.Unmarshal([]byte(text), m))
	return m
}

// recordsByField splits an encoded message into its top-level records
// (tag + value), grouped by field number in wire order. Map fields are
// the one place order is not part of the contract — protobuf-go sorts
// entries by key under Deterministic and this package walks a Go map —
// so their records are sorted before comparison.
func recordsByField(t *testing.T, b []byte, mapFields ...protowire.Number) map[protowire.Number][]string {
	t.Helper()
	out := map[protowire.Number][]string{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		require.GreaterOrEqual(t, n, 0, "corrupt tag")
		vn := protowire.ConsumeFieldValue(num, typ, b[n:])
		require.GreaterOrEqual(t, vn, 0, "corrupt field %d", num)
		out[num] = append(out[num], string(b[:n+vn]))
		b = b[n+vn:]
	}
	for _, f := range mapFields {
		sort.Strings(out[f])
	}
	return out
}

var conformanceMapFields = []protowire.Number{18, 19}

type conformanceCase struct {
	name string
	// ours is the value this package marshals and the value the read
	// direction must produce.
	ours *confAll
	// text is the same value in the schema's terms, for the oracle.
	text string
}

func conformanceCases() []conformanceCase {
	return []conformanceCase{
		{
			name: "scalars",
			ours: &confAll{S: "héllo", I64: 1 << 40, I32: 7, U64: math.MaxUint64, U32: math.MaxUint32,
				Z64: 3, Z32: 5, B: true, F64: 6.02214076e23, F32: 1.5, Buf: []byte{0, 1, 0xff}},
			text: `s: "héllo" i64: 1099511627776 i32: 7 u64: 18446744073709551615 u32: 4294967295
			       z64: 3 z32: 5 b: true f64: 6.02214076e23 f32: 1.5 buf: "\000\001\377"`,
		},
		{
			// int32/int64 negatives are sign-extended 10-byte varints;
			// sint32/sint64 negatives are zigzag. Both readers must agree
			// on which is which — this is #92's shape on the scalar path.
			name: "negative integers",
			ours: &confAll{I64: -1, I32: math.MinInt32, Z64: math.MinInt64, Z32: -1},
			text: `i64: -1 i32: -2147483648 z64: -9223372036854775808 z32: -1`,
		},
		{
			name: "float specials",
			// -0.0 as a Go constant is +0 (constants have no negative
			// zero); Copysign is how a test says what it means.
			ours: &confAll{F64: math.Inf(-1), F32: float32(math.Inf(1)), Reals: []float32{0, float32(math.Copysign(0, -1)), 1e-45}},
			text: `f64: -inf f32: inf reals: [0, -0.0, 1e-45]`,
		},
		{
			name: "nested",
			ours: &confAll{Inner: &confInner{Name: "in", N: -2}},
			text: `inner { name: "in" n: -2 }`,
		},
		{
			// proto3 message fields have presence: an empty submessage
			// is a zero-length record, and it must decode back to a
			// non-nil pointer.
			name: "nested empty",
			ours: &confAll{Inner: &confInner{}},
			text: `inner {}`,
		},
		{
			name: "repeated messages",
			ours: &confAll{List: []*confInner{{Name: "a", N: 1}, {}, {Name: "c"}}},
			text: `list { name: "a" n: 1 } list {} list { name: "c" }`,
		},
		{
			// proto3 packs numeric scalars by default; zeros inside a
			// packed list are elements, not absences.
			name: "packed",
			ours: &confAll{Nums: []uint32{1, 0, 300}, Zigs: []int64{-1, 0, 2}, Reals: []float32{1.5, 0}, Flags: []bool{true, false, true}, Negs: []int64{-1, 0, math.MinInt64}},
			text: `nums: [1, 0, 300] zigs: [-1, 0, 2] reals: [1.5, 0] flags: [true, false, true] negs: [-1, 0, -9223372036854775808]`,
		},
		{
			name: "repeated strings",
			ours: &confAll{Strs: []string{"b", "", "a"}},
			text: `strs: ["b", "", "a"]`,
		},
		{
			// Entries with a zero key or value are pinned separately: see
			// TestConformance_MapEntriesCarryZeroValues.
			name: "maps",
			ours: &confAll{M: map[string]int64{"one": 1, "minus": -1}, MM: map[int32]*confInner{2: {Name: "two"}, -1: {N: 3}}},
			text: `m { key: "one" value: 1 } m { key: "minus" value: -1 }
			       mm { key: 2 value { name: "two" } } mm { key: -1 value { n: 3 } }`,
		},
		{
			name: "oneof string member",
			ours: &confAll{A: "chosen"},
			text: `a: "chosen"`,
		},
		{
			name: "oneof int member",
			ours: &confAll{C: 9},
			text: `c: 9`,
		},
		{
			name: "oneof message member",
			ours: &confAll{D: &confInner{N: 4}},
			text: `d { n: 4 }`,
		},
		{
			name: "everything at once",
			ours: &confAll{S: "x", I64: -5, U32: 9, B: true, F32: 2, Buf: []byte("b"), Inner: &confInner{Name: "i"},
				List: []*confInner{{N: 1}}, Nums: []uint32{4}, Strs: []string{"s"}, M: map[string]int64{"k": 2}, C: 1},
			text: `s: "x" i64: -5 u32: 9 b: true f32: 2 buf: "b" inner { name: "i" } list { n: 1 } nums: [4] strs: ["s"] m { key: "k" value: 2 } c: 1`,
		},
	}
}

// TestConformance_WriteDirection: bytes this package emits, read by
// protobuf-go, are the message protobuf-go builds from the same values;
// and record by record they are the bytes protobuf-go would write.
func TestConformance_WriteDirection(t *testing.T) {
	md := conformanceDesc(t)
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			want := oracleMessage(t, md, tc.text)
			ours, err := pb.Marshal(tc.ours)
			require.NoError(t, err)

			got := dynamicpb.NewMessage(md)
			require.NoError(t, proto.Unmarshal(ours, got), "a conformant decoder must read what this package writes")
			assert.True(t, proto.Equal(want, got), "oracle read our bytes as:\n%v\nwant:\n%v", prototext.Format(got), prototext.Format(want))

			theirs := oracleMarshal(t, want)
			assert.Equal(t, recordsByField(t, theirs, conformanceMapFields...), recordsByField(t, ours, conformanceMapFields...),
				"every record must be the bytes a conformant encoder writes")
		})
	}
}

// TestConformance_ReadDirection: bytes protobuf-go emits decode here to
// the same values.
func TestConformance_ReadDirection(t *testing.T) {
	md := conformanceDesc(t)
	for _, tc := range conformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			theirs := oracleMarshal(t, oracleMessage(t, md, tc.text))
			var got confAll
			require.NoError(t, pb.Unmarshal(theirs, &got), "this package must read what a conformant encoder writes")
			assert.Equal(t, tc.ours, &got)
		})
	}
}

// TestConformance_TwoOneofMembersSet pins what the type model cannot
// forbid: a struct mirroring a oneof can set two members, and this
// package writes both. A conformant reader keeps the last one on the
// wire (proto3 oneof semantics), so which member "wins" is decided by
// field order here. Documented, not fixed: pb has no oneof concept, and
// a schema-driven encoder (encoding/pxf, protobuf-go) is the tool for a
// message whose oneof must be enforced.
func TestConformance_TwoOneofMembersSet(t *testing.T) {
	md := conformanceDesc(t)
	ours, err := pb.Marshal(&confAll{A: "first", C: 2})
	require.NoError(t, err)
	got := dynamicpb.NewMessage(md)
	require.NoError(t, proto.Unmarshal(ours, got))
	choice := md.Oneofs().ByName("choice")
	require.NotNil(t, choice)
	which := got.WhichOneof(choice)
	require.NotNil(t, which, "a oneof member must be set")
	assert.Equal(t, protoreflect.Name("c"), which.Name(), "the later field on the wire wins")
}

// TestConformance_MapEntriesCarryZeroValues pins #105: a map entry always
// carries both its key and its value, zero-valued or not — the layout
// protobuf-go, protoc and C++ protobuf write. Until this change the
// package applied proto3 zero-skipping inside the entry ("zero" → 0 was
// written without its value, "" → -1 without its key); every reader
// accepted either form, so the divergence was lossless and invisible.
// STABILITY.md's v1.13 section in the spec repo records why promise 2's
// bytes moved for this shape.
func TestConformance_MapEntriesCarryZeroValues(t *testing.T) {
	md := conformanceDesc(t)
	want := oracleMessage(t, md, `m { key: "zero" value: 0 } m { key: "" value: -1 } mm { key: 0 value {} }`)
	ours, err := pb.Marshal(&confAll{M: map[string]int64{"zero": 0, "": -1}, MM: map[int32]*confInner{0: {}}})
	require.NoError(t, err)

	// Byte-level: identical to the oracle, entry by entry.
	theirs := recordsByField(t, oracleMarshal(t, want), conformanceMapFields...)
	mine := recordsByField(t, ours, conformanceMapFields...)
	assert.Equal(t, theirs, mine)
	assert.Contains(t, mine[18], string([]byte{0x92, 0x01, 0x08, 0x0a, 0x04, 'z', 'e', 'r', 'o', 0x10, 0x00}), `"zero" → 0 carries its value`)
	assert.Contains(t, mine[18], string([]byte{0x92, 0x01, 0x0d, 0x0a, 0x00, 0x10, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}), `"" → -1 carries its key`)
	assert.Contains(t, mine[19], string([]byte{0x9a, 0x01, 0x04, 0x08, 0x00, 0x12, 0x00}), `0 → {} carries both`)

	// Both forms still read: the oracle's explicit zeros, and the
	// omission a payload written before this change may carry.
	var back confAll
	require.NoError(t, pb.Unmarshal(oracleMarshal(t, want), &back))
	assert.Equal(t, map[string]int64{"zero": 0, "": -1}, back.M)
	assert.Equal(t, map[int32]*confInner{0: {}}, back.MM)
	var old confAll
	require.NoError(t, pb.Unmarshal([]byte{0x92, 0x01, 0x06, 0x0a, 0x04, 'z', 'e', 'r', 'o', 0x9a, 0x01, 0x02, 0x12, 0x00}, &old))
	assert.Equal(t, map[string]int64{"zero": 0}, old.M)
	assert.Equal(t, map[int32]*confInner{0: {}}, old.MM)
}
