// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// (pxf.default) and (pxf.required) apply in every message body a
// document writes (#161): list elements, map values, keyed entries and
// Any bodies, as well as singular message fields. The post-decode pass
// used to recurse into singular message fields only.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const elemDefaultsProtoSrc = `
syntax = "proto3";
package elemdefaults.v1;

import "google/protobuf/any.proto";
import "google/protobuf/field_mask.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";

message P {
  repeated Item items = 1;
  map<string, Item> by_key = 2;
  repeated Item keyed = 3 [(pxf.key) = "id"];
  google.protobuf.Any payload = 4;
  Item one = 5;
  google.protobuf.FieldMask _null = 15;
}

message Item {
  string id = 1;
  string label = 2 [(pxf.default) = "item-dflt"];
  Deep deep = 3;
  repeated Leaf leaves = 4;
  google.protobuf.Any inner = 5;
  oneof pick {
    string pick_s = 6 [(pxf.default) = "pick-dflt"];
    int32 pick_i = 7;
  }
  google.protobuf.StringValue w = 8 [(pxf.default) = "w-dflt"];
}

message Deep {
  int32 n = 1 [(pxf.required) = true];
  optional int32 r = 2;
}

message Strict {
  optional int32 r = 1 [(pxf.required) = true];
}

message Q {
  repeated Strict strict = 1;
}

// No _null mask and no annotation of its own: only its elements' fields
// make BindRow take the full decoder.
message R {
  repeated Item items = 1;
  map<string, Item> by_key = 2;
}

