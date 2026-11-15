// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// A keyed error raised in a @dataset cell is reported at its row and names
// its column, as every other cell error is, and is still a *KeyedError of
// its kind (#290). atRow moved only *Error, so a *KeyedError kept a line
// of the synthetic body the row is decoded from, which points at nothing
// in the caller's file, and named no column.
func TestDatasetKeyedErrorAtRow(t *testing.T) {
	cases := []struct {
		name  string
		table string
		kind  pxf.KeyedErrorKind
		want  string
	}{
		{
			name:  "duplicate key",
			table: "@dataset keyedname.v1.P (kd)\n({ a {} a {} })\n",
			kind:  pxf.KeyedDuplicateKey,
			want:  `2:1: column kd: duplicate key "a" in keyed field "kd"`,
		},
		{
			name:  "empty key",
			table: "@dataset keyedname.v1.P (kd)\n({ \"\" {} })\n",
			kind:  pxf.KeyedEmptyKey,
			want:  `2:1: column kd: empty entry name in keyed field "kd": the empty string is not a valid key`,
		},
		{
			name:  "key conflict",
			table: "@dataset keyedname.v1.P (kn)\n({ a { name = \"b\" } })\n",
			kind:  pxf.KeyedKeyConflict,
			want:  `2:1: column kn: key field "name" = "b" conflicts with entry name "a" in keyed field "kn"`,
		},
		{
			name:  "key conflict through a oneof, second column",
			table: "@dataset keyedname.v1.P (kd, ko)\n({ x {} }, { y { other = \"z\" } })\n",
			kind:  pxf.KeyedKeyConflict,
			want:  `2:1: column ko: field "other" shares oneof "pick" with key field "name", which entry name "y" already set in keyed field "ko"`,
		},
		{
			name:  "quoted name on an unkeyed field",
			table: "@dataset keyedname.v1.P (kn)\n({ a { \"q\" {} } })\n",
			kind:  pxf.KeyedQuotedNameUnkeyed,
			want:  `2:1: column kn: quoted entry name "q" is only valid inside a keyed repeated field's block (draft -01 §3.13)`,
		},
		{
			name:  "second row, cell over several lines",
			table: "@dataset keyedname.v1.P (kd, kn)\n({ x {} }, { a {} })\n({ y {} }, {\n  b {}\n  b {}\n})\n",
			kind:  pxf.KeyedDuplicateKey,
			want:  `3:1: column kn: duplicate key "b" in keyed field "kn"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, opts := keyedEntryNameSetup(t)
			tr, err := opts.NewDatasetReader(strings.NewReader(tc.table))
			require.NoError(t, err)
			for err == nil {
				err = tr.Scan(m)
			}
			require.EqualError(t, err, tc.want)
			var ke *pxf.KeyedError
			require.True(t, errors.As(err, &ke), "%T is not a *KeyedError", err)
			assert.Equal(t, tc.kind, ke.Kind)
		})
	}
}

// The materializing path binds the same way: a parsed row has its
// position, and a row built by hand has none, so its error keeps the
// body's position, numbered without the body's empty first line.
func TestBindRowKeyedErrorAtRow(t *testing.T) {
	doc, err := pxf.Parse([]byte("@dataset keyedname.v1.P (kd, kn)\n({ x {} }, { a {} a {} })\n"))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	m, opts := keyedEntryNameSetup(t)
	desc := m.Descriptor()

	err = opts.BindRow(dynamicpb.NewMessage(desc), ds.Columns, ds.Rows[0])
	require.EqualError(t, err, `2:1: column kn: duplicate key "a" in keyed field "kn"`)
	var ke *pxf.KeyedError
	require.ErrorAs(t, err, &ke)
	assert.Equal(t, pxf.KeyedDuplicateKey, ke.Kind)
	assert.Equal(t, "kn", ke.Field)
	assert.Equal(t, "a", ke.Key)

	byHand := ds.Rows[0]
	byHand.Pos = pxf.Position{}
	err = opts.BindRow(dynamicpb.NewMessage(desc), ds.Columns, byHand)
	require.EqualError(t, err, `8:3: column kn: duplicate key "a" in keyed field "kn"`)
	require.ErrorAs(t, err, &ke)
}

// A row type with no (pxf.default) or (pxf.required) binds through plain
// Unmarshal rather than UnmarshalFull, and moves a keyed error the same way.
func TestBindRowKeyedErrorAtRowWithoutPresence(t *testing.T) {
	desc := compilePlacementSrc(t, `
syntax = "proto3";
package plainkey.v1;
import "pxf/annotations.proto";
message R { repeated E items = 1 [(pxf.key) = "name"]; }
message E { string name = 1; }
`, "plainkey.v1", "R")
	doc, err := pxf.Parse([]byte("@dataset plainkey.v1.R (items)\n({ a {} a {} })\n"))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	err = pxf.BindRow(dynamicpb.NewMessage(desc), ds.Columns, ds.Rows[0])
	require.EqualError(t, err, `2:1: column items: duplicate key "a" in keyed field "items"`)
	var ke *pxf.KeyedError
	require.ErrorAs(t, err, &ke)
	assert.Equal(t, pxf.KeyedDuplicateKey, ke.Kind)
}
