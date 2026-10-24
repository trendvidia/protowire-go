// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Draft §3.4.4: streaming and materializing APIs MUST produce
// byte-identical row sequences for the same input. These inputs hold
// one @dataset each; the stream must yield exactly Parse's rows (with
// Parse's positions), or fail with exactly Parse's error. Issue #141:
// the stream used to end with io.EOF at a malformed or truncated row.
func TestDatasetReader_AgreesWithParse(t *testing.T) {
	const head = "@dataset T (min_bpm, max_bpm)\n(60, 90)\n"
	cases := []struct {
		name, in string
		wantErr  bool
	}{
		// The four inputs from #141.
		{"A row cut mid-number then valid rows", head + "(80, 1\n(90, 120)\n(95, 125)\n", true},
		{"B row cut at end of input", head + "(80, 1", true},
		{"C non-row content between rows", head + "xyz\n(90, 120)\n", true},
		{"D wrong arity", head + "(90)\n(95, 125)\n", true},

		{"open paren only", head + "(", true},
		{"cut after a comma", head + "(80,", true},
		{"cut inside a string cell", head + "(\"ab", true},
		{"string between rows", head + "\"s\"\n(90, 120)\n", true},
		{"block value between rows", head + "{ }\n", true},
		{"unterminated block comment at end", head + "/* never closed", true},
		{"unclosed list cell", head + "([1, 2)\n(3, 4)\n", true},
		{"unclosed block cell", head + "({ a = 1, 2)\n", true},
		{"nested paren in a cell", head + "((1), 2)\n", true},
		{"cut bytes literal", head + "(b\"QQ\nQQ\", 1)\n", true},
		{"bare @ at end", head + "@", true},
		{"bare @ between rows", head + "@\n(90, 120)\n", true},
		{"@ then a space", head + "@ x\n", true},
		{"cut bytes literal in a leading entry", "x = b\"QQ\nQQ\"\n@dataset T (a, b)\n(1, 2)\n", true},
		{"cut bytes literal in a leading directive", "@x b\"\n@dataset T (a, b)\n", true},
		{"character no token starts with", head + "(~, 1)\n", true},
		{"@proto body cut by a newline in a string", "@proto { string a = 1 [default = \"x\n\"]; }\n@dataset T (a, b)\n(1, 2)\n", true},
		{"@proto body never closed", "@proto { string a = 1;\n@dataset T (a, b)\n(1, 2)\n", true},

		{"clean", head + "(90, 120)\n", false},
		{"no rows", "@dataset T (a, b)\n", false},
		{"comments and blank lines", head + "# c\n\n// d\n/* e\n */ (90, 120) # f\n", false},
		{"comment inside a row", head + "(90, /* x */ 120)\n", false},
		{"empty and null cells", head + "(, null)\n(1,)\n", false},
		{"strings with parens and commas", "@dataset T (a, b)\n(\"(,)\", \"#\")\n", false},
		{"multi-line triple string", "@dataset T (a, b)\n(\"\"\"\nx)\n\"\"\", 1)\n", false},
		// Value cells (protowire#341): a cell may hold a list or a block.
		{"list cell", head + "([1], 2)\n", false},
		{"block and map cells", "@dataset T (a, b)\n({ name = \"x)\" n = [1, 2] }, { \"k,\": \"v)\" })\n(, [])\n", false},
		{"multi-line block cell", "@dataset T (a, b)\n({\n  name = \"x\"  # c)\n}, 1)\n", false},
		// Tokens the lexer decides by reading two bytes past a byte that
		// could end them: split after `1h.`, the '.' is read and the
		// digit after it is not.
		{"duration with a fraction after a unit", head + "(1h.5m, 1)\n", false},
		{"micro sign duration", head + "(1\xc2\xb5s, 1)\n", false},
		{"-inf", head + "(-inf, 1)\n", false},
		{"timestamp ended by a comment", head + "(2026-10-23T00:00:00Z// c\n, 1)\n", false},
		{"string escapes", head + "(\"\\x41\\101\\u00e9\\U0001F600\\\\\", 1)\n", false},
		// An @proto body is protobuf source, which Parse skips without
		// lexing it as PXF; a ';' in it is no PXF token.
		{"@proto body", "@proto { string a = 1; }\n@dataset T (a, b)\n(1, 2)\n", false},
		{"named @proto body", "@proto T { string a = 1; map<string, int32> m = 2; }\n@dataset T (a, b)\n(1, 2)\n", false},
		{"@proto name and body on later lines", "@proto\n# c\nT\n{ string a = 1; // )\n}\n@dataset T (a, b)\n(1, 2)\n", false},
		{"@proto body with literals and comments", "@proto { string a = 1 [default = \"})\"]; /* } */ # }\n bytes b = 2 [default = b\"}\"]; }\n@dataset T (a, b)\n(1, 2)\n", false},
		{"@proto source form", "@proto \"\"\"\nsyntax = \"proto3\";\n\"\"\"\n@dataset T (a, b)\n(1, 2)\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, perr := pxf.Parse([]byte(tc.in))
			require.Equal(t, tc.wantErr, perr != nil, "Parse: %v", perr)
			for _, r := range append([]namedReader{
				{"whole", strings.NewReader(tc.in)},
				{"one byte", iotest.OneByteReader(strings.NewReader(tc.in))},
			}, splitReaders(tc.in)...) {
				rows, err := readAllRows(r.src)
				if perr != nil {
					require.Error(t, err, "%s: stream accepted what Parse rejects: %v", r.name, perr)
					assert.Equal(t, perr.Error(), err.Error(), r.name)
					continue
				}
				require.NoError(t, err, r.name)
				assert.Equal(t, doc.Datasets[0].Rows, rows, r.name)
			}
		})
	}
}

