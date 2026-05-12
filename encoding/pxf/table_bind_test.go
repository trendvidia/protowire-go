// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- Happy path: stream rows + bind into AllTypes ---

func TestTableReader_Scan_HappyPath(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, int32_field, bool_field, enum_field)
("alpha", 1, true, STATUS_ACTIVE)
("beta", 2, false, STATUS_INACTIVE)
("gamma", 3, true, STATUS_UNSPECIFIED)`
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)

	var got []map[string]any
	for {
		msg := dynamicpb.NewMessage(allTypes)
		if err := tr.Scan(msg); errors.Is(err, io.EOF) {
			break
		} else {
			require.NoError(t, err)
		}
		got = append(got, map[string]any{
			"s": msg.Get(allTypes.Fields().ByName("string_field")).String(),
			"i": msg.Get(allTypes.Fields().ByName("int32_field")).Int(),
			"b": msg.Get(allTypes.Fields().ByName("bool_field")).Bool(),
			"e": msg.Get(allTypes.Fields().ByName("enum_field")).Enum(),
		})
	}
	require.Len(t, got, 3)
	assert.Equal(t, "alpha", got[0]["s"])
	assert.Equal(t, int64(2), got[1]["i"])
	assert.Equal(t, false, got[1]["b"])
	// STATUS_ACTIVE = 1
	assert.EqualValues(t, 1, got[0]["e"])
}

// --- Empty cell: field stays at zero ---

func TestTableReader_Scan_EmptyCell_FieldUnset(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, int32_field)
("present", 7)
(, 99)
("set", )`
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)

	stringFd := allTypes.Fields().ByName("string_field")
	intFd := allTypes.Fields().ByName("int32_field")

	m1 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m1))
	assert.Equal(t, "present", m1.Get(stringFd).String())
	assert.Equal(t, int64(7), m1.Get(intFd).Int())

	m2 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m2))
	// Empty string_field cell — field stays at proto3 zero value.
	assert.Equal(t, "", m2.Get(stringFd).String())
	assert.Equal(t, int64(99), m2.Get(intFd).Int())

	m3 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m3))
	assert.Equal(t, "set", m3.Get(stringFd).String())
	assert.Equal(t, int64(0), m3.Get(intFd).Int())
}

// --- null on a wrapper: cleared (proto-wise: wrapper stays unset) ---

func TestTableReader_Scan_NullOnWrapper(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, nullable_int)
("with-value", 42)
("nullified", null)`
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)

	nullableIntFd := allTypes.Fields().ByName("nullable_int")

	m1 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m1))
	// Wrapper present with value 42.
	assert.True(t, m1.Has(nullableIntFd))

	m2 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m2))
	// `null` on a wrapper clears it — proto3 wrapper semantics mean
	// the wrapper field is NOT set on the message.
	assert.False(t, m2.Has(nullableIntFd))
}

// --- WKT: Timestamp + Duration bind from RFC3339 / Go-duration cells ---

func TestTableReader_Scan_WellKnownTypes(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, ts_field, dur_field)
("first",  2026-05-12T10:30:00Z, 1h30m)
("second", 2026-05-12T10:30:01Z, 500ms)`
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)

	tsFd := allTypes.Fields().ByName("ts_field")
	durFd := allTypes.Fields().ByName("dur_field")

	m1 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m1))
	tsMsg := m1.Get(tsFd).Message()
	// google.protobuf.Timestamp has fields `seconds` (int64) and `nanos` (int32).
	expected := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)
	gotSec := tsMsg.Get(tsMsg.Descriptor().Fields().ByName("seconds")).Int()
	assert.Equal(t, expected.Unix(), gotSec)

	m2 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m2))
	durMsg := m2.Get(durFd).Message()
	// 500ms → seconds=0, nanos=500_000_000
	gotNanos := durMsg.Get(durMsg.Descriptor().Fields().ByName("nanos")).Int()
	assert.Equal(t, int64(500_000_000), gotNanos)
}

// --- Cell-to-PXF-text round-trip preserves all scalar variants ---

func TestTableReader_Scan_AllScalarVariants(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, int32_field, int64_field, uint32_field, uint64_field, float_field, double_field, bool_field, bytes_field, enum_field)
("hi", -42, 1234567890, 100, 999999999, 3.14, 2.718281828, true, b"aGVsbG8=", STATUS_ACTIVE)`
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)
	msg := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(msg))

	get := func(name string) interface{} {
		return msg.Get(allTypes.Fields().ByName(protoreflect.Name(name))).Interface()
	}
	assert.Equal(t, "hi", get("string_field"))
	assert.EqualValues(t, -42, get("int32_field"))
	assert.EqualValues(t, 1234567890, get("int64_field"))
	assert.EqualValues(t, 100, get("uint32_field"))
	assert.EqualValues(t, 999999999, get("uint64_field"))
	assert.InDelta(t, 3.14, get("float_field"), 0.0001)
	assert.InDelta(t, 2.718281828, get("double_field"), 0.0000001)
	assert.Equal(t, true, get("bool_field"))
	assert.Equal(t, []byte("hello"), get("bytes_field"))
}

// --- Strings with special chars round-trip through the format+reparse pipe ---

func TestTableReader_Scan_StringWithSpecialChars(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field)
("has \"quotes\" and \\ backslashes and a newline\nat the end")`
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)
	msg := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(msg))
	got := msg.Get(allTypes.Fields().ByName("string_field")).String()
	assert.Equal(t, "has \"quotes\" and \\ backslashes and a newline\nat the end", got)
}

