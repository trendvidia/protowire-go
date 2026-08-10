// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// (pxf.default) carries exactly one PXF literal, so it is meaningful only
// on fields one literal can denote. #66 fixed the crash: applyDefaultImpl
// switched on fd.Kind() — StringKind for `repeated string` — and handed a
// scalar to Set, which protoreflect panics on. #68 moved the diagnostic to
// bind time: the placements below are now ViolationDefaultOption from
// ValidateFile, so every entry point rejects the schema before it looks at
// the document, matching how (pxf.key) placements have always been treated
// (draft -01 §annotation-extensions, "Default Placement").
//
// One message per shape so the walker's per-field append is exercised
// independently, and so a test can name a single offending element.
const defaultPlacementProtoSrc = `
syntax = "proto3";
package defaultplacement_test.v1;

import "google/protobuf/duration.proto";
import "pxf/annotations.proto";

message ListScalar {
  repeated string tags = 1 [(pxf.default) = "ignored"];
}

message ListMessage {
  repeated google.protobuf.Duration ds = 1 [(pxf.default) = "5s"];
}

message MapScalar {
  map<string, string> m = 1 [(pxf.default) = "ignored"];
}

message MapMessage {
  map<string, google.protobuf.Duration> m = 1 [(pxf.default) = "5s"];
}

message MessageNonWkt {
  Plain p = 1 [(pxf.default) = "ignored"];
}

message Plain {
  string x = 1;
}
`

// Groups are proto2-only, and they are the one Kind applyDefaultImpl's
// switch leaves to its default arm.
const defaultPlacementGroupProtoSrc = `
syntax = "proto2";
package defaultgroup_test.v1;

import "pxf/annotations.proto";

message WithGroup {
  optional group Sub = 1 [(pxf.default) = "ignored"] {
    optional string x = 2;
  }
}
`

// Everything (pxf.default) is valid on, in one file that MUST bind
// cleanly: singular scalars, an enum, and exactly the message types
// defaultableMessage admits. This is the positive half of the check —
// widening defaultableMessage without widening this file leaves the new
// type untested, and narrowing it breaks this file loudly.
const defaultConformantProtoSrc = `
syntax = "proto3";
package defaultconformant_test.v1;

import "google/protobuf/duration.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";
import "pxf/annotations.proto";
import "pxf/bignum.proto";

enum Role {
  ROLE_UNSPECIFIED = 0;
  ROLE_VIEWER = 1;
}

message Control {
  repeated string tags = 1;
  map<string, string> m = 2;
  string role = 3 [(pxf.default) = "viewer"];
}

message EveryAcceptedPlacement {
  string s = 1 [(pxf.default) = "hello"];
  bool b = 2 [(pxf.default) = "true"];
  int32 i32 = 3 [(pxf.default) = "-1"];
  int64 i64 = 4 [(pxf.default) = "-2"];
  uint32 u32 = 5 [(pxf.default) = "1"];
  uint64 u64 = 6 [(pxf.default) = "2"];
  float f = 7 [(pxf.default) = "1.5"];
  double d = 8 [(pxf.default) = "2.5"];
  bytes by = 9 [(pxf.default) = "aGk="];
  Role r = 10 [(pxf.default) = "ROLE_VIEWER"];

  google.protobuf.Timestamp ts = 11 [(pxf.default) = "2026-01-01T00:00:00Z"];
  google.protobuf.Duration dur = 12 [(pxf.default) = "5s"];

  google.protobuf.BoolValue w_bool = 13 [(pxf.default) = "true"];
  google.protobuf.BytesValue w_bytes = 14 [(pxf.default) = "aGk="];
  google.protobuf.DoubleValue w_double = 15 [(pxf.default) = "1.5"];
  google.protobuf.FloatValue w_float = 16 [(pxf.default) = "2.5"];
  google.protobuf.Int32Value w_i32 = 17 [(pxf.default) = "-1"];
  google.protobuf.Int64Value w_i64 = 18 [(pxf.default) = "-2"];
  google.protobuf.StringValue w_string = 19 [(pxf.default) = "hi"];
  google.protobuf.UInt32Value w_u32 = 20 [(pxf.default) = "1"];
  google.protobuf.UInt64Value w_u64 = 21 [(pxf.default) = "2"];

  pxf.BigInt bi = 22 [(pxf.default) = "42"];
  pxf.Decimal dec = 23 [(pxf.default) = "3.14"];
  pxf.BigFloat bf = 24 [(pxf.default) = "2.718"];
}
`

