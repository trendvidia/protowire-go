// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// A simple string MUST NOT hold an unescaped LF (draft § Strings: "Decoders
// MUST reject a simple-string containing a literal LF"; #143). The strict
// lexer used to keep the newline in the literal; every decode path now
// rejects it at the string, and ParseTolerant still recovers.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func TestSimpleStringRejectsRawNewline(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	const doc = "int32_field = 1\nstring_field = \"x\ny\"\n"
	const want = "2:16: newline in string literal"

	_, err := pxf.Parse([]byte(doc))
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), want), "Parse: %v", err)

	err = pxf.Unmarshal([]byte(doc), dynamicpb.NewMessage(desc))
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), want), "Unmarshal: %v", err)

	_, err = pxf.UnmarshalFull([]byte(doc), dynamicpb.NewMessage(desc))
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), want), "UnmarshalFull: %v", err)

	tr, err := pxf.NewDatasetReader(strings.NewReader("@dataset T (a)\n(\"x\ny\")\n"))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newline in string literal", "DatasetReader")
}

func TestSimpleStringNewlineSpellingsThatStayValid(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	for doc, want := range map[string]string{
		`string_field = "x\ny"`:                   "x\ny", // the escape
		"string_field = \"\"\"\nx\ny\n\"\"\"":     "x\ny", // a triple-quoted string
		"string_field = \"a\"\nint32_field = 2\n": "a",    // a newline after the string
	} {
		m := dynamicpb.NewMessage(desc)
		require.NoError(t, pxf.Unmarshal([]byte(doc), m), doc)
		assert.Equal(t, want, m.Get(desc.Fields().ByName("string_field")).String(), doc)
	}
}

func TestParseTolerantRecoversFromNewlineInString(t *testing.T) {
	doc, errs := pxf.ParseTolerant([]byte("string_field = \"abc\nint32_field = 2\n"))
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Msg, "unterminated string: ended at newline")
	require.Len(t, doc.Entries, 2, "the entry after the broken string still parses")
}
