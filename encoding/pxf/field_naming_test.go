// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Which field a type error names (protowire-go#85).
//
// Two PXF surfaces are typed by a descriptor the document never wrote:
// a well-known wrapper's scalar shorthand, typed by the wrapper's inner
// `value` field, and a map's values, typed by the synthetic map-entry
// message's `value` field. Reporting those sends a reader looking for a
// name their document does not contain — or, worse, to an unrelated
// field of their own that happens to be called `value`.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const fieldNamingProtoSrc = `
syntax = "proto3";
package naming.v1;

import "google/protobuf/wrappers.proto";
import "pxf/secret.proto";

enum Status {
  STATUS_UNSPECIFIED = 0;
  ACTIVE = 1;
}

message Doc {
  // The shorthands typed by a descriptor no document names.
  google.protobuf.Int32Value  count = 1;
  google.protobuf.StringValue label = 2;
  repeated google.protobuf.Int32Value counts = 3;
  map<string, google.protobuf.Int32Value> count_by_key = 4;
  map<string, string> labels = 5;
  map<string, int32> tallies = 6;
  map<string, Status> states = 7;
  pxf.Secret token = 8;

  // Fields whose own descriptor already types them — the control group.
  int32 plain = 9;
  repeated string tags = 10;
  Status state = 11;

  // A field genuinely called "value", to make the confusion concrete.
  string value = 12;
}
`

func fieldNamingDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, "naming.proto", map[string]string{
		"naming.proto":     fieldNamingProtoSrc,
		"pxf/secret.proto": secretProtoSrc,
	})
	md := fd.Messages().ByName("Doc")
	require.NotNil(t, md)
	return md
}

// TestTypeErrorNamesTheFieldTheDocumentWrote sweeps every context where
// the typing descriptor and the named one can differ.
func TestTypeErrorNamesTheFieldTheDocumentWrote(t *testing.T) {
	desc := fieldNamingDesc(t)

	cases := []struct {
		name, in, want string
	}{
		// Wrapper scalar shorthand, in each context a wrapper can appear.
		{"wrapper field", `count = "x"`, `expected integer for field "count"`},
		{"wrapper field, other way", "label = 5", `expected string for field "label"`},
		{"wrapper list element", `counts = ["x"]`, `expected integer for field "counts"`},
		{"wrapper map value", `count_by_key = { "k": "x" }`, `expected integer for field "count_by_key"`},

		// A map's values are typed by the entry message either way.
		{"scalar map value", `labels = { "k": 5 }`, `expected string for field "labels"`},
		{"integer map value", `tallies = { "k": "x" }`, `expected integer for field "tallies"`},
		{"enum map value", "states = { \"k\": 5.5 }", `expected enum name or number for field "states"`},

		// The UTF-8 guard sits inside the same reader.
		{"wrapper utf8", `label = "\xff"`, `invalid UTF-8 in string field "label"`},
		{"map value utf8", `labels = { "k": "\xff" }`, `invalid UTF-8 in string field "labels"`},
		{"secret utf8", `token = "\xff"`, `invalid UTF-8 in string field "token"`},

		// Control: a field that types itself was always named correctly,
		// and must stay that way.
		{"plain scalar", `plain = "x"`, `expected integer for field "plain"`},
		{"list element", "tags = [5]", `expected string for field "tags"`},
		{"enum field", "state = 5.5", `expected enum name or number for field "state"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(tc.in+"\n"), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), `field "value"`,
				"the error names a field the document never wrote")
		})
	}
}

// TestBlockFormStillNamesTheInnerField is the other half of the rule.
// `count { value = "x" }` really does write `value`, so naming it is
// right — the fix must not chase the inner name out of the one place it
// belongs. That form reaches the scalar reader through decodeFields on
// the wrapper's own message, where the field is the one the document
// wrote.
func TestBlockFormStillNamesTheInnerField(t *testing.T) {
	desc := fieldNamingDesc(t)

	for _, in := range []string{
		`count { value = "x" }`,
		`count_by_key = { "k": { value = "x" } }`,
	} {
		t.Run(in, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(in+"\n"), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `expected integer for field "value"`)
		})
	}
}

// TestFieldNamedValueIsNotConfusedWithAWrapper makes the failure mode
// concrete: with the inner name reported, a reader of `count = "x"` who
// went looking for "value" would land on this unrelated string field.
func TestFieldNamedValueIsNotConfusedWithAWrapper(t *testing.T) {
	desc := fieldNamingDesc(t)

	_, err := pxf.UnmarshalDescriptor([]byte("count = \"x\"\n"), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"count"`)

	// The real `value` field still reports itself.
	_, err = pxf.UnmarshalDescriptor([]byte("value = 5\n"), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected string for field "value"`)
}
