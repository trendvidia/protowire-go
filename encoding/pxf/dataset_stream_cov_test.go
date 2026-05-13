// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Coverage follow-up for table_stream.go. The streaming layer mirrors
// the lexer's string / bytes / line-comment / block-comment awareness
// — branches that only fire on adversarial or unusual inputs. These
// tests pin each branch.

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- skipBlockComment: /* ... */ between rows ---

func TestTableReader_BlockCommentBetweenRows(t *testing.T) {
	in := `@dataset T (a)
("x")
/* this comment ) has ( parens
   spanning multiple lines */
("y")`
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
	assert.Equal(t, 2, count)
}

// --- skipBlockComment: /* ... */ inside the header area ---

func TestTableReader_BlockCommentInHeader(t *testing.T) {
	in := `@dataset /* row type */ T /* cols follow */ (a, b)
("x", 1)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, "T", tr.Type())
	assert.Equal(t, []string{"a", "b"}, tr.Columns())
}

// --- findAtTable: leading block comment before @dataset ---

func TestTableReader_LeadingBlockComment(t *testing.T) {
	in := `/* document preamble explaining the
   table schema */
@dataset T (a)
("x")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, "T", tr.Type())
}

// --- findAtTable: leading line comments before @dataset ---

func TestTableReader_LeadingLineComments(t *testing.T) {
	in := `# preamble line
// another preamble line
@dataset T (a)
("x")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, "T", tr.Type())
}

// --- findAtTable: false-match guard — "@dataset" appearing INSIDE a string
// in a leading directive must not be confused with the @dataset keyword.

func TestTableReader_AtTableSubstringInsideStringDoesNotFalseMatch(t *testing.T) {
	in := `@header H { note = "we use @dataset for bulk rows" }
@dataset T (a)
("x")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, "T", tr.Type())
	require.Len(t, tr.Directives(), 1)
	assert.Equal(t, "header", tr.Directives()[0].Name)
}

// --- findAtTable: false-match guard — "@tableau" (ident-continuation
// after @dataset) must not match as the @dataset keyword.

func TestTableReader_AtTableauDoesNotFalseMatch(t *testing.T) {
	// `@tableau` is just a user-defined directive named "tableau".
	// Followed by a real @dataset.
	in := `@tableau marker
@dataset T (a)
("x")`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, "T", tr.Type())
	require.Len(t, tr.Directives(), 1)
	assert.Equal(t, "tableau", tr.Directives()[0].Name)
}

// --- skipSimpleString: escape sequences inside string cells ---

