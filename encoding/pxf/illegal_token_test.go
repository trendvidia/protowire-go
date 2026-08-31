// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// TestIllegalTokenDiagnosticAtEveryValuePosition sweeps the grammar's
// value positions with a token the lexer rejects, and asserts each one
// reports the lexer's diagnostic rather than the shape the decoder
// happened to want (#77).
//
// The sweep is the point: the fix is a guard on the token kind, placed
// wherever a token is interpreted, not a rule about durations. A value
// position added without one shows up here as "expected '{' for …".
func TestIllegalTokenDiagnosticAtEveryValuePosition(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	cases := []struct {
		name string
		in   string
		want string
		pos  string
	}{
		// Every kind of value a field can take.
		{"message field", "dur_field = 5seconds\n", "invalid duration: 5seconds", "1:13"},
		{"string field", "string_field = 5seconds\n", "invalid duration: 5seconds", "1:16"},
		{"integer field", "int32_field = 5sx\n", "invalid duration: 5sx", "1:15"},
		{"bool field", "bool_field = 5seconds\n", "invalid duration: 5seconds", "1:14"},
		{"enum field", "enum_field = 5seconds\n", "invalid duration: 5seconds", "1:14"},
		{"bytes field", "bytes_field = 5seconds\n", "invalid duration: 5seconds", "1:15"},
		{"wrapper field", "nullable_int = 5seconds\n", "invalid duration: 5seconds", "1:16"},
		{"oneof member", "text_choice = 5seconds\n", "invalid duration: 5seconds", "1:15"},
		{"timestamp field", "ts_field = 2020-13-40T00:00:00Z\n", "invalid timestamp: 2020-13-40T00:00:00Z", "1:12"},

		// Inside a block, a list, a map — and where a list or map is
		// expected but a bad token stands in its place.
		{"nested block", "nested_field { value = 5seconds }\n", "invalid duration: 5seconds", "1:24"},
		{"scalar list element", "repeated_string = [5seconds]\n", "invalid duration: 5seconds", "1:20"},
		{"message list element", "repeated_nested = [5seconds]\n", "invalid duration: 5seconds", "1:20"},
		{"list in place of '['", "repeated_string = 5seconds\n", "invalid duration: 5seconds", "1:19"},
		{"map in place of '{'", "string_map = 5seconds\n", "invalid duration: 5seconds", "1:14"},
		{"scalar map value", `string_map = { "k": 5seconds }` + "\n", "invalid duration: 5seconds", "1:21"},
		{"message map value", `nested_map = { "k": 5seconds }` + "\n", "invalid duration: 5seconds", "1:21"},
		{"@dataset row cell", "@dataset test.v1.AllTypes (string_field)\n(5seconds)\n", "invalid duration: 5seconds", "2:2"},

		// Name positions, which keep an expectation of their own
		// (TestIllegalTokenAtNamePosition) but still carry the diagnostic.
		{"entry name", "5seconds = 1\n", "invalid duration: 5seconds", "1:1"},
		{"map key", `string_map = { 5seconds: "x" }` + "\n", "invalid duration: 5seconds", "1:16"},

		// Each of the lexer's other diagnostics reaches the reader by the
		// same route; durations are not special-cased anywhere.
		{"sign", "dur_field = +30s\n", `"+" is valid only in "+inf"`, "1:13"},
		{"sign on a scalar", "int32_field = +30\n", `"+" is valid only in "+inf"`, "1:15"},
		{"stray character", "string_field = $\n", `unexpected character '$'`, "1:16"},
		{"bare @", "string_field = @\n", `"@" must be followed by a directive name`, "1:16"},
		{"string escape", `string_field = "\z"` + "\n", `unknown escape sequence \z`, "1:16"},
		{"unterminated string", "string_field = \"abc\n", "unterminated string", "1:16"},
		{"bytes literal", `bytes_field = b"!!!"` + "\n", "invalid base64 in bytes literal", "1:15"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(tc.in), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), tc.pos+": ", "position preserved")

			// The AST parser reaches the same token through a different
			// code path and must not swallow the diagnostic either.
			_, perr := pxf.Parse([]byte(tc.in))
			require.Error(t, perr)
			assert.Contains(t, perr.Error(), tc.want, "AST parser")
		})
	}
}

