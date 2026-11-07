// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// DatasetReader.Scan binds a row under the options the reader was built
// with, as UnmarshalOptions.BindRow binds it (#280). Scan used to build
// fresh options holding only the limits and the TypeResolver, so
// DiscardUnknown, SkipPostDecode, SkipValidate, OnSecretField and
// Validator had no effect on a streamed row. Each case binds one row
// through both paths, with the option off and on, and requires the two
// paths to agree.

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/check"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const scanOptionsProtoSrc = `
syntax = "proto3";
package so.v1;

import "pxf/annotations.proto";
import "pxf/secret.proto";

message Inner { int32 n = 1; }
message Row {
  string id = 1;
  int32 req = 2 [(pxf.required) = true];
  string dflt = 3 [(pxf.default) = "z"];
  Inner inner = 4;
  pxf.Secret pw = 5;
}
`

// A field named null breaks a bind-time schema rule (draft §3.13). It is
// in a file of its own, because the schema check reads the whole file.
const scanOptionsBadProtoSrc = `
syntax = "proto3";
package sobad.v1;

message Bad {
  string id = 1;
  string null = 2;
}
`

func scanOptionsDesc(t *testing.T, file, src, name string) protoreflect.MessageDescriptor {
	t.Helper()
	fd := compileProtoSources(t, file, map[string]string{
		file:                    src,
		"pxf/annotations.proto": annotationsProtoSrc,
		"pxf/secret.proto":      secretProtoSrc,
	})
	md := fd.Messages().ByName(protoreflect.Name(name))
	require.NotNil(t, md)
	return md
}

// bindUnder binds the one row of table under o, through o's
// DatasetReader.Scan and through Parse + o.BindRow, and requires the two
// to agree. It returns the bound message or the error.
func bindUnder(t *testing.T, o pxf.UnmarshalOptions, md protoreflect.MessageDescriptor, table string) (*dynamicpb.Message, error) {
	t.Helper()
	doc, err := pxf.Parse([]byte(table))
	require.NoError(t, err)
	ds := doc.Datasets[0]
	require.Len(t, ds.Rows, 1)
	viaBind := dynamicpb.NewMessage(md)
	berr := o.BindRow(viaBind, ds.Columns, ds.Rows[0])

	tr, err := o.NewDatasetReader(strings.NewReader(table))
	require.NoError(t, err)
	viaScan := dynamicpb.NewMessage(md)
	serr := tr.Scan(viaScan)
	require.Equal(t, errText(berr), errText(serr), "BindRow vs Scan")
	if berr != nil {
		return nil, berr
	}
	require.True(t, proto.Equal(viaBind, viaScan), "BindRow %v, Scan %v", viaBind, viaScan)
	err = tr.Scan(dynamicpb.NewMessage(md))
	require.True(t, errors.Is(err, io.EOF), "after the row: %v", err)
	return viaScan, nil
}

func TestDatasetScan_SkipPostDecode(t *testing.T) {
	md := scanOptionsDesc(t, "so.proto", scanOptionsProtoSrc, "Row")
	const table = "@dataset so.v1.Row ( id )\n(\"x\")\n"

	_, err := bindUnder(t, pxf.UnmarshalOptions{}, md, table)
	require.EqualError(t, err, `2:1: required field "req" is absent`)

	m, err := bindUnder(t, pxf.UnmarshalOptions{SkipPostDecode: true}, md, table)
	require.NoError(t, err)
	// No post-decode pass: the required field is not checked, and the
	// default is not applied.
	assert.Equal(t, "", m.Get(md.Fields().ByName("dflt")).String())
}

func TestDatasetScan_DiscardUnknown(t *testing.T) {
	md := scanOptionsDesc(t, "so.proto", scanOptionsProtoSrc, "Row")
	const table = "@dataset so.v1.Row ( id, req, inner )\n(\"x\", 1, { n = 2 nope = 3 })\n"

	_, err := bindUnder(t, pxf.UnmarshalOptions{}, md, table)
	require.EqualError(t, err, `2:1: column inner: unknown field "nope" in so.v1.Inner`)

	m, err := bindUnder(t, pxf.UnmarshalOptions{DiscardUnknown: true}, md, table)
	require.NoError(t, err)
	inner := m.Get(md.Fields().ByName("inner")).Message()
	assert.Equal(t, int64(2), inner.Get(inner.Descriptor().Fields().ByName("n")).Int())
}

func TestDatasetScan_OnSecretField(t *testing.T) {
	md := scanOptionsDesc(t, "so.proto", scanOptionsProtoSrc, "Row")
	const table = "@dataset so.v1.Row ( id, req, pw )\n(\"x\", 1, \"s3cret\")\n"
	pwValue := func(m *dynamicpb.Message) string {
		pw := m.Get(md.Fields().ByName("pw")).Message()
		return pw.Get(pw.Descriptor().Fields().ByName("value")).String()
	}

	m, err := bindUnder(t, pxf.UnmarshalOptions{}, md, table)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", pwValue(m))

	// bindUnder binds the row twice, so the hook fires once per path.
	var got []string
	o := pxf.UnmarshalOptions{OnSecretField: func(path, value string) error {
		got = append(got, path+"="+value)
		return nil
	}}
	m, err = bindUnder(t, o, md, table)
	require.NoError(t, err)
	assert.Equal(t, []string{"pw=s3cret", "pw=s3cret"}, got)
	assert.Equal(t, "", pwValue(m))
}

type denyAll struct{}

func (denyAll) Validate(any) (*check.Report, error) {
	return &check.Report{Violations: []check.Violation{{Path: "id", RuleID: "test.deny", Message: "denied"}}}, nil
}

func TestDatasetScan_Validator(t *testing.T) {
	md := scanOptionsDesc(t, "so.proto", scanOptionsProtoSrc, "Row")
	const table = "@dataset so.v1.Row ( id, req )\n(\"x\", 1)\n"

	_, err := bindUnder(t, pxf.UnmarshalOptions{}, md, table)
	require.NoError(t, err)

	_, err = bindUnder(t, pxf.UnmarshalOptions{Validator: denyAll{}}, md, table)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test.deny")
}

func TestDatasetScan_SkipValidate(t *testing.T) {
	md := scanOptionsDesc(t, "sobad.proto", scanOptionsBadProtoSrc, "Bad")
	const table = "@dataset sobad.v1.Bad ( id )\n(\"x\")\n"

	_, err := bindUnder(t, pxf.UnmarshalOptions{}, md, table)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `uses PXF-reserved name "null"`)

	m, err := bindUnder(t, pxf.UnmarshalOptions{SkipValidate: true}, md, table)
	require.NoError(t, err)
	assert.Equal(t, "x", m.Get(md.Fields().ByName("id")).String())
}
