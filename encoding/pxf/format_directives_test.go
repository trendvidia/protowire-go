// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// FormatDocument keeps every directive, with its dataset rows, in document
// order (trendvidia/protowire#558, #262). The expected texts are the ones
// protowire-typescript's format writes for the same sources.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// formatRoundTrip formats src, checks that the output parses to the same
// directives and is a fixed point of formatting, and returns it.
func formatRoundTrip(t *testing.T, src string) string {
	t.Helper()
	doc, err := pxf.Parse([]byte(src))
	require.NoError(t, err)
	out := pxf.FormatDocument(doc)
	again, err := pxf.Parse(out)
	require.NoError(t, err, "%s", out)
	assert.Equal(t, directiveShape(doc), directiveShape(again), "%s", out)
	assert.Equal(t, string(out), string(pxf.FormatDocument(again)), "formatting is a fixed point")
	return string(out)
}

// directiveShape is what a document's directives say, without positions,
// in the order the formatter writes them.
func directiveShape(doc *pxf.Document) []any {
	var out []any
	out = append(out, doc.TypeURL)
	for _, d := range doc.Directives {
		out = append(out, "@"+d.Name, d.Prefixes, string(d.Body), d.Body != nil)
	}
	for _, d := range doc.Datasets {
		out = append(out, "@dataset", d.Type, d.Columns)
		for _, r := range d.Rows {
			cells := make([]string, len(r.Cells))
			for i, c := range r.Cells {
				if c == nil {
					cells[i] = "<absent>"
				} else {
					cells[i] = string(pxf.FormatValue(c))
				}
			}
			out = append(out, cells)
		}
	}
	for _, d := range doc.Protos {
		out = append(out, "@proto", d.Shape, d.TypeName, string(d.Body))
	}
	out = append(out, len(doc.Entries))
	return out
}

func TestFormatDocument_Dataset(t *testing.T) {
	// The issue's document formatted to nothing.
	out := formatRoundTrip(t, "@dataset trades.v1.Trade (id, px)\n(1, 2.5)\n(2, 3.5)\n")
	assert.Equal(t, "@dataset trades.v1.Trade (id, px)\n(1, 2.5)\n(2, 3.5)\n", out)

	// An empty cell is an absent field and null a present one; both stay.
	out = formatRoundTrip(t, "@dataset trades.v1.Trade (id, px, note)\n(1, 2.5, )\n(2, , null)\n")
	assert.Equal(t, "@dataset trades.v1.Trade (id, px, note)\n(1, 2.5, )\n(2, , null)\n", out)
}

func TestFormatDocument_EntryAndNamedDirective(t *testing.T) {
	out := formatRoundTrip(t, "@type a.v1.M\n@entry x { a = 1 }\n@header h.v1.H\n\nname = \"n\"\n")
	assert.Equal(t, "@type a.v1.M\n@entry x { a = 1 }\n@header h.v1.H\n\nname = \"n\"\n", out)
}

func TestFormatDocument_ProtoShapes(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"@proto { string id = 1; }\n@dataset (id)\n(\"a\")\n", ""},
		{"@proto a.v1.R { string id = 1; }\n@dataset a.v1.R (id)\n(\"a\")\n", ""},
		{"@proto \"\"\"syntax = \"proto3\"; message R { string id = 1; }\"\"\"\n@dataset R (id)\n(\"a\")\n", ""},
		// @type moves to the front: it binds no @proto, so this changes nothing.
		{"@proto b\"CgA=\"\n@type a.v1.M\n\nx = 1\n", "@type a.v1.M\n@proto b\"CgA=\"\n\nx = 1\n"},
	} {
		t.Run(tc.src, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.src
			}
			assert.Equal(t, want, formatRoundTrip(t, tc.src))
		})
	}
}

// An anonymous @proto binds the next typed directive, so a @proto written
// after a dataset in the source stays after it.
func TestFormatDocument_DirectivesKeepSourceOrder(t *testing.T) {
	src := "@dataset t.v1.T (id)\n(1)\n@proto { string id = 1; }\n@dataset (id)\n(\"a\")\n"
	assert.Equal(t, src, formatRoundTrip(t, src))
}

func TestFormatDocument_DirectiveComments(t *testing.T) {
	out := formatRoundTrip(t, "# about the rows\n@dataset t.v1.T (id)\n(1)\n# second\n@dataset t.v1.U (id)\n(2)\n")
	assert.Contains(t, out, "# about the rows\n@dataset t.v1.T (id)")
	assert.Contains(t, out, "# second\n@dataset t.v1.U (id)")
}

// A document whose only directive is @type formats as it always has.
func TestFormatDocument_OnlyTypeUnchanged(t *testing.T) {
	doc, err := pxf.Parse([]byte("# c\n@type a.v1.M\n\nx = 1\n"))
	require.NoError(t, err)
	assert.Equal(t, "@type a.v1.M\n\n# c\nx = 1\n", string(pxf.FormatDocument(doc)))
}
