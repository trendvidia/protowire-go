// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Coverage of the direct-decode mirror in decode_fast.go for @table.
// The AST-tier tests in table_test.go cover Parse(); these drive
// UnmarshalFull() to exercise the parallel implementation that has to
// stay in sync.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- All cell variants via UnmarshalFull (covers consumeValue branches) ---

func TestUnmarshalFull_Table_AllCellVariants(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)

	in := []byte(`@table t.T (s, i, f, b, by, ts, d, e, n)
("hi", 42, 3.14, true, b"aGVsbG8=", 2026-05-11T10:00:00Z, 1h30m, ENUM_VAL, null)`)
	res, err := pxf.UnmarshalFull(in, msg)
	require.NoError(t, err)
	require.Len(t, res.Tables(), 1)
	cells := res.Tables()[0].Rows[0].Cells

	_, ok := cells[0].(*pxf.StringVal)
	assert.True(t, ok, "cell 0: StringVal")
	_, ok = cells[1].(*pxf.IntVal)
	assert.True(t, ok, "cell 1: IntVal")
	_, ok = cells[2].(*pxf.FloatVal)
	assert.True(t, ok, "cell 2: FloatVal")
	_, ok = cells[3].(*pxf.BoolVal)
	assert.True(t, ok, "cell 3: BoolVal")
	_, ok = cells[4].(*pxf.BytesVal)
	assert.True(t, ok, "cell 4: BytesVal")
	_, ok = cells[5].(*pxf.TimestampVal)
	assert.True(t, ok, "cell 5: TimestampVal")
	_, ok = cells[6].(*pxf.DurationVal)
	assert.True(t, ok, "cell 6: DurationVal")
	_, ok = cells[7].(*pxf.IdentVal)
	assert.True(t, ok, "cell 7: IdentVal")
	_, ok = cells[8].(*pxf.NullVal)
	assert.True(t, ok, "cell 8: NullVal")
}

// Raw-base64 bytes (no padding) exercise the second decode attempt
// inside consumeValue's BYTES branch.
func TestUnmarshalFull_Table_RawBase64Bytes(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)

	// `aGVsbG8` is "hello" base64-encoded without padding.
	in := []byte(`@table t.T (blob)
(b"aGVsbG8")`)
	res, err := pxf.UnmarshalFull(in, msg)
	require.NoError(t, err)
	require.Len(t, res.Tables()[0].Rows, 1)
	bv, ok := res.Tables()[0].Rows[0].Cells[0].(*pxf.BytesVal)
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), bv.Value)
}

// --- consumeTableDirective error paths via UnmarshalFull ---

func TestUnmarshalFull_Table_ErrorPaths(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	for _, c := range []struct {
		name   string
		in     string
		errSub string
	}{
		{"missing_type", `@table ( col )
( "x" )`, "expected row message type"},
		{"missing_lparen", `@table T col )
( "x" )`, "expected '('"},
		{"empty_column_list", `@table T ( )`, "at least one field name"},
		{"dotted_column", `@table T ( a.b )
( "x" )`, "dotted column paths"},
		{"bad_column_separator", `@table T ( a b )`, "expected ',' or ')'"},
		{"row_arity_short", `@table T ( a, b, c )
( "x", "y" )`, "2 cells, expected 3"},
		{"row_arity_long", `@table T ( a, b )
( "x", "y", "z" )`, "3 cells, expected 2"},
		{"row_unterminated", `@table T ( a )
( "x"`, "expected ',' or ')'"},
		{"list_cell", `@table T ( a, b )
( "x", ["y", "z"] )`, "list values"},
		{"block_cell", `@table T ( a, b )
( "x", { y = 1 } )`, "block values"},
		{"at_type_then_at_table", `@type X
@table T ( a )
( "x" )`, "@type"},
		{"at_table_then_at_type", `@table T ( a )
( "x" )
@type X`, "@type"},
		{"table_with_body_field", `@table T ( a )
( "x" )
extra = "stray"`, "top-level field entries"},
		{"invalid_bytes_cell", `@table T ( a )
( b"!!!not-base64!!!" )`, "invalid base64"},
		{"invalid_duration_cell", `@table T ( a )
( 9999999999999999999999h )`, "invalid duration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := dynamicpb.NewMessage(allTypes)
			_, err := pxf.UnmarshalFull([]byte(c.in), msg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.errSub)
		})
	}
}

// --- consumeRowCell empty cells via UnmarshalFull ---

func TestUnmarshalFull_Table_EmptyCellsViaFastPath(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)

	in := []byte(`@table t.T (a, b, c)
(, "x", )
("y", , "z")`)
	res, err := pxf.UnmarshalFull(in, msg)
	require.NoError(t, err)
	rows := res.Tables()[0].Rows
	require.Len(t, rows, 2)
	// Row 1: empty / "x" / empty
	assert.Nil(t, rows[0].Cells[0])
	assert.NotNil(t, rows[0].Cells[1])
	assert.Nil(t, rows[0].Cells[2])
	// Row 2: "y" / empty / "z"
	assert.NotNil(t, rows[1].Cells[0])
	assert.Nil(t, rows[1].Cells[1])
	assert.NotNil(t, rows[1].Cells[2])
}

// --- Schema check edge cases ---

func TestValidateFile_NilReturnsNil(t *testing.T) {
	assert.Nil(t, pxf.ValidateFile(nil))
}

func TestValidateDescriptor_NilReturnsNil(t *testing.T) {
	assert.Nil(t, pxf.ValidateDescriptor(nil))
}

func TestViolationKind_StringUnknown(t *testing.T) {
	assert.Equal(t, "unknown", pxf.ViolationKind(99).String())
}

// --- UnmarshalFull's reserved-name check fires (covers options.go branch) ---

func TestUnmarshalFull_RejectsReservedNameSchema(t *testing.T) {
	fd := compileFiles(t, map[string]string{
		"bad.proto": `syntax = "proto3";
package bad.v1;
message M {
  bool true = 1;
}`,
	})
	msg := dynamicpb.NewMessage(findMsg(t, fd, "M"))
	_, err := pxf.UnmarshalFull([]byte("true = true"), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved-name")
}

func TestUnmarshalFull_SkipValidateBypasses(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)
	// Clean schema; SkipValidate is exercised on the happy path.
	_, err := pxf.UnmarshalOptions{SkipValidate: true}.UnmarshalFull([]byte(`string_field = "x"`), msg)
	require.NoError(t, err)
}

// --- consumeDirective prefix-lookahead: an IDENT followed by '=' is a body
// field key, not a directive prefix. Driving this through UnmarshalFull
// covers the fast-path peekKind branch in consumeDirective.
func TestUnmarshalFull_NamedDirectiveLookahead(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)

	in := []byte(`@foo
string_field = "x"`)
	res, err := pxf.UnmarshalFull(in, msg)
	require.NoError(t, err)
	require.Len(t, res.Directives(), 1)
	assert.Equal(t, "foo", res.Directives()[0].Name)
	assert.Empty(t, res.Directives()[0].Prefixes,
		"the IDENT followed by '=' must be parsed as a body field key, not a directive prefix")
}

// Column list with a trailing comma (`(a,)`) lands on the inner "expected
// column field name" guard at the top of consumeTableDirective's loop —
// the comma advances past, then the next token is RPAREN, which isn't
// IDENT.
func TestUnmarshalFull_Table_TrailingCommaInColumnList(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)
	_, err := pxf.UnmarshalFull([]byte(`@table T (a,)`), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected column field name")
}
