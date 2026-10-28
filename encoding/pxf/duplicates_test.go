// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// A singular field, any that is not repeated or a map, is bound at most
// once in one message body (draft-trendvidia-protowire-01 § Entries and
// Keys; trendvidia/protowire#347, option a; #184). The decoder rejects
// the second binding: a second scalar does not replace the first, and a
// second block does not merge into it. The fixture corpus under
// testdata/duplicates/ is vendored verbatim from the spec repository
// (trendvidia/protowire testdata/duplicates/, commit 1ff38dd) and is
// shared by every port; keep the two in sync when the spec repo adds
// fixtures. Its README names the spec repo's driver,
// cmd/pxf/duplicates_test.go, which also runs `pxf validate`; in this
// repo the driver is TestDuplicatesCorpus below, and it runs Parse and
// the decoder only.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// duplicatesVerdicts is each fixture's error substring, "" for accept,
// as the spec repo's corpus driver states them.
var duplicatesVerdicts = map[string]string{
	"err-scalar-twice.pxf":              `field "s" is already set`,
	"err-scalar-same-value.pxf":         `field "s" is already set`,
	"err-wrapper-twice.pxf":             `field "w" is already set`,
	"err-message-twice.pxf":             `field "m" is already set`,
	"err-message-assign-then-block.pxf": `field "m" is already set`,
	"err-nested-scalar-twice.pxf":       `field "a" is already set`,
	"err-null-then-value.pxf":           `field "w" is already set`,
	"err-optional-twice.pxf":            `field "o" is already set`,
	"err-oneof-member-twice.pxf":        `field "km" conflicts with already-set field "km"`,
	"err-oneof-scalar-twice.pxf":        `field "ks" conflicts with already-set field "ks"`,
	"once-per-body.pxf":                 "",
	"repeated-twice.pxf":                "",
}

func TestDuplicatesCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "duplicates")
	src, err := os.ReadFile(filepath.Join(dir, "duplicates.proto"))
	require.NoError(t, err)
	fd := compileProtoSources(t, "duplicates.proto", map[string]string{"duplicates.proto": string(src)})
	md := fd.Messages().ByName("Holder")
	require.NotNil(t, md)

	files, err := filepath.Glob(filepath.Join(dir, "*.pxf"))
	require.NoError(t, err)
	require.Len(t, files, len(duplicatesVerdicts), "every fixture has a verdict")
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			want, ok := duplicatesVerdicts[name]
			require.True(t, ok, "fixture has no verdict")
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			_, err = pxf.Parse(data)
			require.NoError(t, err, "a second binding is legal syntax")
			_, _, err = pxf.UnmarshalFullDescriptor(data, md)
			if want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, want)
		})
	}
}

// The corpus's accept fixtures, decoded: the lists concatenate, and each
// body's binding of a lands where it was written. A map value is a body
// of its own, where the rule holds too.
func TestDuplicatesAcceptValues(t *testing.T) {
	desc := compilePlacementSrc(t, `
syntax = "proto3";
package dupval.v1;
message Inner { int32 a = 1; }
message Holder {
  Inner m = 1;
  Inner n = 2;
  repeated Inner items = 3;
  map<string, Inner> by_key = 4;
}
`, "dupval.v1", "Holder")
	m, err := pxf.UnmarshalDescriptor([]byte(`
m { a = 1 }
n { a = 2 }
items = [{ a = 3 }, { a = 4 }]
items = [{ a = 5 }]
`), desc)
	require.NoError(t, err)
	a := func(v protoreflect.Value) int32 {
		return int32(v.Message().Get(desc.Fields().ByName("m").Message().Fields().ByName("a")).Int())
	}
	assert.Equal(t, int32(1), a(m.Get(desc.Fields().ByName("m"))))
	assert.Equal(t, int32(2), a(m.Get(desc.Fields().ByName("n"))))
	items := m.Get(desc.Fields().ByName("items")).List()
	require.Equal(t, 3, items.Len())
	for i, want := range []int32{3, 4, 5} {
		assert.Equal(t, want, a(items.Get(i)))
	}

	_, err = pxf.UnmarshalDescriptor([]byte(`by_key = { k: { a = 1 a = 2 } }`), desc)
	require.ErrorContains(t, err, `1:23: field "a" is already set`)
}

