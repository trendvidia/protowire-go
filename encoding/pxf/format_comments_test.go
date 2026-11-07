// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Comments that precede no entry — at the end of a document or of a block,
// and around dataset rows — survive Parse → FormatDocument in place (#279).
// Before, the parser attached a comment only to the entry after it, so a
// comment with none after it was dropped, or carried out of its block onto
// the next entry.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func TestFormatDocument_CommentsAtTheEnd(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// The issue's three documents.
		{"after an entry", "@type a.v1.M\nname = \"n\"\n# end of doc\n",
			"@type a.v1.M\n\nname = \"n\"\n# end of doc\n"},
		{"after a directive", "@type a.v1.M\n@flag\n# end of doc\n", ""},
		{"after a row", "@dataset t.v1.T (id)\n(1)\n# end of doc\n", ""},
		{"a run of comments", "@type a.v1.M\nname = \"n\" # inline\n# a\n// b\n/* c */\n",
			"@type a.v1.M\n\nname = \"n\" # inline\n# a\n// b\n/* c */\n"},
		{"after @type alone", "@type a.v1.M\n# only\n", "@type a.v1.M\n\n# only\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.src
			}
			assert.Equal(t, want, formatRoundTrip(t, tc.src))
		})
	}

	doc, err := pxf.Parse([]byte("@type a.v1.M\nname = \"n\"\n# end of doc\n"))
	require.NoError(t, err)
	require.Len(t, doc.TrailingComments, 1)
	assert.Equal(t, "# end of doc", doc.TrailingComments[0].Text)
	assert.Empty(t, doc.Entries[0].(*pxf.Assignment).LeadingComments)
}

func TestFormatDocument_CommentsBeforeClosingBrace(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		// Before, this comment moved out of m, onto y.
		{"block then entry", "@type a.v1.M\n\nm {\n  k: 1\n  # end of m\n}\ny = 2\n"},
		{"last block", "@type a.v1.M\n\nb {\n  x = 1\n  # end of b\n}\n"},
		{"empty block", "@type a.v1.M\n\nb {\n  # empty\n}\n"},
		{"block value", "@type a.v1.M\n\nb = {\n  x = 1\n  # end of b\n}\n"},
		{"nested", "@type a.v1.M\n\na {\n  b {\n    x = 1\n    # end of b\n  }\n  # end of a\n}\n"},
		{"after the brace stays after", "@type a.v1.M\n\na {\n  x = 1\n}\n# about y\ny = 2\n"},
		{"block cell", "@dataset t.v1.T (id, m)\n(1, {\n  x = 1\n  # end of m\n})\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.src, formatRoundTrip(t, tc.src))
		})
	}

	doc, err := pxf.Parse([]byte("@type a.v1.M\nm {\n  k: 1\n  # end of m\n}\ny = 2\n"))
	require.NoError(t, err)
	m := doc.Entries[0].(*pxf.Block)
	require.Len(t, m.TrailingComments, 1)
	assert.Equal(t, "# end of m", m.TrailingComments[0].Text)
	assert.Empty(t, doc.Entries[1].(*pxf.Assignment).LeadingComments)
}

func TestFormatDocument_DatasetRowComments(t *testing.T) {
	src := "@dataset t.v1.T (id)\n# first\n(1)\n# between\n(2)\n# end\n"
	assert.Equal(t, src, formatRoundTrip(t, src))

	// A row is written on one line, so a comment inside it moves before it.
	out := formatRoundTrip(t, "@dataset t.v1.T (a, b)\n(1, # one\n 2)\n(3, 4)\n")
	assert.Equal(t, "@dataset t.v1.T (a, b)\n# one\n(1, 2)\n(3, 4)\n", out)

	doc, err := pxf.Parse([]byte(src))
	require.NoError(t, err)
	rows := doc.Datasets[0].Rows
	require.Len(t, rows, 2)
	require.Len(t, rows[1].LeadingComments, 1)
	assert.Equal(t, "# between", rows[1].LeadingComments[0].Text)
	require.Len(t, doc.TrailingComments, 1)
	assert.Equal(t, "# end", doc.TrailingComments[0].Text)
}

// DatasetReader files a row's comments as Parse does.
func TestDatasetReader_RowComments(t *testing.T) {
	for _, body := range []string{
		"# first\n(1, 2)\n# between\n/* b */\n(3, 4)\n# end\n",
		"(1, # one\n 2)\n(3, 4) # after\n(5, 6)\n",
		"(1, 2)\n# about U\n@dataset U (z)\n(9)\n",
	} {
		t.Run(body, func(t *testing.T) {
			checkStreamAgrees(t, "@dataset T (a, b)\n"+body)
		})
	}

	r, err := pxf.NewDatasetReader(strings.NewReader("@dataset T (a, b)\n(1, 2)\n# between\n(3, 4)\n"))
	require.NoError(t, err)
	_, err = r.Next()
	require.NoError(t, err)
	row, err := r.Next()
	require.NoError(t, err)
	require.Len(t, row.LeadingComments, 1)
	assert.Equal(t, "# between", row.LeadingComments[0].Text)
}

// CanonicalizeKeyed keeps a block's closing comments when it rewrites the
// block, and the comments of a redundant key assignment it drops.
func TestCanonicalizeKeyed_KeepsClosingComments(t *testing.T) {
	fd := compileKeyedProto(t)
	for _, tc := range []struct{ name, src, want string }{
		{"dropped last key assignment",
			"@type keyed.v1.Node\n\nchildren {\n  greeting {\n    type = \"Label\"\n    # about the key\n    id = \"greeting\"\n  }\n  # end of children\n}\n",
			"@type keyed.v1.Node\n\nchildren {\n  greeting {\n    type = \"Label\"\n    # about the key\n  }\n  # end of children\n}\n"},
		{"assignment form",
			"@type keyed.v1.Node\n\nchildren = {\n  greeting = {\n    type = \"Label\"\n    # end of greeting\n  }\n  # end of children\n}\n",
			"@type keyed.v1.Node\n\nchildren {\n  greeting {\n    type = \"Label\"\n    # end of greeting\n  }\n  # end of children\n}\n"},
		{"anonymous list",
			"@type keyed.v1.Node\n\nchildren = [\n  {\n    type = \"L\"\n    # about the key\n    id = \"g\"\n  },\n  {\n    id = \"h\"\n    # end of h\n  }\n]\n",
			"@type keyed.v1.Node\n\nchildren {\n  g {\n    type = \"L\"\n    # about the key\n  }\n  h {\n    # end of h\n  }\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc, doc := keyedFixtureDesc(t, fd, []byte(tc.src))
			pxf.CanonicalizeKeyed(doc, desc)
			out := pxf.FormatDocument(doc)
			assert.Equal(t, tc.want, string(out))

			again, err := pxf.Parse(out)
			require.NoError(t, err)
			pxf.CanonicalizeKeyed(again, desc)
			assert.Equal(t, string(out), string(pxf.FormatDocument(again)), "formatting is a fixed point")
		})
	}
}
