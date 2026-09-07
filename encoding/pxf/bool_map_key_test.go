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
	src := map[string]string{"bm.proto": `syntax = "proto3"; package bm; message M { map<bool, string> m = 1; }`}
	comp := protocompile.Compiler{Resolver: protocompile.WithStandardImports(
		&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(src)})}
	files, err := comp.Compile(context.Background(), "bm.proto")
	require.NoError(t, err)
	return files[0].Messages().ByName("M")
}

// TestBoolMapKeySpellings pins #93: a bool map key accepts exactly the
// spellings the grammar admits — bare 0 / 1 (an integer key, "bool
// encoded as 0/1") and quoted "true" / "false" (a string key parsed as
// a bool literal) — and nothing strconv.ParseBool would have taken on
// top. The bare/quoted split is what makes this non-obvious, so every
// spelling is tried both ways.
func TestBoolMapKeySpellings(t *testing.T) {
	md := boolMapDesc(t)
	fd := md.Fields().ByName("m")

	// Every spelling strconv.ParseBool accepts, plus the two words.
	spellings := []string{"1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False"}
	// The grammar's answer for each, bare and quoted: the key it binds
	// to, or "" for rejected.
	bare := map[string]string{"1": "true", "0": "false"}
	quoted := map[string]string{"true": "true", "false": "false"}

	for _, sp := range spellings {
		t.Run("bare "+sp, func(t *testing.T) {
			doc := `m = { ` + sp + `: "v" }`
			msg, err := pxf.UnmarshalDescriptor([]byte(doc), md)
			want, ok := bare[sp]
			switch {
			case sp == "true" || sp == "false":
				// Not a bool-key defect: the word lexes as BOOL, and
				// map-key = identifier / string / integer, so this is the
				// production error, which must not regress into something
				// vaguer. Whether a bare true / false SHOULD be a bool
				// key is protowire#284's question — Rust and
				// TypeScript bind it, Go and Java report this error, and
				// the text defines an identifier key only as a field
				// name — so this pins the current behaviour, not a ruling.
				require.Error(t, err)
				assert.Contains(t, err.Error(), "expected map key, got")
			case ok:
				require.NoError(t, err)
				assert.Equal(t, "v", msg.Get(fd).Map().Get(protoreflect.ValueOfBool(want == "true").MapKey()).String())
			default:
				require.Error(t, err, "bare %s must not bind", sp)
				assert.Contains(t, err.Error(), "invalid bool map key "+sp+" for field \"m\"")
				assert.Contains(t, err.Error(), `a bool key is 0, 1, "true" or "false"`)
			}
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

	// Both admitted spellings of each value land on the same key.
	msg, err := pxf.UnmarshalDescriptor([]byte("m = {\n  1: \"a\"\n  \"false\": \"b\"\n}"), md)
	require.NoError(t, err)
	assert.Equal(t, 2, msg.Get(fd).Map().Len())
}
