// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// A bool default is exactly `true` or `false` (#90).
//
// applyDefaultImpl handled BoolKind with a bare `def == "true"` and no
// validation, so every other spelling silently became false — the
// OPPOSITE of what a schema author writing `[(pxf.default) = "True"]`
// asked for, with no diagnostic at bind time and none at decode time.
// Every other kind parses its literal and errors when it does not fit.
//
// The rule is not new: draft -01 §abnf-grammar gives
// `bool = %s"true" / %s"false"` and
// §booleans-null-and-identifier-values makes the value keywords
// case-sensitive. This is the code matching the ground truth the repo
// already declares, so the near misses below are rejections, not a
// narrowing.
//
// Both spellings and both paths are covered, because the defect was in
// two places: applyDefaultImpl's own BoolKind arm (a plain `bool` field)
// and parseScalarDefault's (a google.protobuf.BoolValue wrapper).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// badBoolLiterals is the set #90 names: each is a plausible thing to
// write, and each used to yield false without a word.
var badBoolLiterals = []string{"TRUE", "True", "FALSE", "False", "1", "0", "yes", "no", "t", "f", "", "notabool"}

// TestBoolDefault_NearMissesAreRejected covers the bracket form on a
// plain bool field — the shape #90 reports.
func TestBoolDefault_NearMissesAreRejected(t *testing.T) {
	for _, lit := range badBoolLiterals {
		t.Run("lit="+lit, func(t *testing.T) {
			md := v12Msg(t, annotFile(
				`message M { bool enabled = 1 [(pxf.default) = "`+lit+`"]; }`), "M")
			_, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
			require.Error(t, err, "%q must not silently become false", lit)
			// The shape the other kinds already use: the literal and the
			// field, both named.
			assert.Contains(t, err.Error(), `invalid default bool "`+lit+`"`)
			assert.Contains(t, err.Error(), `for field "enabled"`)
			assert.Contains(t, err.Error(), `case-sensitive`)
		})
	}
}

// TestBoolDefault_AnnotationSurfaceRejectsToo: #90 is this repo's, not
// the compiler's, so it reaches through the 1327 carrier as well. The
// carrier lowers `@default(true)` to bool_value and this package
// reduces it back to "true"/"false", so the annotation form cannot
// produce a near miss from a bool LITERAL — but it can from a string
// one, which is exactly the mistake a migrating author makes.
func TestBoolDefault_AnnotationSurfaceRejectsToo(t *testing.T) {
	t.Run("string literal on a bool carrier is rejected by the compiler", func(t *testing.T) {
		_, err := tryCompileV12(annotFile(`message M { bool enabled = 1 @default("True"); }`))
		require.Error(t, err, "since protocompile v0.31.0 the kind mismatch is a compile error")
		assert.Contains(t, err.Error(), "string literal")
	})

	// And the bool literals themselves still bind, through the carrier.
	for _, tc := range []struct {
		lit  string
		want bool
	}{{"true", true}, {"false", false}} {
		t.Run("@default("+tc.lit+")", func(t *testing.T) {
			md := v12Msg(t, annotFile(
				`message M { bool enabled = 1 @default(`+tc.lit+`); }`), "M")
			def, ok := pxf.Default(md.Fields().ByName("enabled"))
			require.True(t, ok)
			assert.Equal(t, tc.lit, def)

			msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
			require.NoError(t, err)
			assert.Equal(t, tc.want, msg.ProtoReflect().Get(md.Fields().ByName("enabled")).Bool())
		})
	}
}

// TestBoolDefault_WrapperPathRejectsToo covers parseScalarDefault, the
// second site. A google.protobuf.BoolValue default goes through a
// different function than a plain bool and carried the same bare
// equality — fixing only the one #90 quotes would have left this half
// silently wrong.
func TestBoolDefault_WrapperPathRejectsToo(t *testing.T) {
	for _, lit := range []string{"True", "1", "yes", ""} {
		t.Run("lit="+lit, func(t *testing.T) {
			md := v12Msg(t, annotFile(
				`message M { google.protobuf.BoolValue enabled = 1 [(pxf.default) = "`+lit+`"]; }`), "M")
			_, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
			require.Error(t, err, "the wrapper path must not silently become false either")
			assert.Contains(t, err.Error(), `invalid default bool "`+lit+`"`)
		})
	}

	t.Run("true and false still bind through the wrapper", func(t *testing.T) {
		md := v12Msg(t, annotFile(`
message M {
  google.protobuf.BoolValue on  = 1 [(pxf.default) = "true"];
  google.protobuf.BoolValue off = 2 [(pxf.default) = "false"];
}`), "M")
		msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
		require.NoError(t, err)
		inner := func(name string) bool {
			fd := md.Fields().ByName(protoreflect.Name(name))
			sub := msg.ProtoReflect().Get(fd).Message()
			return sub.Get(fd.Message().Fields().ByName("value")).Bool()
		}
		assert.True(t, inner("on"))
		assert.False(t, inner("off"))
	})
}

// TestBoolDefault_TrueAndFalseStillWork is the regression half: the fix
// must reject the near misses without narrowing what the grammar admits.
func TestBoolDefault_TrueAndFalseStillWork(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  bool on  = 1 [(pxf.default) = "true"];
  bool off = 2 [(pxf.default) = "false"];
}`), "M")

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
	require.NoError(t, err)
	assert.True(t, msg.ProtoReflect().Get(md.Fields().ByName("on")).Bool())
	assert.False(t, msg.ProtoReflect().Get(md.Fields().ByName("off")).Bool())

	// A document value still wins over the default, in both directions —
	// the guard is on the literal, not on the substitution.
	msg, _, err = pxf.UnmarshalFullDescriptor([]byte("on = false\noff = true\n"), md)
	require.NoError(t, err)
	assert.False(t, msg.ProtoReflect().Get(md.Fields().ByName("on")).Bool())
	assert.True(t, msg.ProtoReflect().Get(md.Fields().ByName("off")).Bool())
}

// TestBoolDefault_ExportedApplyDefault covers the entry point layered
// -config consumers reach directly. #90 names it specifically: those
// consumers run their own passes with SkipPostDecode, so a guard that
// lived only in postDecode would miss them.
func TestBoolDefault_ExportedApplyDefault(t *testing.T) {
	md := v12Msg(t, annotFile(`message M { bool enabled = 1; }`), "M")
	fd := md.Fields().ByName("enabled")

	for _, lit := range badBoolLiterals {
		msg := dynamicpb.NewMessage(md)
		err := pxf.ApplyDefault(msg, fd, lit)
		require.Error(t, err, "ApplyDefault(%q) must not silently yield false", lit)
		assert.False(t, msg.Get(fd).Bool(), "the field must be left alone when the literal is rejected")
	}

	msg := dynamicpb.NewMessage(md)
	require.NoError(t, pxf.ApplyDefault(msg, fd, "true"))
	assert.True(t, msg.Get(fd).Bool())

	require.NoError(t, pxf.ApplyDefault(msg, fd, "false"))
	assert.False(t, msg.Get(fd).Bool())
}
