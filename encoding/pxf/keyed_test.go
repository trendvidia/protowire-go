// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Keyed repeated fields (draft-trendvidia-protowire-01 §3.13,
// trendvidia/protowire#116 / protowire-go#50). The fixture corpus under
// testdata/keyed/ is vendored verbatim from the spec repository
// (trendvidia/protowire testdata/keyed/, commit 74741a9) and is shared
// by every port; keep the two in sync when the spec repo adds fixtures.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// compileKeyedProto compiles testdata/keyed/keyed.proto (the fixture
// schema) against the pxf annotations declaration.
func compileKeyedProto(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "keyed", "keyed.proto"))
	require.NoError(t, err)
	return compileProtoSources(t, "keyed.proto", map[string]string{
		"keyed.proto":           string(src),
		"pxf/annotations.proto": annotationsProtoSrc,
	})
}

func compileProtoSources(t *testing.T, path string, sources map[string]string) protoreflect.FileDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), path)
	require.NoError(t, err)
	for _, f := range files {
		if f.Path() == path {
			return f
		}
	}
	t.Fatalf("%s not found in compile result", path)
	return nil
}

func readKeyedFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "keyed", name))
	require.NoError(t, err)
	return data
}

// keyedFixtureDesc resolves the fixture's @type directive against the
// compiled keyed.v1 schema.
func keyedFixtureDesc(t *testing.T, fd protoreflect.FileDescriptor, data []byte) (protoreflect.MessageDescriptor, *pxf.Document) {
	t.Helper()
	doc, err := pxf.Parse(data)
	require.NoError(t, err)
	require.NotEmpty(t, doc.TypeURL, "fixture missing @type directive")
	short := doc.TypeURL[strings.LastIndex(doc.TypeURL, ".")+1:]
	desc := fd.Messages().ByName(protoreflect.Name(short))
	require.NotNil(t, desc, "message %q not found for @type %s", short, doc.TypeURL)
	return desc, doc
}

// cleanKeyedFixture strips comment lines and collapses the blank runs
// they leave behind, yielding the byte-exact document the encoder is
// expected to reproduce (the fixtures' comments are documentation, not
// content).
func cleanKeyedFixture(data []byte) string {
	lines := strings.Split(string(data), "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		kept = append(kept, ln)
	}
	s := strings.Join(kept, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(s, "\n") + "\n"
}

func TestKeyedFixturesRoundtrip(t *testing.T) {
	fd := compileKeyedProto(t)
	for _, name := range []string{"roundtrip-keyed.pxf", "roundtrip-quoted.pxf"} {
		t.Run(name, func(t *testing.T) {
			data := readKeyedFixture(t, name)
			desc, doc := keyedFixtureDesc(t, fd, data)

			msg, err := pxf.UnmarshalDescriptor(data, desc)
			require.NoError(t, err)

			out, err := pxf.MarshalOptions{TypeURL: doc.TypeURL}.Marshal(msg)
			require.NoError(t, err)
			assert.Equal(t, cleanKeyedFixture(data), string(out), "decode → encode must reproduce the body")

			// UnmarshalFull agrees, and the schema layer reports no
			// keyed diagnostics on a conforming document.
			_, _, err = pxf.UnmarshalFullDescriptor(data, desc)
			require.NoError(t, err)
			assert.Empty(t, pxf.KeyedDiagnostics(doc, desc))
		})
	}
}

func TestKeyedAnonymousEquivalence(t *testing.T) {
	fd := compileKeyedProto(t)
	anon := readKeyedFixture(t, "anonymous-equivalence.pxf")
	keyed := readKeyedFixture(t, "roundtrip-keyed.pxf")
	desc, doc := keyedFixtureDesc(t, fd, anon)

	anonMsg, err := pxf.UnmarshalDescriptor(anon, desc)
	require.NoError(t, err)
	keyedMsg, err := pxf.UnmarshalDescriptor(keyed, desc)
	require.NoError(t, err)
	assert.True(t, proto.Equal(anonMsg, keyedMsg), "anonymous form must decode to the same message")

	// Encoding the anonymous-form decode canonicalizes to the keyed form.
	out, err := pxf.MarshalOptions{TypeURL: doc.TypeURL}.Marshal(anonMsg)
	require.NoError(t, err)
	assert.Equal(t, cleanKeyedFixture(keyed), string(out))

	// fmt canonicalizes the document itself to the keyed form.
	pxf.CanonicalizeKeyed(doc, desc)
	formatted := pxf.FormatDocument(doc)
	reparsed, err := pxf.Parse(formatted)
	require.NoError(t, err)
	require.Len(t, reparsed.Entries, 3)
	blk, ok := reparsed.Entries[2].(*pxf.Block)
	require.True(t, ok, "children must canonicalize to a keyed block, got %T", reparsed.Entries[2])
	assert.Equal(t, "children", blk.Name)
	require.Len(t, blk.Entries, 2)
}

func TestKeyedRedundantKeyOK(t *testing.T) {
	fd := compileKeyedProto(t)
	data := readKeyedFixture(t, "redundant-key-ok.pxf")
	desc, doc := keyedFixtureDesc(t, fd, data)

	msg, err := pxf.UnmarshalDescriptor(data, desc)
	require.NoError(t, err)
	children := msg.ProtoReflect().Get(desc.Fields().ByName("children")).List()
	require.Equal(t, 1, children.Len())
	elem := children.Get(0).Message()
	assert.Equal(t, "greeting", elem.Get(desc.Fields().ByName("id")).String())
	assert.Empty(t, pxf.KeyedDiagnostics(doc, desc))

	// Encode and fmt both drop the redundant agreeing assignment: the
	// only remaining `id =` is the root's own.
	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(out), "id = "), "output:\n%s", out)

	pxf.CanonicalizeKeyed(doc, desc)
	formatted := string(pxf.FormatDocument(doc))
	assert.Equal(t, 1, strings.Count(formatted, "id = "), "formatted:\n%s", formatted)
}

