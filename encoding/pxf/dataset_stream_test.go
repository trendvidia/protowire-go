// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- Happy path ---

func TestTableReader_BasicStreaming(t *testing.T) {
	in := `@dataset trades.v1.Trade (symbol, price, qty)
("AAPL", 192.34, 100)
("MSFT", 410.10, 50)
("GOOG", 142.00, 25)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	assert.Equal(t, "trades.v1.Trade", tr.Type())
	assert.Equal(t, []string{"symbol", "price", "qty"}, tr.Columns())
	assert.Empty(t, tr.Directives())

	var rows []pxf.DatasetRow
	for {
		row, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		rows = append(rows, row)
	}
	require.Len(t, rows, 3)
	assert.Len(t, rows[0].Cells, 3)

	// Symbol of row 0 is "AAPL".
	sv, ok := rows[0].Cells[0].(*pxf.StringVal)
	require.True(t, ok)
	assert.Equal(t, "AAPL", sv.Value)
}

func TestTableReader_EmptyTable(t *testing.T) {
	in := `@dataset trades.v1.Trade (symbol, price)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	_, err = tr.Next()
	assert.ErrorIs(t, err, io.EOF)

	// EOF is sticky.
	_, err = tr.Next()
	assert.ErrorIs(t, err, io.EOF)
}

// --- Three cell states ---

func TestTableReader_CellStates(t *testing.T) {
	in := `@dataset t.T (a, b, c)
("x", 1, true)
(null, , 3)
(, "y", null)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	row1, err := tr.Next()
	require.NoError(t, err)
	// All present, distinct types.
	_, ok := row1.Cells[0].(*pxf.StringVal)
	assert.True(t, ok)
	_, ok = row1.Cells[1].(*pxf.IntVal)
	assert.True(t, ok)
	_, ok = row1.Cells[2].(*pxf.BoolVal)
	assert.True(t, ok)

	row2, err := tr.Next()
	require.NoError(t, err)
	// null / empty / present.
	_, ok = row2.Cells[0].(*pxf.NullVal)
	assert.True(t, ok)
	assert.Nil(t, row2.Cells[1], "empty cell should be nil")
	_, ok = row2.Cells[2].(*pxf.IntVal)
	assert.True(t, ok)

	row3, err := tr.Next()
	require.NoError(t, err)
	// empty / present / null.
	assert.Nil(t, row3.Cells[0])
	_, ok = row3.Cells[1].(*pxf.StringVal)
	assert.True(t, ok)
	_, ok = row3.Cells[2].(*pxf.NullVal)
	assert.True(t, ok)

	_, err = tr.Next()
	assert.ErrorIs(t, err, io.EOF)
}

// --- Leading directives ---

func TestTableReader_SideChannelDirectivesBeforeHeader(t *testing.T) {
	in := `@header meta.v1.H { generated_at = 2026-05-11T10:00:00Z }
@dataset trades.v1.Trade (symbol)
("AAPL")
("MSFT")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	require.Len(t, tr.Directives(), 1)
	assert.Equal(t, "header", tr.Directives()[0].Name)
	assert.Equal(t, "meta.v1.H", tr.Directives()[0].Type)

	count := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		count++
	}
	assert.Equal(t, 2, count)
}

// --- Standalone constraint enforced at header read ---

func TestTableReader_RejectsAtTypeWithAtTable(t *testing.T) {
	in := `@type some.Other
@dataset trades.v1.Trade (symbol)
("AAPL")`
	_, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@type")
}

// --- Missing @dataset ---

func TestTableReader_NoTableInStream(t *testing.T) {
	in := `string_field = "x"`
	_, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.ErrorIs(t, err, pxf.ErrNoDataset)
}

func TestTableReader_EmptyInput(t *testing.T) {
	_, err := pxf.NewDatasetReader(strings.NewReader(""))
	require.ErrorIs(t, err, pxf.ErrNoDataset)
}

// --- Error mid-stream ---

func TestTableReader_ErrorsAreSticky(t *testing.T) {
	in := `@dataset T (a, b, c)
("x", 1, 2)
("y", 1)` // arity mismatch
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	_, err = tr.Next()
	require.NoError(t, err)

	_, err = tr.Next()
	require.Error(t, err)
	first := err

	// Subsequent calls return the same sticky error.
	_, err = tr.Next()
	assert.Equal(t, first, err)
}

func TestTableReader_RejectsListCellMidStream(t *testing.T) {
	in := `@dataset T (a, b)
("ok", 1)
("bad", [1, 2])`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	_, err = tr.Next()
	require.NoError(t, err)

	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list values")
}

func TestTableReader_RejectsBlockCellMidStream(t *testing.T) {
	in := `@dataset T (a, b)
("ok", 1)
("bad", { x = 1 })`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	_, err = tr.Next()
	require.NoError(t, err)

	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block values")
}

// --- Strings / comments inside cells don't trip the row-boundary scanner ---