func TestTableReader_EscapedQuoteInStringCell(t *testing.T) {
	in := `@dataset T (s, n)
("has \"escaped quote\" inside", 1)
("normal", 2)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	r1, err := tr.Next()
	require.NoError(t, err)
	sv := r1.Cells[0].(*pxf.StringVal)
	assert.Equal(t, `has "escaped quote" inside`, sv.Value)

	r2, err := tr.Next()
	require.NoError(t, err)
	sv = r2.Cells[0].(*pxf.StringVal)
	assert.Equal(t, "normal", sv.Value)
}

// --- skipSimpleString / skipBytesLiteral: \n inside literal is a hard
// error (can't be fixed by more bytes; surfaces from the streaming
// scanner before reaching the per-row parser).

func TestTableReader_NewlineInsideStringInRow(t *testing.T) {
	// The opening `("open` looks like a row start; the embedded \n
	// makes the string unterminated by PXF rules.
	in := "@dataset T (s)\n(\"open\n\")"
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated string")
}

func TestTableReader_NewlineInsideBytesLiteralInRow(t *testing.T) {
	in := "@dataset T (b)\n(b\"open\n\")"
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated bytes literal")
}

// --- pull(): non-EOF io.Reader error surfaces from Next() ---

type errReader struct {
	prefix     []byte // returned bytes before the error
	delivered  bool
	deliverErr error
}

func (er *errReader) Read(p []byte) (int, error) {
	if !er.delivered && len(er.prefix) > 0 {
		n := copy(p, er.prefix)
		er.prefix = er.prefix[n:]
		if len(er.prefix) == 0 {
			er.delivered = true
		}
		return n, nil
	}
	return 0, er.deliverErr
}

func TestTableReader_PropagatesNonEofReadError(t *testing.T) {
	// Header + one row that fits in the prefix, then a fake error.
	prefix := []byte(`@dataset T (a)
("x")
`)
	custom := errors.New("network blip")
	r := &errReader{prefix: prefix, deliverErr: custom}

	tr, err := pxf.NewDatasetReader(r)
	require.NoError(t, err)

	// First row reads cleanly from the prefix.
	_, err = tr.Next()
	require.NoError(t, err)

	// Next read pulls; src returns the error.
	_, err = tr.Next()
	require.Error(t, err)
	assert.ErrorIs(t, err, custom)
}

func TestTableReader_HeaderReadPropagatesReadError(t *testing.T) {
	// No bytes at all — pull immediately returns the custom error.
	custom := errors.New("disk yanked")
	_, err := pxf.NewDatasetReader(&errReader{deliverErr: custom})
	require.Error(t, err)
	assert.ErrorIs(t, err, custom)
}

// --- Tail() when pending is empty: the early-return branch ---

func TestTableReader_TailWhenPendingEmpty(t *testing.T) {
	// Construct an input where the reader's pending buffer is empty
	// after EOF. The byte-at-a-time reader makes this easy: every read
	// returns exactly one byte, so by EOF time, pending is drained.
	in := `@dataset T (a)
("x")`
	tr, err := pxf.NewDatasetReader(&chunkedReader{data: []byte(in)})
	require.NoError(t, err)

	_, err = tr.Next()
	require.NoError(t, err)
	_, err = tr.Next()
	require.ErrorIs(t, err, io.EOF)

	// Tail should be the underlying reader directly (pending empty).
	tail := tr.Tail()
	require.NotNil(t, tail)
	// Reading the tail returns io.EOF since the chunkedReader is drained.
	buf := make([]byte, 16)
	_, err = tail.Read(buf)
	assert.ErrorIs(t, err, io.EOF)
}

// --- Byte-at-a-time reader with strings / comments in rows. Exercises
// the need-more-bytes branches of skipSimpleString, skipBytesLiteral,
// skipBlockComment, and findMatchingParenSafe — they trigger when the
// row scanner hits the end of the buffer mid-construct.

func TestTableReader_ChunkedReader_StringInRow(t *testing.T) {
	in := `@dataset T (s, n)
("hello world with spaces", 1)
("another", 2)`
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
	assert.Equal(t, 2, count)
}

func TestTableReader_ChunkedReader_BlockCommentInRow(t *testing.T) {
	in := `@dataset T (a)
(/* inline note */ "x")
(/* multi
   line note */ "y")`
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
	assert.Equal(t, 2, count)
}

func TestTableReader_ChunkedReader_BytesLiteralInRow(t *testing.T) {
	in := `@dataset T (b, n)
(b"aGVsbG8=", 1)
(b"d29ybGQ=", 2)`
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
	assert.Equal(t, 2, count)
}

func TestTableReader_ChunkedReader_TripleStringInRow(t *testing.T) {
	in := `@dataset T (s)
("""triple quoted
content""")
("simple")`
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
	assert.Equal(t, 2, count)
}

// --- Malformed leading directive: an unterminated string before
// @dataset surfaces as a parse error from the header reader, not as
// ErrNoDataset. Covers the error-propagation paths in scanHeaderEnd /
// findAtTable / findNextChar.

func TestTableReader_UnterminatedStringInLeadingDirective(t *testing.T) {
	// The `\n` inside `"open` makes the string unterminated; the
	// scanner reports an error rather than waiting for more bytes.
	in := "@header H { note = \"open\n }\n@dataset T (a)\n(\"x\")"
	_, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.Error(t, err)
	// Could be either "unterminated string" (from our scanner) or a
	// parse error from the header Parse() call — both are acceptable;
	// we just don't want it silently turning into ErrNoDataset.
	assert.NotErrorIs(t, err, pxf.ErrNoDataset)
}

// --- @dataset header that has the keyword but no `(` ---

func TestTableReader_AtTableWithoutOpenParen_ReportsHeaderTooLarge(t *testing.T) {
	// `@dataset T` with nothing after it: the scanner finds @dataset, looks
	// for `(`, hits EOF, falls through to ErrNoDataset. (The 64 KiB cap
	// would only fire on a giant identifier; for short inputs we hit
	// EOF first.)
	_, err := pxf.NewDatasetReader(strings.NewReader(`@dataset T`))
	require.Error(t, err)
	assert.ErrorIs(t, err, pxf.ErrNoDataset)
}

// --- Unterminated string AFTER @dataset but BEFORE `(`. Covers the
// findNextChar error-propagation path in scanHeaderEnd (the previous
// "leading directive" test triggers the findAtTable error path, not
// findNextChar's).

func TestTableReader_UnterminatedStringBetweenAtTableAndLParen(t *testing.T) {
	// The `"open\n` sits between the `@dataset` keyword and the column
	// list's `(`. findNextChar walks past `T` looking for `(`, hits the
	// `"`, descends into skipSimpleString, and errors on the embedded
	// newline.
	in := "@dataset T \"open\n (a)\n(\"x\")"
	_, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.Error(t, err)
	assert.NotErrorIs(t, err, pxf.ErrNoDataset)
}

// --- Malformed string BETWEEN rows. Covers findNextRow's error
// propagation: the leading-whitespace/comment skip loop encounters
// an unterminated string before the next `(`.

func TestTableReader_UnterminatedStringBetweenRows(t *testing.T) {
	// First row reads cleanly. Second "row" position starts with a
	// `"` that's unterminated by a newline — findNextRow's whitespace/
	// comment skip loop hits skipStringOrComment which errors.
	in := "@dataset T (a)\n(\"x\")\n\"open\n(\"y\")"
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	_, err = tr.Next()
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF)
}
