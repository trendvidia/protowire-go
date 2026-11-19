// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// A keyed violation in a @dataset cell is a tolerant diagnostic at the
// cell's own position in the file (#296). KeyedDiagnostics walks the body
// only, and a dataset document has none, so editor tooling showed nothing
// for a cell that DatasetReader.Scan and BindRow refuse.
func TestDatasetKeyedDiagnostics(t *testing.T) {
	cases := []struct {
		name  string
		table string
		want  []string // KeyedError.Error() of each finding, in order
		at    []string // the source text each finding's offset starts at
	}{
		{
			name:  "duplicate key",
			table: "@dataset keyedname.v1.P (kd)\n({ a {} a {} })\n",
			want:  []string{`2:9: duplicate key "a" in keyed field "kd"`},
			at:    []string{"a {} })"},
		},
		{
			name:  "empty key",
			table: "@dataset keyedname.v1.P (kd)\n({ \"\" {} })\n",
			want:  []string{`2:4: empty entry name in keyed field "kd": the empty string is not a valid key`},
			at:    []string{`"" {}`},
		},
		{
			name:  "key conflict",
			table: "@dataset keyedname.v1.P (kn)\n({ a { name = \"b\" } })\n",
			want:  []string{`2:15: key field "name" = "b" conflicts with entry name "a" in keyed field "kn"`},
			at:    []string{`"b" }`},
		},
		{
			name:  "key conflict through a oneof, second column",
			table: "@dataset keyedname.v1.P (kd, ko)\n({ x {} }, { y { other = \"z\" } })\n",
			want:  []string{`2:18: field "other" shares oneof "pick" with key field "name", which entry name "y" already set in keyed field "ko"`},
			at:    []string{`other = "z"`},
		},
		{
			name:  "quoted name on an unkeyed field",
			table: "@dataset keyedname.v1.P (kn)\n({ a { \"q\" {} } })\n",
			want:  []string{`2:8: quoted entry name "q" is only valid inside a keyed repeated field's block (draft -01 §3.13)`},
			at:    []string{`"q" {}`},
		},
		{
			name:  "list form",
			table: "@dataset keyedname.v1.P (kn)\n([ { name = \"a\" \"q\" {} } ])\n",
			want:  []string{`2:17: quoted entry name "q" is only valid inside a keyed repeated field's block (draft -01 §3.13)`},
			at:    []string{`"q" {}`},
		},
		{
			name:  "nested keyed block inside a cell",
			table: "@dataset keyedname.v1.P (kn)\n({ a { inner { z {} z {} } } })\n",
			want:  []string{`2:21: duplicate key "z" in keyed field "inner"`},
			at:    []string{"z {} } } })"},
		},
		{
			name: "every row, every column, in source order",
			table: "@dataset keyedname.v1.P (kd, kn)\n" +
				"({ x {} x {} }, { a {} })\n" +
				"({ y {} }, {\n  b {}\n  b {}\n})\n",
			want: []string{
				`2:9: duplicate key "x" in keyed field "kd"`,
				`5:3: duplicate key "b" in keyed field "kn"`,
			},
			at: []string{"x {} }, {", "b {}\n})"},
		},
		{
			name:  "clean rows, empty and null cells",
			table: "@dataset keyedname.v1.P (kd, kn)\n({ a {} b {} }, )\n(, null)\n",
		},
	}
	m, _ := keyedEntryNameSetup(t)
	desc := m.Descriptor()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, errs := pxf.ParseTolerant([]byte(tc.table))
			require.Empty(t, errs)
			require.Len(t, doc.Datasets, 1)
			assert.Empty(t, pxf.KeyedDiagnostics(doc, desc), "the body walk stays body-only")

			diags := pxf.DatasetKeyedDiagnostics(&doc.Datasets[0], desc)
			got := make([]string, len(diags))
			for i, d := range diags {
				got[i] = d.Error()
				// The offset is derived apart from Line:Col; both must name
				// the same spot in the caller's file.
				require.True(t, strings.HasPrefix(tc.table[d.Pos.Offset:], tc.at[i]),
					"finding %d at offset %d starts %q, want %q", i, d.Pos.Offset, tc.table[d.Pos.Offset:], tc.at[i])
			}
			if len(tc.want) == 0 {
				assert.Empty(t, diags)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// A cell is walked against the field its column path ends at, and a
// column that does not resolve is skipped rather than guessed at.
func TestDatasetKeyedDiagnosticsColumnPaths(t *testing.T) {
	desc := compilePlacementSrc(t, `
syntax = "proto3";
package keyedpath.v1;
import "google/protobuf/timestamp.proto";
import "pxf/annotations.proto";
message R {
  Holder h = 1;
  repeated Holder hs = 2;
  string s = 3;
  google.protobuf.Timestamp ts = 4;
}
message Holder {
  repeated E items = 1 [(pxf.key) = "name"];
  Holder deeper = 2;
}
message E { string name = 1; }
`, "keyedpath.v1", "R")

	const table = "@dataset keyedpath.v1.R (h.items, h.deeper.items, hs.items, s.items, nope, ts.items)\n" +
		"({ a {} a {} }, { b {} b {} }, { c {} c {} }, { d {} d {} }, { e {} e {} }, { f {} f {} })\n"
	doc, errs := pxf.ParseTolerant([]byte(table))
	require.Empty(t, errs)
	diags := pxf.DatasetKeyedDiagnostics(&doc.Datasets[0], desc)
	got := make([]string, len(diags))
	for i, d := range diags {
		got[i] = d.Error()
	}
	assert.Equal(t, []string{
		`2:9: duplicate key "a" in keyed field "items"`,
		`2:24: duplicate key "b" in keyed field "items"`,
	}, got)
}

// The row type is the caller's, not the header's: a typeless @dataset
// bound to an anonymous @proto has no name to resolve, and is checked
// against whatever descriptor the caller compiled for it.
func TestDatasetKeyedDiagnosticsTypeless(t *testing.T) {
	m, _ := keyedEntryNameSetup(t)
	const table = "@proto { repeated string unused = 1; }\n@dataset (kd)\n({ a {} a {} })\n"
	doc, errs := pxf.ParseTolerant([]byte(table))
	require.Empty(t, errs)
	require.Len(t, doc.Datasets, 1)
	require.Empty(t, doc.Datasets[0].Type)
	diags := pxf.DatasetKeyedDiagnostics(&doc.Datasets[0], m.Descriptor())
	require.Len(t, diags, 1)
	assert.Equal(t, `3:9: duplicate key "a" in keyed field "kd"`, diags[0].Error())
	assert.Equal(t, pxf.KeyedDuplicateKey, diags[0].Kind)
}

func TestDatasetKeyedDiagnosticsNil(t *testing.T) {
	m, _ := keyedEntryNameSetup(t)
	assert.Nil(t, pxf.DatasetKeyedDiagnostics(nil, m.Descriptor()))
	assert.Nil(t, pxf.DatasetKeyedDiagnostics(&pxf.DatasetDirective{Columns: []string{"kd"}}, nil))
}
