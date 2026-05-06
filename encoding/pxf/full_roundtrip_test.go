// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Comprehensive end-to-end round-trip across every proto3 data type in
// AllTypes. Mirrors protowire4cpp's pxf_full_roundtrip_test.cc.
//
// Pipeline (each step asserted):
//
//   PXF text₀
//     → pxf.UnmarshalDescriptor   → m1
//     → proto.Marshal              → bin1   (deterministic)
//   bin1
//     → proto.Unmarshal           → m2
//     → pxf.Marshal                → PXF text₁
//   PXF text₁
//     → pxf.UnmarshalDescriptor   → m3
//     → proto.Marshal              → bin3
//
// Asserts:
//   - proto.Equal(m1, m2)        binary round-trip is lossless
//   - proto.Equal(m2, m3)        re-encoded text decodes the same
//   - bin1 == bin3                wire format is byte-stable across the loop
//
// This is the "every type at once" matrix the existing test suite was
// missing — TestRoundTrip only checked four fields, and the binary chain
// previously appeared only in null-related tests.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// trip holds the three messages and the wire/text artefacts produced by one
// pipeline run, so individual tests can assert detailed expectations.
type trip struct {
	m1, m2, m3 *dynamicpb.Message
	bin1, bin3 []byte
	text1      []byte
}

func runFullPipeline(t *testing.T, desc protoreflect.MessageDescriptor, src string) trip {
	t.Helper()
	det := proto.MarshalOptions{Deterministic: true}

	m1, err := pxf.UnmarshalDescriptor([]byte(src), desc)
	require.NoError(t, err, "unmarshal m1")
	bin1, err := det.Marshal(m1)
	require.NoError(t, err, "binary marshal m1")

	m2 := dynamicpb.NewMessage(desc)
	require.NoError(t, proto.Unmarshal(bin1, m2), "binary unmarshal m2")

	text1, err := pxf.Marshal(m2)
	require.NoError(t, err, "pxf marshal m2")

	m3, err := pxf.UnmarshalDescriptor(text1, desc)
	require.NoError(t, err, "unmarshal m3")
	bin3, err := det.Marshal(m3)
	require.NoError(t, err, "binary marshal m3")

	return trip{m1: m1, m2: m2, m3: m3, bin1: bin1, bin3: bin3, text1: text1}
}

func expectFullEquality(t *testing.T, tr trip) {
	t.Helper()
	assert.True(t, proto.Equal(tr.m1, tr.m2),
		"m1 != m2 after binary round-trip")
	assert.True(t, proto.Equal(tr.m2, tr.m3),
		"m2 != m3 after PXF re-encode/re-decode\nre-encoded text:\n%s",
		string(tr.text1))
	assert.Equal(t, tr.bin1, tr.bin3,
		"binary serialization drifted across the round-trip\n"+
			"re-encoded text:\n%s", string(tr.text1))
}

// ----------------------------------------------------------------------------

func TestFullRoundTripAllScalarTypes(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
string_field = "hello"
int32_field = -42
int64_field = 1234567890
uint32_field = 7
uint64_field = 18000000000
float_field = 1.5
double_field = 2.71828
bool_field = true
bytes_field = b"AQID"
enum_field = STATUS_ACTIVE
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)

	// Belt-and-suspenders per-field check.
	get := func(m protoreflect.Message, name string) protoreflect.Value {
		return m.Get(desc.Fields().ByName(protoreflect.Name(name)))
	}
	assert.Equal(t, "hello", get(tr.m3.ProtoReflect(), "string_field").String())
	assert.Equal(t, int64(-42), get(tr.m3.ProtoReflect(), "int32_field").Int())
	assert.Equal(t, int64(1234567890), get(tr.m3.ProtoReflect(), "int64_field").Int())
	assert.Equal(t, uint64(7), get(tr.m3.ProtoReflect(), "uint32_field").Uint())
	assert.Equal(t, uint64(18000000000), get(tr.m3.ProtoReflect(), "uint64_field").Uint())
	assert.InDelta(t, 1.5, get(tr.m3.ProtoReflect(), "float_field").Float(), 1e-9)
	assert.InDelta(t, 2.71828, get(tr.m3.ProtoReflect(), "double_field").Float(), 1e-12)
	assert.True(t, get(tr.m3.ProtoReflect(), "bool_field").Bool())
	assert.Equal(t, []byte{1, 2, 3}, get(tr.m3.ProtoReflect(), "bytes_field").Bytes())
	assert.Equal(t, protoreflect.EnumNumber(1),
		get(tr.m3.ProtoReflect(), "enum_field").Enum())
}

func TestFullRoundTripNegativeAndExtremeNumerics(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
int32_field = -2147483648
int64_field = -9223372036854775807
uint32_field = 4294967295
uint64_field = 18446744073709551615
float_field = -3.4028235e38
double_field = -1.7976931348623157e308
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripNestedMessage(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
nested_field {
  name = "child"
  value = 99
}
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripRepeatedScalars(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `repeated_string = ["a", "b", "c"]`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)

	fd := desc.Fields().ByName("repeated_string")
	assert.Equal(t, 3, tr.m3.ProtoReflect().Get(fd).List().Len())
}

func TestFullRoundTripRepeatedMessages(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
repeated_nested = [
  { name = "a" value = 1 }
  { name = "b" value = 2 }
  { name = "c" value = 3 }
]
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripMapsAllKeyKinds(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
string_map = {
  env: "prod"
  team: "platform"
}
nested_map = {
  primary: { name = "p" value = 1 }
  backup:  { name = "b" value = 2 }
}
int_map = {
  404: "Not Found"
  500: "Internal Error"
}
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripTimestampDuration(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
ts_field = 2024-01-15T10:30:00.123456789Z
dur_field = 1h30m45s
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripNegativeDuration(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	tr := runFullPipeline(t, desc, `dur_field = -30s`)
	expectFullEquality(t, tr)
}

func TestFullRoundTripOneofTextBranch(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	tr := runFullPipeline(t, desc, `text_choice = "picked"`)
	expectFullEquality(t, tr)
}

func TestFullRoundTripOneofNumberBranch(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	tr := runFullPipeline(t, desc, `number_choice = 42`)
	expectFullEquality(t, tr)
}

func TestFullRoundTripWrapperTypes(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
nullable_string = "wrapped"
nullable_int = 123
nullable_bool = true
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripEverythingAtOnce(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	src := `
string_field = "kitchen sink"
int32_field = -1
int64_field = -9999999999
uint32_field = 1
uint64_field = 9999999999
float_field = 0.5
double_field = 0.125
bool_field = true
bytes_field = b"SGVsbG8="
enum_field = STATUS_INACTIVE
nested_field {
  name = "root"
  value = 42
}
repeated_string = ["x", "y", "z"]
repeated_nested = [
  { name = "n1" value = 11 }
  { name = "n2" value = 22 }
]
string_map = {
  alpha: "A"
  beta:  "B"
}
nested_map = {
  one: { name = "n3" value = 33 }
}
int_map = {
  1: "one"
  2: "two"
}
ts_field = 2024-01-15T10:30:00Z
dur_field = 1h30m45s
text_choice = "decided"
nullable_string = "wrapped"
nullable_int = 7
nullable_bool = false
`
	tr := runFullPipeline(t, desc, src)
	expectFullEquality(t, tr)
}

func TestFullRoundTripEmptyDocumentIsValid(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	tr := runFullPipeline(t, desc, "")
	expectFullEquality(t, tr)
}
