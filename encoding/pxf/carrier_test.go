// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// The annotation surface (protowire-go#81). PXF spells `required` and
// `default` two ways — the bracket options this binder has always read,
// and the RFC-001 annotation form that lowers into the 1327 carrier
// instead. The two are disjoint by construction, so a schema migrated to
// the annotation form used to bind without error and without
// enforcement: no violation for a missing required field, no default
// substituted, no diagnostic anywhere.
//
// These tests compile both spellings with the reference compiler and
// assert the binder answers the same for each.

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// compileV12 compiles src against the vendored canonical annotation
// declarations (see testdata/schema/README.md). The v1.2 grammar it uses
// — the `@` sigil at a field's tail — parses only with the reference
// compiler fork.
func compileV12(t *testing.T, src string) protoreflect.FileDescriptor {
	t.Helper()
	sources := map[string]string{"annot.proto": src}
	for _, name := range []string{
		"protowire/schema/v1/annotations.proto",
		"protowire/schema/v1/descriptor.proto",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "schema", filepath.FromSlash(name)))
		require.NoError(t, err)
		sources[name] = string(b)
	}
	sources["pxf/annotations.proto"] = annotationsProtoSrc
	sources["pxf/bignum.proto"] = bignumProtoSrc

	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "annot.proto")
	require.NoError(t, err)
	for _, f := range files {
		if f.Path() == "annot.proto" {
			return f
		}
	}
	t.Fatal("annot.proto not found")
	return nil
}

// tryCompileV12 is compileV12 for the cases that must NOT compile: it
// hands back the diagnostic instead of failing the test with it. The
// compiler rejecting an annotation argument is now part of what this
// package relies on, so the message is asserted rather than just the
// absence of a descriptor.
func tryCompileV12(src string) (protoreflect.FileDescriptor, error) {
	sources := map[string]string{
		"annot.proto":           src,
		"pxf/annotations.proto": annotationsProtoSrc,
		"pxf/bignum.proto":      bignumProtoSrc,
	}
	for _, name := range []string{
		"protowire/schema/v1/annotations.proto",
		"protowire/schema/v1/descriptor.proto",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "schema", filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		sources[name] = string(b)
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "annot.proto")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Path() == "annot.proto" {
			return f, nil
		}
	}
	return nil, fmt.Errorf("annot.proto not found")
}

func v12Msg(t *testing.T, src, name string) protoreflect.MessageDescriptor {
	t.Helper()
	md := compileV12(t, src).Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md, "message %q not found", name)
	return md
}

// v12MsgMulti is compileV12 for a schema spread over several files.
func v12MsgMulti(t *testing.T, extra map[string]string, entry, msg string) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{"pxf/annotations.proto": annotationsProtoSrc}
	for _, name := range []string{
		"protowire/schema/v1/annotations.proto",
		"protowire/schema/v1/descriptor.proto",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "schema", filepath.FromSlash(name)))
		require.NoError(t, err)
		sources[name] = string(b)
	}
	for k, v := range extra {
		sources[k] = v
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), entry)
	require.NoError(t, err)
	for _, f := range files {
		if f.Path() == entry {
			md := f.Messages().ByName(protoreflect.Name(msg))
			require.NotNil(t, md)
			return md
		}
	}
	t.Fatalf("%s not found", entry)
	return nil
}

// bothSurfacesSrc declares the same schema twice: once in the bracket
// form the binder has always read, once in the annotation form it could
// not see. Every assertion below runs against both messages and expects
// the same answer.
//
// The two `token` defaults are spelled differently ON PURPOSE, and are
// the one place in this file where that is true. They denote the same
// three bytes. `AnnotationArg.bytes_value` carries a bytes literal's own
// octets, so the annotation form writes them as an escaped byte string;
// `(pxf.default)` is declared `string default = 1315` and a string
// option cannot carry arbitrary bytes, so the bracket form spells them
// in base64 and this package decodes on the way in. Stated in
// protowire/schema/v1/descriptor.proto beside `bytes_value`, and pinned
// as a divergence by TestCarrier_BytesSpellingDiffersByForm below.
const bothSurfacesSrc = `
syntax = "proto3";
package annot.v1;

import "protowire/schema/v1/annotations.proto";
import "pxf/annotations.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_ACTIVE = 1;
}

message Brackets {
  string name = 1 [(pxf.required) = true];
  string role = 2 [(pxf.default) = "viewer"];
  int32 retries = 3 [(pxf.default) = "3"];
  bool enabled = 4 [(pxf.default) = "true"];
  bytes token = 5 [(pxf.default) = "AQID"];
  Status status = 6 [(pxf.default) = "STATUS_ACTIVE"];
  uint64 quota = 7 [(pxf.default) = "18446744073709551615"];
  google.protobuf.Timestamp created_at = 8 [(pxf.default) = "2024-01-15T10:30:00Z"];
  google.protobuf.Duration timeout = 9 [(pxf.default) = "30s"];
  google.protobuf.StringValue nickname = 10 [(pxf.default) = "anon"];
}

message Annotations {
  string name = 1 @required;
  string role = 2 @default("viewer");
  int32 retries = 3 @default(3);
  bool enabled = 4 @default(true);
  bytes token = 5 @default("\001\002\003");
  Status status = 6 @default(STATUS_ACTIVE);
  uint64 quota = 7 @default(18446744073709551615);
  google.protobuf.Timestamp created_at = 8 @default("2024-01-15T10:30:00Z");
  google.protobuf.Duration timeout = 9 @default("30s");
  google.protobuf.StringValue nickname = 10 @default("anon");
}
`

