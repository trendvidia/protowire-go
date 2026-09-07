// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func boolMapDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return mapDescs(t).ByName("M")
}

// mapDescs compiles one message per map key type the bool keyword is
// tried against: bool (M), string (S) and int32 (I).
func mapDescs(t *testing.T) protoreflect.MessageDescriptors {
	t.Helper()
	src := map[string]string{"bm.proto": `syntax = "proto3"; package bm;
message M { map<bool, string> m = 1; }
message S { map<string, string> m = 1; }
message I { map<int32, string> m = 1; }`}
	comp := protocompile.Compiler{Resolver: protocompile.WithStandardImports(
		&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(src)})}
	files, err := comp.Compile(context.Background(), "bm.proto")
	require.NoError(t, err)
	return files[0].Messages()
}

// TestBoolMapKeySpellings pins #93 and #109: a bool map key accepts
// exactly the spellings the grammar admits — the keyword true / false
// bare (map-key = … / bool, protowire#284), bare 0 / 1 (an integer key,
// "bool encoded as 0/1") and quoted "true" / "false" (a string key
// parsed as a bool literal) — and nothing strconv.ParseBool would have
// taken on top. The bare/quoted split is what makes this non-obvious,
// so every spelling is tried both ways.
func TestBoolMapKeySpellings(t *testing.T) {
	md := boolMapDesc(t)
	fd := md.Fields().ByName("m")

	// Every spelling strconv.ParseBool accepts, plus the two words.
	spellings := []string{"1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False"}
	// The grammar's answer for each, bare and quoted: the key it binds
	// to, or "" for rejected.
	bare := map[string]string{"1": "true", "0": "false", "true": "true", "false": "false"}
	quoted := map[string]string{"true": "true", "false": "false"}

	for _, sp := range spellings {
		t.Run("bare "+sp, func(t *testing.T) {
			doc := `m = { ` + sp + `: "v" }`
			msg, err := pxf.UnmarshalDescriptor([]byte(doc), md)
			if want, ok := bare[sp]; ok {
				require.NoError(t, err)
				assert.Equal(t, "v", msg.Get(fd).Map().Get(protoreflect.ValueOfBool(want == "true").MapKey()).String())
				return
			}
			// An identifier key on a bool K matches nothing: an
			// identifier names a field, and a map has none.
			require.Error(t, err, "bare %s must not bind", sp)
			assert.Contains(t, err.Error(), "invalid bool map key "+sp+" for field \"m\"")
			assert.Contains(t, err.Error(), `a bool key is true, false, 0, 1, "true" or "false"`)
		})
		t.Run("quoted "+sp, func(t *testing.T) {
			doc := `m = { "` + sp + `": "v" }`
			msg, err := pxf.UnmarshalDescriptor([]byte(doc), md)
			if want, ok := quoted[sp]; ok {
				require.NoError(t, err)
				assert.Equal(t, "v", msg.Get(fd).Map().Get(protoreflect.ValueOfBool(want == "true").MapKey()).String())
				return
			}
			// Includes "1" and "0": an integer literal inside a string is
			// not a bool literal (decided in #93).
			require.Error(t, err, "quoted %q must not bind", sp)
			assert.Contains(t, err.Error(), `invalid bool map key "`+sp+`" for field "m"`)
		})
	}

	// All three admitted spellings of each value land on the same key.
	msg, err := pxf.UnmarshalDescriptor([]byte("m = {\n  1: \"a\"\n  \"false\": \"b\"\n  true: \"c\"\n  false: \"d\"\n}"), md)
	require.NoError(t, err)
	assert.Equal(t, 2, msg.Get(fd).Map().Len())
	assert.Equal(t, "c", msg.Get(fd).Map().Get(protoreflect.ValueOfBool(true).MapKey()).String(), "the later spelling wins, as any duplicate key does")
	assert.Equal(t, "d", msg.Get(fd).Map().Get(protoreflect.ValueOfBool(false).MapKey()).String())
}

