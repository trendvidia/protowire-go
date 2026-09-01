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
  bytes token = 5 @default("AQID");
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

// TestCarrier_ExponentDefaultOutOfInt64RangeWraps is a PIN, in the same
// arrangement that #149 was and for the residue that fix does not reach.
//
// buildLiteralArg discards the exactness flag NumberToken.Int returns, so
// a numeric literal that does not convert to a uint64 is written as the
// saturated value — `@default(1e100)` as int_value -1, an integer literal
// above uint64 as MaxUint64. `1e100` and `1e10` take the same route and
// differ only in whether the value survives it, which is why the #149 fix
// (routing by spelling) does not catch this one.
//
// A consumer cannot recover from it: by the time the carrier is read,
// int_value -1 is indistinguishable from `@default(-1)`. So the binder
// applies what it is given, exactly as it did before v0.26.0, and this
// asserts the wrong answer on purpose so the upgrade that fixes
// trendvidia/protocompile#165 cannot land quietly.
func TestCarrier_ExponentDefaultOutOfInt64RangeWraps(t *testing.T) {
	md := v12Msg(t, annotFile(`
message M {
  double huge = 1 @default(1e100);
  uint64 over = 2 @default(99999999999999999999999);
  double fits = 3 @default(1e10);
  uint64 max  = 4 @default(18446744073709551615);
}`), "M")

	for _, tc := range []struct{ field, want, note string }{
		{"huge", "-1", `protocompile#165: fix landed — expect "1e+100" and update this pin`},
		{"over", "18446744073709551615", "protocompile#165: fix landed — the literal is above uint64 and must not clamp"},

		// The exact conversions either side of the boundary are correct
		// today and must stay correct when #165 is fixed.
		{"fits", "10000000000", ""},
		{"max", "18446744073709551615", ""},
	} {
		t.Run(tc.field, func(t *testing.T) {
			def, ok := pxf.Default(md.Fields().ByName(protoreflect.Name(tc.field)))
			require.True(t, ok)
			assert.Equal(t, tc.want, def, tc.note)
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