// TestCarrier_EnforcementMatchesBracketForm is the case-for-case
// comparison #81 asks for: decode the same document against both
// spellings and require the same message out of each.
func TestCarrier_EnforcementMatchesBracketForm(t *testing.T) {
	fd := compileV12(t, bothSurfacesSrc)
	brackets := fd.Messages().ByName("Brackets")
	annots := fd.Messages().ByName("Annotations")
	require.NotNil(t, brackets)
	require.NotNil(t, annots)

	const doc = "name = \"n\"\n"

	// UnmarshalFull, not Unmarshal: required-checking and default
	// substitution are postDecode's, and postDecode runs on the Full
	// path (see [UnmarshalFullDescriptor]).
	bm, _, err := pxf.UnmarshalFullDescriptor([]byte(doc), brackets)
	require.NoError(t, err)
	am, _, err := pxf.UnmarshalFullDescriptor([]byte(doc), annots)
	require.NoError(t, err, "the annotation form must bind and enforce, not bind and ignore")

	// Spelled out, so the comparison below cannot pass by both sides
	// being empty — which is exactly what it looked like before the
	// binder read the carrier.
	want := map[string]any{
		"role":     "viewer",
		"retries":  int32(3),
		"enabled":  true,
		"token":    []byte{1, 2, 3},
		"status":   protoreflect.EnumNumber(1),
		"quota":    uint64(18446744073709551615),
		"nickname": "anon",
	}
	for name, w := range want {
		af := annots.Fields().ByName(protoreflect.Name(name))
		got := am.ProtoReflect().Get(af)
		if af.Kind() == protoreflect.MessageKind { // google.protobuf.StringValue
			inner := af.Message().Fields().ByName("value")
			assert.Equal(t, w, got.Message().Get(inner).Interface(), "annotation-form default for %q", name)
			continue
		}
		assert.Equal(t, w, got.Interface(), "annotation-form default for %q", name)
	}

	for _, name := range []string{
		"role", "retries", "enabled", "token", "status", "quota",
		"created_at", "timeout", "nickname",
	} {
		bf := brackets.Fields().ByName(protoreflect.Name(name))
		af := annots.Fields().ByName(protoreflect.Name(name))
		want := bm.ProtoReflect().Get(bf)
		got := am.ProtoReflect().Get(af)
		if bf.Kind() == protoreflect.MessageKind {
			assert.Equal(t,
				prototext.Format(want.Message().Interface()),
				prototext.Format(got.Message().Interface()), "field %q", name)
			continue
		}
		assert.Equal(t, want.Interface(), got.Interface(), "field %q", name)
	}
}

// TestCarrier_BytesSpellingDiffersByForm pins the one exception to #81's
// "same constraint, same meaning, either spelling", and pins it as a
// DIVERGENCE rather than letting it be discovered again downstream.
//
// #81 makes the two spellings agree on the VALUE a constraint denotes.
// It never promised they spell that value with the same characters, and
// for bytes they cannot:
//
//   - `bytes_value` carries the literal's own octets. Decoding them again
//     produces something the author did not write, so this package must
//     not — protowire/schema/v1/descriptor.proto says so beside the
//     member, and trendvidia/protowire#266 is where it was written down.
//   - `(pxf.default)` is `string default = 1315`. A string option has no
//     way to carry arbitrary bytes, so the bracket form needs a text
//     encoding, and base64 is the one this package has always used.
//
// So `@default("AQID")` is four characters and `[(pxf.default) = "AQID"]`
// is the three bytes they encode. Both are correct. Found by
// trendvidia/protocompile#195 measuring this repo's suite against its own
// HEAD, which is the only reason it was caught before release: nothing
// upstream knows the bracket form exists.
//
// This is not the wrapped-value kind of pin that fails when a fix lands.
// It asserts the settled rule, and fails if either half drifts back onto
// the other's encoding.
func TestCarrier_BytesSpellingDiffersByForm(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  bytes escaped = 1 @default("\001\002\003");
  bytes hex     = 2 @default("\x01\x02\x03");
  bytes text    = 3 @default("AQID");
  bytes empty   = 4 @default("");
}`), "M")

	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
	require.NoError(t, err)
	got := func(name string) []byte {
		return msg.ProtoReflect().Get(md.Fields().ByName(protoreflect.Name(name))).Bytes()
	}

	// The two escaped spellings of the same three octets agree, and
	// agree with what the bracket form spells "AQID".
	assert.Equal(t, []byte{1, 2, 3}, got("escaped"))
	assert.Equal(t, []byte{1, 2, 3}, got("hex"))
	assert.Equal(t, []byte{}, got("empty"))

	// And base64 text is text. This is the assertion that fails if a
	// decode layer is ever added back to the carrier path — which is
	// exactly what protocompile#195 caught.
	assert.Equal(t, []byte("AQID"), got("text"),
		"bytes_value is verbatim: a consumer that decodes it reads four characters as three bytes")

	// The bracket form keeps its encoding, so the same three bytes are
	// still written "AQID" there. Both rows below are correct; that they
	// differ is the point.
	bracket := v12Msg(t, annotFile(`