func compilePlacementSrc(t *testing.T, src, pkg, name string) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":            src,
		"pxf/annotations.proto": annotationsProtoSrc,
		"pxf/bignum.proto":      bignumProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName(pkg + "." + name))
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

func compileDefaultPlacement(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	return compilePlacementSrc(t, defaultPlacementProtoSrc, "defaultplacement_test.v1", name)
}

func compileDefaultConformant(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	return compilePlacementSrc(t, defaultConformantProtoSrc, "defaultconformant_test.v1", name)
}

// defaultPlacementCases is the shape matrix. `want` is the runtime error
// applyDefaultImpl produces; `wantDetail` is the bind-time Violation
// detail. Both are asserted, because the two guards are separate code and
// #68's whole point is that they must agree on which placements are bad.
var defaultPlacementCases = []struct {
	msg        string
	field      string
	def        string
	want       string
	wantDetail string
}{
	{
		msg: "ListScalar", field: "tags", def: "ignored",
		want:       `default values not supported for repeated field "tags"`,
		wantDetail: "(pxf.default) is not valid on repeated fields",
	},
	{
		msg: "ListMessage", field: "ds", def: "5s",
		want:       `default values not supported for repeated field "ds"`,
		wantDetail: "(pxf.default) is not valid on repeated fields",
	},
	{
		msg: "MapScalar", field: "m", def: "ignored",
		want:       `default values not supported for map field "m"`,
		wantDetail: "(pxf.default) is not valid on map fields",
	},
	{
		msg: "MapMessage", field: "m", def: "5s",
		want:       `default values not supported for map field "m"`,
		wantDetail: "(pxf.default) is not valid on map fields",
	},
	{
		msg: "MessageNonWkt", field: "p", def: "ignored",
		want:       `default values not supported for message type defaultplacement_test.v1.Plain (field "p")`,
		wantDetail: "(pxf.default) is not valid on message type defaultplacement_test.v1.Plain",
	},
}

// TestValidateDescriptor_DefaultPlacementViolations is the #68 check: the
// walker reports each bad placement as a ViolationDefaultOption naming the
// field and carrying the authored literal, with no document involved.
func TestValidateDescriptor_DefaultPlacementViolations(t *testing.T) {
	for _, c := range defaultPlacementCases {
		c := c
		t.Run(c.msg, func(t *testing.T) {
			desc := compileDefaultPlacement(t, c.msg)
			element := "defaultplacement_test.v1." + c.msg + "." + c.field

			var got *pxf.Violation
			for _, v := range pxf.ValidateDescriptor(desc) {
				if v.Element == element {
					v := v
					got = &v
				}
			}
			require.NotNil(t, got, "no violation reported for %s", element)

			assert.Equal(t, pxf.ViolationDefaultOption, got.Kind)
			assert.Equal(t, c.def, got.Name, "Name carries the authored literal")
			assert.Equal(t, "test.proto", got.File)
			assert.Contains(t, got.Detail, c.wantDetail)
			assert.Contains(t, got.String(), `invalid (pxf.default) = "`+c.def+`"`)
			assert.Contains(t, got.String(), "draft -01 §annotation-extensions")
		})
	}
}

func TestViolationKind_DefaultOptionString(t *testing.T) {
	assert.Equal(t, "default field option", pxf.ViolationDefaultOption.String())
}

