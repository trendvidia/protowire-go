// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// layeredAnnotatedProtoSrc declares a schema with every scalar kind we
// care about, each annotated with (pxf.required) and (pxf.default), so
// the tests can drive ApplyDefault / IsRequired / Default through
// every path in the kind-dispatch switch.
const layeredAnnotatedProtoSrc = `
syntax = "proto3";
package layered_test.v1;

import "pxf/annotations.proto";

message AllScalars {
  string s = 1 [(pxf.required) = true, (pxf.default) = "hello"];
  bool b = 2 [(pxf.default) = "true"];
  int32 i32 = 3 [(pxf.default) = "32"];
  int64 i64 = 4 [(pxf.default) = "64"];
  uint32 u32 = 5 [(pxf.default) = "320"];
  uint64 u64 = 6 [(pxf.default) = "640"];
  float f32 = 7 [(pxf.default) = "1.5"];
  double f64 = 8 [(pxf.default) = "3.14"];
  bytes raw = 9 [(pxf.default) = "aGVsbG8="];

  Color color = 10 [(pxf.default) = "RED"];
  Color color_by_num = 11 [(pxf.default) = "2"];

  string no_default = 12;
  string optional_no_anno = 13;
}

enum Color {
  UNKNOWN = 0;
  RED = 1;
  BLUE = 2;
}

message Nested {
  AllScalars inner = 1;
  string outer = 2 [(pxf.required) = true];
}
`