type namedReader struct {
	name string
	src  io.Reader
}

// splitReaders returns, for every offset k inside in, a reader whose
// first read ends at k: each byte boundary in turn falls on a read
// boundary with a run of bytes before it, which one-byte reads, lexing
// again after every byte, never give.
func splitReaders(in string) []namedReader {
	rs := make([]namedReader, 0, len(in))
	for k := 1; k < len(in); k++ {
		rs = append(rs, namedReader{fmt.Sprintf("split at %d", k), io.MultiReader(strings.NewReader(in[:k]), strings.NewReader(in[k:]))})
	}
	return rs
}

// readAllRows streams every row of the first @dataset in src.
func readAllRows(src io.Reader) ([]pxf.DatasetRow, error) {
	tr, err := pxf.NewDatasetReader(src)
	if err != nil {
		return nil, err
	}
	return readRows(tr)
}

// readRows reads tr's rows to io.EOF.
func readRows(tr *pxf.DatasetReader) ([]pxf.DatasetRow, error) {
	var rows []pxf.DatasetRow
	for {
		row, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
}

// A following directive ends a table's rows without error, so the next
// table can be read from Tail (the one exception #141 keeps).
func TestDatasetReader_DirectiveEndsRows(t *testing.T) {
	in := "@dataset A (x)\n(1)\n(2)\n# between\n@dataset B (y)\n(3)\n"
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)
	for range 2 {
		_, err := tr.Next()
		require.NoError(t, err)
	}
	_, err = tr.Next()
	require.ErrorIs(t, err, io.EOF)

	tr2, err := pxf.NewDatasetReader(tr.Tail())
	require.NoError(t, err)
	assert.Equal(t, "B", tr2.Type())
	row, err := tr2.Next()
	require.NoError(t, err)
	assert.Equal(t, "3", row.Cells[0].(*pxf.IntVal).Raw)
	_, err = tr2.Next()
	require.ErrorIs(t, err, io.EOF)
}

// A reader chained on Tail reports the next table's rows and errors at
// their document positions, as Parse does.
func TestDatasetReader_TailKeepsDocumentPositions(t *testing.T) {
	const two = "@dataset A (x)\n(1)\n\n@x {}\n@proto { message M {} }\n@dataset B (y)\n(3)\n"
	for _, tc := range []struct{ name, in string }{
		{"rows", two},
		{"error", two + "(4\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, perr := pxf.Parse([]byte(tc.in))
			for _, r := range []struct {
				name string
				src  io.Reader
			}{
				{"whole", strings.NewReader(tc.in)},
				{"one byte", iotest.OneByteReader(strings.NewReader(tc.in))},
			} {
				tr, err := pxf.NewDatasetReader(r.src)
				require.NoError(t, err, r.name)
				for {
					if _, err := tr.Next(); err != nil {
						require.ErrorIs(t, err, io.EOF, r.name)
						break
					}
				}
				tr2, err := pxf.NewDatasetReader(tr.Tail())
				require.NoError(t, err, r.name)
				rows, err := readRows(tr2)
				if perr != nil {
					require.Error(t, err, r.name)
					assert.Equal(t, perr.Error(), err.Error(), r.name)
					continue
				}
				require.NoError(t, err, r.name)
				assert.Equal(t, doc.Datasets[1].Rows, rows, r.name)
				assert.Equal(t, doc.Directives, tr2.Directives(), r.name)
				assert.Equal(t, doc.Datasets[1].Columns, tr2.Columns(), r.name)
			}
		})
	}
}

