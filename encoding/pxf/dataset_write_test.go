// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
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
    google.protobuf.StringValue c_w = 50 [(pxf.default) = "picked"];
    int32 c_i = 51;
    bool c_b = 52;
  }

  // A scalar member with a default: unset with no member set, it is an
  // empty cell (null is rejected on a scalar) and reads back as the
  // default.
  oneof other {
    string o_s = 53 [(pxf.default) = "other"];
    int32 o_i = 54;
  }

  // Value cells (protowire#341): lists and blocks.
  repeated int32 list = 60;
  map<string, int32> m = 61;
  Inner inner = 62;
  repeated Inner inners = 63;
  repeated Keyed keyed = 64 [(pxf.key) = "id"];
  map<string, Inner> by_key = 65;
}

message Keyed {
  string id = 1;
  int32 v = 2;
}

message Inner {
  int32 x = 1;
  string label = 2;
}

message Pick {
  oneof choice {
    google.protobuf.StringValue c_w = 1 [(pxf.default) = "picked"];
    int32 c_i = 2;
    bool c_b = 3;
  }
  oneof other {
    string o_s = 4 [(pxf.default) = "other"];
    int32 o_i = 5;
  }
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
	"c_w", "c_i", "c_b", "o_s", "o_i",
	"list", "m", "inner", "inners", "keyed", "by_key",
}

// defaultedColumns are the columns of dsw.v1.Row with (pxf.default).
// A table that leaves one out reads it back as its default, so a round
// trip over a column subset always keeps these.
var defaultedColumns = []string{"d_s", "d_i32", "d_opt", "d_w", "d_ts", "d_color", "c_w", "o_s"}

// makeWritable sets what a row cannot carry unset (see
// AppendDatasetRow): d_opt, an optional scalar with a default, and a
// member of oneof "other", whose defaulted member is a scalar. Written
// unset they are empty cells and read back as their defaults, so a
// round trip expecting equal messages sets them. "other" counts as
// unset when its set member is not among cols, since that member is
// not written. Only fields among cols are touched.
func makeWritable(m *dynamicpb.Message, cols []string) {
	desc := m.Descriptor()
	if fd := desc.Fields().ByName("d_opt"); slices.Contains(cols, "d_opt") && !m.Has(fd) {
		m.Set(fd, protoreflect.ValueOfInt32(0))
	}
	set := m.WhichOneof(desc.Oneofs().ByName("other"))
	if slices.Contains(cols, "o_s") && (set == nil || !slices.Contains(cols, string(set.Name()))) {
		m.Set(desc.Fields().ByName("o_s"), protoreflect.ValueOfString(""))
	}
}

// onlyColumns is a copy of m with only the fields among cols: what a
// row of those columns reads back as.
func onlyColumns(m *dynamicpb.Message, cols []string) *dynamicpb.Message {
	desc := m.Descriptor()
	out := dynamicpb.NewMessage(desc)
	for _, c := range cols {
		fd := desc.Fields().ByName(protoreflect.Name(c))
		if m.Has(fd) {
			out.Set(fd, m.Get(fd))
		}
	}
	return out
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
		if doc := randomNested(r, c); doc != "" {
			if err := pxf.Unmarshal([]byte(doc), m); err != nil {
				t.Fatalf("setting %s: %v", doc, err)
			}
			continue
		}
		if fd.Kind() == protoreflect.MessageKind {
			lit := randomLiteral(r, fd.Message().FullName())
			if err := pxf.Unmarshal([]byte(c+" = "+lit), m); err != nil {
				t.Fatalf("setting %s = %s: %v", c, lit, err)
			}
			continue
		}
		m.Set(fd, randomScalar(r, fd))
	}
	makeWritable(m, rowColumns)
	return m
}

var trickyStrings = []string{"", "plain", "a, b", "(x)", "\"q\"", "back\\slash", "line\nbreak", "tab\t", "# not a comment", "// nor this", "ünï ✓", "null", "true", "@dataset"}