message B {
  bytes token = 1 [(pxf.default) = "AQID"];
}`), "B")
	bmsg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), bracket)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3},
		bmsg.ProtoReflect().Get(bracket.Fields().ByName("token")).Bytes())
}

// TestCarrier_RequiredIsEnforced is the failure #81 names first: an
// absent @required field bound without a word of complaint.
func TestCarrier_RequiredIsEnforced(t *testing.T) {
	fd := compileV12(t, bothSurfacesSrc)

	for _, name := range []string{"Brackets", "Annotations"} {
		t.Run(name, func(t *testing.T) {
			md := fd.Messages().ByName(protoreflect.Name(name))
			require.NotNil(t, md)
			_, _, err := pxf.UnmarshalFullDescriptor([]byte("role = \"r\"\n"), md)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `required field "name" is absent`)
		})
	}
}

// TestCarrier_AccessorsSeeBothSurfaces covers the exported readers that
// layered-config consumers (chameleon) run their own passes with: they
// answer for either spelling, or those consumers keep the #81 gap.
func TestCarrier_AccessorsSeeBothSurfaces(t *testing.T) {
	fd := compileV12(t, bothSurfacesSrc)

	for _, name := range []string{"Brackets", "Annotations"} {
		t.Run(name, func(t *testing.T) {
			md := fd.Messages().ByName(protoreflect.Name(name))
			assert.True(t, pxf.IsRequired(md.Fields().ByName("name")))
			assert.False(t, pxf.IsRequired(md.Fields().ByName("role")))

			def, ok := pxf.Default(md.Fields().ByName("role"))
			assert.True(t, ok)
			assert.Equal(t, "viewer", def)

			_, ok = pxf.Default(md.Fields().ByName("name"))
			assert.False(t, ok)
		})
	}
}

// TestCarrier_SurfacesStayDisjoint pins the rule that makes the two
// spellings safe to read side by side: the compiler emits the one the
// author wrote and synthesizes nothing (RFC-001 §8.5), and this binder
// adds nothing either. A refactor that "helpfully" unified them would
// fail here rather than quietly changing what every port sees on the
// wire.
func TestCarrier_SurfacesStayDisjoint(t *testing.T) {
	fd := compileV12(t, bothSurfacesSrc)

	bracketOpts := fieldOptionNumbers(t, fd.Messages().ByName("Brackets").Fields().ByName("name"))
	assert.Contains(t, bracketOpts, 1314, "bracket form must lower to (pxf.required)")
	assert.NotContains(t, bracketOpts, 1327, "bracket form must not synthesize the annotation carrier")

	annotOpts := fieldOptionNumbers(t, fd.Messages().ByName("Annotations").Fields().ByName("name"))
	assert.Contains(t, annotOpts, 1327, "annotation form must lower to the carrier")
	assert.NotContains(t, annotOpts, 1314, "annotation form must not synthesize (pxf.required)")

	bd := fieldOptionNumbers(t, fd.Messages().ByName("Brackets").Fields().ByName("role"))
	assert.Contains(t, bd, 1315)
	assert.NotContains(t, bd, 1327)
	ad := fieldOptionNumbers(t, fd.Messages().ByName("Annotations").Fields().ByName("role"))
	assert.Contains(t, ad, 1327)
	assert.NotContains(t, ad, 1315)
}

// fieldOptionNumbers reports every extension number present on f's
// options, from either the resolved-extension or the unknown-bytes side.
func fieldOptionNumbers(t *testing.T, f protoreflect.FieldDescriptor) []int {
	t.Helper()
	require.NotNil(t, f)
	opts, _ := f.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return nil
	}
	var nums []int
	rm := opts.ProtoReflect()
	rm.Range(func(ofd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		nums = append(nums, int(ofd.Number()))
		return true
	})
	b := rm.GetUnknown()
	for len(b) > 0 {
		num, _, total := protowire.ConsumeField(b)
		if total < 0 {
			break
		}
		nums = append(nums, int(num))
		b = b[total:]
	}
	return nums
}

// annotFile wraps body in a v1.2 file that imports both annotation
// surfaces, for the one-message placement cases below.
func annotFile(body string) string {
	return `
