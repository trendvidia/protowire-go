// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const datasetWriteProtoSrc = `
syntax = "proto3";
package dsw.v1;

import "google/protobuf/duration.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";
import "pxf/bignum.proto";
import "pxf/secret.proto";

enum Color {
  COLOR_UNSPECIFIED = 0;
  COLOR_RED = 1;
  COLOR_GREEN = 2;
}

message Row {
  string s = 1;
  bool b = 2;
  int32 i32 = 3;
  sint32 si32 = 4;
  sfixed32 sf32 = 5;
  int64 i64 = 6;
  sint64 si64 = 7;
  sfixed64 sf64 = 8;
  uint32 u32 = 9;
  fixed32 f32 = 10;
  uint64 u64 = 11;
  fixed64 f64 = 12;
  float fl = 13;
  double db = 14;
  bytes by = 15;
  Color color = 16;
  optional int32 opt = 17;

  google.protobuf.Timestamp ts = 20;
  google.protobuf.Duration dur = 21;
  google.protobuf.StringValue w_s = 22;
  google.protobuf.Int64Value w_i64 = 23;
  google.protobuf.BoolValue w_b = 24;
  google.protobuf.BytesValue w_by = 25;
  pxf.BigInt bi = 26;
  pxf.Decimal dec = 27;
  pxf.BigFloat bf = 28;
  pxf.Secret sec = 29;

  // Defaults: an empty cell would read back as the default.
  string d_s = 40 [(pxf.default) = "dflt"];
  int32 d_i32 = 41 [(pxf.default) = "7"];
  optional int32 d_opt = 42 [(pxf.default) = "8"];
  google.protobuf.Int32Value d_w = 43 [(pxf.default) = "9"];
  google.protobuf.Timestamp d_ts = 44 [(pxf.default) = "2026-01-01T00:00:00Z"];
  Color d_color = 45 [(pxf.default) = "COLOR_GREEN"];

  oneof choice {
    string c_s = 50 [(pxf.default) = "picked"];
    int32 c_i = 51;
    bool c_b = 52;
  }

  // Not cell-shaped.
  repeated int32 list = 60;
  map<string, int32> m = 61;
  Inner inner = 62;
}

message Inner {
  int32 x = 1;
}