func TestTableReader_StringWithParens(t *testing.T) {
	in := `@dataset T (note, n)
("contains (paren) inside", 1)
("normal", 2)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	r1, err := tr.Next()
	require.NoError(t, err)
	sv := r1.Cells[0].(*pxf.StringVal)
	assert.Equal(t, "contains (paren) inside", sv.Value)

	r2, err := tr.Next()
	require.NoError(t, err)
	sv = r2.Cells[0].(*pxf.StringVal)
	assert.Equal(t, "normal", sv.Value)
}

func TestTableReader_TripleQuotedStringWithParens(t *testing.T) {
	in := `@dataset T (note)
("""multi
line ) with paren""")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	row, err := tr.Next()
	require.NoError(t, err)
	sv := row.Cells[0].(*pxf.StringVal)
	assert.Contains(t, sv.Value, "with paren")
}

func TestTableReader_BytesLiteralWithParens(t *testing.T) {
	// `b"..."` content is base64 — no parens possible in valid input,
	// but the byte scanner mustn't fall over on `b"` opening.
	in := `@dataset T (blob, n)
(b"aGVsbG8=", 1)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	row, err := tr.Next()
	require.NoError(t, err)
	bv, ok := row.Cells[0].(*pxf.BytesVal)
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), bv.Value)
}

func TestTableReader_CommentBetweenRows(t *testing.T) {
	in := `@dataset T (a)
("x")
# this is a comment, with ( a paren ) inside
("y")
// another comment ) here
("z")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	count := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		count++
	}
	assert.Equal(t, 3, count)
}

// --- Whitespace / blank lines between rows ---

func TestTableReader_BlankLinesBetweenRows(t *testing.T) {
	in := `@dataset T (a)


("x")



("y")
`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err := tr.Next()
		require.NoErrorf(t, err, "row %d", i)
	}
	_, err = tr.Next()
	assert.ErrorIs(t, err, io.EOF)
}

// --- Chunked io.Reader: rows split across reads ---

// chunkedReader returns one byte per Read until exhausted. Maximally
// adversarial for any buffering bug.
type chunkedReader struct {
	data []byte
	i    int
}

func (cr *chunkedReader) Read(p []byte) (int, error) {
	if cr.i >= len(cr.data) {
		return 0, io.EOF
	}
	p[0] = cr.data[cr.i]
	cr.i++
	return 1, nil
}

func TestTableReader_HandlesByteAtATimeReader(t *testing.T) {
	in := `@dataset T (a, b, c)
("hello", 42, true)
("world", 99, false)
("end", 0, null)`
	tr, err := pxf.NewDatasetReader(&chunkedReader{data: []byte(in)})
	require.NoError(t, err)

	count := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		count++
	}
	assert.Equal(t, 3, count)
}

// --- Multi-table documents: consecutive readers ---

func TestTableReader_MultipleTablesViaReuse(t *testing.T) {
	in := `@dataset events.v1.Created (id, ts)
("e-1", 2026-05-11T10:00:00Z)
("e-2", 2026-05-11T10:00:01Z)
@dataset events.v1.Deleted (id, ts)
("e-9", 2026-05-11T10:00:02Z)`
	br := strings.NewReader(in)

	tr1, err := pxf.NewDatasetReader(br)
	require.NoError(t, err)
	assert.Equal(t, "events.v1.Created", tr1.Type())
	c1 := 0
	for {
		_, err := tr1.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		c1++
	}
	assert.Equal(t, 2, c1)

	// Multi-table: chain via Tail() so the second reader picks up
	// the bytes the first reader buffered but didn't consume.
	tr2, err := pxf.NewDatasetReader(tr1.Tail())
	require.NoError(t, err)
	assert.Equal(t, "events.v1.Deleted", tr2.Type())
	c2 := 0
	for {
		_, err := tr2.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		c2++
	}
	assert.Equal(t, 1, c2)
}

// --- Streaming and materializing produce equivalent rows ---
//
// Spec requirement (draft §3.4.4): streaming and materializing APIs
// MUST produce byte-identical row sequences for the same input.
// "Byte-identical" can't be tested literally (the AST has Pos info
// that differs), but cell *Val types and values must match exactly.

func TestTableReader_EquivalentToMaterializingPath(t *testing.T) {
	in := `@dataset t.T (a, b, c)
("alpha", 1, true)
("beta", null, false)
(, , )
("gamma", 99, true)`

	// Materializing path.
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Datasets, 1)
	mat := doc.Datasets[0].Rows

	// Streaming path.
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	var stream []pxf.DatasetRow
	for {
		row, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		stream = append(stream, row)
	}

	require.Equal(t, len(mat), len(stream))
	for i := range mat {
		require.Equal(t, len(mat[i].Cells), len(stream[i].Cells), "row %d arity", i)
		for j := range mat[i].Cells {
			assert.Equalf(t, fmt.Sprintf("%T", mat[i].Cells[j]),
				fmt.Sprintf("%T", stream[i].Cells[j]),
				"row %d cell %d type", i, j)
		}
	}
}

// --- Header size limit ---

func TestTableReader_RejectsOversizedHeader(t *testing.T) {
	// Construct a header with an absurdly long type name (> 64 KiB).
	long := strings.Repeat("a", 70*1024)
	in := "@dataset " + long + ".T (col)\n(1)"
	_, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "header exceeds")
}

// --- Smoke: bytes.Buffer round-trip ---

func TestTableReader_BytesBufferSource(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(`@dataset T (a)
("x")
("y")`)
	tr, err := pxf.NewDatasetReader(&buf)
	require.NoError(t, err)
	count := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		count++
	}
	assert.Equal(t, 2, count)
}
