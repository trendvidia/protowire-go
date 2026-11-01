// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// An entry name of a keyed block populates the element's key field (draft
// -01 §3.13), so it counts as the document supplying that field (#214).
// The post-decode pass reads presence only: without the mark, a default on
// the key replaced the name, a required key was reported absent, and a
// default on a oneof sibling of the key cleared it.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const keyedEntryNameProtoSrc = `
syntax = "proto3";
package keyedname.v1;

import "google/protobuf/any.proto";
import "pxf/annotations.proto";

message P {
  repeated WithDefault kd = 1 [(pxf.key) = "name"];
  repeated WithRequired kr = 2 [(pxf.key) = "name"];
  repeated InOneof ko = 3 [(pxf.key) = "name"];
  repeated Nests kn = 4 [(pxf.key) = "name"];
  google.protobuf.Any payload = 5;
}

message WithDefault {
  string name = 1 [(pxf.default) = "dflt"];
}

message WithRequired {
  string name = 1 [(pxf.required) = true];
}

message InOneof {
  oneof pick {
    string name = 1;
    string other = 2 [(pxf.default) = "other-dflt"];
  }
}

message Nests {
  string name = 1;
  repeated WithDefault inner = 2 [(pxf.key) = "name"];
}
`

func keyedEntryNameSetup(t *testing.T) (*dynamicpb.Message, pxf.UnmarshalOptions) {
	t.Helper()
	desc := compilePlacementSrc(t, keyedEntryNameProtoSrc, "keyedname.v1", "P")
	res := &testResolver{descs: map[string]protoreflect.MessageDescriptor{}}
	msgs := desc.ParentFile().Messages()
	for i := range msgs.Len() {
		res.descs[string(msgs.Get(i).FullName())] = msgs.Get(i)
	}
	return dynamicpb.NewMessage(desc), pxf.UnmarshalOptions{TypeResolver: res}
}

func TestKeyedEntryName_SuppliesTheKeyField(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		path []string
		mark string // the presence path the entry name records
	}{
		{`kd { alpha {} }`, []string{"kd", "0", "name"}, "kd[0].name"},
		{`kd { "not ident" {} }`, []string{"kd", "0", "name"}, "kd[0].name"},
		{`kd { alpha = {} }`, []string{"kd", "0", "name"}, "kd[0].name"},
		{`kd = { alpha {} }`, []string{"kd", "0", "name"}, "kd[0].name"},
		{`kd { a {} alpha {} }`, []string{"kd", "1", "name"}, "kd[1].name"},
		{`kd { alpha { name = "alpha" } }`, []string{"kd", "0", "name"}, "kd[0].name"},
		{`kr { alpha {} }`, []string{"kr", "0", "name"}, "kr[0].name"},
		{`ko { alpha {} }`, []string{"ko", "0", "name"}, "ko[0].name"},
		{`kn { a { inner { alpha {} } } }`, []string{"kn", "0", "inner", "0", "name"}, "kn[0].inner[0].name"},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			m, opts := keyedEntryNameSetup(t)
			r, err := opts.UnmarshalFull([]byte(tc.doc), m)
			require.NoError(t, err)
			want := "alpha"
			if strings.Contains(tc.doc, "not ident") {
				want = "not ident"
			}
			assert.Equal(t, want, get(t, m, tc.path...).String())
			assert.True(t, r.IsSet(tc.mark), "present: %v", r.PresentFields())
		})
	}
}

// The default still fills an element the anonymous form leaves keyless:
// only an entry name counts as supplying the key.
func TestKeyedEntryName_AnonymousElementTakesTheDefault(t *testing.T) {
	m, opts := keyedEntryNameSetup(t)
	_, err := opts.UnmarshalFull([]byte(`kd = [{}]`), m)
	require.NoError(t, err)
	assert.Equal(t, "dflt", get(t, m, "kd", "0", "name").String())

	m, opts = keyedEntryNameSetup(t)
	_, err = opts.UnmarshalFull([]byte(`kr = [{}]`), m)
	require.ErrorContains(t, err, `required field "kr[0].name" is absent`)
}

// The other members of the key's oneof see the key as chosen, in the
// sibling check the post-decode pass and layered callers both read.
func TestKeyedEntryName_OneofSiblingDefaultStaysOff(t *testing.T) {
	m, opts := keyedEntryNameSetup(t)
	_, err := opts.UnmarshalFull([]byte(`ko { alpha {} }`), m)
	require.NoError(t, err)
	assert.Equal(t, "", get(t, m, "ko", "0", "other").String())
}

// An Any body records its fields under the Any's path.
func TestKeyedEntryName_InsideAnyBody(t *testing.T) {
	m, opts := keyedEntryNameSetup(t)
	const doc = `payload { @type = "keyedname.v1.Nests" inner { alpha {} } }`
	_, err := opts.UnmarshalFull([]byte(doc), m)
	require.NoError(t, err)
	inner := unpack(t, m.Descriptor(), get(t, m, "payload").Message(), "Nests")
	assert.Equal(t, "alpha", get(t, inner, "inner", "0", "name").String())
}

// A dataset cell takes the same decoder.
func TestKeyedEntryName_InDatasetCell(t *testing.T) {
	m, opts := keyedEntryNameSetup(t)
	const table = "@dataset keyedname.v1.P (kd)\n({ alpha {} })\n"
	tr, err := opts.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	require.NoError(t, tr.Scan(m))
	assert.Equal(t, "alpha", get(t, m, "kd", "0", "name").String())
}

// Layered callers run their own defaults pass from the presence set, so
// the mark has to be there without the post-decode pass too.
func TestKeyedEntryName_MarkedUnderSkipPostDecode(t *testing.T) {
	m, opts := keyedEntryNameSetup(t)
	opts.SkipPostDecode = true
	r, err := opts.UnmarshalFull([]byte(`kd { alpha {} }`), m)
	require.NoError(t, err)
	assert.True(t, r.IsSet("kd[0].name"), "present: %v", r.PresentFields())
}

// The entry name binds the key field, so the body can't undo it, in any
// spelling of the binding (#215). A oneof in a nested message is a
// different oneof, so its members stay legal.
func TestKeyedEntryName_BodyCannotUndoIt(t *testing.T) {
	const src = `
