// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Bool map-key spellings (draft-trendvidia-protowire-01 § Entries and
// Keys; trendvidia/protowire#284, protowire-go#93 / #109). The fixture
// corpus under testdata/map-keys/ is vendored verbatim from the spec
// repository (trendvidia/protowire testdata/map-keys/, commit 405835b)
// and is shared by every port; keep the two in sync when the spec repo
// adds fixtures. Its README states each document's verdict: the three
// at the top MUST bind to the keys true and false, and every document
// under invalid/ MUST be rejected with an error naming the key.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// compileMapKeysProto compiles the fixture schema, mapkeys.v1.Flags with
// its one field map<bool, string> by_flag = 1.
func compileMapKeysProto(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "map-keys", "bool-keys.proto"))
	require.NoError(t, err)
	fd := compileProtoSources(t, "bool-keys.proto", map[string]string{"bool-keys.proto": string(src)})
	md := fd.Messages().ByName("Flags")
	require.NotNil(t, md)
	return md
}

// mapKeysEntryKey finds the key as the document spells it — the token
// before the ':' of a `key: "value"` entry, quotes included when the
// spelling is quoted — so a rejection can be checked to name it.
var mapKeysEntryKey = regexp.MustCompile(`(\S+):\s+"`)

func TestMapKeysFixturesBind(t *testing.T) {
	md := compileMapKeysProto(t)
	fd := md.Fields().ByName("by_flag")
	files, err := filepath.Glob(filepath.Join("testdata", "map-keys", "*.pxf"))
	require.NoError(t, err)
	require.Len(t, files, 3, "the README lists three MUST-bind documents")
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
	md := compileMapKeysProto(t)
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