syntax = "proto3";
package annot.v1;
import "protowire/schema/v1/annotations.proto";
import "pxf/annotations.proto";
import "google/protobuf/struct.proto";
import "google/protobuf/wrappers.proto";
import "pxf/bignum.proto";
` + body
}

// TestCarrier_PlacementRulesApplyToBothSurfaces: the placement rules of
// draft -01 §annotation-extensions are about what a single literal can
// denote, which is a property of the field, not of the spelling. The
// compiler leaves all of these to bind time, so this port is where they
// are caught for either form.
func TestCarrier_PlacementRulesApplyToBothSurfaces(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			"repeated", `message M { repeated string t = 1 @default("x"); }`,
			"@default is not valid on repeated fields",
		},
		{
			"map", `message M { map<string, string> m = 1 @default("x"); }`,
			"@default is not valid on map fields",
		},
		{
			"message type with no literal",
			`message M { google.protobuf.Struct s = 1 @default("x"); }`,
			"@default is not valid on message type google.protobuf.Struct",
		},
		{
			"required on a oneof member",
			`message M { oneof choice { string a = 1 @required; string b = 2; } }`,
			`@required is not valid on a member of oneof "choice"`,
		},
		{
			"two defaults in one oneof",
			`message M { oneof choice { string a = 1 @default("x"); string b = 2 @default("y"); } }`,
			`at most one member of oneof "choice" may carry a default`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := v12Msg(t, annotFile(tc.body), "M")
			vs := pxf.ValidateDescriptor(md)
			require.NotEmpty(t, vs, "annotation form must be checked, not waved through")
			var joined string
			for _, v := range vs {
				joined += v.String() + "\n"
			}
			assert.Contains(t, joined, tc.want)
			// The diagnostic quotes the spelling the author wrote: being
			// told to remove a bracket option a file does not contain is
			// the specific way a two-surface feature wastes an afternoon.
			assert.NotContains(t, joined, "(pxf.default)")
			assert.NotContains(t, joined, "(pxf.required)")
		})
	}
}

// TestCarrier_ListLiteralDefaultIsRejected: a list argument reduces to no
// single literal, so it cannot be applied. The point is that it is
// reported rather than dropped — dropping it is the silent
// non-enforcement #81 is about, and the field it sits on is not always
// one the placement rules already reject.
func TestCarrier_ListLiteralDefaultIsRejected(t *testing.T) {
	md := v12Msg(t, annotFile(`message M { string s = 1 @default(["a", "b"]); }`), "M")
	vs := pxf.ValidateDescriptor(md)
	require.NotEmpty(t, vs)
	assert.Equal(t, pxf.ViolationDefaultOption, vs[0].Kind)
	assert.Contains(t, vs[0].Detail, "a default is exactly one literal")
}

// TestCarrier_ConflictingSurfacesAreRejected pins the coexistence rule.
// The two spellings are disjoint by construction, so a field carrying
// both was assembled by hand. Equal values agree and are applied; unequal
// ones cannot be resolved without the reconciliation RFC-001 §8.5
// forbids, and picking by precedence would attach meaning to which
// spelling the author migrated first — so the schema is rejected, which
// is what draft -01 already does for two defaults on one oneof.
func TestCarrier_ConflictingSurfacesAreRejected(t *testing.T) {
	t.Run("disagree", func(t *testing.T) {
		md := v12Msg(t, annotFile(`message M { string s = 1 [(pxf.default) = "a"] @default("b"); }`), "M")
		vs := pxf.ValidateDescriptor(md)
		require.NotEmpty(t, vs)
		assert.Equal(t, pxf.ViolationDefaultOption, vs[0].Kind)
		assert.Contains(t, vs[0].Detail, "carry different values")
		assert.Contains(t, vs[0].Detail, "must not be reconciled")

		_, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
		require.Error(t, err, "a schema the binder cannot resolve must not decode")
	})

	t.Run("agree", func(t *testing.T) {
		md := v12Msg(t, annotFile(`message M { string s = 1 [(pxf.default) = "a"] @default("a"); }`), "M")
		assert.Empty(t, pxf.ValidateDescriptor(md), "the same value twice is not a disagreement")

		msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
		require.NoError(t, err)
		assert.Equal(t, "a", msg.ProtoReflect().Get(md.Fields().ByName("s")).String())
	})

	t.Run("required in both", func(t *testing.T) {
		md := v12Msg(t, annotFile(`message M { string s = 1 [(pxf.required) = true] @required; }`), "M")
		assert.Empty(t, pxf.ValidateDescriptor(md), "one boolean semantic has one answer")
		_, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `required field "s" is absent`)
	})
}

// TestCarrier_BindCheckScopeReachesImports: the bind-time checks cover
// the whole import closure (draft -01 §schema-constraints, #71), and the
// annotation form is checked over the same scope — an @default misplaced
// in an imported file is reported whether or not any field of the bound
// message refers to it.
func TestCarrier_BindCheckScopeReachesImports(t *testing.T) {
	sources := map[string]string{
		"annot.proto": `
syntax = "proto3";
package annot.v1;
import "lib.proto";
message M { annot.lib.Leaf leaf = 1; }
`,
		"lib.proto": `