// A group field is the one Kind applyDefaultImpl's switch leaves to its
// default arm, so it needs its own proto2 fixture.
func TestValidateDescriptor_DefaultOnGroupField(t *testing.T) {
	desc := compilePlacementSrc(t, defaultPlacementGroupProtoSrc, "defaultgroup_test.v1", "WithGroup")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 1)
	assert.Equal(t, pxf.ViolationDefaultOption, vs[0].Kind)
	assert.Contains(t, vs[0].Detail, "(pxf.default) is not valid on group fields")
}

// Every entry point rejects the schema, including the non-Full ones that
// never apply defaults at all. This is the compatibility break #68 weighed:
// before it, plain Unmarshal decoded such a schema happily and
// UnmarshalFull only failed when the annotated field was absent.
func TestDecode_DefaultPlacementRejectedByEveryEntryPoint(t *testing.T) {
	for _, c := range defaultPlacementCases {
		c := c
		t.Run(c.msg, func(t *testing.T) {
			desc := compileDefaultPlacement(t, c.msg)

			t.Run("UnmarshalFullDescriptor", func(t *testing.T) {
				_, _, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.wantDetail)
			})

			t.Run("UnmarshalDescriptor", func(t *testing.T) {
				_, err := pxf.UnmarshalDescriptor([]byte(``), desc)
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.wantDetail)
			})

			t.Run("Unmarshal", func(t *testing.T) {
				err := pxf.Unmarshal([]byte(``), dynamicpb.NewMessage(desc))
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.wantDetail)
			})
		})
	}
}

// The pre-#68 escape from the diagnostic — populate the field in every
// document — no longer works. This is the case the issue was filed about:
// a schema that decoded forever while carrying a meaningless annotation.
func TestDecode_DefaultOnRepeatedFieldRejectedEvenWhenPresent(t *testing.T) {
	desc := compileDefaultPlacement(t, "ListScalar")

	_, _, err := pxf.UnmarshalFullDescriptor([]byte(`tags = ["a", "b"]`), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(pxf.default) is not valid on repeated fields")
}

// SkipValidate is the documented escape hatch, and it is all-or-nothing:
// it bypasses the reserved-name check too. With it, the decode-time guard
// from #66 is what remains, so the behaviour is exactly pre-#68.
func TestDecode_SkipValidateBypassesDefaultPlacementCheck(t *testing.T) {
	desc := compileDefaultPlacement(t, "ListScalar")
	opts := pxf.UnmarshalOptions{SkipValidate: true}

	t.Run("present field decodes", func(t *testing.T) {
		msg, _, err := opts.UnmarshalFullDescriptor([]byte(`tags = ["a", "b"]`), desc)
		require.NoError(t, err)
		list := msg.ProtoReflect().Get(desc.Fields().ByName("tags")).List()
		require.Equal(t, 2, list.Len())
		assert.Equal(t, "a", list.Get(0).String())
	})

	t.Run("absent field still hits the runtime guard", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() {
			_, _, err = opts.UnmarshalFullDescriptor([]byte(``), desc)
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `default values not supported for repeated field "tags"`)
	})
}

// ValidateFile walks the whole file, so one bad placement makes every
// message declared beside it unbindable — the same blast radius the
// reserved-name check has always had. Worth pinning: it is the part of the
// break that is easy to miss when reading the issue.
func TestValidateDescriptor_BadPlacementPoisonsWholeFile(t *testing.T) {
	// Plain carries no annotation at all and is not the offending message.
	desc := compileDefaultPlacement(t, "Plain")

	vs := pxf.ValidateDescriptor(desc)
	require.NotEmpty(t, vs, "sibling message inherits the file's violations")

	_, err := pxf.UnmarshalDescriptor([]byte(``), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(pxf.default) is not valid on")
}

// The accepted set binds clean. Guards against a check that is too eager —
// every scalar kind, an enum, and each message type defaultableMessage
// admits.
func TestValidateDescriptor_AcceptedPlacementsAreClean(t *testing.T) {
	desc := compileDefaultConformant(t, "EveryAcceptedPlacement")

	vs := pxf.ValidateDescriptor(desc)
	assert.Empty(t, vs, "conformant placements must not be reported")

	// And the file actually decodes, applying each default.
	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)
	fields := desc.Fields()
	assert.Equal(t, "hello", msg.ProtoReflect().Get(fields.ByName("s")).String())
	assert.Equal(t, int32(-1), int32(msg.ProtoReflect().Get(fields.ByName("i32")).Int()))
	assert.Equal(t, "ROLE_VIEWER", string(fields.ByName("r").Enum().
		Values().ByNumber(msg.ProtoReflect().Get(fields.ByName("r")).Enum()).Name()))
}