// randomNested is a document setting one of the value-cell columns at
// random, or "" for any other column.
func randomNested(r *rand.Rand, col string) string {
	inner := func() string {
		switch r.IntN(3) {
		case 0:
			return "{}"
		case 1:
			return fmt.Sprintf("{ x = %d }", r.Int32()-r.Int32())
		}
		return fmt.Sprintf("{ x = %d label = %q }", r.IntN(9), trickyStrings[r.IntN(len(trickyStrings))])
	}
	n := r.IntN(4)
	var parts []string
	switch col {
	case "list":
		for range n {
			parts = append(parts, fmt.Sprint(r.Int32()-r.Int32()))
		}
		return "list = [" + strings.Join(parts, ", ") + "]"
	case "m":
		for i := range n {
			parts = append(parts, fmt.Sprintf("%q: %d", fmt.Sprint(i)+trickyStrings[r.IntN(len(trickyStrings))], r.IntN(100)))
		}
		return "m = { " + strings.Join(parts, " ") + " }"
	case "inner":
		return "inner " + inner()
	case "inners":
		for range n {
			parts = append(parts, inner())
		}
		return "inners = [" + strings.Join(parts, ", ") + "]"
	case "keyed":
		if r.IntN(2) == 0 { // the keyed form: distinct, non-empty keys
			for i := range n {
				parts = append(parts, fmt.Sprintf("k%d { v = %d }", i, r.IntN(9)))
			}
			return "keyed { " + strings.Join(parts, " ") + " }"
		}
		for range n { // anonymous: no key at all
			parts = append(parts, fmt.Sprintf("{ v = %d }", r.IntN(9)))
		}
		return "keyed = [" + strings.Join(parts, ", ") + "]"
	case "by_key":
		for i := range n {
			parts = append(parts, fmt.Sprintf("%q: %s", fmt.Sprint("k", i), inner()))
		}
		return "by_key = { " + strings.Join(parts, " ") + " }"
	}
	return ""
}

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
		before := len(out)
		out, err = pxf.AppendDatasetRow(out, m, columns)
		require.NoError(t, err, "row for %v", m)
		// One row per line, nested cells included (#155).
		require.Equal(t, 1, bytes.Count(out[before:], []byte("\n")), "row %q", out[before:])
	}
	return out
}

// assertTableReadsBack reads table both ways, streaming (Scan) and
// materializing (Parse + BindRow), and requires each row to equal msgs,
// in order.
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
	empty := dynamicpb.NewMessage(desc) // every column unset that can be
	makeWritable(empty, rowColumns)
	msgs = append(msgs, empty)
	table := writeTable(t, desc, rowColumns, msgs)
	assertTableReadsBack(t, desc, table, msgs)
}