// --- EOF on empty table ---

func TestTableReader_Scan_EmptyTable_ReturnsEOF(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	tr, err := pxf.NewTableReader(strings.NewReader(`@table test.v1.AllTypes (string_field)`))
	require.NoError(t, err)
	msg := dynamicpb.NewMessage(allTypes)
	err = tr.Scan(msg)
	assert.ErrorIs(t, err, io.EOF)
}

// --- Scan after EOF stays sticky ---

func TestTableReader_Scan_StickyEOF(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	tr, err := pxf.NewTableReader(strings.NewReader(`@table test.v1.AllTypes (string_field)
("x")`))
	require.NoError(t, err)
	m1 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, tr.Scan(m1))
	m2 := dynamicpb.NewMessage(allTypes)
	require.ErrorIs(t, tr.Scan(m2), io.EOF)
	m3 := dynamicpb.NewMessage(allTypes)
	require.ErrorIs(t, tr.Scan(m3), io.EOF)
}

// --- BindRow standalone: usable against the materializing path ---

func TestBindRow_AgainstMaterializingPath(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, int32_field)
("alpha", 1)
("beta", 2)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 1)

	tbl := doc.Tables[0]
	for i, row := range tbl.Rows {
		msg := dynamicpb.NewMessage(allTypes)
		require.NoErrorf(t, pxf.BindRow(msg, tbl.Columns, row), "row %d", i)
	}
}

// --- BindRow arity mismatch: programmer error ---

func TestBindRow_ArityMismatch_Rejects(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)
	row := pxf.TableRow{Cells: []pxf.Value{&pxf.StringVal{Value: "x"}}}
	err := pxf.BindRow(msg, []string{"a", "b"}, row)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "columns vs")
}

// --- BindRow: hand-constructed row with a non-leaf cell value
// surfaces the "unexpected cell value type" defensive error.
// The parser rejects list/block cells in @table rows, so this branch
// is only reachable by callers that construct a TableRow manually
// (e.g. wiring BindRow into a non-PXF pipeline). Worth covering for
// the error message itself.

func TestBindRow_NonLeafCellValue_Rejects(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)
	row := pxf.TableRow{
		Cells: []pxf.Value{
			&pxf.ListVal{Elements: []pxf.Value{&pxf.StringVal{Value: "x"}}},
		},
	}
	err := pxf.BindRow(msg, []string{"string_field"}, row)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scalar-shaped")
}

// --- Equivalence: Scan vs. UnmarshalFull-then-BindRow produces same fields ---

func TestTableReader_Scan_EquivalentToMaterializingBind(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@table test.v1.AllTypes (string_field, int32_field, enum_field, ts_field, nullable_int)
("alpha", 1, STATUS_ACTIVE, 2026-05-12T10:00:00Z, 42)
("beta",  2, STATUS_INACTIVE, 2026-05-12T10:00:01Z, null)
( ,       3, STATUS_ACTIVE, 2026-05-12T10:00:02Z, )`

	// Streaming: Scan into N messages.
	tr, err := pxf.NewTableReader(strings.NewReader(in))
	require.NoError(t, err)
	var stream []*dynamicpb.Message
	for {
		m := dynamicpb.NewMessage(allTypes)
		if err := tr.Scan(m); errors.Is(err, io.EOF) {
			break
		} else {
			require.NoError(t, err)
		}
		stream = append(stream, m)
	}

	// Materializing: Parse the table, BindRow each into N messages.
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 1)
	tbl := doc.Tables[0]
	var mat []*dynamicpb.Message
	for _, row := range tbl.Rows {
		m := dynamicpb.NewMessage(allTypes)
		require.NoError(t, pxf.BindRow(m, tbl.Columns, row))
		mat = append(mat, m)
	}

	require.Equal(t, len(mat), len(stream))
	for i := range mat {
		matBytes, err := matMarshalCanonical(mat[i])
		require.NoError(t, err)
		streamBytes, err := matMarshalCanonical(stream[i])
		require.NoError(t, err)
		assert.Equal(t, matBytes, streamBytes, "row %d: streaming and materializing bind must produce identical wire bytes", i)
	}
}

// matMarshalCanonical uses proto.MarshalOptions{Deterministic: true}
// so map ordering doesn't perturb the comparison.
func matMarshalCanonical(m *dynamicpb.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}