// Sanity that the WKT defaults really landed, spelled out rather than
// squeezed into the assertion above.
func TestDecode_AcceptedWktDefaultsApply(t *testing.T) {
	desc := compileDefaultConformant(t, "EveryAcceptedPlacement")
	msg, _, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)

	fields := desc.Fields()
	r := msg.ProtoReflect()

	dur := r.Get(fields.ByName("dur")).Message()
	assert.EqualValues(t, 5, dur.Get(dur.Descriptor().Fields().ByName("seconds")).Int())

	ts := r.Get(fields.ByName("ts")).Message()
	assert.EqualValues(t, 1767225600, ts.Get(ts.Descriptor().Fields().ByName("seconds")).Int())

	ws := r.Get(fields.ByName("w_string")).Message()
	assert.Equal(t, "hi", ws.Get(ws.Descriptor().Fields().ByName("value")).String())

	bi := r.Get(fields.ByName("bi")).Message()
	assert.NotEmpty(t, bi.Get(bi.Descriptor().Fields().ByName("abs")).Bytes())
}

// TestApplyDefault_RepeatedAndMapFieldsError covers the exported entry
// point, which layered-config consumers (chameleon) call directly with
// descriptors the decoder never sees and which ValidateFile therefore
// never guarded. The runtime guard from #66 stays load-bearing for them.
func TestApplyDefault_RepeatedAndMapFieldsError(t *testing.T) {
	for _, c := range defaultPlacementCases {
		c := c
		t.Run(c.msg, func(t *testing.T) {
			desc := compileDefaultPlacement(t, c.msg)
			msg := dynamicpb.NewMessage(desc)
			fd := desc.Fields().ByName(protoreflect.Name(c.field))
			require.NotNil(t, fd)

			var err error
			require.NotPanics(t, func() {
				err = pxf.ApplyDefault(msg, fd, c.def)
			})
			require.Error(t, err)
			assert.Equal(t, c.want, err.Error())
			switch {
			case fd.IsMap():
				assert.Equal(t, 0, msg.Get(fd).Map().Len(), "field left untouched")
			case fd.IsList():
				assert.Equal(t, 0, msg.Get(fd).List().Len(), "field left untouched")
			default:
				assert.False(t, msg.Has(fd), "field left untouched")
			}
		})
	}
}

// The non-Full entry points never call postDecode, so they never apply
// defaults — the singular default is not applied there either. Post-#68
// this is only observable with SkipValidate for a schema whose placements
// are conformant; the pinned fact is about defaults, not validation.
func TestUnmarshalDescriptor_NeverAppliesDefaults(t *testing.T) {
	desc := compileDefaultConformant(t, "Control")

	msg, err := pxf.UnmarshalDescriptor([]byte(``), desc)
	require.NoError(t, err)
	assert.Equal(t, "", msg.ProtoReflect().Get(desc.Fields().ByName("role")).String())
}

// The conformant control still behaves as before: a singular default
// applies, unannotated repeated/map fields are untouched, and a default is
// not an input-set field.
func TestDecode_DefaultOnSingularFieldUnaffected(t *testing.T) {
	desc := compileDefaultConformant(t, "Control")
	msg, res, err := pxf.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)

	fields := desc.Fields()
	assert.Equal(t, "viewer", msg.ProtoReflect().Get(fields.ByName("role")).String())
	assert.Equal(t, 0, msg.ProtoReflect().Get(fields.ByName("tags")).List().Len())
	assert.Equal(t, 0, msg.ProtoReflect().Get(fields.ByName("m")).Map().Len())
	assert.False(t, res.IsSet("role"), "a default is not an input-set field")
}

