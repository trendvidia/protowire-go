// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/check"
	"github.com/trendvidia/protowire-go/encoding/sbe"
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

func violationReport() *check.Report {
	return &check.Report{Violations: []check.Violation{
		{Path: "value", RuleID: "test.rule", Message: "value out of range"},
	}}
}

// marshalSimple encodes a Simple{id, value} message with a plain codec.
func marshalSimple(t *testing.T, fd protoreflect.FileDescriptor, id uint32, value int32) []byte {
	t.Helper()
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	desc := fd.Messages().ByName("Simple")
	require.NotNil(t, desc)
	msg := dynamicpb.NewMessage(desc)
	msg.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint32(id))
	msg.Set(desc.Fields().ByName("value"), protoreflect.ValueOfInt32(value))
	data, err := codec.Marshal(msg)
	require.NoError(t, err)
	return data
}

func TestCodecValidatorViolation(t *testing.T) {
	fd := compileTestProto(t)
	data := marshalSimple(t, fd, 42, -100)

	fv := &fakeValidator{rep: violationReport()}
	codec, err := sbe.CodecOptions{Validator: fv}.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Simple")
	msg := dynamicpb.NewMessage(desc)
	err = codec.Unmarshal(data, msg)

	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "value", ce.Report.Violations[0].Path)
	// The validator sees the decoded message, fully populated.
	require.Same(t, msg, fv.got)
	assert.Equal(t, int64(-100), msg.Get(desc.Fields().ByName("value")).Int())
}

func TestCodecValidatorCleanPass(t *testing.T) {
	fd := compileTestProto(t)
	data := marshalSimple(t, fd, 42, -100)

	codec, err := sbe.CodecOptions{Validator: &fakeValidator{}}.NewCodec(fd)
	require.NoError(t, err)

	msg := dynamicpb.NewMessage(fd.Messages().ByName("Simple"))
	assert.NoError(t, codec.Unmarshal(data, msg))
}

func TestCodecValidatorEngineError(t *testing.T) {
	fd := compileTestProto(t)
	data := marshalSimple(t, fd, 42, -100)

	engineErr := errors.New("engine failure")
	codec, err := sbe.CodecOptions{Validator: &fakeValidator{err: engineErr}}.NewCodec(fd)
	require.NoError(t, err)

	msg := dynamicpb.NewMessage(fd.Messages().ByName("Simple"))
	assert.ErrorIs(t, codec.Unmarshal(data, msg), engineErr)
}

func TestUnmarshalDescriptorValidatorViolation(t *testing.T) {
	fd := compileTestProto(t)
	data := marshalSimple(t, fd, 42, -100)

	codec, err := sbe.CodecOptions{Validator: &fakeValidator{rep: violationReport()}}.NewCodec(fd)
	require.NoError(t, err)

	msg, err := codec.UnmarshalDescriptor(data, fd.Messages().ByName("Simple"))
	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Nil(t, msg)
}

func TestStreamDecoderRunsValidator(t *testing.T) {
	fd := compileTestProto(t)
	desc := fd.Messages().ByName("Simple")

	// Encode one framed message with a plain codec.
	plain, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	var buf bytes.Buffer
	enc := plain.NewEncoder(&buf)
	src := dynamicpb.NewMessage(desc)
	src.Set(desc.Fields().ByName("id"), protoreflect.ValueOfUint32(7))
	require.NoError(t, enc.Encode(src))

	// Decode through a validating codec's stream decoder.
	codec, err := sbe.CodecOptions{Validator: &fakeValidator{rep: violationReport()}}.NewCodec(fd)
	require.NoError(t, err)
	dec := codec.NewDecoder(&buf)

	msg := dynamicpb.NewMessage(desc)
	err = dec.Decode(msg)
	var ce *check.Error
	require.ErrorAs(t, err, &ce)

	// A validation failure consumes the frame but must not poison the
	// stream the way framing corruption does.
	assert.ErrorIs(t, dec.Decode(msg), io.EOF)
}
