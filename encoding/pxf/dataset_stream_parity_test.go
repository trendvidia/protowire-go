// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"io"
	"reflect"
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
		{"list cell", head + "([1], 2)\n", true},
		{"nested paren in a cell", head + "((1), 2)\n", true},
		{"cut bytes literal", head + "(b\"QQ\nQQ\", 1)\n", true},

		{"clean", head + "(90, 120)\n", false},
		{"no rows", "@dataset T (a, b)\n", false},
		{"comments and blank lines", head + "# c\n\n// d\n/* e\n */ (90, 120) # f\n", false},
		{"comment inside a row", head + "(90, /* x */ 120)\n", false},
		{"empty and null cells", head + "(, null)\n(1,)\n", false},
		{"strings with parens and commas", "@dataset T (a, b)\n(\"(,)\", \"#\")\n", false},
		{"multi-line triple string", "@dataset T (a, b)\n(\"\"\"\nx)\n\"\"\", 1)\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, perr := pxf.Parse([]byte(tc.in))
			require.Equal(t, tc.wantErr, perr != nil, "Parse: %v", perr)
			for _, r := range []struct {
				name string
				src  io.Reader
			}{
				{"whole", strings.NewReader(tc.in)},
				{"one byte", iotest.OneByteReader(strings.NewReader(tc.in))},
			} {
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

// readAllRows streams every row of the first @dataset in src.
func readAllRows(src io.Reader) ([]pxf.DatasetRow, error) {
	tr, err := pxf.NewDatasetReader(src)
	if err != nil {
		return nil, err
	}
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

// An error is sticky: the rows after a malformed one are never yielded.
func TestDatasetReader_ErrorIsSticky(t *testing.T) {
	tr, err := pxf.NewDatasetReader(strings.NewReader("@dataset T (a, b)\n(1, 2\n(3, 4)\n"))
	require.NoError(t, err)
	_, err = tr.Next()
	require.Error(t, err)
	_, err2 := tr.Next()
	assert.Equal(t, err, err2)
}

// FuzzDatasetStreamParity holds the stream to Parse on arbitrary rows
// read one byte at a time, so every row boundary also falls on a read
// boundary. The stream may stop early only at a directive, where Parse
// goes on to read the next one; everything before that must agree.
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
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		in := "@dataset T (a, b)\n" + body
		doc, perr := pxf.Parse([]byte(in))

		tr, err := pxf.NewDatasetReader(iotest.OneByteReader(strings.NewReader(in)))
		if err != nil {
			t.Fatalf("header rejected: %v", err)
		}
		var rows []pxf.DatasetRow
		for {
			row, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if perr == nil || perr.Error() != err.Error() {
					t.Fatalf("stream error %q, Parse error %v", err, perr)
				}
				return
			}
			rows = append(rows, row)
		}
		rest, _ := io.ReadAll(tr.Tail())
		stop := len(in) - len(rest)
		if len(rest) > 0 && rest[0] != '@' {
			t.Fatalf("stream ended at %q, not at a directive", rest)
		}
		if perr != nil {
			var pe *pxf.Error
			if !errors.As(perr, &pe) || pe.Pos.Offset < stop {
				t.Fatalf("stream ended cleanly at offset %d, Parse failed before it: %v", stop, perr)
			}
			return
		}
		if !reflect.DeepEqual(doc.Datasets[0].Rows, rows) {
			t.Fatalf("rows differ:\nParse  %#v\nstream %#v", doc.Datasets[0].Rows, rows)
		}
	})
}