syntax = "proto3";
package annot.lib;
import "protowire/schema/v1/annotations.proto";
message Leaf { repeated string tags = 1 @default("x"); }
`,
	}
	md := v12MsgMulti(t, sources, "annot.proto", "M")
	vs := pxf.ValidateDescriptor(md)
	require.NotEmpty(t, vs, "an annotation-form violation in an import must be reported")
	assert.Equal(t, "lib.proto", vs[0].File)
	assert.Contains(t, vs[0].String(), "@default is not valid on repeated fields")
}

// TestCarrier_FloatDefaultKeepsItsFraction covers the one AnnotationArg
// variant the carrier could not previously reach.
//
// `annotation default(value: any)` declares a non-scalar parameter, and
// protocompile up to v0.25.0 routed a number to
// AnnotationArg.double_value only for a declared float parameter or for
// no parameter at all — so a float literal under `any` fell through to
// the integer lowering and `@default(1.5)` arrived as int_value 1. This
// test was written inverted, asserting the truncation and naming the
// issue, so that the upgrade could not land quietly; v0.26.0 fixed it
// (trendvidia/protocompile#149) and the assertion turned over.
//
// It asserts the applied value, not just the reduced literal: the float
// path runs through argLiteral's Fixed64 branch, FormatFloat, and
// ApplyDefault's ParseFloat, and only the decoded field proves all three
// agree.
func TestCarrier_FloatDefaultKeepsItsFraction(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  double d = 1 @default(1.5);
  float  f = 2 @default(2.25);
  double neg = 3 @default(-0.125);
  double small = 4 @default(0.1);
  double whole = 5 @default(2.0);
  double sci = 6 @default(1.5e3);
  double tiny = 7 @default(1e-3);
  double bare = 8 @default(.5);
}`), "M")

	for _, tc := range []struct {
		field, lit string
		want       any
	}{
		{"d", "1.5", 1.5},
		{"f", "2.25", float32(2.25)},
		{"neg", "-0.125", -0.125},
		{"small", "0.1", 0.1},
		// A float literal spelled with a zero fraction stays a float:
		// the lowering follows the literal's spelling, not its value.
		{"whole", "2", 2.0},
		{"sci", "1500", 1500.0},
		{"tiny", "0.001", 0.001},
		{"bare", "0.5", 0.5},
	} {
		t.Run(tc.field, func(t *testing.T) {
			def, ok := pxf.Default(md.Fields().ByName(protoreflect.Name(tc.field)))
			require.True(t, ok)
			assert.Equal(t, tc.lit, def)

			msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
			require.NoError(t, err)
			got := msg.ProtoReflect().Get(md.Fields().ByName(protoreflect.Name(tc.field)))
			assert.Equal(t, tc.want, got.Interface())
		})
	}
}

// TestCarrier_OutOfRangeDefaultKeepsItsValue covers what
// trendvidia/protocompile#165 fixed in v0.27.0: a numeric literal that
// does not convert exactly to a uint64 used to be written as the
// saturated value — `@default(1e100)` as int_value -1, a literal above
// uint64 clamped to MaxUint64 — because buildLiteralArg discarded the
// exactness flag NumberToken.Int returns. It now takes the double route.
//
// This test was the inverted pin that asserted the old wrong answers; it
// fired on the v0.28.0 bump and turned over. Read with
// [TestCarrier_Int64BandIsAmbiguousAboveMaxInt64], which carries what is
// still outstanding.
func TestCarrier_OutOfRangeDefaultKeepsItsValue(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  double huge  = 1 @default(1e100);
  double big   = 2 @default(99999999999999999999999);
  double neg   = 3 @default(-1e100);
  double fits  = 4 @default(1e10);
  uint64 max   = 5 @default(18446744073709551615);
  double fract = 6 @default(1.1);
}`), "M")

	for _, tc := range []struct {
		field, lit string
		want       any
	}{
		{"huge", "1e+100", 1e100},
		{"big", "1e+23", 1e23},

		// Negating a magnitude past MaxInt64 used to flip the sign back:
		// `@default(-18446744073709551615)` arrived as int_value 1.
		{"neg", "-1e+100", -1e100},

		// 1.1 is not exactly representable, and NumberToken.Int's two
		// storage paths used to disagree about it — a literal whose
		// fraction was a negative power of two truncated, one that was
		// not returned zero (protocompile#167).
		{"fract", "1.1", 1.1},

		// v0.29.0 routes a numeric literal by the type of the field it
		// is attached to, so every numeric default on a floating
		// carrier now records double_value — including this one, which
		// converted exactly and used to record int_value. The literal
		// changes spelling; the applied value does not.
		{"fits", "1e+10", 1e10},

		// An integer carrier is untouched by that routing, and uint64
		// max still round-trips exactly through the two's-complement
		// int_value the carrier has always used for it.
		{"max", "18446744073709551615", uint64(18446744073709551615)},
	} {
		t.Run(tc.field, func(t *testing.T) {
			fd := md.Fields().ByName(protoreflect.Name(tc.field))
			def, ok := pxf.Default(fd)
			require.True(t, ok)
			assert.Equal(t, tc.lit, def)

			msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
			require.NoError(t, err)
			assert.Equal(t, tc.want, msg.ProtoReflect().Get(fd).Interface())
		})
	}
}

// TestCarrier_NumericDefaultFollowsTheCarrierType covers what
// trendvidia/protocompile#172 fixed in v0.29.0, and it is a broader rule
// than the issue asked for.
//
// Every literal in (MaxInt64, MaxUint64] used to lower to a negative
// int_value, whatever its spelling, because an exact uint64 conversion is
// reinterpreted through int64 to preserve its bits. The binder recovered
// the author's value only where the annotated field was unsigned. The fix
// routes a numeric argument by the type of the field it is attached to
// rather than by the literal's spelling, so a floating carrier always
// records double_value — which settles the band and, incidentally, moves
// every other numeric default on a float or double field too.
//
// Neither of the two routes #172 proposed was taken; in particular the
// carrier contract did not change, so nothing here reads a new field.
func TestCarrier_NumericDefaultFollowsTheCarrierType(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  double band     = 1 @default(1e19);
  double band_dec = 2 @default(10000000000000000000);
  double edge     = 3 @default(9223372036854775807);
  double whole    = 4 @default(42);
  float  narrow   = 5 @default(1e19);
  uint64 unsigned = 6 @default(1e19);
  int64  signed   = 7 @default(9223372036854775807);
}`), "M")

	for _, tc := range []struct {
		field, lit string
		want       any
	}{
		// The band itself: spelling no longer matters, and both forms
		// now reach the value the author wrote.
		{"band", "1e+19", 1e19},
		{"band_dec", "1e+19", 1e19},
		{"narrow", "1e+19", float32(1e19)},

		// Broader than the band. MaxInt64 converts exactly and used to
		// arrive as that exact integer; on a double carrier it now
		// arrives as the value a double can actually hold, which is
		// what the field will store either way.
		{"edge", "9.223372036854776e+18", 9.223372036854776e+18},
		{"whole", "42", 42.0},

		// Integer carriers keep the routing they had.
		{"unsigned", "10000000000000000000", uint64(10000000000000000000)},
		{"signed", "9223372036854775807", int64(9223372036854775807)},
	} {
		t.Run(tc.field, func(t *testing.T) {
			fd := md.Fields().ByName(protoreflect.Name(tc.field))
			def, ok := pxf.Default(fd)
			require.True(t, ok)
			assert.Equal(t, tc.lit, def)

			msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
			require.NoError(t, err)
			assert.Equal(t, tc.want, msg.ProtoReflect().Get(fd).Interface())
		})
	}
}