// The rule in the bodies the corpus does not reach: a list element, a
// keyed element, an Any body opened by @type, and the pxf.Secret
// block the OnSecretField hook decodes on a loop of its own. A position
// pins the error to the second binding.
func TestDuplicatesEveryBody(t *testing.T) {
	desc, opts := anyEntrySetup(t)
	for _, c := range []struct{ doc, want string }{
		{`leaves = [{ n = 1 n = 2 }]`, `1:19: field "n" is already set`},
		{`payload { @type = "anyentry.v1.Leaf" n = 1 n = 2 }`, `1:44: field "n" is already set`},
		{"leaf = null\nleaf { n = 1 }", `2:1: field "leaf" is already set`},
	} {
		t.Run(c.doc, func(t *testing.T) {
			_, err := opts.UnmarshalDescriptor([]byte(c.doc), desc)
			require.ErrorContains(t, err, c.want)
		})
	}

	t.Run("keyed element", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "keyed", "keyed.proto"))
		require.NoError(t, err)
		fd := compileProtoSources(t, "keyed.proto", map[string]string{
			"keyed.proto":           string(data),
			"pxf/annotations.proto": annotationsProtoSrc,
		})
		node := fd.Messages().ByName("Node")
		_, err = pxf.UnmarshalDescriptor([]byte(`children { a { weight = 1 weight = 2 } }`), node)
		require.ErrorContains(t, err, `1:27: field "weight" is already set`)
	})

	t.Run("secret hook", func(t *testing.T) {
		desc := secretDemoDesc(t)
		for _, doc := range []string{
			`db_password { value = "a" value = "b" }`,
			`db_password { hint = "a" hint = "a" }`,
			`db_password { fingerprint = "a" value = "b" fingerprint = "c" }`,
		} {
			var got []recordedSecret
			opts := pxf.UnmarshalOptions{OnSecretField: recorder(&got)}
			_, err := opts.UnmarshalDescriptor([]byte(doc), desc)
			require.ErrorContains(t, err, "is already set", doc)
			assert.LessOrEqual(t, len(got), 1, "the hook sees at most the first value")
		}
	})
}

// The edges of the rule. A keyed element's body opens with its key field
// already set from the entry name, and that is not a binding: one
// explicit, agreeing assignment is accepted, a second is not. A keyed
// field written as two blocks concatenates even when a name repeats,
// since the duplicate-key check is per block (draft -01 § Keyed Repeated
// Fields, Decoding). A map key bound twice is a question the spec left
// open (trendvidia/protowire#396); the decoder keeps the last value, and
// this pins that so the day it changes is seen.
func TestDuplicatesEdges(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "keyed", "keyed.proto"))
	require.NoError(t, err)
	fd := compileProtoSources(t, "keyed.proto", map[string]string{
		"keyed.proto":           string(data),
		"pxf/annotations.proto": annotationsProtoSrc,
	})
	node := fd.Messages().ByName("Node")
	children := node.Fields().ByName("children")
	id := node.Fields().ByName("id")

	t.Run("keyed element key", func(t *testing.T) {
		m, err := pxf.UnmarshalDescriptor([]byte(`children { a { id = "a" weight = 1 } }`), node)
		require.NoError(t, err)
		assert.Equal(t, "a", m.Get(children).List().Get(0).Message().Get(id).String())

		_, err = pxf.UnmarshalDescriptor([]byte(`children { a { id = "a" id = "a" } }`), node)
		require.ErrorContains(t, err, `1:25: field "id" is already set`)
	})

	t.Run("keyed field in two blocks", func(t *testing.T) {
		m, err := pxf.UnmarshalDescriptor([]byte("children { a { weight = 1 } }\nchildren { a { weight = 2 } }"), node)
		require.NoError(t, err)
		list := m.Get(children).List()
		require.Equal(t, 2, list.Len())
		for i := range 2 {
			assert.Equal(t, "a", list.Get(i).Message().Get(id).String())
		}
	})

	t.Run("map key twice", func(t *testing.T) {
		desc := compilePlacementSrc(t, `
syntax = "proto3";
package dupmap.v1;
message M { map<string, string> labels = 1; }
`, "dupmap.v1", "M")
		labels := desc.Fields().ByName("labels")
		for _, doc := range []string{
			`labels = { a: "1" a: "2" }`,
			"labels = { a: \"1\" }\nlabels = { a: \"2\" }",
		} {
			m, err := pxf.UnmarshalDescriptor([]byte(doc), desc)
			require.NoError(t, err, doc)
			got := m.Get(labels).Map()
			assert.Equal(t, 1, got.Len(), doc)
			assert.Equal(t, "2", got.Get(protoreflect.ValueOfString("a").MapKey()).String(), doc)
		}
	})
}

// Unknown fields skipped under DiscardUnknown bind nothing, so a second
// one is skipped as well.
func TestDuplicatesDiscardUnknown(t *testing.T) {
	desc, _ := anyEntrySetup(t)
	_, err := pxf.UnmarshalOptions{DiscardUnknown: true}.UnmarshalDescriptor([]byte(`gone = 1
gone = 2`), desc)
	require.NoError(t, err)
}

// boundFields keeps the first 64 field indexes in a word and the rest in
// a slice it grows; the rule holds on both sides of that seam.
func TestDuplicatesWideMessage(t *testing.T) {
	var src strings.Builder
	src.WriteString("syntax = \"proto3\";\npackage wide.v1;\nmessage W {\n")
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&src, "  int32 f%d = %d;\n", i, i)
	}
	src.WriteString("}\n")
	desc := compilePlacementSrc(t, src.String(), "wide.v1", "W")

	var all strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&all, "f%d = %d\n", i, i)
	}
	m, err := pxf.UnmarshalDescriptor([]byte(all.String()), desc)
	require.NoError(t, err, "each of 200 fields once")
	assert.Equal(t, int32(200), int32(m.Get(desc.Fields().ByName("f200")).Int()))

	for _, i := range []int{1, 64, 65, 128, 129, 200} {
		doc := fmt.Sprintf("f%d = 1\nf3 = 3\nf%d = 2\n", i, i)
		_, err := pxf.UnmarshalDescriptor([]byte(doc), desc)
		require.ErrorContains(t, err, fmt.Sprintf(`3:1: field "f%d" is already set`, i))
	}
}