// TestIllegalTokenAtNamePosition pins the one place the decoder keeps its
// own expectation in front of the diagnostic. "expected map key" tells a
// reader something a value position's "expected string" would not — that
// the token sits where a key belongs — so the two are reported together.
func TestIllegalTokenAtNamePosition(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	for _, tc := range []struct{ name, in, want string }{
		{"entry name", "5seconds = 1\n", "expected identifier, string, or integer, got invalid duration: 5seconds"},
		{"map key", `string_map = { 5seconds: "x" }` + "\n", "expected map key, got invalid duration: 5seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(tc.in), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// Directive headers name a type or a column, so they read the same
	// way (draft §3.4.1, §3.4.4).
	for _, tc := range []struct{ name, in, want string }{
		{"@type name", "@type 5seconds\n", "expected type name after @type, got invalid duration: 5seconds"},
		{"@dataset type name", "@dataset 5seconds (string_field)\n",
			"expected '(' to start @dataset column list, got invalid duration: 5seconds"},
		{"@dataset column name", "@dataset test.v1.AllTypes (5seconds)\n",
			"@dataset column list must contain at least one field name, got invalid duration: 5seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(tc.in), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// A keyed repeated field's block names its elements, so its entry
	// name is a third position of the same kind (draft -01 §3.13).
	t.Run("keyed entry name", func(t *testing.T) {
		fd := compileKeyedProto(t)
		md := fd.Messages().ByName("Node")
		require.NotNil(t, md)
		_, err := pxf.UnmarshalDescriptor([]byte("children { 5seconds { } }\n"), md)
		require.Error(t, err)
		assert.Contains(t, err.Error(),
			`expected entry name (identifier or string) in keyed field "children", got invalid duration: 5seconds`)
	})
}

// TestIllegalTokenAfterSchemaCheck pins the guard's placement: it belongs
// at the point a token is interpreted, never in front of a schema check.
// A malformed value on a field the message does not have is still an
// unknown field, which is the more useful of the two things wrong.
func TestIllegalTokenAfterSchemaCheck(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	_, err := pxf.UnmarshalDescriptor([]byte("bogus = 5seconds\n"), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "bogus"`)
}

// TestMessageFieldErrorNamesAcceptedForms covers the other half of #77:
// a token that lexes cleanly but can start no value the field admits.
// "5seconds" is one malformed token, but "1.5x" and "2μs" are a valid
// number followed by letters, so they arrive at a Duration field as a
// FLOAT and an INT — and telling their author to open a brace names the
// one form they plainly did not mean.
func TestMessageFieldErrorNamesAcceptedForms(t *testing.T) {
	desc := msgDesc(t, "AllTypes")

	cases := []struct {
		name, in, want string
	}{
		{"duration, fractional", "dur_field = 1.5x\n", `expected a duration literal or '{' for message field "dur_field", got float ("1.5")`},
		{"duration, greek mu", "dur_field = 2μs\n", `expected a duration literal or '{' for message field "dur_field", got integer ("2")`},
		{"duration, plain number", "dur_field = 5\n", `expected a duration literal or '{' for message field "dur_field", got integer ("5")`},
		{"duration, quoted", `dur_field = "5s"` + "\n", `expected a duration literal or '{' for message field "dur_field", got string ("5s")`},
		{"timestamp", "ts_field = 5\n", `expected a timestamp literal or '{' for message field "ts_field", got integer ("5")`},
		{"plain message keeps '{'", "nested_field = 5\n", `expected '{' for message field "nested_field", got integer ("5")`},
		{"list element", "repeated_nested = [5]\n", `expected '{' for repeated message element, got integer ("5")`},
		{"map value", `nested_map = { "k": 5 }` + "\n", `expected '{' for map message value, got integer ("5")`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(tc.in), desc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
