// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// wktRedefinitions has a wrong shape for every type matched by name (#218).
var wktRedefinitions = []struct {
	pkg, name, body, doc, detail string
}{
	{"pxf", "Secret", "string other = 1;", `pw = "x"`, `no field "value"`},
	{"pxf", "Secret", "int32 value = 1;", `pw = "x"`, `field "value" is int32`},
	{"pxf", "Secret", "repeated string value = 1;", `pw = "x"`, `field "value" has the wrong cardinality`},
	{"pxf", "Secret", "string value = 1; int32 hint = 2;", `pw = "x"`, `field "hint" is int32`},
	{"pxf", "BigInt", "string other = 1;", `pw = 5`, `no field "abs"`},
	{"pxf", "BigInt", "int32 abs = 1; bool negative = 2;", `pw = 5`, `field "abs" is int32`},
	{"pxf", "Decimal", "string other = 1;", `pw = 1.5`, `no field "unscaled"`},
	{"pxf", "BigFloat", "string other = 1;", `pw = 1.5`, `no field "mantissa"`},
	{"google.protobuf", "Timestamp", "string other = 1;", `pw = 2026-01-01T00:00:00Z`, `no field "seconds"`},
	{"google.protobuf", "Duration", "string other = 1;", `pw = 5s`, `no field "seconds"`},
	{"google.protobuf", "StringValue", "string other = 1;", `pw = "x"`, `no field "value"`},
	{"google.protobuf", "StringValue", "int32 value = 1;", `pw = "x"`, `field "value" is int32`},
	{"google.protobuf", "Int32Value", "string value = 1;", `pw = 5`, `field "value" is string`},
	{"google.protobuf", "Any", "string other = 1;", `pw { @type = "x" }`, `no field "type_url"`},
}

func compileRedefinition(t *testing.T, pkg, name, body string) protoreflect.MessageDescriptor {
	t.Helper()
	src := fmt.Sprintf("syntax = \"proto3\";\npackage %s;\nmessage %s { %s }\nmessage H { %s pw = 1; repeated %s l = 2; map<string, %s> m = 3; }\n",
		pkg, name, body, name, name, name)
	return compileProtoSources(t, "p.proto", map[string]string{"p.proto": src}).Messages().ByName("H")
}

func TestWellKnownShapeRefusedAtBind(t *testing.T) {
	for _, tc := range wktRedefinitions {
		t.Run(tc.name+"/"+tc.body, func(t *testing.T) {
			md := compileRedefinition(t, tc.pkg, tc.name, tc.body)
			vs := pxf.ValidateDescriptor(md)
			require.Len(t, vs, 1)
			assert.Equal(t, pxf.ViolationWellKnownShape, vs[0].Kind)
			assert.Equal(t, tc.pkg+"."+tc.name, vs[0].Element)
			assert.Contains(t, vs[0].String(), tc.detail)
			assert.Contains(t, vs[0].String(), "§wkt-shapes")
			require.ErrorContains(t, pxf.Unmarshal([]byte(tc.doc), dynamicpb.NewMessage(md)), "redefines a well-known type")
		})
	}
}