// The stream parses the header and every row under the options' limits,
// as ParseOptions.Parse does under the same limits.
func TestDatasetReader_HonoursLimits(t *testing.T) {
	const head = "@dataset T (a)\n"
	cases := []struct {
		name, in string
		wantErr  bool
	}{
		{"bytes literal in a row over the limit", head + "(b\"QUFBQUFBQUFBQUFB\")\n", true},
		{"bytes literal in a row within the limit", head + "(b\"QUFB\")\n", false},
		{"bytes literal in a leading directive over the limit", "@x { k = b\"QUFBQUFBQUFBQUFB\" }\n" + head, true},
		{"nesting in a leading directive over the limit", "@x { a { b { c {} } } }\n" + head, true},
	}
	popts := pxf.ParseOptions{MaxBytesLiteralLength: 4, MaxNestingDepth: 2}
	uopts := pxf.UnmarshalOptions{MaxBytesLiteralLength: 4, MaxNestingDepth: 2}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, perr := popts.Parse([]byte(tc.in))
			require.Equal(t, tc.wantErr, perr != nil, "Parse: %v", perr)
			tr, err := uopts.NewDatasetReader(strings.NewReader(tc.in))
			var rows []pxf.DatasetRow
			if err == nil {
				rows, err = readRows(tr)
			}
			if perr != nil {
				require.Error(t, err, "stream accepted what Parse rejects: %v", perr)
				assert.Equal(t, perr.Error(), err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, doc.Datasets[0].Rows, rows)
		})
	}
}

// An error is sticky: the rows after a malformed one are never yielded.
func TestDatasetReader_ErrorIsSticky(t *testing.T) {
	tr, err := pxf.NewDatasetReader(strings.NewReader("@dataset T (a, b)\n(1, 2\n(3, 4)\n"))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	_, err2 := tr.Next()
	assert.Equal(t, err, err2)
}

// directiveStart matches the start of a directive token, '@' and a
// name, after the newlines and comments that precede it: the reader
// leaves those in Tail, since Parse gives them to the directive (#149).
var directiveStart = regexp.MustCompile(`^(?:\s|#[^\n]*|//[^\n]*|/\*(?s:.*?)\*/)*@[A-Za-z0-9_.]`)

// FuzzDatasetStreamParity holds the stream to Parse on arbitrary rows
// read one byte at a time, so every row boundary also falls on a read
// boundary, and split in two at every offset (checkStreamAgrees).
func FuzzDatasetStreamParity(f *testing.F) {
	for _, s := range []string{
		"(1, 2)\n(3, 4)\n",
		"(80, 1\n(90, 120)\n",
		"(80, 1",
		"xyz\n(1, 2)\n",
		"(1)\n",
		"(\"a)\", b\"QQ==\")\n# c\n(, null)\n",
		"(\"\"\"\n)\n\"\"\", 1)\n/* x */",
		"(1, 2)\n@dataset U (z)\n(9)\n",
		"(1, [2])\n",
		"(1, {})\n",
		"(\"x\ny\", 1)\n",
		"(1, 2)\n@",
		"(1, 2)\n@ x\n",
		"(1h.5m, 1)\n",
		"(1\xc2\xb5s, 1)\n",
		"(-inf, 2)\n",
		"(~, 1)\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		checkStreamAgrees(t, "@dataset T (a, b)\n"+body)
	})
}

