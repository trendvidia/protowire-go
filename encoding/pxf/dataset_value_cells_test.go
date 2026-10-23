// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Value cells (protowire#341, draft -01 §3.4.4): a @dataset cell may hold
// any value, lists and blocks included, and a non-empty cell in column c
// means exactly what `c = <value>` means in a body of the row type.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// The rows exercise a block on a message field, a list on a repeated
// scalar, a list of blocks on a repeated message, a block on a map, `{}`,
// `[]`, an empty cell, and `)` / `,` inside strings and a comment.
const valueCellsDoc = `@dataset test.v1.AllTypes (string_field, nested_field, repeated_string, repeated_nested, string_map, int32_field)
("r1", { name = "n)1," value = 7 }, ["a", "b)"], [{ name = "x" }, { name = "y" value = 2 }], { "k": "v)" }, 1)
("r2", {}, [], , /* ) , */ { }, 2)
("r3", , , , , 3)
`

// valueCellsBodies are the bodies each row stands for. Empty cells write
// nothing, so they are absent here too.
var valueCellsBodies = []string{
	`string_field = "r1"
nested_field = { name = "n)1," value = 7 }
repeated_string = ["a", "b)"]
repeated_nested = [{ name = "x" }, { name = "y" value = 2 }]
string_map = { "k": "v)" }
int32_field = 1`,
	`string_field = "r2"
nested_field = {}
repeated_string = []
string_map = { }
int32_field = 2`,
	`string_field = "r3"
int32_field = 3`,
}

func bodyMsg(t *testing.T, md protoreflect.MessageDescriptor, body string) *dynamicpb.Message {
	t.Helper()
	m := dynamicpb.NewMessage(md)
	require.NoError(t, pxf.Unmarshal([]byte(body), m))
	return m
}

// Each row binds to the same message as the body it stands for, through
// Parse + BindRow and through the streaming reader.
func TestValueCells_RowsBindAsTheirBodies(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")

	doc, err := pxf.Parse([]byte(valueCellsDoc))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	require.Len(t, ds.Rows, len(valueCellsBodies))
	for i, row := range ds.Rows {
		got := dynamicpb.NewMessage(allTypes)
		require.NoError(t, pxf.BindRow(got, ds.Columns, row), "row %d", i)
		assert.True(t, proto.Equal(bodyMsg(t, allTypes, valueCellsBodies[i]), got), "Parse row %d: got %v", i, got)
	}

	tr, err := pxf.NewDatasetReader(strings.NewReader(valueCellsDoc))
	require.NoError(t, err)
	n := 0
	for {
		got := dynamicpb.NewMessage(allTypes)
		err := tr.Scan(got)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "stream row %d", n)
		assert.True(t, proto.Equal(bodyMsg(t, allTypes, valueCellsBodies[n]), got), "stream row %d: got %v", n, got)
		n++
	}
	assert.Equal(t, len(valueCellsBodies), n)
}

// `{}` is a present, empty sub-message and an empty cell is absent: the
// distinction a block cell has to keep.
func TestValueCells_EmptyBlockIsPresent(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	nested := allTypes.Fields().ByName("nested_field")
	doc, err := pxf.Parse([]byte(valueCellsDoc))
	require.NoError(t, err)
	ds := doc.Datasets[0]

	r2 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, pxf.BindRow(r2, ds.Columns, ds.Rows[1]))
	assert.True(t, r2.Has(nested), "row 2 wrote {}: nested_field is present")

	r3 := dynamicpb.NewMessage(allTypes)
	require.NoError(t, pxf.BindRow(r3, ds.Columns, ds.Rows[2]))
	assert.False(t, r3.Has(nested), "row 3 left the cell empty: nested_field is absent")
}

// The direct decoder hands a list or block cell to the AST parser on its
// own lexer; it must resume at the token after the value, so the cells,
// rows and directives that follow still parse, and its rows must match
// Parse's.
func TestValueCells_FastPathResumesAfterTheCell(t *testing.T) {
	in := valueCellsDoc + `
@dataset test.v1.AllTypes (repeated_string, string_field)
(["z"], "after")
`
	msg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	res, err := pxf.UnmarshalFull([]byte(in), msg)
	require.NoError(t, err)
	fast := res.Datasets()
	require.Len(t, fast, 2)

	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Datasets, 2)
	for d := range fast {
		require.Len(t, fast[d].Rows, len(doc.Datasets[d].Rows))
		for r := range fast[d].Rows {
			fc, pc := fast[d].Rows[r].Cells, doc.Datasets[d].Rows[r].Cells
			require.Len(t, fc, len(pc))
			for c := range fc {
				if pc[c] == nil {
					assert.Nil(t, fc[c], "dataset %d row %d cell %d", d, r, c)
					continue
				}
				assert.Equal(t, string(pxf.FormatValue(pc[c])), string(pxf.FormatValue(fc[c])), "dataset %d row %d cell %d", d, r, c)
			}
		}
	}
	assert.Equal(t, "after", fast[1].Rows[0].Cells[1].(*pxf.StringVal).Value)
}