// Where validation does not run the message is ordinary: every form in
// every position is an error or a block, and nothing panics.
func TestWellKnownShapeOrdinaryWithoutValidation(t *testing.T) {
	var runs int
	for _, tc := range wktRedefinitions {
		t.Run(tc.name+"/"+tc.body, func(t *testing.T) {
			md := compileRedefinition(t, tc.pkg, tc.name, tc.body)
			value := tc.doc[len("pw = "):]
			if tc.name == "Any" {
				value = `{ }`
			}
			for _, opts := range []pxf.UnmarshalOptions{
				{SkipValidate: true},
				{SkipValidate: true, OnSecretField: func(string, string) error {
					t.Error("OnSecretField fired for a message of the wrong shape")
					return nil
				}},
			} {
				for _, doc := range []string{"pw = " + value, "l = [ " + value + " ]", `m = { "k": ` + value + " }"} {
					runs++
					err := opts.Unmarshal([]byte(doc), dynamicpb.NewMessage(md))
					if tc.name != "Any" {
						require.Error(t, err, doc)
					}
					_, err = pxf.UnmarshalOptions{SkipValidate: true}.UnmarshalFull([]byte(doc), dynamicpb.NewMessage(md))
					if tc.name != "Any" {
						require.Error(t, err, doc)
					}
				}
			}

			m := dynamicpb.NewMessage(md)
			fds := md.Fields()
			m.Mutable(fds.ByName("pw"))
			l := m.Mutable(fds.ByName("l")).List()
			l.Append(l.NewElement())
			mp := m.Mutable(fds.ByName("m")).Map()
			mp.Set(protoreflect.ValueOfString("k").MapKey(), mp.NewValue())
			out, err := pxf.Marshal(m)
			require.NoError(t, err)
			assert.Contains(t, string(out), "pw {")
			assert.NotContains(t, string(out), "0x", "a list address in the output")

			row, err := pxf.AppendDatasetRow(nil, m, []string{"pw"})
			require.ErrorContains(t, err, "redefines a well-known type", "row %q", row)
		})
	}
	assert.Equal(t, len(wktRedefinitions)*2*3, runs)
}

// Only the shape counts: a copy of a canonical type, a smaller Secret and
// extra fields are all still the well-known type.
func TestWellKnownShapeAccepted(t *testing.T) {
	for _, tc := range []struct{ pkg, name, body, doc string }{
		{"pxf", "Secret", "string value = 1;", `pw = "x"`},
		{"pxf", "Secret", "string value = 1; string hint = 2; string fingerprint = 3;", `pw = "x"`},
		{"pxf", "Secret", "string value = 7; string note = 8;", `pw = "x"`},
		{"pxf", "Secret", "optional string value = 1;", `pw = "x"`},
		{"pxf", "BigInt", "bytes abs = 1; bool negative = 2;", `pw = 5`},
		{"pxf", "Decimal", "bytes unscaled = 1; int32 scale = 2; bool negative = 3;", `pw = 1.5`},
		{"pxf", "BigFloat", "bytes mantissa = 1; int32 exponent = 2; uint32 prec = 3; bool negative = 4;", `pw = 1.5`},
		{"google.protobuf", "Timestamp", "int64 seconds = 1; int32 nanos = 2;", `pw = 2026-01-01T00:00:00Z`},
		{"google.protobuf", "Int32Value", "int32 value = 1;", `pw = 5`},
	} {
		t.Run(tc.name+"/"+tc.body, func(t *testing.T) {
			md := compileRedefinition(t, tc.pkg, tc.name, tc.body)
			assert.Empty(t, pxf.ValidateDescriptor(md))
			m := dynamicpb.NewMessage(md)
			require.NoError(t, pxf.Unmarshal([]byte(tc.doc), m))
			assert.True(t, m.Has(md.Fields().ByName("pw")))
		})
	}
}

// A Secret may leave hint out, and a block naming it is then an error
// rather than a nil field in the hook's path.
func TestSecretHookWithoutDeclaredHint(t *testing.T) {
	md := compileRedefinition(t, "pxf", "Secret", "string value = 1;")
	opts := pxf.UnmarshalOptions{OnSecretField: func(string, string) error { return nil }}
	for _, doc := range []string{`pw { hint = "h" }`, `pw { fingerprint = "f" }`} {
		require.ErrorContains(t, opts.Unmarshal([]byte(doc), dynamicpb.NewMessage(md)), "this schema does not declare it")
	}
}

// A null mask needs the real FieldMask: this one has no paths to read.
func TestNullMaskRequiresFieldMaskShape(t *testing.T) {
	src := "syntax = \"proto3\";\npackage google.protobuf;\nmessage FieldMask { string other = 1; }\nmessage H { FieldMask _null = 1; string s = 2; }\n"
	md := compileProtoSources(t, "p.proto", map[string]string{"p.proto": src}).Messages().ByName("H")
	require.Len(t, pxf.ValidateDescriptor(md), 1)
	m := dynamicpb.NewMessage(md)
	m.Mutable(md.Fields().ByName("_null"))
	_, err := pxf.Marshal(m)
	require.NoError(t, err)
}
