// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- Parse: AST-level happy path ---

func TestParseTable_Basic(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, price, qty)
("AAPL", 192.34, 100)
("MSFT", 410.10, 50)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 1)

	tbl := doc.Tables[0]
	assert.Equal(t, "trades.v1.Trade", tbl.Type)
	assert.Equal(t, []string{"symbol", "price", "qty"}, tbl.Columns)
	require.Len(t, tbl.Rows, 2)
	assert.Len(t, tbl.Rows[0].Cells, 3)
	assert.Len(t, tbl.Rows[1].Cells, 3)
}

func TestParseTable_EmptyRows(t *testing.T) {
	// @table with header but no rows MUST be accepted (zero-row dataset).
	doc, err := pxf.Parse([]byte(`@table trades.v1.Trade (symbol, price)`))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 1)
	assert.Equal(t, []string{"symbol", "price"}, doc.Tables[0].Columns)
	assert.Empty(t, doc.Tables[0].Rows)
}

// --- Three cell states: empty / null / value ---

func TestParseTable_ThreeCellStates(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, price, qty)
("AAPL", 192.34, 100)
("MSFT", null, 50)
("GOOG", , )`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 1)
	rows := doc.Tables[0].Rows
	require.Len(t, rows, 3)

	// Row 1: all present values.
	for _, c := range rows[0].Cells {
		assert.NotNil(t, c, "all cells should be non-nil")
	}
	_, ok := rows[0].Cells[0].(*pxf.StringVal)
	assert.True(t, ok)

	// Row 2: middle cell is explicit null.
	assert.NotNil(t, rows[1].Cells[0])
	_, isNull := rows[1].Cells[1].(*pxf.NullVal)
	assert.True(t, isNull, "explicit null literal should produce *NullVal, not nil")
	assert.NotNil(t, rows[1].Cells[2])

	// Row 3: trailing cells empty (absent).
	assert.NotNil(t, rows[2].Cells[0])
	assert.Nil(t, rows[2].Cells[1], "empty cell should produce nil Value (absent)")
	assert.Nil(t, rows[2].Cells[2])
}

func TestParseTable_LeadingEmptyCell(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, price)
( , 192.34)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	row := doc.Tables[0].Rows[0]
	assert.Nil(t, row.Cells[0], "leading empty cell should produce nil")
	assert.NotNil(t, row.Cells[1])
}

func TestParseTable_AllEmptyRow(t *testing.T) {
	in := `@table trades.v1.Trade (a, b, c)
(,,)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	row := doc.Tables[0].Rows[0]
	require.Len(t, row.Cells, 3)
	for i, c := range row.Cells {
		assert.Nil(t, c, "cell %d should be nil", i)
	}
}

// --- Arity enforcement ---

func TestParseTable_ArityShort_Rejects(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, price, qty)
("AAPL", 1.0)`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 cells, expected 3")
}

func TestParseTable_ArityLong_Rejects(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, price)
("AAPL", 1.0, 100)`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "3 cells, expected 2")
}

// --- v1 cell-grammar restrictions ---

func TestParseTable_ListCell_Rejects(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, tags)
("AAPL", ["tech", "blue-chip"])`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list values")
}

func TestParseTable_BlockCell_Rejects(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, meta)
("AAPL", { exchange = "NASDAQ" })`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block values")
}

// --- Column-entry restrictions ---

func TestParseTable_DottedColumn_Rejects(t *testing.T) {
	in := `@table trades.v1.Trade (symbol, meta.exchange)
("AAPL", "NASDAQ")`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dotted column paths")
}