// TestBoolKeywordKeyOnOtherKeyTypes pins the other half of #109: the
// keyword is a key on a map<bool,V> field and nothing else (draft -01
// §entries-and-keys). On a string K it is rejected naming the key — the
// string "true" is spelled quoted — and on an integer K it fails the way
// an identifier does.
func TestBoolKeywordKeyOnOtherKeyTypes(t *testing.T) {
	descs := mapDescs(t)

	t.Run("string key", func(t *testing.T) {
		md := descs.ByName("S")
		for _, kw := range []string{"true", "false"} {
			_, err := pxf.UnmarshalDescriptor([]byte(`m = { `+kw+`: "v" }`), md)
			require.Error(t, err, "bare %s on a string key must not bind", kw)
			assert.Contains(t, err.Error(), `invalid string map key `+kw+` for field "m"`)
			assert.Contains(t, err.Error(), `write "`+kw+`" for the string`)
		}
		// Quoted, it is the string.
		msg, err := pxf.UnmarshalDescriptor([]byte(`m = { "true": "v" }`), md)
		require.NoError(t, err)
		assert.Equal(t, "v", msg.Get(md.Fields().ByName("m")).Map().Get(protoreflect.ValueOfString("true").MapKey()).String())
	})
	t.Run("int32 key", func(t *testing.T) {
		md := descs.ByName("I")
		_, err := pxf.UnmarshalDescriptor([]byte(`m = { true: "v" }`), md)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid int32 map key: true")
	})
}

// TestBoolKeywordKeyInParser pins the AST side of #109: fmt and validate
// parse before they decode, so the keyword must be a key there too — and
// only with the ':' tail, exactly as an integer key is.
func TestBoolKeywordKeyInParser(t *testing.T) {
	doc, err := pxf.Parse([]byte("m = {\n  true: \"a\"\n  false: \"b\"\n}\n"))
	require.NoError(t, err)
	require.Len(t, doc.Entries, 1)
	asg, ok := doc.Entries[0].(*pxf.Assignment)
	require.True(t, ok, "got %T", doc.Entries[0])
	blk, ok := asg.Value.(*pxf.BlockVal)
	require.True(t, ok, "got %T", asg.Value)
	require.Len(t, blk.Entries, 2)
	for i, want := range []string{"true", "false"} {
		me, ok := blk.Entries[i].(*pxf.MapEntry)
		require.True(t, ok, "entry %d is %T", i, blk.Entries[i])
		assert.Equal(t, want, me.Key)
	}
	// fmt reproduces the keyword bare.
	assert.Equal(t, "m = {\n  true: \"a\"\n  false: \"b\"\n}\n", string(pxf.FormatDocument(doc)))

	for _, src := range []string{"m = {\n  true = \"a\"\n}\n", "m = {\n  true { }\n}\n"} {
		_, err := pxf.Parse([]byte(src))
		require.Error(t, err, "%q: a bool key takes only the ':' tail", src)
		assert.Contains(t, err.Error(), "identifier or string key, got bool")
	}
	// A bool value is still a value: the keyword at value position is
	// not mistaken for the next entry's key.
	doc, err = pxf.Parse([]byte("flag = true\nother = 1\n"))
	require.NoError(t, err)
	require.Len(t, doc.Entries, 2)
}

// TestBoolMapKeyRoundTrip pins that a map<bool,V> survives encode →
// decode. The encoder has always written the keyword bare; until #109
// the decoder rejected its own output.
func TestBoolMapKeyRoundTrip(t *testing.T) {
	md := boolMapDesc(t)
	fd := md.Fields().ByName("m")
	msg, err := pxf.UnmarshalDescriptor([]byte("m = {\n  1: \"yes\"\n  0: \"no\"\n}"), md)
	require.NoError(t, err)

	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "true: \"yes\"")
	assert.Contains(t, string(out), "false: \"no\"")

	back, err := pxf.UnmarshalDescriptor(out, md)
	require.NoError(t, err, "the decoder accepts the encoder's spelling:\n%s", out)
	assert.Equal(t, "yes", back.Get(fd).Map().Get(protoreflect.ValueOfBool(true).MapKey()).String())
	assert.Equal(t, "no", back.Get(fd).Map().Get(protoreflect.ValueOfBool(false).MapKey()).String())
}
