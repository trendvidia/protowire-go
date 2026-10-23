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

import "google/protobuf/field_mask.proto";
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

// A row type with a _null mask, without and with an annotated field.
message NullRow {
  google.protobuf.StringValue w = 1;
  google.protobuf.FieldMask _null = 15;
}

message NullRowDefault {
  google.protobuf.StringValue w = 1;
  string d = 2 [(pxf.default) = "z"];
  google.protobuf.FieldMask _null = 15;
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
		// The message, not the position: the stream reports every row at
		// 1:1 until #144 gives its rows document positions.
		require.Equal(t, errMsg(bindErr), errMsg(streamErr))
		return nil, bindErr
	}
	require.ErrorContains(t, streamErr, "EOF")
	require.Len(t, streamed, len(bound))
	for i := range bound {
		require.True(t, proto.Equal(streamed[i], bound[i]), "row %d: Scan %v, BindRow %v", i, streamed[i], bound[i])
	}
	return bound, nil
}

// errMsg is err's text without its position.
func errMsg(err error) string {
	if pe, ok := err.(*pxf.Error); ok {
		return pe.Msg
	}
	return err.Error()
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

// A null cell is recorded in the row type's _null mask whether or not
// some other field carries an annotation: the mask must not depend on
// which decoder an unrelated field selects.
func TestBindRow_NullCellIsRecordedInNullMask(t *testing.T) {
	for _, name := range []string{"NullRow", "NullRowDefault"} {
		t.Run(name, func(t *testing.T) {
			desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", name)
			rows, err := bindRows(t, desc, "@dataset bindann.v1."+name+" (w)\n(null)\n(\"x\")\n")
			require.NoError(t, err)
			paths := func(m *dynamicpb.Message) []string {
				var out []string
				if !m.Has(desc.Fields().ByName("_null")) {
					return out
				}
				l := m.Get(desc.Fields().ByName("_null")).Message().Get(desc.Fields().ByName("_null").Message().Fields().ByName("paths")).List()
				for i := range l.Len() {
					out = append(out, l.Get(i).String())
				}
				return out
			}
			assert.Equal(t, []string{"w"}, paths(rows[0]))
			assert.Empty(t, paths(rows[1]))
			assert.False(t, rows[0].Has(desc.Fields().ByName("w")))
		})
	}
}

// The annotation-form spelling (@default / @required, the 1327 carrier)
// binds rows the same as the bracket options.
func TestBindRow_AnnotationForm(t *testing.T) {
	desc := v12Msg(t, annotFile(`message M { string s = 1 @default("hi"); string r = 2 @required; }`), "M")
	rows, err := bindRows(t, desc, "@dataset annot.v1.M (s, r)\n(, \"x\")\n")
	require.NoError(t, err)
	assert.Equal(t, "hi", rows[0].Get(desc.Fields().ByName("s")).String())

	_, err = bindRows(t, desc, "@dataset annot.v1.M (s, r)\n(\"a\", )\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `required field "r" is absent`)
}

// A schema the bind-time checks reject is rejected for rows too, rather
// than bound by picking one of two defaults (RFC-001 §8.5) or by failing
// every row that chooses another arm of a oneof.
func TestBindRow_SchemaViolationsReject(t *testing.T) {
	t.Run("conflicting defaults", func(t *testing.T) {
		desc := v12Msg(t, annotFile(`message M { string s = 1 [(pxf.default) = "a"] @default("b"); string t = 2; }`), "M")
		_, err := bindRows(t, desc, "@dataset annot.v1.M (s, t)\n(, \"x\")\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "carry different values")
	})
	t.Run("required oneof member", func(t *testing.T) {
		desc := compilePlacementSrc(t, `
syntax = "proto3";
package bindbad.v1;
import "pxf/annotations.proto";
message M { oneof pick { string a = 1 [(pxf.required) = true]; string b = 2; } }
`, "bindbad.v1", "M")
		_, err := bindRows(t, desc, "@dataset bindbad.v1.M (a, b)\n(, \"x\")\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PXF schema violations")
	})
}

// A row's error names the row: the decoder sees a synthetic body, so
// without this a required field left empty on row 3 reports 1:1.
func TestBindRow_ErrorNamesTheRow(t *testing.T) {
	desc := compilePlacementSrc(t, bindAnnotationsProtoSrc, "bindann.v1", "Req")
	doc, err := pxf.Parse([]byte("@dataset bindann.v1.Req (id, name)\n(1, \"a\")\n  (2, )\n"))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	require.NoError(t, pxf.BindRow(dynamicpb.NewMessage(desc), ds.Columns, ds.Rows[0]))
	err = pxf.BindRow(dynamicpb.NewMessage(desc), ds.Columns, ds.Rows[1])
	require.Error(t, err)
	assert.Equal(t, `3:3: required field "name" is absent`, err.Error())
}