// TestCarrier_WrapperCarrierFollowsItsScalar covers what
// trendvidia/protocompile#174 fixed in v0.30.0.
//
// v0.29.0 routed a numeric argument by the annotated field's predeclared
// scalar type, which a message-typed field does not have — so the
// wrappers kept the spelling-based route and with it the
// (MaxInt64, MaxUint64] ambiguity that release had just removed from bare
// scalars. Each wrapper now resolves to the scalar it wraps, so
// google.protobuf.DoubleValue lowers exactly as double does.
//
// Asserted against the bare scalar rather than against a literal string:
// what matters is that the two agree, whatever the scalar rule becomes.
func TestCarrier_WrapperCarrierFollowsItsScalar(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  double                      bare_d = 1 @default(1e19);
  google.protobuf.DoubleValue wrap_d = 2 @default(1e19);
  float                       bare_f = 3 @default(1e19);
  google.protobuf.FloatValue  wrap_f = 4 @default(1e19);
  uint64                      bare_u = 5 @default(1e19);
  google.protobuf.UInt64Value wrap_u = 6 @default(1e19);
  int32                       bare_i = 7 @default(42);
  google.protobuf.Int32Value  wrap_i = 8 @default(42);
}`), "M")

	for _, pair := range []struct{ bare, wrapped string }{
		{"bare_d", "wrap_d"},
		{"bare_f", "wrap_f"},
		// The unsigned wrapper recovered its own value before the fix,
		// through unsignedTarget reaching the inner type, and must keep
		// doing so — the fix maps it to predeclared.UInt64, which the
		// scalar rule leaves alone.
		{"bare_u", "wrap_u"},
		{"bare_i", "wrap_i"},
	} {
		t.Run(pair.wrapped, func(t *testing.T) {
			bare, ok := pxf.Default(md.Fields().ByName(protoreflect.Name(pair.bare)))
			require.True(t, ok)
			wrapped, ok := pxf.Default(md.Fields().ByName(protoreflect.Name(pair.wrapped)))
			require.True(t, ok)
			assert.Equal(t, bare, wrapped, "a wrapper must lower as the scalar it wraps")
		})
	}

	// And the value the band produces is now the one the author wrote.
	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
	require.NoError(t, err)
	inner := func(field string) any {
		fd := md.Fields().ByName(protoreflect.Name(field))
		sub := msg.ProtoReflect().Get(fd).Message()
		return sub.Get(fd.Message().Fields().ByName("value")).Interface()
	}
	assert.Equal(t, 1e19, inner("wrap_d"))
	assert.Equal(t, float32(1e19), inner("wrap_f"))
	assert.Equal(t, uint64(10000000000000000000), inner("wrap_u"))
}

// TestCarrier_OutOfBandDefaultIsDiagnosed covers what
// trendvidia/protocompile#177 fixed in v0.31.0, and is the pin
// TestCarrier_SignedSixtyFourBandWrapsSilently turned into.
//
// A literal in (MaxInt64, MaxUint64] was diagnosed on the 32-bit
// carriers, because the wrapped value still did not fit 32 bits — but on
// int64 / sint64 / sfixed64 the wrap landed inside the type's range, so
// nothing caught it and `@default(1e19)` applied -8446744073709551616.
// Exactly the carriers where the mistake was invisible were the ones that
// went unreported, and int64 is the common case for nanosecond timestamps
// and byte counts.
//
// It could not be caught here: int_value -8446744073709551616 on an int64
// carrier is also what `@default(-8446744073709551616)` produces, which
// is a legal int64 default. The fix is the compile-time bound v0.27.0
// already applied to declared integer parameters, now applied to an
// untyped argument against the type it annotates.
//
// The whole band is asserted, not one literal: below it every carrier
// still takes the value, so a bound that is merely too tight fails here.
func TestCarrier_OutOfBandDefaultIsDiagnosed(t *testing.T) {
	for _, ty := range []string{"int64", "sint64", "sfixed64", "int32"} {
		t.Run(ty, func(t *testing.T) {
			_, err := tryCompileV12(annotFile("message M { " + ty + " x = 1 @default(1e19); }"))
			require.Error(t, err, "a literal past the carrier's range must not compile")
			assert.Contains(t, err.Error(), "is out of range for the annotated type `"+ty+"`")
		})
	}

	// A signed 64-bit carrier still takes everything that does fit,
	// including its own extremes — the bound must reject the band, not
	// the type's range.
	t.Run("in range still compiles", func(t *testing.T) {
		md := v12Msg(t, annotFile(`