// checkStreamAgrees holds the stream to Parse on in, read one byte at a
// time and, for inputs small enough, split in two at every offset. The
// stream reads the first table Parse reads, or fails with Parse's error;
// it may stop early only at a directive, where Parse goes on to read the
// next one, and everything before that must agree.
func checkStreamAgrees(t *testing.T, in string) {
	doc, perr := pxf.Parse([]byte(in))
	rs := []namedReader{{"one byte", iotest.OneByteReader(strings.NewReader(in))}}
	if len(in) <= 512 {
		rs = append(rs, splitReaders(in)...)
	}
	for _, r := range rs {
		tr, err := pxf.NewDatasetReader(r.src)
		if err != nil {
			if perr == nil || perr.Error() != err.Error() {
				t.Fatalf("%s: header error %q, Parse error %v", r.name, err, perr)
			}
			continue
		}
		if perr == nil && (tr.Type() != doc.Datasets[0].Type || !reflect.DeepEqual(tr.Columns(), doc.Datasets[0].Columns)) {
			t.Fatalf("%s: header %s %v, Parse %s %v", r.name, tr.Type(), tr.Columns(), doc.Datasets[0].Type, doc.Datasets[0].Columns)
		}
		rows, err := readRows(tr)
		if err != nil {
			if perr == nil || perr.Error() != err.Error() {
				t.Fatalf("%s: stream error %q, Parse error %v", r.name, err, perr)
			}
			continue
		}
		rest, _ := io.ReadAll(tr.Tail())
		stop := len(in) - len(rest)
		if len(rest) > 0 && !directiveStart.Match(rest) {
			t.Fatalf("%s: stream ended at %q, not at a directive", r.name, rest)
		}
		if perr != nil {
			var pe *pxf.Error
			if !errors.As(perr, &pe) || pe.Pos.Offset < stop {
				t.Fatalf("%s: stream ended cleanly at offset %d, Parse failed before it: %v", r.name, stop, perr)
			}
			continue
		}
		if !reflect.DeepEqual(doc.Datasets[0].Rows, rows) {
			t.Fatalf("%s: rows differ:\nParse  %#v\nstream %#v", r.name, doc.Datasets[0].Rows, rows)
		}
	}
}

// FuzzDatasetHeaderParity holds the stream to Parse on arbitrary content
// before a dataset (checkStreamAgrees): leading directives, @proto
// bodies (raw protobuf source, which Parse does not lex as PXF) and
// entries.
func FuzzDatasetHeaderParity(f *testing.F) {
	for _, s := range []string{
		"",
		"# c",
		"@type T",
		"@x { k = 1 }",
		"@x a b { k = [1, 2] }",
		"@proto { string a = 1; }",
		"@proto T { string a = 1; }",
		"@proto\nT\n{ x }",
		"@proto { \"}\" }",
		"@proto { /* } */ }",
		"@proto { b\"}\" }",
		"@proto { a\n",
		"@proto \"\"\"syntax\"\"\"",
		"x = 1h.5m",
		"x = 1\xc2\xb5s",
		"@dataset X (q)\n(1)",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, header string) {
		checkStreamAgrees(t, header+"\n@dataset T (a, b)\n(1, 2)\n")
	})
}

// A reader chained on Tail sees the directives between two tables as
// Parse does, with the comments before each one as its LeadingComments
// (#149): the trailing comment on the last row's line, and the lines
// before the directive. The reader used to consume them while looking
// for the next row.
func TestDatasetReader_TailKeepsDirectiveComments(t *testing.T) {
	for _, in := range []string{
		// The issue's input.
		"@dataset A (x)\n(1) # a\n# b\n@x {}\n@dataset B (y)\n(3)\n",
		"@dataset A (x)\n(1)\n/* block\n */ // line\n@x {}\n@y { k = 1 }\n@dataset B (y)\n(3)\n",
		"@dataset A (x)\n(1)\n\n# only before the dataset\n@dataset B (y)\n(3)\n",
		"@dataset A (x)\n# no rows\n@x {}\n@dataset B (y)\n",
	} {
		doc, err := pxf.Parse([]byte(in))
		require.NoError(t, err, in)
		// Parse's directives between the two tables: all of them here,
		// since nothing precedes table A.
		want := doc.Directives

		for _, oneByte := range []bool{false, true} {
			var src io.Reader = strings.NewReader(in)
			if oneByte {
				src = iotest.OneByteReader(src)
			}
			tr, err := pxf.NewDatasetReader(src)
			require.NoError(t, err)
			for {
				if _, err := tr.Next(); err != nil {
					require.ErrorIs(t, err, io.EOF)
					break
				}
			}
			tr2, err := pxf.NewDatasetReader(tr.Tail())
			require.NoError(t, err, in)
			assert.Equal(t, "B", tr2.Type())
			if len(want) == 0 {
				assert.Empty(t, tr2.Directives(), "%q one-byte=%v", in, oneByte)
				continue
			}
			assert.Equal(t, want, tr2.Directives(), "%q one-byte=%v", in, oneByte)
		}
	}
}
