// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Map-key spellings (draft-trendvidia-protowire-01 § Entries and Keys;
// trendvidia/protowire#284 and #306, protowire-go#93 / #109 / #123). The
// fixture corpus under testdata/map-keys/ is vendored verbatim from the
// spec repository (trendvidia/protowire testdata/map-keys/, commit
// a0dd520) and is shared by every port; keep the two in sync when the
// spec repo adds fixtures. Its README states each document's verdict:
// the three bool-key documents at the top MUST bind to the keys true and
// false, every document under invalid/ MUST be rejected with an error
// naming the key, and the two fmt-* pairs pin the formatter's key
// spelling and MUST bind as well.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// compileMapKeysProto compiles the fixture schema and returns the named
// message: mapkeys.v1.Flags (map<bool, string> by_flag = 1) or
// mapkeys.v1.Labels (map<string, string> by_label = 1).
func compileMapKeysProto(t *testing.T, name protoreflect.Name) protoreflect.MessageDescriptor {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "map-keys", "bool-keys.proto"))
	require.NoError(t, err)
	fd := compileProtoSources(t, "bool-keys.proto", map[string]string{"bool-keys.proto": string(src)})
	md := fd.Messages().ByName(name)
	require.NotNil(t, md, "message %s", name)
	return md
}

// mapKeysEntryKey finds the key as the document spells it — the token
// before the ':' of a `key: "value"` entry, quotes included when the
// spelling is quoted — so a rejection can be checked to name it.
var mapKeysEntryKey = regexp.MustCompile(`(\S+):\s+"`)

func TestMapKeysFixturesBind(t *testing.T) {
	md := compileMapKeysProto(t, "Flags")
	fd := md.Fields().ByName("by_flag")
	all, err := filepath.Glob(filepath.Join("testdata", "map-keys", "*.pxf"))
	require.NoError(t, err)
	// The fmt-* canonicalization pairs have their own test below.
	var files []string
	for _, path := range all {
		if !strings.HasPrefix(filepath.Base(path), "fmt-") {
			files = append(files, path)
		}
	}
	require.Len(t, files, 3, "the README lists three MUST-bind bool-key documents")
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			// The AST parser admits every spelling too: fmt and
			// validate go through it before the decoder does.
			_, err = pxf.Parse(data)
			require.NoError(t, err)

			msg, err := pxf.UnmarshalDescriptor(data, md)
			require.NoError(t, err)
			m := msg.Get(fd).Map()
			assert.Equal(t, 2, m.Len(), "the document binds exactly the keys true and false")
			assert.True(t, m.Has(protoreflect.ValueOfBool(true).MapKey()), "key true")
			assert.True(t, m.Has(protoreflect.ValueOfBool(false).MapKey()), "key false")
		})
	}
}

func TestMapKeysFixturesReject(t *testing.T) {
	md := compileMapKeysProto(t, "Flags")
	files, err := filepath.Glob(filepath.Join("testdata", "map-keys", "invalid", "*.pxf"))
	require.NoError(t, err)
	require.Len(t, files, 13, "the README lists thirteen MUST-NOT-bind documents")
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			m := mapKeysEntryKey.FindSubmatch(data)
			require.NotNil(t, m, "fixture has one `key: \"value\"` entry")
			key := string(m[1])

			_, err = pxf.UnmarshalDescriptor(data, md)
			require.Error(t, err, "%s must not bind", key)
			assert.Contains(t, err.Error(), "invalid bool map key "+key+" for field \"by_flag\"",
				"the rejection names the key and the field")
		})
	}
}

// mapKeySpellings collects every map entry in doc as key → KeyQuoted,
// wherever it sits.
func mapKeySpellings(doc *pxf.Document) map[string]bool {
	out := map[string]bool{}
	var walk func(entries []pxf.Entry)
	walk = func(entries []pxf.Entry) {
		for _, e := range entries {
			switch n := e.(type) {
			case *pxf.MapEntry:
				out[n.Key] = n.KeyQuoted
				if bv, ok := n.Value.(*pxf.BlockVal); ok {
					walk(bv.Entries)
				}
			case *pxf.Assignment:
				if bv, ok := n.Value.(*pxf.BlockVal); ok {
					walk(bv.Entries)
				}
			case *pxf.Block:
				walk(n.Entries)
			}
		}
	}
	walk(doc.Entries)
	return out
}