func TestKeyedAnonymousDuplicateStaysAnonymous(t *testing.T) {
	fd := compileKeyedProto(t)
	data := readKeyedFixture(t, "anonymous-duplicate-ok.pxf")
	desc, doc := keyedFixtureDesc(t, fd, data)

	msg, err := pxf.UnmarshalDescriptor(data, desc)
	require.NoError(t, err)
	children := msg.ProtoReflect().Get(desc.Fields().ByName("children")).List()
	require.Equal(t, 2, children.Len())
	for i := range 2 {
		assert.Equal(t, "dup", children.Get(i).Message().Get(desc.Fields().ByName("id")).String())
	}

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "children = [", "duplicate keys must stay in anonymous form")

	pxf.CanonicalizeKeyed(doc, desc)
	formatted := string(pxf.FormatDocument(doc))
	assert.Contains(t, formatted, "children = [", "fmt must keep the anonymous form")
}

func TestKeyedRejectFixtures(t *testing.T) {
	fd := compileKeyedProto(t)
	cases := map[string]pxf.KeyedErrorKind{
		"err-duplicate-key.pxf":          pxf.KeyedDuplicateKey,
		"err-duplicate-key-spelling.pxf": pxf.KeyedDuplicateKey,
		"err-key-conflict.pxf":           pxf.KeyedKeyConflict,
		"err-empty-key.pxf":              pxf.KeyedEmptyKey,
		"err-empty-key-anonymous.pxf":    pxf.KeyedEmptyKey,
		"err-quoted-name-unkeyed.pxf":    pxf.KeyedQuotedNameUnkeyed,
	}
	for name, kind := range cases {
		t.Run(name, func(t *testing.T) {
			data := readKeyedFixture(t, name)
			desc, doc := keyedFixtureDesc(t, fd, data)

			_, err := pxf.UnmarshalDescriptor(data, desc)
			require.Error(t, err)
			var kerr *pxf.KeyedError
			require.ErrorAs(t, err, &kerr, "want KeyedError, got %v", err)
			assert.Equal(t, kind, kerr.Kind, "error: %v", err)

			_, _, err = pxf.UnmarshalFullDescriptor(data, desc)
			require.Error(t, err)

			// Tolerant path: the document parses; the same violation
			// surfaces as a positioned diagnostic.
			tolDoc, syntaxErrs := pxf.ParseTolerant(data)
			require.Empty(t, syntaxErrs, "reject fixtures are syntactically valid")
			diags := pxf.KeyedDiagnostics(tolDoc, desc)
			require.NotEmpty(t, diags, "expected keyed diagnostics")
			assert.Equal(t, kind, diags[0].Kind, "diagnostic: %v", diags[0].Msg)
			assert.NotZero(t, diags[0].Pos.Line)

			_ = doc
		})
	}
}

