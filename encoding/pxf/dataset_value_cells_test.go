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
