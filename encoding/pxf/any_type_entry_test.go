// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// The type entry `@type = "<type-url>"` opens a block bound to
// google.protobuf.Any (draft -01 "Any Blocks", protowire#354; #173).
// Parse used to reject it everywhere while Unmarshal accepted it in
// singular fields only, and Marshal wrote it, so Marshal's output did
// not parse.

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

const anyEntryProtoSrc = `
syntax = "proto3";
package anyentry.v1;
import "google/protobuf/any.proto";
message P {
  google.protobuf.Any payload = 1;
  repeated google.protobuf.Any anys = 2;
  map<string, google.protobuf.Any> by_key = 3;
  Leaf leaf = 4;
  repeated Leaf leaves = 5;
}
message Leaf {
  string tag = 1;
  int32 n = 2;
}
`

func anyEntrySetup(t *testing.T) (protoreflect.MessageDescriptor, pxf.UnmarshalOptions) {
	t.Helper()
	desc := compilePlacementSrc(t, anyEntryProtoSrc, "anyentry.v1", "P")
	leaf := desc.ParentFile().Messages().ByName("Leaf")
	res := &testResolver{descs: map[string]protoreflect.MessageDescriptor{
		"anyentry.v1.Leaf":                     leaf,
		"type.googleapis.com/anyentry.v1.Leaf": leaf,
	}}
	return desc, pxf.UnmarshalOptions{TypeResolver: res}
}

// leafOf unpacks an Any holding an anyentry.v1.Leaf.
func leafOf(t *testing.T, desc protoreflect.MessageDescriptor, anyMsg protoreflect.Message) (url string, leaf *dynamicpb.Message) {
	t.Helper()
	f := anyMsg.Descriptor().Fields()
	leaf = dynamicpb.NewMessage(desc.ParentFile().Messages().ByName("Leaf"))
	require.NoError(t, proto.Unmarshal(anyMsg.Get(f.ByName("value")).Bytes(), leaf))
	return anyMsg.Get(f.ByName("type_url")).String(), leaf
}

func TestAnyTypeEntry_DecodesInEveryBody(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	f := desc.Fields()
	for _, tc := range []struct {
		doc string
		any func(m *dynamicpb.Message) protoreflect.Message
	}{
		{`payload { @type = "anyentry.v1.Leaf" tag = "t" }`,
			func(m *dynamicpb.Message) protoreflect.Message { return m.Get(f.ByName("payload")).Message() }},
		{"payload = {\n  @type = \"anyentry.v1.Leaf\"\n  tag = \"t\"\n}",
			func(m *dynamicpb.Message) protoreflect.Message { return m.Get(f.ByName("payload")).Message() }},
		{`anys = [{ @type = "anyentry.v1.Leaf" n = 1 }, { @type = "anyentry.v1.Leaf" tag = "t" }]`,
			func(m *dynamicpb.Message) protoreflect.Message {
				return m.Get(f.ByName("anys")).List().Get(1).Message()
			}},
		{`by_key = { "k": { @type = "anyentry.v1.Leaf" tag = "t" } }`,
			func(m *dynamicpb.Message) protoreflect.Message {
				return m.Get(f.ByName("by_key")).Map().Get(protoreflect.ValueOfString("k").MapKey()).Message()
			}},
	} {
		for _, full := range []bool{false, true} {
			m := dynamicpb.NewMessage(desc)
			var err error
			if full {
				_, err = opts.UnmarshalFull([]byte(tc.doc), m)
			} else {
				err = opts.Unmarshal([]byte(tc.doc), m)
			}
			require.NoError(t, err, tc.doc)
			url, leaf := leafOf(t, desc, tc.any(m))
			assert.Equal(t, "anyentry.v1.Leaf", url, tc.doc)
			assert.Equal(t, "t", leaf.Get(leaf.Descriptor().Fields().ByName("tag")).String(), tc.doc)

			_, err = pxf.Parse([]byte(tc.doc))
			require.NoError(t, err, "Parse: %s", tc.doc)
		}
	}
}

