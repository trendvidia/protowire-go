// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/check"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

type fakeValidator struct {
	rep *check.Report
	err error
	got any
}

func (f *fakeValidator) Validate(v any) (*check.Report, error) {
	f.got = v
	return f.rep, f.err
}

const validatorProtoSrc = `syntax = "proto3";
message ValConfig {
  string name = 1;
  int32 port = 2;
}
`

func violationReport() *check.Report {
	return &check.Report{Violations: []check.Violation{
		{Path: "port", RuleID: "test.rule", Message: "port out of range"},
	}}
}

func TestUnmarshalRunsValidator(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")
	require.NotNil(t, desc)

	fv := &fakeValidator{rep: violationReport()}
	msg := dynamicpb.NewMessage(desc)
	err := pxf.UnmarshalOptions{Validator: fv}.Unmarshal([]byte(`name = "x"`), msg)

	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "port", ce.Report.Violations[0].Path)
	// The validator sees the decoded proto.Message, fully populated.
	got, ok := fv.got.(proto.Message)
	require.True(t, ok, "validator should receive a proto.Message, got %T", fv.got)
	name := got.ProtoReflect().Get(desc.Fields().ByName("name")).String()
	assert.Equal(t, "x", name)
}

func TestUnmarshalCleanValidator(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	fv := &fakeValidator{rep: &check.Report{}}
	msg := dynamicpb.NewMessage(desc)
	err := pxf.UnmarshalOptions{Validator: fv}.Unmarshal([]byte(`name = "x"`), msg)
	assert.NoError(t, err)
	assert.NotNil(t, fv.got)
}

func TestUnmarshalValidatorEngineError(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	engineErr := errors.New("engine failure")
	msg := dynamicpb.NewMessage(desc)
	err := pxf.UnmarshalOptions{Validator: &fakeValidator{err: engineErr}}.Unmarshal([]byte(`name = "x"`), msg)
	assert.ErrorIs(t, err, engineErr)
	var ce *check.Error
	assert.False(t, errors.As(err, &ce))
}

func TestUnmarshalFullAttachesReport(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	rep := violationReport()
	msg := dynamicpb.NewMessage(desc)
	result, err := pxf.UnmarshalOptions{Validator: &fakeValidator{rep: rep}}.UnmarshalFull([]byte(`name = "x"`), msg)

	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	require.NotNil(t, result, "Result must be returned alongside a check.Error")
	assert.Same(t, rep, result.Report())
	assert.True(t, result.IsSet("name"))
}

func TestUnmarshalFullCleanReportAttached(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	rep := &check.Report{}
	msg := dynamicpb.NewMessage(desc)
	result, err := pxf.UnmarshalOptions{Validator: &fakeValidator{rep: rep}}.UnmarshalFull([]byte(`name = "x"`), msg)
	require.NoError(t, err)
	assert.Same(t, rep, result.Report())
}

func TestUnmarshalFullNoValidatorNilReport(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	msg := dynamicpb.NewMessage(desc)
	result, err := pxf.UnmarshalFull([]byte(`name = "x"`), msg)
	require.NoError(t, err)
	assert.Nil(t, result.Report())
}

func TestUnmarshalFullDescriptorReturnsMessageOnViolation(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	rep := violationReport()
	msg, result, err := pxf.UnmarshalOptions{Validator: &fakeValidator{rep: rep}}.UnmarshalFullDescriptor([]byte(`name = "x"`), desc)

	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	require.NotNil(t, msg, "decoded message must be returned alongside a check.Error")
	require.NotNil(t, result)
	assert.Same(t, rep, result.Report())
}

func TestUnmarshalDescriptorValidatorViolation(t *testing.T) {
	fd := compileFiles(t, map[string]string{"val.proto": validatorProtoSrc})
	desc := fd.Messages().ByName("ValConfig")

	msg, err := pxf.UnmarshalOptions{Validator: &fakeValidator{rep: violationReport()}}.UnmarshalDescriptor([]byte(`name = "x"`), desc)
	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Nil(t, msg)
}