syntax = "proto3";
package undo.v1;
import "pxf/annotations.proto";
message P {
  repeated O ko = 1 [(pxf.key) = "name"];
  repeated N kn = 2 [(pxf.key) = "name"];
}
message O {
  oneof pick { string name = 1; string other = 2; Sub sub = 3; }
  string free = 4;
  Sub nested = 5;
}
message Sub { oneof pick { string name = 1; string other = 2; } }
message N { optional string name = 1; }
`
	desc := compilePlacementSrc(t, src, "undo.v1", "P")
	for _, tc := range []struct {
		doc string
		bad bool
	}{
		{`ko { alpha { other = "x" } }`, true},
		{`ko { alpha { other = null } }`, true},
		{`ko { alpha = { other = "x" } }`, true},
		{`ko = { alpha { other = "x" } }`, true},
		{`ko { "not ident" { other = "x" } }`, true},
		{`ko { alpha { sub { name = "x" } } }`, true},
		{`ko { alpha { sub = { name = "x" } } }`, true},
		{`ko { alpha { free = "x" other = "y" } }`, true},
		{`kn { alpha { name = null } }`, true},
		{`ko { alpha { name = "alpha" } }`, false},
		{`ko { alpha { free = "x" } }`, false},
		{`ko { alpha { nested { other = "x" } } }`, false},
		{`ko { alpha { nested { name = null } } }`, false},
		{`ko = [ { other = "x" } ]`, false},
		{`ko = [ { other = null } ]`, false},
		{`kn { alpha { name = "alpha" } }`, false},
		{`kn = [ { name = null } ]`, false},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			_, _, err := pxf.UnmarshalFullDescriptor([]byte(tc.doc), desc)
			doc, syntaxErrs := pxf.ParseTolerant([]byte(tc.doc))
			require.Empty(t, syntaxErrs)
			diags := pxf.KeyedDiagnostics(doc, desc)
			if !tc.bad {
				require.NoError(t, err)
				assert.Empty(t, diags)
				return
			}
			var kerr *pxf.KeyedError
			require.ErrorAs(t, err, &kerr, "want KeyedError, got %v", err)
			assert.Equal(t, pxf.KeyedKeyConflict, kerr.Kind)
			require.Len(t, diags, 1)
			assert.Equal(t, pxf.KeyedKeyConflict, diags[0].Kind)
			assert.Equal(t, kerr.Pos, diags[0].Pos, "decoder and diagnostics disagree on position")
		})
	}
}
