// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// A block cell (protowire#341) fills a sub-message, and that
// sub-message's own (pxf.default) and (pxf.required) apply as they do in
// the body `<column> = { ... }` the cell stands for (draft §3.4.4, #145).
// BindRow skips the post-decode pass for a row type that needs none, so
// it must look into sub-messages, not only the row type's own fields.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const nestedPresenceProtoSrc = `
syntax = "proto3";
package nestedpres.v1;

import "pxf/annotations.proto";

message Row {
  string id = 1;
  Inner inner = 2;
}

message Inner {
  string label = 1 [(pxf.default) = "dflt"];
  Deep deep = 2;
}

message Deep {
  int32 n = 1 [(pxf.required) = true];
}
`

func TestValueCells_BlockCellAppliesNestedDefault(t *testing.T) {
	desc := compilePlacementSrc(t, nestedPresenceProtoSrc, "nestedpres.v1", "Row")
	// Row's own fields carry no annotation; Inner's label does. An empty
	// block cell is a present, empty Inner, so label takes its default,
	// as it does for the body `inner = {}`.

	table := "@dataset nestedpres.v1.Row (id, inner)\n(\"r\", {})\n"

	want := dynamicpb.NewMessage(desc)
	_, err := pxf.UnmarshalFull([]byte(`id = "r" inner = {}`), want)
	require.NoError(t, err)
	label := desc.Fields().ByName("inner").Message().Fields().ByName("label")
	require.Equal(t, "dflt", want.Get(desc.Fields().ByName("inner")).Message().Get(label).String())

	doc, err := pxf.Parse([]byte(table))
	require.NoError(t, err)
	bound := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.BindRow(bound, doc.Datasets[0].Columns, doc.Datasets[0].Rows[0]))
	require.True(t, proto.Equal(want, bound), "BindRow %v, body %v", bound, want)

	tr, err := pxf.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	scanned := dynamicpb.NewMessage(desc)
	require.NoError(t, tr.Scan(scanned))
	require.True(t, proto.Equal(want, scanned), "Scan %v, body %v", scanned, want)
}

// A value set inside a block cell survives, and a required field inside
// one is enforced: the cell is bound as `inner = { ... }`, which records
// presence under `inner.` like the block-tail form (#156).
func TestValueCells_BlockCellKeepsValuesAndEnforcesRequired(t *testing.T) {
	desc := compilePlacementSrc(t, nestedPresenceProtoSrc, "nestedpres.v1", "Row")
	label := desc.Fields().ByName("inner").Message().Fields().ByName("label")

	table := "@dataset nestedpres.v1.Row (id, inner)\n(\"r\", { label = \"x\" })\n"
	tr, err := pxf.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, tr.Scan(m))
	require.Equal(t, "x", m.Get(desc.Fields().ByName("inner")).Message().Get(label).String())

	doc, err := pxf.Parse([]byte(table))
	require.NoError(t, err)
	b := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.BindRow(b, doc.Datasets[0].Columns, doc.Datasets[0].Rows[0]))
	require.True(t, proto.Equal(m, b), "Scan %v, BindRow %v", m, b)

	bad := "@dataset nestedpres.v1.Row (id, inner)\n(\"s\", { deep {} })\n"
	tr, err = pxf.NewDatasetReader(strings.NewReader(bad))
	require.NoError(t, err)
	err = tr.Scan(dynamicpb.NewMessage(desc))
	require.Error(t, err)
	require.Contains(t, err.Error(), `required field "inner.deep.n" is absent`)
}