// The type URL is stored as written, prefix and all; the resolver maps it.
func TestAnyTypeEntry_URLVerbatim(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, opts.Unmarshal([]byte(`payload { @type = "type.googleapis.com/anyentry.v1.Leaf" tag = "t" }`), m))
	url, _ := leafOf(t, desc, m.Get(desc.Fields().ByName("payload")).Message())
	assert.Equal(t, "type.googleapis.com/anyentry.v1.Leaf", url)
}

// Without a type entry, an Any block is its own fields, in both
// spellings and with or without a resolver. `payload = { type_url ... }`
// used to fail when a resolver was set.
func TestAnyTypeEntry_PlainFields(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	for _, doc := range []string{
		`payload { type_url = "anyentry.v1.Leaf" value = b"CgF0" }`,
		`payload = { type_url = "anyentry.v1.Leaf" value = b"CgF0" }`,
		`anys = [{ type_url = "anyentry.v1.Leaf" value = b"CgF0" }]`,
	} {
		for _, o := range []pxf.UnmarshalOptions{opts, {}} {
			m := dynamicpb.NewMessage(desc)
			require.NoError(t, o.Unmarshal([]byte(doc), m), doc)
		}
	}
}

func TestAnyTypeEntry_Rejected(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	for _, tc := range []struct {
		doc, parse, decode string // "" parse: Parse accepts (a binding rule)
	}{
		// Not first: in an Any block, the block is then the Any's own fields.
		{`payload { tag = "t" @type = "anyentry.v1.Leaf" }`, "1:21: @type is allowed only as the first entry", `1:11: unknown field "tag" in google.protobuf.Any`},
		{`payload { @type = "anyentry.v1.Leaf" @type = "anyentry.v1.Leaf" }`, "1:38: @type is allowed only as the first entry", "1:38: @type is allowed only as the first entry"},
		{`leaf { @type = "anyentry.v1.Leaf" }`, "", "1:8: @type is allowed only as the first entry"},
		{`leaves = [{ @type = "anyentry.v1.Leaf" }]`, "", "1:13: @type is allowed only as the first entry"},
		{`@type = "anyentry.v1.Leaf"`, "1:7: expected type name after @type", "expected type name after @type"},
		{`payload { @type = anyentry.v1.Leaf }`, "1:19: expected a string type URL after @type =", "1:19: expected a string type URL after @type ="},
		{`payload { @type "anyentry.v1.Leaf" }`, "1:17: expected '=' after @type", "1:17: expected '=' after @type"},
		{`payload { @type = "anyentry.v1.Nope" tag = "t" }`, "", `1:19: cannot resolve Any type "anyentry.v1.Nope"`},
		{`by_key = { @type = "anyentry.v1.Leaf" }`, "", "1:12: expected"},
	} {
		_, err := pxf.Parse([]byte(tc.doc))
		if tc.parse == "" {
			assert.NoError(t, err, "Parse: %s", tc.doc)
		} else {
			assert.ErrorContains(t, err, tc.parse, "Parse: %s", tc.doc)
		}
		err = opts.Unmarshal([]byte(tc.doc), dynamicpb.NewMessage(desc))
		assert.ErrorContains(t, err, tc.decode, "Unmarshal: %s", tc.doc)
	}
	// No resolver: an error that says so, not a syntax error.
	err := pxf.Unmarshal([]byte(`payload { @type = "anyentry.v1.Leaf" }`), dynamicpb.NewMessage(desc))
	require.ErrorContains(t, err, `1:19: cannot resolve Any type "anyentry.v1.Leaf": no TypeResolver`)
}