func TestKeyedFmtFixtures(t *testing.T) {
	fd := compileKeyedProto(t)
	for _, pair := range []string{"fmt-unquote", "fmt-anonymous-to-keyed"} {
		t.Run(pair, func(t *testing.T) {
			input := readKeyedFixture(t, pair+".pxf")
			expected := readKeyedFixture(t, pair+".expected.pxf")

			desc, doc := keyedFixtureDesc(t, fd, input)
			pxf.CanonicalizeKeyed(doc, desc)
			assert.Equal(t, string(expected), string(pxf.FormatDocument(doc)))

			// The expected file is a fmt fixed point.
			fpDesc, fpDoc := keyedFixtureDesc(t, fd, expected)
			pxf.CanonicalizeKeyed(fpDoc, fpDesc)
			assert.Equal(t, string(expected), string(pxf.FormatDocument(fpDoc)))
		})
	}
}

func TestKeyedDescriptorHelpers(t *testing.T) {
	fd := compileKeyedProto(t)
	node := fd.Messages().ByName("Node")
	doc := fd.Messages().ByName("Doc")

	children := node.Fields().ByName("children")
	require.True(t, pxf.IsKeyed(children))
	keyFd := pxf.KeyField(children)
	require.NotNil(t, keyFd)
	assert.Equal(t, protoreflect.Name("id"), keyFd.Name())
	name, ok := pxf.KeyFieldName(children)
	assert.True(t, ok)
	assert.Equal(t, "id", name)

	assert.False(t, pxf.IsKeyed(node.Fields().ByName("id")))
	assert.Nil(t, pxf.KeyField(node.Fields().ByName("id")))
	assert.False(t, pxf.IsKeyed(doc.Fields().ByName("items")), "items carries no (pxf.key)")
	_, ok = pxf.KeyFieldName(doc.Fields().ByName("items"))
	assert.False(t, ok)
}

// badKeyProtoSrc exercises every (pxf.key) placement rule of draft -01
// §3.13's bind-time checks.
const badKeyProtoSrc = `
syntax = "proto3";
package badkey.v1;

import "pxf/annotations.proto";

message Item {
  string id = 1;
  int32 count = 2;
  repeated string aliases = 3;
}

message Bad {
  repeated string tags = 1 [(pxf.key) = "x"];
  Item single = 2 [(pxf.key) = "id"];
  repeated Item missing = 3 [(pxf.key) = "nope"];
  repeated Item non_string = 4 [(pxf.key) = "count"];
  repeated Item non_singular = 5 [(pxf.key) = "aliases"];
  repeated Item good = 6 [(pxf.key) = "id"];
}
`