message Req {
  int32 id = 1 [(pxf.required) = true];
  optional string name = 2 [(pxf.required) = true];
  string note = 3;
}
`

func compileDatasetWrite(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, "dsw.proto", map[string]string{
		"dsw.proto":             datasetWriteProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
		"pxf/bignum.proto":      bignumProtoSrc,
		"pxf/secret.proto":      secretProtoSrc,
	})
	md := fd.Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md)
	return md
}

// rowColumns is every cell-shaped field of dsw.v1.Row.
var rowColumns = []string{
	"s", "b", "i32", "si32", "sf32", "i64", "si64", "sf64", "u32", "f32", "u64", "f64",
	"fl", "db", "by", "color", "opt",
	"ts", "dur", "w_s", "w_i64", "w_b", "w_by", "bi", "dec", "bf", "sec",
	"d_s", "d_i32", "d_opt", "d_w", "d_ts", "d_color",
	"c_s", "c_i", "c_b",
}

// randomRow fills each column of a dsw.v1.Row at random, leaving about
// a third unset. Scalars are set through protoreflect; literal-shaped
// messages through Unmarshal of a random literal, which is how a caller
// would hold them.
func randomRow(t testing.TB, r *rand.Rand, desc protoreflect.MessageDescriptor) *dynamicpb.Message {
	m := dynamicpb.NewMessage(desc)
	for _, c := range rowColumns {
		if r.IntN(3) == 0 {
			continue
		}
		fd := desc.Fields().ByName(protoreflect.Name(c))
		if fd.Kind() == protoreflect.MessageKind {
			lit := randomLiteral(r, fd.Message().FullName())
			if err := pxf.Unmarshal([]byte(c+" = "+lit), m); err != nil {
				t.Fatalf("setting %s = %s: %v", c, lit, err)
			}
			continue
		}
		m.Set(fd, randomScalar(r, fd))
	}
	return m
}

var trickyStrings = []string{"", "plain", "a, b", "(x)", "\"q\"", "back\\slash", "line\nbreak", "tab\t", "# not a comment", "// nor this", "ünï ✓", "null", "true", "@dataset"}

func randomScalar(r *rand.Rand, fd protoreflect.FieldDescriptor) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(trickyStrings[r.IntN(len(trickyStrings))])
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(r.IntN(2) == 0)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32([]int32{0, -1, 1, math.MinInt32, math.MaxInt32, r.Int32()}[r.IntN(6)])
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64([]int64{0, -1, math.MinInt64, math.MaxInt64, r.Int64() - r.Int64()}[r.IntN(5)])
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32([]uint32{0, 1, math.MaxUint32, r.Uint32()}[r.IntN(4)])
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64([]uint64{0, 1, math.MaxUint64, r.Uint64()}[r.IntN(4)])
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32([]float32{0, -0.5, 3.25, math.MaxFloat32, math.SmallestNonzeroFloat32, float32(math.Inf(-1)), float32(math.NaN()), r.Float32()}[r.IntN(8)])
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64([]float64{0, -1e300, 1.0 / 3, math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.NaN(), r.NormFloat64()}[r.IntN(8)])
	case protoreflect.BytesKind:
		b := make([]byte, r.IntN(12))
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		return protoreflect.ValueOfBytes(b)
	case protoreflect.EnumKind:
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(r.IntN(3)))
	}
	panic(fmt.Sprintf("no generator for %s", fd.Kind()))
}

func randomLiteral(r *rand.Rand, name protoreflect.FullName) string {
	switch name {
	case "google.protobuf.Timestamp":
		return []string{"1970-01-01T00:00:00Z", "2026-10-23T12:34:56.789Z", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999999Z"}[r.IntN(4)]
	case "google.protobuf.Duration":
		return []string{"0s", "1.5s", "-3h2m", "1ns", "-1µs"}[r.IntN(5)]
	case "google.protobuf.StringValue", "pxf.Secret":
		return fmt.Sprintf("%q", trickyStrings[r.IntN(len(trickyStrings))])
	case "google.protobuf.Int64Value":
		return []string{"0", "-9223372036854775808", "42"}[r.IntN(3)]
	case "google.protobuf.Int32Value":
		return []string{"0", "-2147483648", "42"}[r.IntN(3)]
	case "google.protobuf.BoolValue":
		return []string{"true", "false"}[r.IntN(2)]
	case "google.protobuf.BytesValue":
		return []string{`b""`, `b"AQID"`}[r.IntN(2)]
	case "pxf.BigInt":
		return []string{"0", "-1", "123456789012345678901234567890"}[r.IntN(3)]
	case "pxf.Decimal":
		return []string{"0", "-0.001", "3.14159265358979323846", "100000000000000000000"}[r.IntN(4)]
	case "pxf.BigFloat":
		return []string{"0", "-2.5", "1.25e-30"}[r.IntN(3)]
	}
	panic(fmt.Sprintf("no literal generator for %s", name))
}

// writeTable writes a header and one row per message.
func writeTable(t testing.TB, desc protoreflect.MessageDescriptor, columns []string, msgs []*dynamicpb.Message) []byte {
	t.Helper()
	out, err := pxf.AppendDatasetHeader(nil, desc, columns)
	require.NoError(t, err)
	for _, m := range msgs {
		out, err = pxf.AppendDatasetRow(out, m, columns)
		require.NoError(t, err, "row for %v", m)
	}
	return out
}

// assertTableReadsBack reads table both ways, streaming (Scan) and
// materializing (Parse + BindRow), and requires each row to equal the
// message it was written from.
func assertTableReadsBack(t testing.TB, desc protoreflect.MessageDescriptor, table []byte, msgs []*dynamicpb.Message) {
	t.Helper()
	tr, err := pxf.NewDatasetReader(bytes.NewReader(table))
	require.NoError(t, err)
	require.Equal(t, string(desc.FullName()), tr.Type())
	for i, want := range msgs {
		got := dynamicpb.NewMessage(desc)
		require.NoError(t, tr.Scan(got), "row %d", i)
		if !proto.Equal(want, got) {
			t.Fatalf("Scan row %d:\nwrote %v\nread  %v\ntable:\n%s", i, want, got, table)
		}
	}
	_, err = tr.Next()
	require.ErrorIs(t, err, io.EOF)

	doc, err := pxf.Parse(table)
	require.NoError(t, err)
	require.Len(t, doc.Datasets, 1)
	ds := doc.Datasets[0]
	require.Len(t, ds.Rows, len(msgs))
	for i, want := range msgs {
		got := dynamicpb.NewMessage(desc)
		require.NoError(t, pxf.BindRow(got, ds.Columns, ds.Rows[i]), "row %d", i)
		if !proto.Equal(want, got) {
			t.Fatalf("BindRow row %d:\nwrote %v\nread  %v", i, want, got)
		}
	}
}

func TestDatasetWrite_RoundTrip(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	r := rand.New(rand.NewPCG(142, 1))
	msgs := make([]*dynamicpb.Message, 2000)
	for i := range msgs {
		msgs[i] = randomRow(t, r, desc)
	}
	msgs = append(msgs, dynamicpb.NewMessage(desc)) // every column unset
	table := writeTable(t, desc, rowColumns, msgs)
	assertTableReadsBack(t, desc, table, msgs)
}

// FuzzDatasetWriteRoundTrip is the round trip on a fuzzer-chosen seed
// and column subset.
func FuzzDatasetWriteRoundTrip(f *testing.F) {
	f.Add(uint64(1), uint64(0))
	f.Add(uint64(142), uint64(math.MaxUint64))
	f.Fuzz(func(t *testing.T, seed, colMask uint64) {
		desc := compileDatasetWrite(t, "Row")
		var cols []string
		for i, c := range rowColumns {
			if colMask&(1<<uint(i)) != 0 {
				cols = append(cols, c)
			}
		}
		if len(cols) == 0 {
			cols = rowColumns[:1]
		}
		r := rand.New(rand.NewPCG(seed, 7))
		msgs := make([]*dynamicpb.Message, 8)
		for i := range msgs {
			full := randomRow(t, r, desc)
			// Only the columns are written; keep the rest unset so the
			// message can come back equal.
			m := dynamicpb.NewMessage(desc)
			for _, c := range cols {
				fd := desc.Fields().ByName(protoreflect.Name(c))
				if full.Has(fd) {
					m.Set(fd, full.Get(fd))
				}
			}
			msgs[i] = m
		}
		assertTableReadsBack(t, desc, writeTable(t, desc, cols, msgs), msgs)
	})
}

// Appending rows to a file that already holds a header and rows gives
// the same bytes as writing them all at once: nothing earlier changes.
func TestDatasetWrite_AppendNeedsNoRewrite(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	r := rand.New(rand.NewPCG(7, 7))
	msgs := make([]*dynamicpb.Message, 50)
	for i := range msgs {
		msgs[i] = randomRow(t, r, desc)
	}
	whole := writeTable(t, desc, rowColumns, msgs)

	file := writeTable(t, desc, rowColumns, msgs[:20])
	before := bytes.Clone(file)
	for _, m := range msgs[20:] {
		var err error
		file, err = pxf.AppendDatasetRow(file, m, rowColumns)
		require.NoError(t, err)
	}
	assert.Equal(t, before, file[:len(before)])
	assert.Equal(t, whole, file)
	assertTableReadsBack(t, desc, file, msgs)
}

func TestDatasetWrite_Text(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	cols := []string{"s", "i32", "opt", "d_i32", "d_opt", "color", "ts"}
	h, err := pxf.AppendDatasetHeader(nil, desc, cols)
	require.NoError(t, err)
	assert.Equal(t, "@dataset dsw.v1.Row (s, i32, opt, d_i32, d_opt, color, ts)\n", string(h))

	m := dynamicpb.NewMessage(desc)
	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	// Unset: empty, except d_i32 (zero written, so the default does not
	// replace it) and d_opt (null, so it stays unset).
	assert.Equal(t, "(, , , 0, null, , )\n", string(row))

	require.NoError(t, pxf.Unmarshal([]byte(`s = "a, (b)" i32 = -3 opt = 0 d_i32 = 4 color = COLOR_RED ts = 2026-10-23T00:00:00Z`), m))
	row, err = pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	assert.Equal(t, `("a, (b)", -3, 0, 4, null, COLOR_RED, 2026-10-23T00:00:00Z)`+"\n", string(row))
}

func TestDatasetWrite_OneofDefaults(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	cols := []string{"c_s", "c_i", "c_b"}

	// No member set: the member with the default (at most one may have
	// one) is null, which keeps the default off.
	m := dynamicpb.NewMessage(desc)
	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	assert.Equal(t, "(null, , )\n", string(row))

	// A member set: the others stay empty.
	m2 := dynamicpb.NewMessage(desc)
	m2.Set(desc.Fields().ByName("c_b"), protoreflect.ValueOfBool(false))
	row2, err := pxf.AppendDatasetRow(nil, m2, cols)
	require.NoError(t, err)
	assert.Equal(t, "(, , false)\n", string(row2))

	h, err := pxf.AppendDatasetHeader(nil, desc, cols)
	require.NoError(t, err)
	assertTableReadsBack(t, desc, append(append(h, row...), row2...), []*dynamicpb.Message{m, m2})
}

func TestDatasetWrite_Required(t *testing.T) {
	desc := compileDatasetWrite(t, "Req")

	_, err := pxf.AppendDatasetHeader(nil, desc, []string{"id", "note"})
	require.ErrorContains(t, err, `omit "name", which is (pxf.required)`)

	cols := []string{"id", "name", "note"}
	m := dynamicpb.NewMessage(desc)
	_, err = pxf.AppendDatasetRow(nil, m, cols)
	require.ErrorContains(t, err, `column "name": unset, and the field is (pxf.required)`)

	m.Set(desc.Fields().ByName("name"), protoreflect.ValueOfString(""))
	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	// id is a proto3 scalar: its zero is written, so it is present.
	assert.Equal(t, `(0, "", )`+"\n", string(row))
}

func TestDatasetWrite_RefusesWhatACellCannotHold(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	for _, tc := range []struct {
		cols []string
		want string
	}{
		{nil, "at least one column"},
		{[]string{"inner.x"}, "dotted column paths are not supported"},
		{[]string{"nope"}, "dsw.v1.Row has no such field"},
		{[]string{"s", "s"}, `column "s" appears twice`},
		{[]string{"list"}, "a repeated field cannot be held"},
		{[]string{"m"}, "a map field cannot be held"},
		{[]string{"inner"}, "a dsw.v1.Inner field needs a block"},
		{[]string{"S"}, "no such field"}, // proto names, exactly
	} {
		t.Run(strings.Join(tc.cols, ","), func(t *testing.T) {
			dst := []byte("keep")
			out, err := pxf.AppendDatasetHeader(dst, desc, tc.cols)
			require.ErrorContains(t, err, tc.want)
			assert.Equal(t, "keep", string(out))
			out, err = pxf.AppendDatasetRow(dst, dynamicpb.NewMessage(desc), tc.cols)
			require.ErrorContains(t, err, tc.want)
			assert.Equal(t, "keep", string(out))
		})
	}
}

func TestDatasetWrite_RefusesSecretWithMetadata(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(`sec { value = "pw" hint = "vault" }`), m))
	_, err := pxf.AppendDatasetRow(nil, m, []string{"sec"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a block")
	assert.True(t, errors.Unwrap(err) != nil)
}