// The AST carries the type entry first, and the formatter writes it back.
func TestAnyTypeEntry_ParseAndFormat(t *testing.T) {
	const doc = "payload {\n  # the type\n  @type = \"anyentry.v1.Leaf\" # trailing\n  tag = \"t\"\n}\n"
	d, err := pxf.Parse([]byte(doc))
	require.NoError(t, err)
	blk := d.Entries[0].(*pxf.Block)
	te, ok := blk.Entries[0].(*pxf.TypeEntry)
	require.True(t, ok, "%T", blk.Entries[0])
	assert.Equal(t, "anyentry.v1.Leaf", te.URL)
	assert.Equal(t, "# the type", te.LeadingComments[0].Text)
	assert.Equal(t, "# trailing", te.TrailingComment)
	start, end := pxf.EntrySpan(te)
	assert.Equal(t, `@type = "anyentry.v1.Leaf"`, doc[start.Offset:end.Offset])
	assert.Equal(t, doc, string(pxf.FormatDocument(d)))
}

// Tolerant parsing records a misplaced type entry and parses on.
func TestAnyTypeEntry_Tolerant(t *testing.T) {
	d, errs := pxf.ParseTolerant([]byte(`payload { tag = "t" @type = "x" n = 1 }`))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "@type is allowed only as the first entry")
	assert.Len(t, d.Entries[0].(*pxf.Block).Entries, 2)
}

// Marshal writes the type entry when it has a resolver; its output now
// parses and decodes back.
func TestAnyTypeEntry_MarshalOutputParses(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, opts.Unmarshal([]byte(`payload { @type = "anyentry.v1.Leaf" tag = "t" n = 2 }`), m))
	out, err := pxf.MarshalOptions{TypeResolver: opts.TypeResolver}.Marshal(m)
	require.NoError(t, err)
	require.Contains(t, string(out), `@type = "anyentry.v1.Leaf"`)
	_, err = pxf.Parse(out)
	require.NoError(t, err, "%s", out)
	back := dynamicpb.NewMessage(desc)
	require.NoError(t, opts.Unmarshal(out, back))
	assert.True(t, proto.Equal(m, back))
}

// A @dataset cell may hold an Any block with a type entry (#173): it
// parses, and a reader with a resolver binds it.
func TestAnyTypeEntry_DatasetCell(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	const table = "@dataset anyentry.v1.P (payload, anys)\n" +
		"({ @type = \"anyentry.v1.Leaf\" tag = \"t\" }, [{ @type = \"anyentry.v1.Leaf\" n = 3 }])\n"
	_, err := pxf.Parse([]byte(table))
	require.NoError(t, err)
	tr, err := opts.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, tr.Scan(m))
	_, leaf := leafOf(t, desc, m.Get(desc.Fields().ByName("payload")).Message())
	assert.Equal(t, "t", leaf.Get(leaf.Descriptor().Fields().ByName("tag")).String())
	_, leaf = leafOf(t, desc, m.Get(desc.Fields().ByName("anys")).List().Get(0).Message())
	assert.Equal(t, int64(3), leaf.Get(leaf.Descriptor().Fields().ByName("n")).Int())

	// BindRow on the materialized rows: with the options' resolver it
	// binds, and without one the cell's type cannot be resolved.
	doc, err := pxf.Parse([]byte(table))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	require.NoError(t, opts.BindRow(dynamicpb.NewMessage(desc), ds.Columns, ds.Rows[0]))
	require.ErrorContains(t, pxf.BindRow(dynamicpb.NewMessage(desc), ds.Columns, ds.Rows[0]), "no TypeResolver")
}

// One document packs its Any body to the same bytes every time, so two
// decodes of it are proto.Equal. A plain proto.Marshal of the dynamicpb
// body ordered its fields differently from run to run (measured on main
// a6fa212: two encodings over 300 decodes).
func TestAnyTypeEntry_PacksDeterministically(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	const doc = `payload { @type = "anyentry.v1.Leaf" tag = "t" n = 2 }`
	var first []byte
	for i := range 200 {
		m := dynamicpb.NewMessage(desc)
		require.NoError(t, opts.Unmarshal([]byte(doc), m))
		a := m.Get(desc.Fields().ByName("payload")).Message()
		got := a.Get(a.Descriptor().Fields().ByName("value")).Bytes()
		if i == 0 {
			first = got
			continue
		}
		require.Equal(t, first, got, "decode %d", i)
	}
}