// FuzzDatasetWriteRoundTrip is the round trip on a fuzzer-chosen seed
// and column subset. The whole message is written, as a caller exporting
// some of its fields would write it, and each row must read back as the
// message's column fields alone. A field outside the columns can still
// shape a cell: a set oneof member that is not a column must not keep a
// column member's default off.
func FuzzDatasetWriteRoundTrip(f *testing.F) {
	f.Add(uint64(1), uint64(0))
	f.Add(uint64(142), uint64(math.MaxUint64))
	f.Fuzz(func(t *testing.T, seed, colMask uint64) {
		desc := compileDatasetWrite(t, "Row")
		var cols []string
		for i, c := range rowColumns {
			if colMask&(1<<uint(i)) != 0 || slices.Contains(defaultedColumns, c) {
				cols = append(cols, c)
			}
		}
		r := rand.New(rand.NewPCG(seed, 7))
		msgs := make([]*dynamicpb.Message, 8)
		want := make([]*dynamicpb.Message, len(msgs))
		for i := range msgs {
			msgs[i] = randomRow(t, r, desc)
			for _, c := range []string{"c_i", "c_b", "o_i"} {
				// Set a non-defaulted oneof member now and then, so the
				// ones left out of cols get exercised.
				if r.IntN(4) == 0 {
					fd := desc.Fields().ByName(protoreflect.Name(c))
					msgs[i].Set(fd, randomScalar(r, fd))
				}
			}
			makeWritable(msgs[i], cols)
			want[i] = onlyColumns(msgs[i], cols)
		}
		assertTableReadsBack(t, desc, writeTable(t, desc, cols, msgs), want)
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
	cols := []string{"s", "i32", "opt", "d_i32", "d_w", "color", "ts"}
	h, err := pxf.AppendDatasetHeader(nil, desc, cols)
	require.NoError(t, err)
	assert.Equal(t, "@dataset dsw.v1.Row (s, i32, opt, d_i32, d_w, color, ts)\n", string(h))

	m := dynamicpb.NewMessage(desc)
	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	// Unset: empty, except d_i32 (zero written, so the default does not
	// replace it) and d_w (a wrapper: null, so it stays unset).
	assert.Equal(t, "(, , , 0, null, , )\n", string(row))

	require.NoError(t, pxf.Unmarshal([]byte(`s = "a, (b)" i32 = -3 opt = 0 d_i32 = 4 color = COLOR_RED ts = 2026-10-23T00:00:00Z`), m))
	row, err = pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	assert.Equal(t, `("a, (b)", -3, 0, 4, null, COLOR_RED, 2026-10-23T00:00:00Z)`+"\n", string(row))
}

func TestDatasetWrite_OneofDefaults(t *testing.T) {
	desc := compileDatasetWrite(t, "Pick")
	f := desc.Fields()
	cols := []string{"c_w", "c_i", "c_b", "o_s", "o_i"}

	// No member of "choice" set: its defaulted member is a wrapper, so it
	// is null, which keeps the default off. "other" has o_i set.
	m := dynamicpb.NewMessage(desc)
	m.Set(f.ByName("o_i"), protoreflect.ValueOfInt32(4))
	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	assert.Equal(t, "(null, , , , 4)\n", string(row))

	// A member set: the others stay empty, the defaulted one included.
	m2 := dynamicpb.NewMessage(desc)
	m2.Set(f.ByName("c_b"), protoreflect.ValueOfBool(false))
	m2.Set(f.ByName("o_s"), protoreflect.ValueOfString(""))
	row2, err := pxf.AppendDatasetRow(nil, m2, cols)
	require.NoError(t, err)
	assert.Equal(t, `(, , false, "", )`+"\n", string(row2))

	h, err := pxf.AppendDatasetHeader(nil, desc, cols)
	require.NoError(t, err)
	assertTableReadsBack(t, desc, append(append(h, row...), row2...), []*dynamicpb.Message{m, m2})

	// No member of "other" set: its defaulted member is a scalar, which
	// no cell holds unset (null is rejected on a scalar), so it is empty
	// and reads back as its default.
	row3, err := pxf.AppendDatasetRow(nil, dynamicpb.NewMessage(desc), cols)
	require.NoError(t, err)
	assert.Equal(t, "(null, , , , )\n", string(row3))

	// A member set, but not among the columns: it is not written, so it
	// cannot keep the default off. The wrapper member is null, as if no
	// member were set, and reads back unset; the scalar member is empty,
	// as in row3, and reads back as its default.
	m4 := dynamicpb.NewMessage(desc)
	m4.Set(f.ByName("c_i"), protoreflect.ValueOfInt32(5))
	m4.Set(f.ByName("o_i"), protoreflect.ValueOfInt32(6))
	row4, err := pxf.AppendDatasetRow(nil, m4, []string{"c_w", "o_s"})
	require.NoError(t, err)
	assert.Equal(t, "(null, )\n", string(row4))
	// o_i is a column here, so it keeps o_s's default off and the row
	// reads back as m4 without c_i.
	row5, err := pxf.AppendDatasetRow(nil, m4, []string{"c_w", "o_i"})
	require.NoError(t, err)
	assert.Equal(t, "(null, 6)\n", string(row5))
	h5, err := pxf.AppendDatasetHeader(nil, desc, []string{"c_w", "o_i"})
	require.NoError(t, err)
	want5 := dynamicpb.NewMessage(desc)
	want5.Set(f.ByName("o_i"), protoreflect.ValueOfInt32(6))
	assertTableReadsBack(t, desc, append(h5, row5...), []*dynamicpb.Message{want5})
}

// The reader parses rows with the bind-time schema checks skipped, so
// the writers refuse a schema Unmarshal refuses rather than write a
// table that cannot be read back: a field named null makes the header
// unparsable, and two defaulted members of one oneof would be two null
// cells in one row.
func TestDatasetWrite_RefusesInvalidSchema(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		cols      []string
		want      string
	}{
		{"reserved", `message Res { int32 null = 1; int32 ok = 2; }`, []string{"ok"}, `uses PXF-reserved name "null"`},
		{"two oneof defaults", `message Two { oneof k {
		  google.protobuf.StringValue a = 1 [(pxf.default) = "x"];
		  google.protobuf.StringValue b = 2 [(pxf.default) = "y"]; } }`, []string{"a", "b"}, `at most one member of oneof "k" may carry a default`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "syntax = \"proto3\";\npackage dswbad.v1;\nimport \"google/protobuf/wrappers.proto\";\nimport \"pxf/annotations.proto\";\n" + tc.src
			fd := compileProtoSources(t, "bad.proto", map[string]string{"bad.proto": src, "pxf/annotations.proto": annotationsProtoSrc})
			desc := fd.Messages().Get(0)
			_, err := pxf.AppendDatasetHeader(nil, desc, tc.cols)
			require.ErrorContains(t, err, tc.want)
			_, err = pxf.AppendDatasetRow(nil, dynamicpb.NewMessage(desc), tc.cols)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// A message-typed field in the message's _null mask is null, as Marshal
// writes it, so a default stays off and a required field is satisfied.
// A scalar in the mask is written as if the mask did not name it: the
// draft rejects null on a scalar, while Marshal writes it
// (trendvidia/protowire#339). That divergence is pinned here.
func TestDatasetWrite_NullMask(t *testing.T) {
	src := `syntax = "proto3";
package dswnull.v1;
import "google/protobuf/field_mask.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";
message N {
  google.protobuf.StringValue w = 1;
  google.protobuf.Int32Value dw = 2 [(pxf.default) = "9"];
  google.protobuf.Int32Value rw = 3 [(pxf.required) = true];
  int32 n = 4;
  google.protobuf.FieldMask _null = 5;
}`
	fd := compileProtoSources(t, "null.proto", map[string]string{"null.proto": src, "pxf/annotations.proto": annotationsProtoSrc})
	desc := fd.Messages().ByName("N")
	cols := []string{"w", "dw", "rw", "n"}

	m := dynamicpb.NewMessage(desc)
	_, err := pxf.UnmarshalFull([]byte("w = null\ndw = null\nrw = null\nn = null\n"), m)
	require.NoError(t, err)
	require.NoError(t, pxf.Unmarshal([]byte(`w = "set anyway"`+"\nn = 3\n"), m))
	marshalled, err := pxf.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, "w = null\ndw = null\nrw = null\nn = null\n", string(marshalled))

	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	assert.Equal(t, "(null, null, null, 3)\n", string(row))

	// Without the mask, the set wrapper is its value and the unset
	// required one is refused.
	m.Clear(desc.Fields().ByName("_null"))
	_, err = pxf.AppendDatasetRow(nil, m, cols)
	require.ErrorContains(t, err, `column "rw": unset, and the field is (pxf.required)`)
}

// An unset optional scalar with a default is an empty cell: it reads
// back as the default, which is what an absent annotated field means.
func TestDatasetWrite_UnsetOptionalScalarWithDefaultIsEmpty(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	row, err := pxf.AppendDatasetRow(nil, dynamicpb.NewMessage(desc), []string{"d_opt"})
	require.NoError(t, err)
	assert.Equal(t, "()\n", string(row))
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
		{[]string{"inner.x"}, "does not write dotted column paths yet"},
		{[]string{"nope"}, "dsw.v1.Row has no such field"},
		{[]string{"s", "s"}, `column "s" appears twice`},
		// The reader's header rules, applied before any column resolves.
		{[]string{"inner", "inner.x"}, `column "inner.x" overlaps column "inner"`},
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

// A list, map or block value is one cell on one line (protowire#341),
// the text Marshal writes for the field; a pxf.Secret with a hint, which
// needs a block, is one now too (#155).
func TestDatasetWrite_ValueCells(t *testing.T) {
	desc := compileDatasetWrite(t, "Row")
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(`list = [1, 2, 3] m = { "a b": 1 "c": 2 } inner {} inners = [{ x = 1 }, { label = "l,)" }] keyed { a { v = 1 } } by_key = { "k": { x = 2 } } sec { value = "pw" hint = "vault" }`), m))
	cols := []string{"list", "m", "inner", "inners", "keyed", "by_key", "sec"}
	row, err := pxf.AppendDatasetRow(nil, m, cols)
	require.NoError(t, err)
	assert.Equal(t, `([ 1, 2, 3 ], { "a b": 1 c: 2 }, { }, [ { x = 1 }, { label = "l,)" } ], { a { v = 1 } }, { k: { x = 2 } }, { value = "pw" hint = "vault" })`+"\n", string(row))

	h, err := pxf.AppendDatasetHeader(nil, desc, cols)
	require.NoError(t, err)
	assertTableReadsBack(t, desc, append(h, row...), []*dynamicpb.Message{withDefaults(t, desc, m, cols)})
}

// withDefaults is m as a table with these columns reads it back: every
// field of dsw.v1.Row outside the columns that has (pxf.default) takes it.
func withDefaults(t *testing.T, desc protoreflect.MessageDescriptor, m *dynamicpb.Message, cols []string) *dynamicpb.Message {
	t.Helper()
	out := proto.Clone(m).(*dynamicpb.Message)
	for _, c := range defaultedColumns {
		if slices.Contains(cols, c) {
			continue
		}
		fd := desc.Fields().ByName(protoreflect.Name(c))
		if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() && out.WhichOneof(od) != nil {
			continue
		}
		def, _ := pxf.Default(fd)
		require.NoError(t, pxf.ApplyDefault(out, fd, def))
	}
	return out
}
