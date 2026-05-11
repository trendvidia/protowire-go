// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Coverage follow-up for the v0.73.0 (#8) patch. Codecov flagged ~10
// lines unhit in encoding/pxf/parser.go and decode_fast.go after the
// initial coverage pass. This file fills the realistic gaps — the
// remaining unhit lines are lexer-pre-validated defensive fallbacks in
// consumeValue (bytes / timestamp / duration), structurally identical
// to the same dead branches in the long-pre-existing parser.parseValue
// and findMatchingBrace ("defensive belt-and-braces" per the comment in
// parser.go). The lexer guarantees BYTES tokens have valid base64,
// TIMESTAMP tokens parse as RFC3339(Nano), and DURATION tokens parse
// as time.ParseDuration — so the parser-side fallback paths are dead in
// practice. Mirroring is the right call; we don't aim for 100% of them.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- @type with no following identifier ---
//
// Covers parser.parseDocument's "expected type name after @type" branch
// (AST tier) and decode_fast.consumeDirectives' parallel branch.

func TestParse_AtTypeWithoutIdentifier_Rejects(t *testing.T) {
	// `@type <ident>` is accepted (<ident> becomes the TypeURL even when
	// it looks like a field name); the error path only fires when the
	// token after @type is not an IDENT at all.
	for _, c := range []struct {
		name string
		in   string
	}{
		{"eof", `@type`},
		{"lbrace", `@type {}`},
		{"equals", `@type = 1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := pxf.Parse([]byte(c.in))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "expected type name after @type")
		})
	}
}

func TestUnmarshalFull_AtTypeWithoutIdentifier_Rejects(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	msg := dynamicpb.NewMessage(allTypes)
	_, err := pxf.UnmarshalFull([]byte(`@type`), msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type name after @type")
}

// --- @table row's FIRST cell errors ---
//
// table_test.go's TestParseTable_ListCell_Rejects / _BlockCell_Rejects
// trip the SECOND-cell error path in parseTableRow (because the row
// starts with "AAPL"). The first-cell-error branch — the `if err != nil`
// immediately after parseRowCell on line 217 of parser.go — needs a
// row that starts with a forbidden value.

func TestParseTable_FirstCellList_Rejects(t *testing.T) {
	_, err := pxf.Parse([]byte(`@table T (a, b)
( [1, 2], "x" )`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list values")
}

func TestParseTable_FirstCellBlock_Rejects(t *testing.T) {
	_, err := pxf.Parse([]byte(`@table T (a, b)
( { x = 1 }, "x" )`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block values")
}