// TestMapKeysFmtPairs pins the formatter's key spelling on the spec
// corpus's two fmt pairs (draft -01 § Entries and Keys, "Canonical
// spelling of map keys"; protowire#306, #123): the input formats to
// exactly the expected file, the expected file is a fixed point, and
// both bind to the same keys. fmt-keyword-keys (string-keyed): "true",
// "false", "null" and "123" stay quoted — bare they would be a bool key,
// no key at all, or an integer key — the quoted identifier-safe "plain"
// canonicalizes to bare, bare stays bare. fmt-bare-keys (bool-keyed): a
// bare true and a bare 0 stay bare — the formatter does not add quotes
// the author did not write. Marshal, which has no document spelling to
// keep, quotes a string key exactly where the formatter would keep the
// quotes and writes a bool key bare, so the two agree on every key both
// can produce; the bool pair's `0` is one bare spelling of false, and
// Marshal's `false` is the other.
func TestMapKeysFmtPairs(t *testing.T) {
	strKey := func(s string) protoreflect.MapKey { return protoreflect.ValueOfString(s).MapKey() }
	boolKey := func(b bool) protoreflect.MapKey { return protoreflect.ValueOfBool(b).MapKey() }
	for _, tc := range []struct {
		pair    string
		message protoreflect.Name
		field   protoreflect.Name
		keys    []protoreflect.MapKey
		// marshal is key → quoted as Marshal writes the bound message.
		marshal map[string]bool
	}{
		{"fmt-keyword-keys", "Labels", "by_label", []protoreflect.MapKey{
			strKey("true"), strKey("false"), strKey("null"), strKey("123"), strKey("plain"), strKey("bare")},
			map[string]bool{"true": true, "false": true, "null": true, "123": true, "plain": false, "bare": false}},
		{"fmt-bare-keys", "Flags", "by_flag", []protoreflect.MapKey{boolKey(true), boolKey(false)},
			map[string]bool{"true": false, "false": false}},
	} {
		t.Run(tc.pair, func(t *testing.T) {
			dir := filepath.Join("testdata", "map-keys")
			input, err := os.ReadFile(filepath.Join(dir, tc.pair+".pxf"))
			require.NoError(t, err)
			expected, err := os.ReadFile(filepath.Join(dir, tc.pair+".expected.pxf"))
			require.NoError(t, err)

			doc, err := pxf.Parse(input)
			require.NoError(t, err)
			assert.Equal(t, string(expected), string(pxf.FormatDocument(doc)), "the input formats to the expected file")
			want, err := pxf.Parse(expected)
			require.NoError(t, err)
			assert.Equal(t, string(expected), string(pxf.FormatDocument(want)), "the expected file is a fixed point")

			md := compileMapKeysProto(t, tc.message)
			fd := md.Fields().ByName(tc.field)
			var marshalled []byte
			for _, in := range []struct {
				name string
				data []byte
			}{{"input", input}, {"expected", expected}} {
				msg, err := pxf.UnmarshalDescriptor(in.data, md)
				require.NoError(t, err, in.name)
				m := msg.Get(fd).Map()
				assert.Equal(t, len(tc.keys), m.Len(), "%s binds exactly the listed keys", in.name)
				for _, k := range tc.keys {
					assert.True(t, m.Has(k), "%s: key %v", in.name, k.Interface())
				}
				if marshalled == nil {
					marshalled, err = pxf.Marshal(msg.Interface())
					require.NoError(t, err)
				}
			}
			got, err := pxf.Parse(marshalled)
			require.NoError(t, err)
			assert.Equal(t, tc.marshal, mapKeySpellings(got), "Marshal's key spellings:\n%s", marshalled)
			if tc.message == "Labels" {
				assert.Equal(t, tc.marshal, mapKeySpellings(want), "the expected file spells the string keys as Marshal does")
			}
		})
	}
}

// TestMapEntryBuiltInCodeSpelling pins the spelling of a MapEntry no
// parser produced, so KeyQuoted carries no document spelling (#123): a
// key that lexes as one bare map-key token — an identifier, an integer,
// a bool — is written bare; anything else (the empty key, a key with a
// space, null, a float) is quoted so the output still parses; KeyQuoted
// forces the quotes on an identifier-shaped or integer-shaped key meant
// as a string, and is dropped again where the bare spelling denotes the
// same key.
func TestMapEntryBuiltInCodeSpelling(t *testing.T) {
	entry := func(key string, quoted bool) pxf.Entry {
		return &pxf.MapEntry{Key: key, KeyQuoted: quoted, Value: &pxf.StringVal{Value: "v"}}
	}
	doc := &pxf.Document{Entries: []pxf.Entry{&pxf.Assignment{Key: "m", Value: &pxf.BlockVal{Entries: []pxf.Entry{
		entry("plain", false), entry("404", false), entry("-5", false), entry("true", false),
		entry("", false), entry("my key", false), entry("null", false), entry("1.5", false),
		entry("123", true), entry("true", true), entry("plain", true),
	}}}}}
	want := "m = {\n" +
		"  plain: \"v\"\n" +
		"  404: \"v\"\n" +
		"  -5: \"v\"\n" +
		"  true: \"v\"\n" +
		"  \"\": \"v\"\n" +
		"  \"my key\": \"v\"\n" +
		"  \"null\": \"v\"\n" +
		"  \"1.5\": \"v\"\n" +
		"  \"123\": \"v\"\n" +
		"  \"true\": \"v\"\n" +
		"  plain: \"v\"\n" +
		"}\n"
	got := pxf.FormatDocument(doc)
	assert.Equal(t, want, string(got))
	// It parses back, and the parsed spellings are a fixed point.
	back, err := pxf.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, want, string(pxf.FormatDocument(back)))
}