func TestKeyedPlacementValidation(t *testing.T) {
	fd := compileProtoSources(t, "badkey.proto", map[string]string{
		"badkey.proto":          badKeyProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	})
	bad := fd.Messages().ByName("Bad")

	violations := pxf.ValidateFile(fd)
	byElement := map[string]pxf.Violation{}
	for _, v := range violations {
		assert.Equal(t, pxf.ViolationKeyOption, v.Kind)
		byElement[v.Element] = v
	}
	for _, field := range []string{"tags", "single", "missing", "non_string", "non_singular"} {
		v, hit := byElement["badkey.v1.Bad."+field]
		assert.True(t, hit, "expected a violation for %s", field)
		assert.NotEmpty(t, v.Detail)
		assert.Contains(t, v.String(), "(pxf.key)")
	}
	assert.NotContains(t, byElement, "badkey.v1.Bad.good")
	assert.Len(t, violations, 5)

	// Invalid placements never resolve to a key field.
	for _, field := range []string{"tags", "single", "missing", "non_string", "non_singular"} {
		assert.Nil(t, pxf.KeyField(bad.Fields().ByName(protoreflect.Name(field))), field)
	}
	require.NotNil(t, pxf.KeyField(bad.Fields().ByName("good")))

	// The per-decode bind check rejects the schema outright.
	_, err := pxf.UnmarshalDescriptor([]byte(`good { a { count = 1 } }`), bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(pxf.key)")
}

func TestKeyedAssignmentSpelling(t *testing.T) {
	fd := compileKeyedProto(t)
	desc := fd.Messages().ByName("Node")

	// `children = { ... }` is the unabbreviated spelling of the keyed
	// block form, and `name = { ... }` of an entry.
	input := `
id = "root"
children = {
  greeting = { type = "Label" }
  counter_row { type = "HBox" }
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)
	children := msg.ProtoReflect().Get(desc.Fields().ByName("children")).List()
	require.Equal(t, 2, children.Len())
	assert.Equal(t, "greeting", children.Get(0).Message().Get(desc.Fields().ByName("id")).String())
	assert.Equal(t, "counter_row", children.Get(1).Message().Get(desc.Fields().ByName("id")).String())

	// A non-block value on a named entry is an error: the element type
	// is a message.
	_, err = pxf.UnmarshalDescriptor([]byte(`children { greeting = 42 }`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block value")

	// fmt normalizes both assignment spellings to block form.
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)
	pxf.CanonicalizeKeyed(doc, desc)
	formatted := string(pxf.FormatDocument(doc))
	assert.Contains(t, formatted, "children {\n")
	assert.Contains(t, formatted, "greeting {\n")
}

func TestKeyedConcatenationAcrossBlocks(t *testing.T) {
	fd := compileKeyedProto(t)
	desc := fd.Messages().ByName("Node")

	// The repeated-field concatenation rule applies unchanged: two
	// bindings concatenate, and duplicate detection is per block.
	input := `
id = "root"
children {
  greeting { type = "Label" }
}
children {
  greeting { type = "HBox" }
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)
	children := msg.ProtoReflect().Get(desc.Fields().ByName("children")).List()
	require.Equal(t, 2, children.Len())
	typeFd := desc.Fields().ByName("type")
	assert.Equal(t, "Label", children.Get(0).Message().Get(typeFd).String())
	assert.Equal(t, "HBox", children.Get(1).Message().Get(typeFd).String())
}

func TestKeyedNestedBlocks(t *testing.T) {
	fd := compileKeyedProto(t)
	desc := fd.Messages().ByName("Node")

	input := `
id = "root"
children {
  outer {
    type = "VBox"
    children {
      inner { type = "Label" }
    }
  }
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)
	childrenFd := desc.Fields().ByName("children")
	idFd := desc.Fields().ByName("id")
	outer := msg.ProtoReflect().Get(childrenFd).List().Get(0).Message()
	assert.Equal(t, "outer", outer.Get(idFd).String())
	inner := outer.Get(childrenFd).List().Get(0).Message()
	assert.Equal(t, "inner", inner.Get(idFd).String())

	// Nested keyed fields re-emit in keyed form, key fields omitted.
	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "id = \"outer\"")
	assert.NotContains(t, string(out), "id = \"inner\"")
	assert.Contains(t, string(out), "inner {")
}

func TestQuotedEntryNameGrammar(t *testing.T) {
	// Quoted entry names parse everywhere (grammar is schema-independent)
	// and round-trip through FormatDocument with their spelling intact.
	input := "regions {\n  \"us-east-1\" {\n    replicas = 3\n  }\n}\n"
	doc, err := pxf.Parse([]byte(input))
	require.NoError(t, err)
	regions, ok := doc.Entries[0].(*pxf.Block)
	require.True(t, ok)
	entry, ok := regions.Entries[0].(*pxf.Block)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", entry.Name)
	assert.True(t, entry.NameQuoted)
	assert.Equal(t, input, string(pxf.FormatDocument(doc)))

	// Assignment spelling with a quoted key.
	doc, err = pxf.Parse([]byte("\"us-east-1\" = { replicas = 3 }\n"))
	require.NoError(t, err)
	a, ok := doc.Entries[0].(*pxf.Assignment)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", a.Key)
	assert.True(t, a.KeyQuoted)

	// Tolerant parse agrees with strict parse on valid input.
	tolDoc, errs := pxf.ParseTolerant([]byte(input))
	require.Empty(t, errs)
	assert.Equal(t, doc.TypeURL, tolDoc.TypeURL)

	// Integer keys remain invalid at entry-name position.
	_, err = pxf.Parse([]byte("42 = 1\n"))
	require.Error(t, err)
	_, err = pxf.Parse([]byte("42 { }\n"))
	require.Error(t, err)
}

func TestKeyedErrorIsTyped(t *testing.T) {
	fd := compileKeyedProto(t)
	desc := fd.Messages().ByName("Node")

	_, err := pxf.UnmarshalDescriptor([]byte("children {\n  a { }\n  a { }\n}\n"), desc)
	require.Error(t, err)
	var kerr *pxf.KeyedError
	require.True(t, errors.As(err, &kerr))
	assert.Equal(t, pxf.KeyedDuplicateKey, kerr.Kind)
	assert.Equal(t, "children", kerr.Field)
	assert.Equal(t, "a", kerr.Key)
	assert.Equal(t, 3, kerr.Pos.Line)
	assert.Contains(t, kerr.Error(), "duplicate key")
	assert.Equal(t, "duplicate key", kerr.Kind.String())
}
