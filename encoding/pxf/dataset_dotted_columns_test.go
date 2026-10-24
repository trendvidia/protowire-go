// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Dotted column paths (protowire#342, draft -01 §3.4.4): a column may be
// a path into a sub-message, and a row binds as the body that groups its
// non-empty cells by path prefix into one block each.

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const dottedProtoSrc = `
syntax = "proto3";
package dc.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";
import "pxf/secret.proto";

message Tempo { int32 min_bpm = 1; int32 max_bpm = 2; }
message Desc {
  double energy = 1;
  Tempo tempo = 2;
  repeated string tags = 3;
  google.protobuf.StringValue label = 4;
}
message Pick { string name = 1; int32 n = 2; }
message Req {
  int32 n = 1 [(pxf.required) = true];
  string d = 2 [(pxf.default) = "dflt"];
}
message Event {
  string id = 1;
  Desc desc = 2;
  repeated Pick picks = 3;
  map<string, Pick> by_name = 4;
  google.protobuf.Timestamp ts = 5;
  oneof choice {
    Pick a = 6;
    Pick b = 7;
  }
  Req req = 8;
  int32 plain = 9;
  google.protobuf.Int32Value wrap = 10;
  pxf.Secret pw = 11;
}
`

func dottedDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, "dc.proto", map[string]string{
		"dc.proto":              dottedProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
		"pxf/secret.proto":      secretProtoSrc,
	})
	md := fd.Messages().ByName("Event")
	require.NotNil(t, md)
	return md
}