// protocompile resolves (pxf.*) options into known extension fields, so
// every other test in this file exercises only pxfFieldOptions' fast
// path. protoc-produced descriptors — and anything parsed without
// pxf/annotations.proto in the resolver — carry them as unknown bytes
// instead, which is the fallback branch. Build a descriptor with the
// options as raw wire bytes to reach it, interleaving a varint and a
// fixed64 field so the skip arms run too, and set both annotations so
// the single pass has to fill both accumulators.
//
// The field here is outside any oneof, so the (pxf.required) the walker
// now reads is inert — TestValidateFile_RequiredOnOneofMemberFromUnknownBytes
// is what pins the varint arm's effect.
func TestValidateFile_OptionsFromUnknownBytes(t *testing.T) {
	var raw []byte
	// (pxf.required) = true — varint, read into pxfOptions.required.
	raw = protowire.AppendVarint(protowire.AppendTag(raw, 50000, protowire.VarintType), 1)
	// An unrelated fixed64 nobody reads — skipped.
	raw = protowire.AppendFixed64(protowire.AppendTag(raw, 59999, protowire.Fixed64Type), 7)
	// (pxf.key) = "id" and (pxf.default) = "ignored".
	raw = protowire.AppendString(protowire.AppendTag(raw, 50002, protowire.BytesType), "id")
	raw = protowire.AppendString(protowire.AppendTag(raw, 50001, protowire.BytesType), "ignored")

	opts := &descriptorpb.FieldOptions{}
	opts.ProtoReflect().SetUnknown(protoreflect.RawFields(raw))

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("unknownbytes.proto"),
		Package: proto.String("unknownbytes_test.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("ListScalar"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("tags"),
				Number:   proto.Int32(1),
				JsonName: proto.String("tags"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Options:  opts,
			}},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)

	vs := pxf.ValidateFile(fd)
	require.Len(t, vs, 2, "both annotations read in one options pass: %v", vs)

	byKind := map[pxf.ViolationKind]pxf.Violation{}
	for _, v := range vs {
		byKind[v.Kind] = v
	}

	def, ok := byKind[pxf.ViolationDefaultOption]
	require.True(t, ok, "no ViolationDefaultOption in %v", vs)
	assert.Equal(t, "ignored", def.Name)
	assert.Contains(t, def.Detail, "(pxf.default) is not valid on repeated fields")

	key, ok := byKind[pxf.ViolationKeyOption]
	require.True(t, ok, "no ViolationKeyOption in %v", vs)
	assert.Equal(t, "id", key.Name)
	assert.Contains(t, key.Detail, "(pxf.key) is valid only on repeated message-typed fields")
}