message Leaf {
  string tag = 1 [(pxf.default) = "leaf-dflt"];
}
`

func elemDefaultsSetup(t *testing.T) (protoreflect.MessageDescriptor, pxf.UnmarshalOptions) {
	t.Helper()
	desc := compilePlacementSrc(t, elemDefaultsProtoSrc, "elemdefaults.v1", "P")
	res := &testResolver{descs: map[string]protoreflect.MessageDescriptor{}}
	msgs := desc.ParentFile().Messages()
	for i := range msgs.Len() {
		res.descs[string(msgs.Get(i).FullName())] = msgs.Get(i)
	}
	return desc, pxf.UnmarshalOptions{TypeResolver: res}
}

// get walks a path of field names, list indexes ("0") and map keys
// ("k"), returning the value at its end.
func get(t *testing.T, m protoreflect.Message, path ...string) protoreflect.Value {
	t.Helper()
	var v protoreflect.Value
	var fd protoreflect.FieldDescriptor
	for i := 0; i < len(path); i++ {
		fd = m.Descriptor().Fields().ByName(protoreflect.Name(path[i]))
		require.NotNil(t, fd, path[i])
		v = m.Get(fd)
		switch {
		case fd.IsList():
			i++
			n := 0
			for _, c := range path[i] {
				n = n*10 + int(c-'0')
			}
			require.Less(t, n, v.List().Len(), strings.Join(path, "."))
			v = v.List().Get(n)
		case fd.IsMap():
			i++
			v = v.Map().Get(protoreflect.ValueOfString(path[i]).MapKey())
		}
		if i < len(path)-1 {
			m = v.Message()
		}
	}
	return v
}

// unpack decodes an Any's value as its own type.
func unpack(t *testing.T, desc protoreflect.MessageDescriptor, anyMsg protoreflect.Message, typeName string) protoreflect.Message {
	t.Helper()
	inner := dynamicpb.NewMessage(desc.ParentFile().Messages().ByName(protoreflect.Name(typeName)))
	b := anyMsg.Get(anyMsg.Descriptor().Fields().ByName("value")).Bytes()
	require.NoError(t, proto.Unmarshal(b, inner))
	return inner
}

func TestElementDefaults_Applied(t *testing.T) {
	desc, opts := elemDefaultsSetup(t)
	for _, tc := range []struct {
		doc  string
		path []string
		want string
	}{
		{`items = [{}]`, []string{"items", "0", "label"}, "item-dflt"},
		{`items = [{ label = "x" }, {}]`, []string{"items", "0", "label"}, "x"},
		{`items = [{ label = "x" }, {}]`, []string{"items", "1", "label"}, "item-dflt"},
		{`items = [{ pick_i = 3 }]`, []string{"items", "0", "pick_s"}, ""}, // a set oneof sibling keeps the default off
		{`items = [{}]`, []string{"items", "0", "pick_s"}, "pick-dflt"},
		{`by_key = { "k": {} }`, []string{"by_key", "k", "label"}, "item-dflt"},
		{`keyed { a {} }`, []string{"keyed", "0", "label"}, "item-dflt"},
		{`items = [{ leaves = [{}, { tag = "t" }] }]`, []string{"items", "0", "leaves", "0", "tag"}, "leaf-dflt"},
		{`items = [{ leaves = [{}, { tag = "t" }] }]`, []string{"items", "0", "leaves", "1", "tag"}, "t"},
		{`by_key = { "k": { leaves = [{}] } }`, []string{"by_key", "k", "leaves", "0", "tag"}, "leaf-dflt"},
		{`one { leaves = [{}] }`, []string{"one", "leaves", "0", "tag"}, "leaf-dflt"},
	} {
		m := dynamicpb.NewMessage(desc)
		_, err := opts.UnmarshalFull([]byte(tc.doc), m)
		require.NoError(t, err, tc.doc)
		assert.Equal(t, tc.want, get(t, m, tc.path...).String(), "%s: %s", tc.doc, strings.Join(tc.path, "."))
	}
}

func TestElementDefaults_AnyBody(t *testing.T) {
	desc, opts := elemDefaultsSetup(t)

	m := dynamicpb.NewMessage(desc)
	_, err := opts.UnmarshalFull([]byte(`payload { @type = "elemdefaults.v1.Item" leaves = [{}] }`), m)
	require.NoError(t, err)
	inner := unpack(t, desc, get(t, m, "payload").Message(), "Item")
	assert.Equal(t, "item-dflt", get(t, inner, "label").String())
	assert.Equal(t, "leaf-dflt", get(t, inner, "leaves", "0", "tag").String())

	// An Any inside a list element.
	m = dynamicpb.NewMessage(desc)
	_, err = opts.UnmarshalFull([]byte(`items = [{ inner = { @type = "elemdefaults.v1.Leaf" } }]`), m)
	require.NoError(t, err)
	leaf := unpack(t, desc, get(t, m, "items", "0", "inner").Message(), "Leaf")
	assert.Equal(t, "leaf-dflt", get(t, leaf, "tag").String())
}

func TestElementDefaults_RequiredChecked(t *testing.T) {
	desc, opts := elemDefaultsSetup(t)
	for _, tc := range []struct{ doc, path string }{
		{`items = [{ deep { n = 1 } }, { deep {} }]`, "items[1].deep.n"},
		{`by_key = { "k": { deep {} } }`, `by_key["k"].deep.n`},
		{`keyed { a { deep {} } }`, "keyed[0].deep.n"},
		{`payload { @type = "elemdefaults.v1.Item" deep {} }`, "payload.deep.n"},
		{`items = [{ inner = { @type = "elemdefaults.v1.Deep" } }]`, "items[0].inner.n"},
	} {
		_, err := opts.UnmarshalFull([]byte(tc.doc), dynamicpb.NewMessage(desc))
		require.ErrorContains(t, err, fmt.Sprintf("required field %q is absent", tc.path), tc.doc)
	}
	// null satisfies required, and its body is not entered.
	_, err := opts.UnmarshalFull([]byte(`items = [{ deep = null }]`), dynamicpb.NewMessage(desc))
	require.NoError(t, err)
}

// SkipPostDecode and Unmarshal apply nothing, in elements or Any bodies.
func TestElementDefaults_NotAppliedWithoutPostDecode(t *testing.T) {
	desc, opts := elemDefaultsSetup(t)
	const doc = `items = [{ deep {} }] payload { @type = "elemdefaults.v1.Item" }`

	skip := opts
	skip.SkipPostDecode = true
	m := dynamicpb.NewMessage(desc)
	_, err := skip.UnmarshalFull([]byte(doc), m)
	require.NoError(t, err)
	assert.Equal(t, "", get(t, m, "items", "0", "label").String())
	assert.Equal(t, "", get(t, unpack(t, desc, get(t, m, "payload").Message(), "Item"), "label").String())

	m = dynamicpb.NewMessage(desc)
	require.NoError(t, opts.Unmarshal([]byte(doc), m))
	assert.Equal(t, "", get(t, m, "items", "0", "label").String())
	assert.Equal(t, "", get(t, unpack(t, desc, get(t, m, "payload").Message(), "Item"), "label").String())
}

// A table's rows read back as the messages they came from when a list,
// map or block cell holds a body with unset annotated fields (#161): the
// writer applies its row rules inside the cell. label (no presence) is
// written as "", w (a wrapper) as null; pick_s, a oneof scalar, is left
// out and reads back as its default, as an unset optional scalar with a
// default does in a row (#146). Every Item here sets pick_i or expects
// pick_s's default.
func TestElementDefaults_WriterCellsReadBack(t *testing.T) {
	desc, opts := elemDefaultsSetup(t)
	cols := []string{"items", "by_key", "one", "keyed"}
	h, err := pxf.AppendDatasetHeader(nil, desc, cols)
	require.NoError(t, err)
	for _, doc := range []string{
		`items = [{ id = "a" pick_i = 1 }, { label = "x" pick_i = 2 w = "y" }]`,
		`by_key = { "k": { pick_i = 1 } } one { pick_i = 0 leaves = [{}] }`,
		`keyed { a { pick_i = 1 } }`,
		`items = [{}]`,
	} {
		m := dynamicpb.NewMessage(desc)
		require.NoError(t, opts.Unmarshal([]byte(doc), m), doc)
		row, err := pxf.AppendDatasetRow(nil, m, cols)
		require.NoError(t, err, doc)

		want := proto.Clone(m).(*dynamicpb.Message)
		if doc == `items = [{}]` {
			// The oneof's default is what the empty element reads back as.
			el := want.Mutable(desc.Fields().ByName("items")).List().Get(0).Message()
			el.Set(el.Descriptor().Fields().ByName("pick_s"), protoreflect.ValueOfString("pick-dflt"))
		}
		tr, err := opts.NewDatasetReader(strings.NewReader(string(h) + string(row)))
		require.NoError(t, err)
		back := dynamicpb.NewMessage(desc)
		require.NoError(t, tr.Scan(back), "%s\n%s", doc, row)
		// A null written for an unset wrapper in a singular sub-message is
		// recorded in the _null mask, as null always is there.
		back.Clear(desc.Fields().ByName("_null"))
		assert.True(t, proto.Equal(want, back), "%s\nrow:  %s\nwant: %v\nback: %v", doc, row, want, back)
	}
}

// Inside a cell, an unset required field with presence is refused, as it
// is in a row: the reader would reject the row.
func TestElementDefaults_WriterRefusesUnsetRequiredInCell(t *testing.T) {
	desc := compilePlacementSrc(t, elemDefaultsProtoSrc, "elemdefaults.v1", "Q")
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(`strict = [{ r = 1 }, {}]`), m))
	_, err := pxf.AppendDatasetRow(nil, m, []string{"strict"})
	require.ErrorContains(t, err, "r is unset, and the field is (pxf.required)")
}

// A row type whose only annotations sit inside elements is bound with
// defaults applied: BindRow takes the full decoder for it.
func TestElementDefaults_BindRowReachesElements(t *testing.T) {
	_, opts := elemDefaultsSetup(t)
	desc := compilePlacementSrc(t, elemDefaultsProtoSrc, "elemdefaults.v1", "R")
	const table = "@dataset elemdefaults.v1.R (items, by_key)\n" +
		"([{ pick_i = 1 }], { \"k\": { leaves = [{}] } })\n"
	tr, err := opts.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	m := dynamicpb.NewMessage(desc)
	require.NoError(t, tr.Scan(m))
	assert.Equal(t, "item-dflt", get(t, m, "items", "0", "label").String())
	assert.Equal(t, "leaf-dflt", get(t, m, "by_key", "k", "leaves", "0", "tag").String())
}