message M {
  int64 max = 1 @default(9223372036854775807);
  int64 min = 2 @default(-9223372036854775808);
}`), "M")
		msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
		require.NoError(t, err)
		assert.Equal(t, int64(math.MaxInt64),
			msg.ProtoReflect().Get(md.Fields().ByName("max")).Interface())
		assert.Equal(t, int64(math.MinInt64),
			msg.ProtoReflect().Get(md.Fields().ByName("min")).Interface())
	})

	// And the unsigned 64-bit carriers, for which the band is in range,
	// stay exact: the fix must not trade one carrier's silence for
	// another's false alarm.
	t.Run("unsigned 64-bit is exact", func(t *testing.T) {
		md := v12Msg(t, annotFile(`
message M {
  uint64  a = 1 @default(1e19);
  fixed64 b = 2 @default(18446744073709551615);
}`), "M")
		msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
		require.NoError(t, err)
		assert.Equal(t, uint64(10000000000000000000),
			msg.ProtoReflect().Get(md.Fields().ByName("a")).Interface())
		assert.Equal(t, uint64(18446744073709551615),
			msg.ProtoReflect().Get(md.Fields().ByName("b")).Interface())
	})
}

// TestCarrier_ArbitraryPrecisionCarriersKeepTheirValue covers what
// trendvidia/protocompile#176 fixed in v0.31.0, and is the pin
// TestCarrier_Int64BandSurvivesOnArbitraryPrecisionCarriers turned into.
//
// pxf.BigInt, pxf.Decimal and pxf.BigFloat are message types with no
// predeclared scalar, so an untyped argument has nothing to convert to
// and draft -01 leaves the literal's own type standing: `1e19` is spelled
// as a float, so it lowers to double_value. Before v0.31.0 it took the
// spelling route into int_value and wrapped, and the result was not
// merely imprecise but the wrong SIGN — a negative BigInt, from the one
// type in the schema whose purpose is holding values above int64.
//
// The second half of the fix is this package's. A double_value reduces to
// a PXF literal, and 'g' formatting writes `1e+19`, which parseBigInt and
// parseDecimal reject — so the sign flip would have become a bind-time
// error rather than the value the author wrote. formatCarrierDouble
// renders positionally for those two carriers. Both halves are asserted
// below, because either alone leaves the field wrong.
func TestCarrier_ArbitraryPrecisionCarriersKeepTheirValue(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  pxf.BigInt   i   = 1 @default(1e19);
  pxf.Decimal  d   = 2 @default(1e19);
  pxf.BigFloat f   = 3 @default(1e19);
  pxf.BigInt   ten = 4 @default(1e10);
  pxf.BigInt   sml = 5 @default(42);
  pxf.Decimal  fr  = 6 @default(1.5);
  pxf.BigInt   neg = 7 @default(-1e19);
}`), "M")

	// The literal each carrier reduces to. The exact-decimal carriers get
	// positional notation; BigFloat keeps the exponent, which
	// big.Float.Parse takes.
	for _, tc := range []struct{ field, lit string }{
		{"i", "10000000000000000000"},
		{"d", "10000000000000000000"},
		{"f", "1e+19"},
		{"ten", "10000000000"},
		{"sml", "42"},
		{"fr", "1.5"},
		{"neg", "-10000000000000000000"},
	} {
		def, ok := pxf.Default(md.Fields().ByName(protoreflect.Name(tc.field)))
		require.True(t, ok, "field %q", tc.field)
		assert.Equal(t, tc.lit, def, "field %q", tc.field)
	}

	// And the value that reaches the field, which is what the literal
	// exists to produce. 1e19 is exactly 10^19 in a float64 — 5^19 fits
	// in 53 bits — so "exact" here is a real claim, not a rounding.
	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), md)
	require.NoError(t, err)
	want, _ := new(big.Int).SetString("10000000000000000000", 10)

	bi := md.Fields().ByName("i")
	sub := msg.ProtoReflect().Get(bi).Message()
	assert.False(t, sub.Get(bi.Message().Fields().ByName("negative")).Bool(),
		"the sign flip protocompile#176 recorded must be gone")
	assert.Equal(t, want.Bytes(), sub.Get(bi.Message().Fields().ByName("abs")).Bytes())

	nf := md.Fields().ByName("neg")
	nsub := msg.ProtoReflect().Get(nf).Message()
	assert.True(t, nsub.Get(nf.Message().Fields().ByName("negative")).Bool(),
		"a negative literal is still negative")
	assert.Equal(t, want.Bytes(), nsub.Get(nf.Message().Fields().ByName("abs")).Bytes())

	// Decimal keeps its scale rather than being routed through an
	// integer, so a fractional default is still exact.
	df := md.Fields().ByName("fr")
	dsub := msg.ProtoReflect().Get(df).Message()
	assert.Equal(t, []byte{15}, dsub.Get(df.Message().Fields().ByName("unscaled")).Bytes())
	assert.Equal(t, int32(1), int32(dsub.Get(df.Message().Fields().ByName("scale")).Int()))
}