// A cell counts toward MaxNestingDepth like any value, on both paths.
func TestValueCells_NestingDepthIsEnforced(t *testing.T) {
	deep := strings.Repeat("[", pxf.MaxNestingDepth+1) + strings.Repeat("]", pxf.MaxNestingDepth+1)
	in := []byte("@dataset T (a)\n(" + deep + ")\n")

	_, err := pxf.Parse(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxNestingDepth")

	msg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	_, err = pxf.UnmarshalFull(in, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxNestingDepth")

	tr, err := pxf.NewDatasetReader(bytes.NewReader(in))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxNestingDepth")
}

// The reader's own limits apply to its cells, not the package constants:
// Next parses under its MaxNestingDepth and Scan binds under its
// MaxRepeatedCount. Each limit gets an accept row at the bound and a
// reject row one past it.
func TestValueCells_StreamHonoursItsLimits(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	read := func(o pxf.UnmarshalOptions, row string) error {
		in := "@dataset test.v1.AllTypes (repeated_string)\n" + row + "\n"
		tr, err := o.NewDatasetReader(strings.NewReader(in))
		require.NoError(t, err)
		return tr.Scan(dynamicpb.NewMessage(allTypes))
	}

	depth := pxf.UnmarshalOptions{MaxNestingDepth: 2}
	tr, err := depth.NewDatasetReader(strings.NewReader("@dataset T (a)\n([[\"a\"]])\n"))
	require.NoError(t, err)
	_, err = tr.Next()
	assert.NoError(t, err, "depth 2 is within MaxNestingDepth=2")
	tr, err = depth.NewDatasetReader(strings.NewReader("@dataset T (a)\n([[[\"a\"]]])\n"))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxNestingDepth=2")

	count := pxf.UnmarshalOptions{MaxRepeatedCount: 2}
	assert.NoError(t, read(count, `(["a", "b"])`), "2 elements are within MaxRepeatedCount=2")
	err = read(count, `(["a", "b", "c"])`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxRepeatedCount=2")
}

// Comments before a block cell are not the cell's: they stay out of its
// entries on both paths, and a comment written inside the cell stays in.
func TestValueCells_CommentsOutsideACellStayOutside(t *testing.T) {
	in := `@dataset test.v1.AllTypes (string_field, nested_field)
# before the row
( # before the cell
"x", { # inside
name = "y" })
`
	want := "{\n  # inside\n  name = \"y\"\n}"

	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	assert.Equal(t, want, string(pxf.FormatValue(doc.Datasets[0].Rows[0].Cells[1])), "Parse")

	res, err := pxf.UnmarshalFull([]byte(in), dynamicpb.NewMessage(msgDesc(t, "AllTypes")))
	require.NoError(t, err)
	fast := string(pxf.FormatValue(res.Datasets()[0].Rows[0].Cells[1]))
	assert.NotContains(t, fast, "before", "UnmarshalFull")
}

// A token the lexer rejects inside a list or block cell reports the
// lexer's diagnostic, with no expectation in front of it, on every path
// — as it does in a scalar cell and in every other value position.
func TestValueCells_IllegalTokenInsideACell(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	for _, tc := range []struct{ row, want string }{
		{`("x", 5seconds)`, "2:7: invalid duration: 5seconds"},
		{`("x", [5seconds])`, "2:8: invalid duration: 5seconds"},
		{`("x", { name = 5seconds })`, "2:16: invalid duration: 5seconds"},
	} {
		in := []byte("@dataset test.v1.AllTypes (string_field, nested_field)\n" + tc.row + "\n")
		_, err := pxf.Parse(in)
		require.Error(t, err, tc.row)
		assert.Equal(t, tc.want, err.Error(), "Parse %s", tc.row)

		_, err = pxf.UnmarshalFull(in, dynamicpb.NewMessage(allTypes))
		require.Error(t, err, tc.row)
		assert.Equal(t, tc.want, err.Error(), "UnmarshalFull %s", tc.row)
	}
}

// ParseTolerant closes an unclosed list or block cell at its row's ')',
// so one typo in a cell costs one error and the rows after it survive.
func TestValueCells_TolerantClosesAnUnclosedCellAtTheRow(t *testing.T) {
	for _, tc := range []struct {
		row  string
		want []string
	}{
		{`("x", [1, )`, []string{"expected ']', got )"}},
		{`("x", { name = "a" )`, []string{"unclosed block: expected '}' before ')'"}},
		{`("x", { name = )`, []string{`expected value, got ) (")")`, "unclosed block: expected '}' before ')'"}},
	} {
		in := "@dataset test.v1.AllTypes (string_field, repeated_int32)\n" + tc.row + "\n(\"y\", [2])\n(\"z\", [3])\n"
		doc, errs := pxf.ParseTolerant([]byte(in))
		var got []string
		for _, e := range errs {
			got = append(got, e.Msg)
		}
		assert.Equal(t, tc.want, got, tc.row)
		require.Len(t, doc.Datasets, 1, tc.row)
		rows := doc.Datasets[0].Rows
		require.Len(t, rows, 3, tc.row)
		assert.Equal(t, "z", rows[2].Cells[0].(*pxf.StringVal).Value, tc.row)
	}
}