func TestParseTable_EmptyColumnList_Rejects(t *testing.T) {
	_, err := pxf.Parse([]byte(`@table trades.v1.Trade ()`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one field name")
}

// --- Standalone constraint ---

func TestParseTable_WithAtType_Rejects(t *testing.T) {
	in := `@type trades.v1.Wrapper
@table trades.v1.Trade (symbol)
("AAPL")`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@type")
}

func TestParseTable_AtTypeAfter_Rejects(t *testing.T) {
	// Ordering shouldn't matter — @type after @table is also a conflict.
	in := `@table trades.v1.Trade (symbol)
("AAPL")
@type trades.v1.Wrapper`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
}

func TestParseTable_WithBodyEntries_Rejects(t *testing.T) {
	in := `@table trades.v1.Trade (symbol)
("AAPL")
extra_field = "stray"`
	_, err := pxf.Parse([]byte(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "top-level field")
}

// --- Multiple tables ---

func TestParseTable_MultipleTables_OrderPreserved(t *testing.T) {
	in := `@table events.v1.Created (id, ts)
("e-1", 2026-05-11T10:00:00Z)
("e-2", 2026-05-11T10:00:01Z)
@table events.v1.Deleted (id, ts)
("e-9", 2026-05-11T10:00:02Z)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 2)
	assert.Equal(t, "events.v1.Created", doc.Tables[0].Type)
	assert.Equal(t, "events.v1.Deleted", doc.Tables[1].Type)
	assert.Len(t, doc.Tables[0].Rows, 2)
	assert.Len(t, doc.Tables[1].Rows, 1)
}

// --- UnmarshalFull surfaces tables ---

func TestUnmarshalFull_TablesAccessor(t *testing.T) {
	// Decode against a dummy message — the body is empty, so the bound
	// message stays zero-valued. The @table data flows through Result.
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)

	in := []byte(`@table test.v1.AllTypes (string_field, int32_field)
("row-1", 1)
("row-2", null)
( , 3)`)
	res, err := pxf.UnmarshalFull(in, msg)
	require.NoError(t, err)
	tables := res.Tables()
	require.Len(t, tables, 1)
	assert.Equal(t, "test.v1.AllTypes", tables[0].Type)
	assert.Equal(t, []string{"string_field", "int32_field"}, tables[0].Columns)
	require.Len(t, tables[0].Rows, 3)

	// Cell-state spot check via the fast path.
	r0 := tables[0].Rows[0].Cells
	r1 := tables[0].Rows[1].Cells
	r2 := tables[0].Rows[2].Cells
	assert.NotNil(t, r0[0])
	assert.NotNil(t, r0[1])
	_, isNull := r1[1].(*pxf.NullVal)
	assert.True(t, isNull)
	assert.Nil(t, r2[0])
	assert.NotNil(t, r2[1])
}

// --- Error message quality ---

func TestParseTable_ErrorMessagesMentionDraftSection(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"list_cell", `@table T (a, b)
(1, [2,3])`, "§3.4.4"},
		{"block_cell", `@table T (a, b)
(1, {x=1})`, "§3.4.4"},
		{"dotted_col", `@table T (a.b)`, "§3.4.4"},
		{"with_type", `@type X
@table T (a)
(1)`, "§3.4.4"},
		{"with_body", `@table T (a)
(1)
b = 2`, "§3.4.4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := pxf.Parse([]byte(c.in))
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), c.want),
				"error %q should mention %q", err.Error(), c.want)
		})
	}
}

// --- AST-tier error paths (mirror of the fast-path coverage in
// table_fastpath_test.go; both implementations must reject) ---

func TestParseTable_AstErrorPaths(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     string
		errSub string
	}{
		{"missing_type", `@table ( col )
( "x" )`, "expected row message type"},
		{"missing_lparen", `@table T col )
( "x" )`, "expected '('"},
		{"bad_column_separator", `@table T ( a b )`, "expected ',' or ')'"},
		{"trailing_comma_in_columns", `@table T (a,)`, "expected column field name"},
		{"row_unterminated", `@table T ( a )
( "x"`, "expected ',' or ')'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := pxf.Parse([]byte(c.in))
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.errSub)
		})
	}
}

// --- AT_TABLE lexer recognition ---

func TestParseTable_LexerRecognizes_AtTable(t *testing.T) {
	// Confirm @table tokenizes as AT_TABLE, not as @directive named
	// "table" — otherwise the column-list syntax would be parsed as
	// a body entry.
	doc, err := pxf.Parse([]byte(`@table x.y (a)`))
	require.NoError(t, err)
	require.Len(t, doc.Tables, 1)
	assert.Empty(t, doc.Directives, "@table must not appear in Directives slot")
}

// --- Cells preserve value variants ---

func TestParseTable_CellVariants(t *testing.T) {
	in := `@table t.T (s, i, f, b, n, ts, d, e)
("hi", 42, 3.14, true, null, 2026-05-11T10:00:00Z, 1h30m, ENUM_VALUE)`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	cells := doc.Tables[0].Rows[0].Cells

	assertCellKind := func(idx int, want any) {
		t.Helper()
		switch want.(type) {
		case *pxf.StringVal:
			_, ok := cells[idx].(*pxf.StringVal)
			assert.True(t, ok, "cell %d: want StringVal", idx)
		case *pxf.IntVal:
			_, ok := cells[idx].(*pxf.IntVal)
			assert.True(t, ok, "cell %d: want IntVal", idx)
		case *pxf.FloatVal:
			_, ok := cells[idx].(*pxf.FloatVal)
			assert.True(t, ok, "cell %d: want FloatVal", idx)
		case *pxf.BoolVal:
			_, ok := cells[idx].(*pxf.BoolVal)
			assert.True(t, ok, "cell %d: want BoolVal", idx)
		case *pxf.NullVal:
			_, ok := cells[idx].(*pxf.NullVal)
			assert.True(t, ok, "cell %d: want NullVal", idx)
		case *pxf.TimestampVal:
			_, ok := cells[idx].(*pxf.TimestampVal)
			assert.True(t, ok, "cell %d: want TimestampVal", idx)
		case *pxf.DurationVal:
			_, ok := cells[idx].(*pxf.DurationVal)
			assert.True(t, ok, "cell %d: want DurationVal", idx)
		case *pxf.IdentVal:
			_, ok := cells[idx].(*pxf.IdentVal)
			assert.True(t, ok, "cell %d: want IdentVal", idx)
		}
	}
	assertCellKind(0, (*pxf.StringVal)(nil))
	assertCellKind(1, (*pxf.IntVal)(nil))
	assertCellKind(2, (*pxf.FloatVal)(nil))
	assertCellKind(3, (*pxf.BoolVal)(nil))
	assertCellKind(4, (*pxf.NullVal)(nil))
	assertCellKind(5, (*pxf.TimestampVal)(nil))
	assertCellKind(6, (*pxf.DurationVal)(nil))
	assertCellKind(7, (*pxf.IdentVal)(nil))
}
