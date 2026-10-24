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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
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

// A null recorded under an element or Any path is written back by
// Marshal at that element, and only there: the encoder's null lookup
// extends its path the way the decoder does.
func TestEqBlock_NullInsideElementRoundTrips(t *testing.T) {
	desc := eqBlockDesc(t)
	for _, tc := range []struct{ doc, path string }{
		{`need = 1 items = [{ id = "a" }, { deep = null }]`, "items[1].deep"},
		{`need = 1 by_key = { "j": { id = "a" } "k": { deep = null } }`, `by_key["k"].deep`},
		{`need = 1 keyed { a { label = "x" } b { deep = null } }`, "keyed[1].deep"},
	} {
		m, res, err := decodeFull(t, desc, tc.doc)
		require.NoError(t, err, tc.doc)
		require.True(t, res.IsNull(tc.path), tc.doc)
		out, err := pxf.MarshalOptions{NullFields: res}.Marshal(m)
		require.NoError(t, err, tc.doc)
		assert.Equal(t, 1, strings.Count(string(out), "deep = null"), "%s:\n%s", tc.doc, out)

		_, back, err := decodeFull(t, desc, string(out))
		require.NoError(t, err, "%s:\n%s", tc.doc, out)
		assert.True(t, back.IsNull(tc.path), "%s:\n%s", tc.doc, out)
	}
}

// The Any body here is a Container, whose payload is a message field and
// so can hold null; a scalar in an Any body cannot (#150).
func TestEqBlock_NullInsideAnyRoundTrips(t *testing.T) {
	fd, resolver := compileAnyProto(t)
	desc := fd.Messages().ByName("Container")
	resolver.descs["any_test.v1.Container"] = desc
	opts := pxf.UnmarshalOptions{TypeResolver: resolver}
	m := dynamicpb.NewMessage(desc)
	res, err := opts.UnmarshalFull(
		[]byte(`payload { @type = "any_test.v1.Container" payload = null name = "x" }`), m)
	require.NoError(t, err)
	require.True(t, res.IsNull("payload.payload"))

	out, err := pxf.MarshalOptions{TypeResolver: resolver, NullFields: res}.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(out), "payload = null"), "%s", out)

	_, err = opts.UnmarshalFull(
		[]byte(`payload { @type = "any_test.v1.Detail" reason = null code = 1 }`), dynamicpb.NewMessage(desc))
	require.ErrorContains(t, err, `null is not allowed on scalar field "reason"`)
}

const secretPathsProtoSrc = `
syntax = "proto3";
package secretpaths.v1;

import "google/protobuf/any.proto";
import "pxf/secret.proto";

message DB {
  pxf.Secret password = 1;
}

message Holder {
  pxf.Secret pw = 1;
}

message Root {
  DB db = 1;
  repeated DB servers = 2;
  map<string, DB> by_host = 3;
  google.protobuf.Any payload = 4;
  map<string, pxf.Secret> tenant_keys = 5;
  pxf.Secret pw = 6;
}
`

// Every row of the OnSecretField path table (options.go): a Secret is
// reported under the body it sits in, whichever way the body is
// written.
func TestOnSecretField_PathNamesTheBody(t *testing.T) {
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{
				"test.proto":       secretPathsProtoSrc,
				"pxf/secret.proto": secretProtoSrc,
			}),
		}),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	fd := files.FindFileByPath("test.proto")
	desc := fd.Messages().ByName("Root")
	resolver := &testResolver{descs: map[string]protoreflect.MessageDescriptor{
		"secretpaths.v1.Holder": fd.Messages().ByName("Holder"),
	}}

	for _, tc := range []struct{ doc, path string }{
		{`pw = "x"`, "pw"},
		{`db { password = "x" }`, "db.password"},
		{`db = { password = "x" }`, "db.password"},
		{`pw { value = "x" hint = "h" }`, "pw"},
		{`pw = { value = "x" }`, "pw"},
		{`servers = [{ password = "x" }]`, "servers[0].password"},
		{`by_host = { "a": { password = "x" } }`, `by_host["a"].password`},
		{`payload { @type = "secretpaths.v1.Holder" pw = "x" }`, "payload.pw"},
		{`tenant_keys = { "acme": "x" }`, `tenant_keys["acme"]`},
	} {
		for _, full := range []bool{false, true} {
			var got []string
			o := pxf.UnmarshalOptions{TypeResolver: resolver, OnSecretField: func(path, value string) error {
				assert.Equal(t, "x", value, tc.doc)
				got = append(got, path)
				return nil
			}}
			m := dynamicpb.NewMessage(desc)
			if full {
				_, err = o.UnmarshalFull([]byte(tc.doc), m)
			} else {
				err = o.Unmarshal([]byte(tc.doc), m)
			}
			require.NoError(t, err, tc.doc)
			assert.Equal(t, []string{tc.path}, got, "%s (full=%v)", tc.doc, full)
		}
	}
}

const nullMaskNumberProtoSrc = `
syntax = "proto3";
package nullnum.v1;

import "google/protobuf/field_mask.proto";

message Root {
  repeated Elem items = 1;
  map<string, Elem> by_key = 2;
  Elem one = 3;
  google.protobuf.FieldMask _null = 15;
}

message Elem {
  int32 x = 15;
}
`

// Only the root's _null field is skipped by Marshal. An element field
// that shares its number is data and is written.
func TestMarshal_ElementFieldWithNullMaskNumberIsWritten(t *testing.T) {
	desc := compilePlacementSrc(t, nullMaskNumberProtoSrc, "nullnum.v1", "Root")
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(`items = [{ x = 7 }] by_key = { "k": { x = 8 } } one { x = 9 }`), m))
	out, err := pxf.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(out), "x = 7")
	assert.Contains(t, string(out), "x = 8")
	assert.Contains(t, string(out), "x = 9")
}

// The _null mask holds paths of singular fields only (protowire#352,
// owner decision on #170): a null inside a list element is kept in the
// Result and lost once the message goes through protobuf binary, while
// a null on a field reached through singular fields survives.
func TestEqBlock_ElementNullDoesNotSurviveBinary(t *testing.T) {
	desc := eqBlockDesc(t)
	m, res, err := decodeFull(t, desc, `need = 1 items = [{ id = "a" deep = null }] one { deep = null }`)
	require.NoError(t, err)
	require.True(t, res.IsNull("items[0].deep"))
	require.True(t, res.IsNull("one.deep"))

	b, err := proto.Marshal(m)
	require.NoError(t, err)
	back := dynamicpb.NewMessage(desc)
	require.NoError(t, proto.Unmarshal(b, back))
	out, err := pxf.Marshal(back)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(out), "deep = null"), "%s", out)
	assert.Contains(t, string(out), "one {\n  label = \"item-dflt\"\n  deep = null\n}", "%s", out)
	assert.Contains(t, string(out), `id = "a"`, "%s", out)
}
