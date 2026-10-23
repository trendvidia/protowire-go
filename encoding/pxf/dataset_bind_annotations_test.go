// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Empty and null @dataset cells under (pxf.default) and (pxf.required)
// (draft §3.4.4, "Three-state cells"; #145). An empty cell is an absent
// field: the default applies, a required field rejects the row. A null
// cell is present-but-null: it satisfies required and keeps the default
// off. Scan and BindRow ignored both annotations before #145.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const bindAnnotationsProtoSrc = `
syntax = "proto3";
package bindann.v1;

import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";

message Row {
  string s = 1 [(pxf.default) = "hello"];
  int32 n = 2 [(pxf.default) = "-1"];
  google.protobuf.BoolValue w = 3 [(pxf.default) = "true"];
  string plain = 4;

  oneof pick {
    string p_s = 10 [(pxf.default) = "picked"];
    int32 p_i = 11;
  }
}

message Req {
  int32 id = 1;
  google.protobuf.StringValue name = 2 [(pxf.required) = true];
}
`

// bindRows binds every row of the single @dataset in table, through
// Scan and through Parse + BindRow, and requires the two to agree. It
// returns the messages, or the first error (identical on both paths).
func bindRows(t *testing.T, desc protoreflect.MessageDescriptor, table string) ([]*dynamicpb.Message, error) {
	t.Helper()
	var streamed []*dynamicpb.Message
	var streamErr error
	tr, err := pxf.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	for {
		m := dynamicpb.NewMessage(desc)
		if streamErr = tr.Scan(m); streamErr != nil {
			break
		}
		streamed = append(streamed, m)
	}

	doc, err := pxf.Parse([]byte(table))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	var bound []*dynamicpb.Message
	var bindErr error
	for _, row := range ds.Rows {
		m := dynamicpb.NewMessage(desc)
		if bindErr = pxf.BindRow(m, ds.Columns, row); bindErr != nil {
			break
		}
		bound = append(bound, m)
	}

	if bindErr != nil {
		require.Error(t, streamErr)
		require.Equal(t, bindErr.Error(), streamErr.Error())
		return nil, bindErr
	}
	require.ErrorContains(t, streamErr, "EOF")
	require.Len(t, streamed, len(bound))
	for i := range bound {
		require.True(t, proto.Equal(streamed[i], bound[i]), "row %d: Scan %v, BindRow %v", i, streamed[i], bound[i])
	}
	return bound, nil
}

func TestBindRow_EmptyCellTakesDefault(t *testing.T) {
	desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", "Row")
	rows, err := bindRows(t, desc, "@dataset bindann.v1.Row (s, n, w, plain)\n(, , , )\n")
	require.NoError(t, err)
	m := rows[0]
	f := desc.Fields()
	assert.Equal(t, "hello", m.Get(f.ByName("s")).String())
	assert.Equal(t, int64(-1), m.Get(f.ByName("n")).Int())
	require.True(t, m.Has(f.ByName("w")))
	assert.True(t, m.Get(f.ByName("w")).Message().Get(f.ByName("w").Message().Fields().ByName("value")).Bool())
	assert.False(t, m.Has(f.ByName("plain")))
	// The oneof member's default applies too when no member is present,
	// even though it is not a column: it is absent like any other field.
	assert.Equal(t, "picked", m.Get(f.ByName("p_s")).String())
}

func TestBindRow_ValueAndZeroKeepTheDefaultOff(t *testing.T) {
	desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", "Row")
	rows, err := bindRows(t, desc, "@dataset bindann.v1.Row (s, n, w)\n(\"x\", 0, false)\n")
	require.NoError(t, err)
	m := rows[0]
	f := desc.Fields()
	assert.Equal(t, "x", m.Get(f.ByName("s")).String())
	// The default applies only to an absent field, not to one set to
	// the proto3 zero value (draft, pxf.default).
	assert.Equal(t, int64(0), m.Get(f.ByName("n")).Int())
	require.True(t, m.Has(f.ByName("w")))
	assert.False(t, m.Get(f.ByName("w")).Message().Get(f.ByName("w").Message().Fields().ByName("value")).Bool())
}

func TestBindRow_NullKeepsTheDefaultOff(t *testing.T) {
	desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", "Row")
	rows, err := bindRows(t, desc, "@dataset bindann.v1.Row (w)\n(null)\n")
	require.NoError(t, err)
	assert.False(t, rows[0].Has(desc.Fields().ByName("w")))
}

func TestBindRow_SetOneofMemberKeepsSiblingDefaultOff(t *testing.T) {
	desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", "Row")
	rows, err := bindRows(t, desc, "@dataset bindann.v1.Row (p_s, p_i)\n(, 3)\n")
	require.NoError(t, err)
	m := rows[0]
	assert.Equal(t, int64(3), m.Get(desc.Fields().ByName("p_i")).Int())
	assert.False(t, m.Has(desc.Fields().ByName("p_s")))
}

func TestBindRow_Required(t *testing.T) {
	desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", "Req")

	_, err := bindRows(t, desc, "@dataset bindann.v1.Req (id, name)\n(1, )\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")

	// Not a column at all is absent too.
	_, err = bindRows(t, desc, "@dataset bindann.v1.Req (id)\n(1)\n")
	require.Error(t, err)

	// null satisfies required; a value does too.
	rows, err := bindRows(t, desc, "@dataset bindann.v1.Req (id, name)\n(1, null)\n(2, \"n\")\n")
	require.NoError(t, err)
	assert.False(t, rows[0].Has(desc.Fields().ByName("name")))
	assert.True(t, rows[1].Has(desc.Fields().ByName("name")))
}