// bindAll binds every row of the first @dataset in doc three ways — Parse
// + BindRow, UnmarshalFull's rows + BindRow, and DatasetReader.Scan — and
// requires the three to agree, error for error.
func bindAll(t *testing.T, md protoreflect.MessageDescriptor, doc string) ([]*dynamicpb.Message, error) {
	t.Helper()
	parsed, err := pxf.Parse([]byte(doc))
	require.NoError(t, err)
	ds := parsed.Datasets[0]

	res, err := pxf.UnmarshalFull([]byte(doc), dynamicpb.NewMessage(md))
	require.NoError(t, err)
	fast := res.Datasets()[0]
	require.Len(t, fast.Rows, len(ds.Rows))

	tr, err := pxf.NewDatasetReader(strings.NewReader(doc))
	require.NoError(t, err)

	var out []*dynamicpb.Message
	for i, row := range ds.Rows {
		viaParse := dynamicpb.NewMessage(md)
		perr := pxf.BindRow(viaParse, ds.Columns, row)
		viaFast := dynamicpb.NewMessage(md)
		ferr := pxf.BindRow(viaFast, fast.Columns, fast.Rows[i])
		viaScan := dynamicpb.NewMessage(md)
		serr := tr.Scan(viaScan)
		require.Equal(t, errText(perr), errText(ferr), "row %d: Parse vs UnmarshalFull", i)
		require.Equal(t, errText(perr), errText(serr), "row %d: Parse vs Scan", i)
		if perr != nil {
			return out, perr
		}
		assert.True(t, proto.Equal(viaParse, viaFast), "row %d: Parse %v, UnmarshalFull %v", i, viaParse, viaFast)
		assert.True(t, proto.Equal(viaParse, viaScan), "row %d: Parse %v, Scan %v", i, viaParse, viaScan)
		out = append(out, viaParse)
	}
	err = tr.Scan(dynamicpb.NewMessage(md))
	require.True(t, errors.Is(err, io.EOF), "after the last row: %v", err)
	return out, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fullBody(t *testing.T, md protoreflect.MessageDescriptor, body string) *dynamicpb.Message {
	t.Helper()
	m := dynamicpb.NewMessage(md)
	_, err := pxf.UnmarshalFull([]byte(body), m)
	require.NoError(t, err)
	return m
}

// The base rule: the cells sharing a prefix land in one block, in the
// order the prefix first appears, wherever their columns sit in the
// header. A sub-message whose cells are all empty is absent (decision 1).
func TestDottedColumns_RowsBindAsTheirGroupedBodies(t *testing.T) {
	md := dottedDesc(t)
	doc := `@dataset dc.v1.Event (id, desc.energy, desc.tempo.min_bpm, desc.tags, plain, desc.tempo.max_bpm)
("r1", 0.5, 60, ["x"], 1, 120)
("r2", 0.25, , , 2, )
("r3", , , , 3, )
("r4", , , , 4, 9)
`
	bodies := []string{
		`id = "r1" desc { energy = 0.5 tempo { min_bpm = 60 max_bpm = 120 } tags = ["x"] } plain = 1`,
		`id = "r2" desc { energy = 0.25 } plain = 2`,
		`id = "r3" plain = 3`,
		`id = "r4" desc { tempo { max_bpm = 9 } } plain = 4`,
	}
	got, err := bindAll(t, md, doc)
	require.NoError(t, err)
	require.Len(t, got, len(bodies))
	for i, body := range bodies {
		assert.True(t, proto.Equal(fullBody(t, md, body), got[i]), "row %d: got %v", i, got[i])
	}
	descFD := md.Fields().ByName("desc")
	assert.False(t, got[2].Has(descFD), "r3 left every desc.* cell empty: desc is absent")
	tempoFD := descFD.Message().Fields().ByName("tempo")
	assert.False(t, got[1].Get(descFD).Message().Has(tempoFD), "r2 left every desc.tempo.* cell empty: tempo is absent")
}

// null in a dotted cell applies to the leaf, and the parent is present
// (decision 2).
func TestDottedColumns_NullAppliesToTheLeaf(t *testing.T) {
	md := dottedDesc(t)
	got, err := bindAll(t, md, "@dataset dc.v1.Event (id, desc.label)\n(\"n\", null)\n")
	require.NoError(t, err)
	descFD := md.Fields().ByName("desc")
	require.True(t, got[0].Has(descFD), "a null leaf makes its parent present")
	assert.False(t, got[0].Get(descFD).Message().Has(descFD.Message().Fields().ByName("label")))
	assert.True(t, proto.Equal(fullBody(t, md, `id = "n" desc { label = null }`), got[0]))
}

// A path through a repeated or map field, into a type with a literal
// spelling, through a scalar, or naming no field is rejected (decisions
// 3 and 6). The check is on the header: a row of empty cells is
// rejected too.
func TestDottedColumns_HeaderPathsAreCheckedAgainstTheType(t *testing.T) {
	md := dottedDesc(t)
	for _, tc := range []struct{ col, want string }{
		{"picks.name", `"picks" is a repeated field`},
		{"by_name.name", `"by_name" is a map field`},
		{"ts.seconds", `"ts" is a google.protobuf.Timestamp, which is written as a literal`},
		{"wrap.value", `"wrap" is a google.protobuf.Int32Value, which is written as a literal`},
		{"plain.x", `"plain" is not a message field`},
		{"desc.nope", `dc.v1.Desc has no field "nope"`},
		{"desc.tempo.nope", `dc.v1.Tempo has no field "nope"`},
	} {
		t.Run(tc.col, func(t *testing.T) {
			for _, row := range []string{`("x", 1)`, `("x", )`} {
				_, err := bindAll(t, md, "@dataset dc.v1.Event (id, "+tc.col+")\n"+row+"\n")
				require.Error(t, err, row)
				assert.Contains(t, err.Error(), `@dataset column "`+tc.col+`": `+tc.want)
			}
		})
	}
}

// A pxf.Secret has a block form beside its literal, so a path may go
// into one, as the block form would.
func TestDottedColumns_PathIntoSecret(t *testing.T) {
	md := dottedDesc(t)
	got, err := bindAll(t, md, "@dataset dc.v1.Event (pw.value, pw.hint)\n(\"x\", \"h\")\n")
	require.NoError(t, err)
	assert.True(t, proto.Equal(fullBody(t, md, `pw { value = "x" hint = "h" }`), got[0]), "got %v", got[0])
}

// Columns may reach different members of one oneof, and two columns may
// fill the same member: they share one block, so the member is set once.
// A row that fills two members is rejected, as the body would be
// (decision 4).
func TestDottedColumns_Oneof(t *testing.T) {
	md := dottedDesc(t)
	doc := `@dataset dc.v1.Event (a.name, b.name, a.n)
("x", , 1)
(, "y", )
`
	got, err := bindAll(t, md, doc)
	require.NoError(t, err)
	assert.True(t, proto.Equal(fullBody(t, md, `a { name = "x" n = 1 }`), got[0]), "got %v", got[0])
	assert.True(t, proto.Equal(fullBody(t, md, `b { name = "y" }`), got[1]), "got %v", got[1])

	_, err = bindAll(t, md, "@dataset dc.v1.Event (a.name, b.name)\n(\"x\", \"y\")\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2:1: column b.name: ")
	assert.Contains(t, err.Error(), "conflicts with already-set field")
}

// A column beside a path inside it, a repeated path, and a path with an
// empty segment are rejected at the header, at the column, by every
// reader (decision 5). Sibling paths are fine.
func TestDottedColumns_OverlapAndDuplicatesRejected(t *testing.T) {
	md := dottedDesc(t)
	for _, tc := range []struct{ cols, at, want string }{
		{"id, desc, desc.energy", "1:33", `column "desc.energy" overlaps column "desc"`},
		{"id, desc.energy, desc", "1:40", `column "desc" overlaps column "desc.energy"`},
		{"id, desc.tempo.min_bpm, desc.tempo", "1:47", `column "desc.tempo" overlaps column "desc.tempo.min_bpm"`},
		{"id, desc.energy, desc.energy", "1:40", `column "desc.energy" appears twice`},
		{"id, id", "1:27", `column "id" appears twice`},
		{"id, desc..energy", "1:27", `column "desc..energy": a column path has an empty segment`},
		{"id, desc.", "1:27", `column "desc.": a column path has an empty segment`},
	} {
		t.Run(tc.cols, func(t *testing.T) {
			doc := "@dataset dc.v1.Event (" + tc.cols + ")\n(\"x\", 1, 2)\n"
			want := tc.at + ": @dataset " + tc.want
			_, err := pxf.Parse([]byte(doc))
			require.ErrorContains(t, err, want, "Parse")
			_, err = pxf.UnmarshalFull([]byte(doc), dynamicpb.NewMessage(md))
			require.ErrorContains(t, err, want, "UnmarshalFull")
			_, err = pxf.NewDatasetReader(strings.NewReader(doc))
			if err == nil {
				tr, _ := pxf.NewDatasetReader(strings.NewReader(doc))
				_, err = tr.Next()
			}
			require.ErrorContains(t, err, want, "DatasetReader")
		})
	}
	_, err := bindAll(t, md, "@dataset dc.v1.Event (desc.energy, desc.tempo.min_bpm, desc.tempo.max_bpm)\n(1, 2, 3)\n")
	require.NoError(t, err, "sibling paths")
}

// (pxf.required) and (pxf.default) apply under a present parent only,
// as in a body (decision 7).
func TestDottedColumns_RequiredAndDefaultUnderThePresentParent(t *testing.T) {
	md := dottedDesc(t)
	reqFD := md.Fields().ByName("req")

	got, err := bindAll(t, md, "@dataset dc.v1.Event (id, req.d, req.n)\n(\"absent\", , )\n(\"set\", , 5)\n")
	require.NoError(t, err)
	assert.False(t, got[0].Has(reqFD), "every req.* cell empty: req is absent, so req.n is not required")
	require.True(t, got[1].Has(reqFD))
	assert.Equal(t, "dflt", got[1].Get(reqFD).Message().Get(reqFD.Message().Fields().ByName("d")).String(),
		"req is present: req.d takes its default")
	assert.True(t, proto.Equal(fullBody(t, md, `id = "set" req { n = 5 }`), got[1]))

	_, err = bindAll(t, md, "@dataset dc.v1.Event (id, req.d, req.n)\n(\"missing\", \"x\", )\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2:1: ")
	assert.Contains(t, err.Error(), `"req.n"`)
}

// A row's decode error names its column, for a plain column as for a
// dotted one.
func TestDottedColumns_ErrorsNameTheColumn(t *testing.T) {
	md := dottedDesc(t)
	for _, tc := range []struct{ cols, row, want string }{
		{"id, plain", `("x", "nope")`, `2:1: column plain: `},
		{"id, desc.tempo.min_bpm", `("x", "nope")`, `2:1: column desc.tempo.min_bpm: `},
		{"id, desc.tags", `("x", [1])`, `2:1: column desc.tags: `},
	} {
		_, err := bindAll(t, md, "@dataset dc.v1.Event ("+tc.cols+")\n"+tc.row+"\n")
		require.Error(t, err, tc.cols)
		assert.Contains(t, err.Error(), tc.want)
	}
}

// A row built by hand has no position, so its error keeps the body's,
// numbered as the body's entries are: the first cell's entry is line 1,
// as it was before dotted columns.
func TestDottedColumns_HandBuiltRowKeepsTheBodyPosition(t *testing.T) {
	md := dottedDesc(t)
	err := pxf.BindRow(dynamicpb.NewMessage(md), []string{"id", "plain"},
		pxf.DatasetRow{Cells: []pxf.Value{&pxf.IntVal{Raw: "1"}, nil}})
	require.ErrorContains(t, err, `1:6: column id: expected string for field "id"`)
	err = pxf.BindRow(dynamicpb.NewMessage(md), []string{"id", "plain"},
		pxf.DatasetRow{Cells: []pxf.Value{&pxf.StringVal{Value: "x"}, &pxf.StringVal{Value: "y"}}})
	require.ErrorContains(t, err, `2:9: column plain: expected integer for field "plain"`)
}

// A column path that does not resolve fails at the row being bound, as
// a *pxf.Error carrying the row's position, on every row. A row built
// by hand has no position to give.
func TestDottedColumns_HeaderErrorIsAtTheRow(t *testing.T) {
	md := dottedDesc(t)
	doc := "@dataset dc.v1.Event (id, desc.nope)\n(\"a\", )\n(\"b\", 1)\n"
	tr, err := pxf.NewDatasetReader(strings.NewReader(doc))
	require.NoError(t, err)
	for _, line := range []int{2, 3} {
		err := tr.Scan(dynamicpb.NewMessage(md))
		var pe *pxf.Error
		require.ErrorAs(t, err, &pe)
		assert.Equal(t, line, pe.Pos.Line)
		assert.Contains(t, pe.Msg, `@dataset column "desc.nope": dc.v1.Desc has no field "nope"`)
	}
	err = pxf.BindRow(dynamicpb.NewMessage(md), []string{"desc.nope"}, pxf.DatasetRow{Cells: []pxf.Value{nil}})
	require.EqualError(t, err, `pxf: @dataset column "desc.nope": dc.v1.Desc has no field "nope"`)
}

// Scan checks the column paths once per row type, so a row bound into a
// type the paths do not resolve against is still rejected after rows of
// a type they do.
func TestDottedColumns_ScanChecksEachRowType(t *testing.T) {
	md := dottedDesc(t)
	tr, err := pxf.NewDatasetReader(strings.NewReader("@dataset dc.v1.Event (desc.energy)\n(1)\n(2)\n(3)\n"))
	require.NoError(t, err)
	require.NoError(t, tr.Scan(dynamicpb.NewMessage(md)))
	pick := md.Fields().ByName("a").Message()
	require.ErrorContains(t, tr.Scan(dynamicpb.NewMessage(pick)), `3:1: @dataset column "desc.energy": dc.v1.Pick has no field "desc"`)
	require.NoError(t, tr.Scan(dynamicpb.NewMessage(md)))
}
