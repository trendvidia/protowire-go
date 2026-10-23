// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Presence paths follow every descent into a message body (#156).
//
// UnmarshalFull records which fields a document set under dotted paths,
// and its post-decode pass reads them to apply (pxf.default) and check
// (pxf.required). The block-tail form `name { ... }` extended the path
// by `name.`; `name = { ... }`, list elements, map values, keyed
// entries and Any bodies did not. Their fields were recorded under the
// parent: a field set inside the body read as absent (its default
// overwrote it), and a parent field of the same name read as present
// (its default stayed off, or its required check passed).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const eqBlockProtoSrc = `
syntax = "proto3";
package eqblock.v1;

import "google/protobuf/field_mask.proto";
import "pxf/annotations.proto";

message P {
  string label = 1 [(pxf.default) = "parent-dflt"];
  int32 need = 2 [(pxf.required) = true];
  repeated Item items = 3;
  map<string, Item> by_key = 4;
  Item one = 5;
  repeated Item keyed = 6 [(pxf.key) = "id"];
  google.protobuf.FieldMask _null = 15;
}

message Item {
  string id = 1;
  string label = 2 [(pxf.default) = "item-dflt"];
  int32 need = 3;
  Deep deep = 4;
}

message Deep {
  int32 n = 1 [(pxf.required) = true];
}
`

func eqBlockDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return compilePlacementSrc(t, eqBlockProtoSrc, "eqblock.v1", "P")
}

func decodeFull(t *testing.T, desc protoreflect.MessageDescriptor, doc string) (*dynamicpb.Message, *pxf.Result, error) {
	t.Helper()
	m := dynamicpb.NewMessage(desc)
	r, err := pxf.UnmarshalFull([]byte(doc), m)
	return m, r, err
}

func str(m protoreflect.Message, path ...string) string {
	for _, p := range path[:len(path)-1] {
		m = m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(p))).Message()
	}
	return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(path[len(path)-1]))).String()
}

// The two spellings of a singular message field decode alike: the set
// value survives, and the parent's same-named field takes its default.
func TestEqBlock_AssignmentFormMatchesBlockForm(t *testing.T) {
	desc := eqBlockDesc(t)
	eq, eqRes, err := decodeFull(t, desc, `need = 1 one = { label = "x" }`)
	require.NoError(t, err)
	blk, _, err := decodeFull(t, desc, `need = 1 one { label = "x" }`)
	require.NoError(t, err)

	assert.True(t, proto.Equal(blk, eq), "= form %v, block form %v", eq, blk)
	assert.Equal(t, "x", str(eq, "one", "label"), "a value set in the body is not replaced by its default")
	assert.Equal(t, "parent-dflt", str(eq, "label"), "the body's label is not the parent's")
	assert.Contains(t, eqRes.PresentFields(), "one.label")
	assert.NotContains(t, eqRes.PresentFields(), "label")
}

func TestEqBlock_NestedRequiredInAssignmentForm(t *testing.T) {
	desc := eqBlockDesc(t)
	for _, doc := range []string{
		`need = 1 one = { deep {} }`,
		`need = 1 one = { deep = {} }`,
		`need = 1 one { deep = {} }`,
		`need = 1 one { deep {} }`,
	} {
		_, _, err := decodeFull(t, desc, doc)
		require.Error(t, err, doc)
		assert.Contains(t, err.Error(), `required field "one.deep.n" is absent`, doc)
	}
}

// A field set inside a list element, map value or keyed entry is not
// the parent's: it neither satisfies the parent's required field nor
// keeps the parent's default off.
func TestEqBlock_ElementFieldsAreNotTheParents(t *testing.T) {
	desc := eqBlockDesc(t)

	for _, doc := range []string{
		`items = [{ need = 5 }]`,
		`by_key = { "k": { need = 5 } }`,
		`keyed { a { need = 5 } }`,
		`keyed = { a { need = 5 } }`,
	} {
		_, _, err := decodeFull(t, desc, doc)
		require.Error(t, err, doc)
		assert.Contains(t, err.Error(), `required field "need" is absent`, doc)
	}

	for _, doc := range []string{
		`need = 1 items = [{ label = "x" }]`,
		`need = 1 by_key = { "k": { label = "x" } }`,
		`need = 1 keyed { a { label = "x" } }`,
	} {
		m, res, err := decodeFull(t, desc, doc)
		require.NoError(t, err, doc)
		assert.Equal(t, "parent-dflt", str(m, "label"), doc)
		assert.NotContains(t, res.PresentFields(), "label", doc)
	}
}

func TestEqBlock_ElementPathsCarryIndexOrKey(t *testing.T) {
	desc := eqBlockDesc(t)
	_, res, err := decodeFull(t, desc, `need = 1 items = [{ id = "a" }, { label = "x" }] by_key = { "k": { id = "b" } } keyed { z { label = "y" } }`)
	require.NoError(t, err)
	present := res.PresentFields()
	for _, p := range []string{"items[0].id", "items[1].label", `by_key["k"].id`, "keyed[0].label"} {
		assert.Contains(t, present, p)
	}
}

// A null inside a list element is recorded in the Result under its
// element path, and kept out of the root _null mask: a FieldMask path
// cannot name a list element, and before #156 it was recorded as the
// parent's own field.
func TestEqBlock_NullInsideElementStaysOutOfTheMask(t *testing.T) {
	desc := eqBlockDesc(t)
	m, res, err := decodeFull(t, desc, `need = 1 items = [{ deep = null }] one = { deep = null }`)
	require.NoError(t, err)
	assert.True(t, res.IsNull("items[0].deep"))
	assert.True(t, res.IsNull("one.deep"))
	assert.False(t, res.IsNull("deep"))

	mask := m.Get(desc.Fields().ByName("_null")).Message()
	paths := mask.Get(mask.Descriptor().Fields().ByName("paths")).List()
	var got []string
	for i := range paths.Len() {
		got = append(got, paths.Get(i).String())
	}
	assert.Equal(t, []string{"one.deep"}, got)
}

// The Secret hook sees a value written `name = { value = ... }`, as it
// already did for `name { value = ... }`; the plaintext stays off the
// message in both spellings.
func TestEqBlock_SecretHookInAssignmentForm(t *testing.T) {
	desc := secretDemoDesc(t)
	for _, doc := range []string{
		`db_password { value = "s3cret" hint = "vault" }`,
		`db_password = { value = "s3cret" hint = "vault" }`,
	} {
		var seen map[string]string
		o := pxf.UnmarshalOptions{OnSecretField: func(path, value string) error {
			if seen == nil {
				seen = map[string]string{}
			}
			seen[path] = value
			return nil
		}}
		m := dynamicpb.NewMessage(desc)
		_, err := o.UnmarshalFull([]byte(doc), m)
		require.NoError(t, err, doc)
		assert.Equal(t, map[string]string{"db_password": "s3cret"}, seen, doc)
		sec := m.Get(desc.Fields().ByName("db_password")).Message()
		assert.Equal(t, "", sec.Get(sec.Descriptor().Fields().ByName("value")).String(), "%s: plaintext reached the message", doc)
		assert.Equal(t, "vault", sec.Get(sec.Descriptor().Fields().ByName("hint")).String(), doc)
	}
}