func compileLayeredProto(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":            layeredAnnotatedProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("layered_test.v1.AllScalars")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// --- map-value WKT shorthand for the WKTs not covered in
//     bignum_test.go or secret_test.go: Timestamp, Duration, wrapper
//     types, BigFloat. Round-trips both decode and encode.

const wktMapsProtoSrc = `
syntax = "proto3";
package wktmaps_test.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";
import "pxf/bignum.proto";

message WKTMaps {
  map<string, google.protobuf.Timestamp> when = 1;
  map<string, google.protobuf.Duration> howlong = 2;
  map<string, google.protobuf.StringValue> labels = 3;
  map<string, google.protobuf.Int32Value> counts = 4;
  map<string, pxf.BigFloat> ratios = 5;
}
`

func compileWKTMaps(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":       wktMapsProtoSrc,
		"pxf/bignum.proto": bignumProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("wktmaps_test.v1.WKTMaps")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

func TestMapValueShorthand_TimestampDurationWrappersBigFloat(t *testing.T) {
	desc := compileWKTMaps(t)
	input := `
when = {
  "start": 2026-05-09T12:00:00Z
  "end":   2026-05-10T18:30:00Z
}
howlong = {
  "fast": 100ms
  "slow": 5s
}
labels = {
  "env": "prod"
  "tier": "free"
}
counts = {
  "errors": 42
  "warnings": 7
}
ratios = {
  "small": 0.5
  "large": 1024.0
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	// Spot-check each map's size.
	for _, name := range []string{"when", "howlong", "labels", "counts", "ratios"} {
		fd := msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
		require.Equal(t, 2, msg.ProtoReflect().Get(fd).Map().Len(), "map %q size", name)
	}

	// Round-trip: re-emit and verify scalar shorthand survives, not block form.
	out, err := pxf.Marshal(msg)
	require.NoError(t, err)
	s := string(out)
	// Each map entry must render as `key: <scalar>`, not `key: { ... }`.
	assert.Contains(t, s, "start: ")
	assert.Contains(t, s, "fast: ")
	assert.Contains(t, s, "env: ")
	assert.Contains(t, s, "errors: ")
	assert.Contains(t, s, "small: ")
	assert.NotContains(t, s, "start: {",
		"Timestamp map values must round-trip as scalar shorthand")
	assert.NotContains(t, s, "fast: {",
		"Duration map values must round-trip as scalar shorthand")
	assert.NotContains(t, s, "env: {",
		"StringValue map values must round-trip as scalar shorthand")
}

// --- markInnerPresent for non-Secret WKTs at the top level ---

func TestMarkInnerPresent_TimestampScalarShorthand(t *testing.T) {
	const src = `
syntax = "proto3";
package mip_test.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";
import "pxf/bignum.proto";

message WKTContainer {
  google.protobuf.Timestamp t = 1;
  google.protobuf.Duration d = 2;
  google.protobuf.StringValue sv = 3;
  pxf.BigInt bi = 4;
  pxf.Decimal dec = 5;
  pxf.BigFloat bf = 6;
}
`
	sources := map[string]string{
		"test.proto":       src,
		"pxf/bignum.proto": bignumProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("mip_test.v1.WKTContainer")
	require.NoError(t, err)
	desc := d.(protoreflect.MessageDescriptor)

	input := `
t = 2026-05-09T12:00:00Z
d = 1h30m
sv = "hello"
bi = 100
dec = 3.14
bf = 2.71828
`
	_, res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)

	// Each WKT scalar shorthand must mark its inner fields present —
	// the markPresent fix this PR carries. Without it, all of these
	// would be IsAbsent.
	assert.True(t, res.IsSet("t.seconds"), "Timestamp shorthand marks .seconds")
	assert.True(t, res.IsSet("t.nanos"), "Timestamp shorthand marks .nanos")
	assert.True(t, res.IsSet("d.seconds"), "Duration shorthand marks .seconds")
	assert.True(t, res.IsSet("sv.value"), "StringValue shorthand marks .value")
	assert.True(t, res.IsSet("bi.abs"), "BigInt shorthand marks .abs")
	// negative is only marked when value is negative; bi=100 is positive
	// so .negative is set to default (false) but parser still markPresent's it.
	assert.True(t, res.IsSet("bi.negative"))
	assert.True(t, res.IsSet("dec.unscaled"))
	assert.True(t, res.IsSet("bf.mantissa"))
}

// --- ApplyDefault ---

func TestApplyDefault_AllScalarKinds(t *testing.T) {
	desc := compileLayeredProto(t)
	msg := dynamicpb.NewMessage(desc)

	cases := []struct {
		field string
		def   string
		check func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor)
	}{
		{"s", `hello`, func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.Equal(t, "hello", m.Get(fd).String())
		}},
		{"b", "true", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.True(t, m.Get(fd).Bool())
		}},
		{"i32", "32", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.Equal(t, int64(32), m.Get(fd).Int())
		}},
		{"i64", "64", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.Equal(t, int64(64), m.Get(fd).Int())
		}},
		{"u32", "320", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.Equal(t, uint64(320), m.Get(fd).Uint())
		}},
		{"u64", "640", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.Equal(t, uint64(640), m.Get(fd).Uint())
		}},
		{"f32", "1.5", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.InDelta(t, 1.5, m.Get(fd).Float(), 0.0001)
		}},
		{"f64", "3.14", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.InDelta(t, 3.14, m.Get(fd).Float(), 0.0001)
		}},
		{"raw", "aGVsbG8=", func(t *testing.T, m protoreflect.Message, fd protoreflect.FieldDescriptor) {
			assert.Equal(t, []byte("hello"), m.Get(fd).Bytes())
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.field, func(t *testing.T) {
			fd := desc.Fields().ByName(protoreflect.Name(c.field))
			require.NotNil(t, fd)
			require.NoError(t, pxf.ApplyDefault(msg, fd, c.def))
			c.check(t, msg, fd)
		})
	}
}

func TestApplyDefault_EnumByName(t *testing.T) {
	desc := compileLayeredProto(t)
	msg := dynamicpb.NewMessage(desc)
	fd := desc.Fields().ByName("color")
	require.NotNil(t, fd)
	require.NoError(t, pxf.ApplyDefault(msg, fd, "RED"))
	assert.Equal(t, protoreflect.EnumNumber(1), msg.Get(fd).Enum())
}

func TestApplyDefault_EnumByNumber(t *testing.T) {
	desc := compileLayeredProto(t)
	msg := dynamicpb.NewMessage(desc)
	fd := desc.Fields().ByName("color_by_num")
	require.NotNil(t, fd)
	require.NoError(t, pxf.ApplyDefault(msg, fd, "2"))
	assert.Equal(t, protoreflect.EnumNumber(2), msg.Get(fd).Enum())
}

func TestApplyDefault_InvalidValueErrors(t *testing.T) {
	desc := compileLayeredProto(t)
	msg := dynamicpb.NewMessage(desc)
	fd := desc.Fields().ByName("i32")

	require.Error(t, pxf.ApplyDefault(msg, fd, "not-an-int"))
}

func TestApplyDefault_RawStdAndRawStdEncodingBytes(t *testing.T) {
	// Bytes-default falls back to RawStdEncoding when StdEncoding fails
	// (i.e. unpadded base64). Cover both code paths.
	desc := compileLayeredProto(t)
	fd := desc.Fields().ByName("raw")

	t.Run("padded", func(t *testing.T) {
		msg := dynamicpb.NewMessage(desc)
		require.NoError(t, pxf.ApplyDefault(msg, fd, "aGVsbG8="))
		assert.Equal(t, []byte("hello"), msg.Get(fd).Bytes())
	})
	t.Run("unpadded", func(t *testing.T) {
		msg := dynamicpb.NewMessage(desc)
		require.NoError(t, pxf.ApplyDefault(msg, fd, "aGVsbG8"))
		assert.Equal(t, []byte("hello"), msg.Get(fd).Bytes())
	})
	t.Run("invalid", func(t *testing.T) {
		msg := dynamicpb.NewMessage(desc)
		require.Error(t, pxf.ApplyDefault(msg, fd, "!!!not-base64!!!"))
	})
}

// --- ApplyDefault for message-typed WKT fields ---

const wktDefaultsProtoSrc = `
syntax = "proto3";
package wktdef_test.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";
import "pxf/bignum.proto";

message WKTDefaults {
  google.protobuf.Timestamp t = 1;
  google.protobuf.Duration d = 2;
  google.protobuf.StringValue sv = 3;
  google.protobuf.Int32Value i32v = 4;
  google.protobuf.Int64Value i64v = 5;
  google.protobuf.UInt32Value u32v = 6;
  google.protobuf.UInt64Value u64v = 7;
  google.protobuf.BoolValue bv = 8;
  google.protobuf.FloatValue fv = 9;
  google.protobuf.DoubleValue dv = 10;
  google.protobuf.BytesValue bytv = 11;
  pxf.BigInt bi = 12;
  pxf.Decimal dec = 13;
  pxf.BigFloat bf = 14;
}
`

func compileWKTDefaults(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":       wktDefaultsProtoSrc,
		"pxf/bignum.proto": bignumProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("wktdef_test.v1.WKTDefaults")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

func TestApplyDefault_WKTMessageFields(t *testing.T) {
	desc := compileWKTDefaults(t)

	cases := []struct {
		field string
		def   string
		check func(t *testing.T, sub protoreflect.Message)
	}{
		{"t", "2026-05-09T12:00:00Z", func(t *testing.T, sub protoreflect.Message) {
			seconds := sub.Get(sub.Descriptor().Fields().ByName("seconds")).Int()
			assert.Greater(t, seconds, int64(0))
		}},
		{"d", "1h30m", func(t *testing.T, sub protoreflect.Message) {
			seconds := sub.Get(sub.Descriptor().Fields().ByName("seconds")).Int()
			assert.Equal(t, int64(5400), seconds)
		}},
		{"sv", "hello", func(t *testing.T, sub protoreflect.Message) {
			assert.Equal(t, "hello", sub.Get(sub.Descriptor().Fields().ByName("value")).String())
		}},
		{"i32v", "42", func(t *testing.T, sub protoreflect.Message) {
			assert.Equal(t, int64(42), sub.Get(sub.Descriptor().Fields().ByName("value")).Int())
		}},
		{"i64v", "9000000000", func(t *testing.T, sub protoreflect.Message) {
			assert.Equal(t, int64(9000000000), sub.Get(sub.Descriptor().Fields().ByName("value")).Int())
		}},
		{"u32v", "100", func(t *testing.T, sub protoreflect.Message) {
			assert.Equal(t, uint64(100), sub.Get(sub.Descriptor().Fields().ByName("value")).Uint())
		}},
		{"u64v", "200", func(t *testing.T, sub protoreflect.Message) {
			assert.Equal(t, uint64(200), sub.Get(sub.Descriptor().Fields().ByName("value")).Uint())
		}},
		{"bv", "true", func(t *testing.T, sub protoreflect.Message) {
			assert.True(t, sub.Get(sub.Descriptor().Fields().ByName("value")).Bool())
		}},
		{"fv", "1.5", func(t *testing.T, sub protoreflect.Message) {
			assert.InDelta(t, 1.5, sub.Get(sub.Descriptor().Fields().ByName("value")).Float(), 0.0001)
		}},
		{"dv", "3.14", func(t *testing.T, sub protoreflect.Message) {
			assert.InDelta(t, 3.14, sub.Get(sub.Descriptor().Fields().ByName("value")).Float(), 0.0001)
		}},
		{"bi", "12345", func(t *testing.T, sub protoreflect.Message) {
			abs := sub.Get(sub.Descriptor().Fields().ByName("abs")).Bytes()
			assert.NotEmpty(t, abs)
		}},
		{"dec", "3.14", func(t *testing.T, sub protoreflect.Message) {
			scale := sub.Get(sub.Descriptor().Fields().ByName("scale")).Int()
			assert.Equal(t, int64(2), scale)
		}},
		{"bf", "2.71828", func(t *testing.T, sub protoreflect.Message) {
			prec := sub.Get(sub.Descriptor().Fields().ByName("prec")).Uint()
			assert.Greater(t, prec, uint64(0))
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.field, func(t *testing.T) {
			msg := dynamicpb.NewMessage(desc)
			fd := desc.Fields().ByName(protoreflect.Name(c.field))
			require.NotNil(t, fd)
			require.NoError(t, pxf.ApplyDefault(msg, fd, c.def))
			sub := msg.Get(fd).Message()
			c.check(t, sub)
		})
	}
}

func TestApplyDefault_WKTInvalidValue(t *testing.T) {
	desc := compileWKTDefaults(t)

	cases := []struct {
		field   string
		bad     string
		errKind string
	}{
		{"t", "not-a-timestamp", "timestamp"},
		{"d", "not-a-duration", "duration"},
		{"i32v", "not-a-number", "int32"},
		{"bi", "not-a-bigint", "big integer"},
		{"dec", "not-a-decimal", "decimal"},
		{"bf", "not-a-bigfloat", "big float"},
		// BytesValue defaults are unsupported by parseScalarDefault
		// (pre-existing protowire-go limitation): the wrapper-defaults
		// path doesn't include a BytesKind branch. Pinning this so a
		// future change either supports it or stays explicit about
		// rejecting it.
		{"bytv", "aGVsbG8=", "unsupported default kind"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.field, func(t *testing.T) {
			msg := dynamicpb.NewMessage(desc)
			fd := desc.Fields().ByName(protoreflect.Name(c.field))
			require.NotNil(t, fd)
			err := pxf.ApplyDefault(msg, fd, c.bad)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.errKind)
		})
	}
}

// --- IsRequired / Default ---

func TestIsRequired(t *testing.T) {
	desc := compileLayeredProto(t)
	assert.True(t, pxf.IsRequired(desc.Fields().ByName("s")),
		"field 's' annotated (pxf.required) = true")
	assert.False(t, pxf.IsRequired(desc.Fields().ByName("b")),
		"field 'b' has no required annotation")
	assert.False(t, pxf.IsRequired(desc.Fields().ByName("no_default")),
		"field 'no_default' has no required annotation")
}

func TestDefault(t *testing.T) {
	desc := compileLayeredProto(t)

	cases := []struct {
		field    string
		wantOK   bool
		wantText string
	}{
		{"s", true, "hello"},
		{"b", true, "true"},
		{"i32", true, "32"},
		{"f64", true, "3.14"},
		{"no_default", false, ""},
		{"optional_no_anno", false, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.field, func(t *testing.T) {
			fd := desc.Fields().ByName(protoreflect.Name(c.field))
			require.NotNil(t, fd)
			got, ok := pxf.Default(fd)
			assert.Equal(t, c.wantOK, ok)
			if c.wantOK {
				assert.Equal(t, c.wantText, got)
			}
		})
	}
}

// --- SkipPostDecode ---

func TestSkipPostDecode(t *testing.T) {
	desc := compileLayeredProto(t)

	t.Run("default mode rejects missing required field", func(t *testing.T) {
		// `s` is required. postDecode runs and errors. (Note:
		// protowire-go's postDecode checks required BEFORE applying
		// defaults, so a default annotation does NOT satisfy required
		// at the per-parse level. Layered consumers handle that
		// distinction in their own merged-result pass — see chameleon.)
		input := `i32 = 99`
		_, _, err := pxf.UnmarshalOptions{}.UnmarshalFullDescriptor([]byte(input), desc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("SkipPostDecode bypasses required validation", func(t *testing.T) {
		// Same input that errored above. With SkipPostDecode, the
		// parse succeeds: the caller (e.g. chameleon) is expected to
		// run its own validation against a merged result.
		input := `i32 = 99`
		_, _, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFullDescriptor([]byte(input), desc)
		require.NoError(t, err, "SkipPostDecode disables per-parse required validation")
	})

	t.Run("SkipPostDecode bypasses default application", func(t *testing.T) {
		// Without SkipPostDecode, a non-required defaulted field gets
		// its default value. With SkipPostDecode, it stays at zero.
		// `b` is annotated (pxf.default) = "true", not required.
		input := `s = "x"`

		_, _, err := pxf.UnmarshalOptions{}.UnmarshalFullDescriptor([]byte(input), desc)
		require.NoError(t, err, "default mode satisfies non-required defaulted fields")

		msgSkip := dynamicpb.NewMessage(desc)
		_, err = pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFull([]byte(input), msgSkip)
		require.NoError(t, err)
		bField := desc.Fields().ByName("b")
		assert.False(t, msgSkip.Get(bField).Bool(),
			"SkipPostDecode leaves defaulted field at zero — caller applies later")
	})
}

// --- Result.PresentFields ---

func TestPresentFields_ReturnsAllPresentPaths(t *testing.T) {
	desc := compileLayeredProto(t)
	input := `
s = "explicit"
i32 = 42
b = null
`
	_, res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)

	paths := res.PresentFields()
	// PresentFields returns set + null entries (the union).
	assert.Contains(t, paths, "s")
	assert.Contains(t, paths, "i32")
	assert.Contains(t, paths, "b")
	// no_default never mentioned — must NOT be in PresentFields.
	for _, p := range paths {
		assert.NotEqual(t, "no_default", p)
	}
}

func TestPresentFields_EmptyForUnpopulatedMessage(t *testing.T) {
	desc := compileLayeredProto(t)
	_, res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFullDescriptor([]byte(``), desc)
	require.NoError(t, err)
	assert.Empty(t, res.PresentFields())
}

func TestPresentFields_UnionWithNulls(t *testing.T) {
	// Verify PresentFields returns BOTH set and null (union per godoc).
	desc := compileLayeredProto(t)
	input := `s = "x"  i32 = null`
	_, res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFullDescriptor([]byte(input), desc)
	require.NoError(t, err)

	paths := res.PresentFields()
	pathSet := make(map[string]bool)
	for _, p := range paths {
		pathSet[p] = true
	}
	assert.True(t, pathSet["s"], "set field included")
	assert.True(t, pathSet["i32"], "null field included in PresentFields union")
	assert.True(t, res.IsNull("i32"))
	assert.True(t, res.IsSet("s"))
	assert.False(t, res.IsSet("i32"), "null does not count as IsSet")
}