// TestCarrier_ArbitraryPrecisionBeyondInt64 replaces
// TestCarrier_ArbitraryPrecisionBeyondInt64IsDiagnosed, which pinned the
// edge that could not be reached from this side and said what would fix it:
//
//	Resolving it needs a carrier member that can hold an
//	arbitrary-precision literal, which is a wire-contract change and is
//	open as trendvidia/protowire#263.
//
// protowire#263 landed in schema v1.13: AnnotationArg gained
// big_int_value, decimal_value and big_float_value, and protocompile
// v0.32.0 emits them. A value these types exist to hold now arrives in a
// member that can hold it, rather than as a compile error -- and before
// protocompile v0.31.0, as a NEGATIVE int_value.
func TestCarrier_ArbitraryPrecisionBeyondInt64(t *testing.T) {
	for _, tc := range []struct{ ty, lit, want string }{
		// MaxUint64: one past int_value's range, exact here.
		{"pxf.BigInt", "18446744073709551615", "18446744073709551615"},
		{"pxf.Decimal", "18446744073709551615", "18446744073709551615"},
		// Far beyond any fixed width.
		{"pxf.BigInt", "123456789012345678901234567890",
			"123456789012345678901234567890"},
		{"pxf.BigInt", "-123456789012345678901234567890",
			"-123456789012345678901234567890"},
		// The exact decimal a float64 cannot hold: through double_value
		// this came back as 12345678901234567168.
		{"pxf.Decimal", "12345678901234567890", "12345678901234567890"},
	} {
		t.Run(tc.ty+"/"+tc.lit, func(t *testing.T) {
			md := v12Msg(t, annotFile(
				"message M { "+tc.ty+" x = 1 @default("+tc.lit+"); }"), "M")
			def, ok := pxf.Default(md.Fields().ByName("x"))
			require.True(t, ok, "%s must carry %s", tc.ty, tc.lit)
			assert.Equal(t, tc.want, def)
		})
	}
}

// TestCarrier_MalformedBytesAreSurvivable: the carrier is walked as raw
// wire bytes, and wire bytes on a descriptor are not necessarily
// well-formed — a truncated descriptor set, a hand-assembled one, a
// producer this port has never met. The walk must not panic and must not
// invent an annotation, and a truncated tail must not erase the entries
// that parsed before it.
func TestCarrier_MalformedBytesAreSurvivable(t *testing.T) {
	// One well-formed @required entry, built by hand so the truncation
	// cases below can cut it at any point.
	entry := func(name string) []byte {
		var ann []byte
		ann = protowire.AppendTag(ann, 1, protowire.BytesType) // Annotation.name
		ann = protowire.AppendString(ann, name)
		var list []byte
		list = protowire.AppendTag(list, 1, protowire.BytesType) // AnnotationList.entries
		list = protowire.AppendBytes(list, ann)
		return list
	}
	carrier := func(payload []byte) []byte {
		var b []byte
		b = protowire.AppendTag(b, 1327, protowire.BytesType)
		return protowire.AppendBytes(b, payload)
	}

	// An entry whose name parses and whose remaining bytes do not.
	entryThenGarbage := func(name string) []byte {
		var ann []byte
		ann = protowire.AppendTag(ann, 1, protowire.BytesType)
		ann = protowire.AppendString(ann, name)
		ann = append(ann, 0xff, 0xff)
		var list []byte
		list = protowire.AppendTag(list, 1, protowire.BytesType)
		return protowire.AppendBytes(list, ann)
	}

	good := entry("protowire.schema.v1.required")

	cases := []struct {
		name    string
		unknown []byte
		want    bool // required
	}{
		{"well-formed", carrier(good), true},
		{"empty carrier", carrier(nil), false},
		{"truncated payload", carrier(good[:len(good)-3]), false},
		{"garbage payload", carrier([]byte{0xff, 0xff, 0xff, 0xff}), false},
		{"truncated envelope", carrier(good)[:len(carrier(good))-2], false},
		{"unknown annotation", carrier(entry("protowire.schema.v1.description")), false},
		{"entry with no name", carrier([]byte{0x0a, 0x00}), false},
		{"good entry then garbage", carrier(append(append([]byte{}, good...), 0xff, 0xff)), true},
		{"entry named, then garbled", carrier(entryThenGarbage("protowire.schema.v1.required")), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := synthField(t, tc.unknown)
			assert.NotPanics(t, func() {
				assert.Equal(t, tc.want, pxf.IsRequired(fd))
				_, ok := pxf.Default(fd)
				assert.False(t, ok)
			})
		})
	}
}

// synthField builds a one-field message descriptor whose FieldOptions
// carry unknown as raw bytes, via protodesc rather than the compiler —
// the path a descriptor set loaded from disk takes.
func synthField(t *testing.T, unknown []byte) protoreflect.FieldDescriptor {
	t.Helper()
	opts := &descriptorpb.FieldOptions{}
	opts.ProtoReflect().SetUnknown(unknown)

	syntax := "proto3"
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("syn.proto"),
		Package: proto.String("syn.v1"),
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:    proto.String("s"),
				Number:  proto.Int32(1),
				Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:    descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Options: opts,
			}},
		}},
	}
	file, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)
	return file.Messages().ByName("M").Fields().ByName("s")
}