// ValidateFile walks the transitive import closure, so a misplaced
// (pxf.default) on a type declared in an IMPORTED .proto is a bind-time
// violation like any other (#71, spec side trendvidia/protowire#228).
//
// This inverts TestValidateDescriptor_ImportedFileIsNotWalked, which
// pinned the previous file-scoped behaviour. Before, the annotation
// bound clean through Root and fell through to the #66 decode-time
// guard — and only for a document that made `inner` present, so a
// schema whose imported annotated field was populated in every document
// carried a dead annotation forever with no diagnostic. That was the
// one hole left in #68's "bind-time, independently of what any document
// contains" promise, and closing it is what #71 was filed for.
func TestValidateDescriptor_ImportedFileIsWalked(t *testing.T) {
	const importedSrc = `
syntax = "proto3";
package defaultimported_test.v1;

import "pxf/annotations.proto";

message Inner {
  repeated string tags = 1 [(pxf.default) = "ignored"];
}
`
	const rootSrc = `
syntax = "proto3";
package defaultroot_test.v1;

import "imported.proto";

message Root {
  defaultimported_test.v1.Inner inner = 1;
}
`
	sources := map[string]string{
		"test.proto":            rootSrc,
		"imported.proto":        importedSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("defaultroot_test.v1.Root")
	require.NoError(t, err)
	desc := d.(protoreflect.MessageDescriptor)

	// Binding Root reports the violation the imported file declares, and
	// attributes it to the file that declares it rather than to the file
	// that was bound.
	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 1)
	assert.Equal(t, pxf.ViolationDefaultOption, vs[0].Kind)
	assert.Equal(t, "imported.proto", vs[0].File)
	assert.Equal(t, "defaultimported_test.v1.Inner.tags", vs[0].Element)

	// Binding Inner's own file reports the same one violation — the
	// closure of a file that imports nothing is that file.
	inner := desc.Fields().ByName("inner").Message()
	require.Equal(t, protoreflect.FullName("defaultimported_test.v1.Inner"), inner.FullName())
	assert.Equal(t, vs, pxf.ValidateDescriptor(inner))

	// Both documents now fail at bind time, where before only the first
	// failed and only at decode time. The second is the case #71 was
	// filed about: nothing in the document mentions the annotated field.
	for _, doc := range []string{`inner { }`, ``} {
		_, _, err = pxf.UnmarshalFullDescriptor([]byte(doc), desc)
		require.Error(t, err, "document %q", doc)
		assert.Contains(t, err.Error(), "PXF schema violations")
		assert.Contains(t, err.Error(), `invalid (pxf.default) = "ignored"`)
		assert.NotContains(t, err.Error(), "default values not supported",
			"bind-time violation should preempt the #66 decode-time guard")
	}

	// Plain Unmarshal never applied defaults at all, and now rejects too.
	_, err = pxf.UnmarshalDescriptor([]byte(``), desc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PXF schema violations")
}

// The raw-unknown-bytes fallback shared by pxfFieldOptions,
// getStringOption and getBoolOption walks a buffer it does not own. A
// truncated fixed32/fixed64 must end the walk, not slice past the end.
func TestValidateFile_TruncatedUnknownOptionBytes(t *testing.T) {
	build := func(t *testing.T, raw []byte) protoreflect.FileDescriptor {
		t.Helper()
		opts := &descriptorpb.FieldOptions{}
		opts.ProtoReflect().SetUnknown(protoreflect.RawFields(raw))
		fdp := &descriptorpb.FileDescriptorProto{
			Name:    proto.String("truncated.proto"),
			Package: proto.String("truncated_test.v1"),
			Syntax:  proto.String("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{{
				Name: proto.String("M"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:     proto.String("tags"),
					Number:   proto.Int32(1),
					JsonName: proto.String("tags"),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					Options:  opts,
				}},
			}},
		}
		fd, err := protodesc.NewFile(fdp, nil)
		require.NoError(t, err)
		return fd
	}

	cases := []struct {
		name string
		raw  []byte
	}{
		{"fixed32", append(protowire.AppendTag(nil, 59998, protowire.Fixed32Type), 0x01, 0x02)},
		{"fixed64", append(protowire.AppendTag(nil, 59999, protowire.Fixed64Type), 0x01, 0x02, 0x03)},
		{"varint", append(protowire.AppendTag(nil, 59997, protowire.VarintType), 0x80)},
		{"bytes-length", append(protowire.AppendTag(nil, 59996, protowire.BytesType), 0x7f)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd := build(t, c.raw)
			f := fd.Messages().Get(0).Fields().Get(0)
			assert.NotPanics(t, func() {
				pxf.ValidateFile(fd) // pxfFieldOptions
				pxf.Default(f)       // getStringOption
				pxf.KeyFieldName(f)  // getStringOption
				pxf.IsRequired(f)    // getBoolOption
			})
		})
	}
}

// The aggregated error names every offending field, not just the first, so
// a schema author fixes them in one pass.
func TestValidateDescriptor_ReportsAllOffendersAtOnce(t *testing.T) {
	desc := compileDefaultPlacement(t, "ListScalar")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, len(defaultPlacementCases))

	var sb strings.Builder
	for _, v := range vs {
		sb.WriteString(v.String())
		sb.WriteString("\n")
	}
	for _, c := range defaultPlacementCases {
		assert.Contains(t, sb.String(), c.msg+"."+c.field)
	}
}
