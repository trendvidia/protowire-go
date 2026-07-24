// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/check"
	"github.com/trendvidia/protowire-go/encoding/pb"
)

type optsFakeValidator struct {
	rep *check.Report
	err error
	got any
}

func (f *optsFakeValidator) Validate(v any) (*check.Report, error) {
	f.got = v
	return f.rep, f.err
}

type valMsg struct {
	Name string `protowire:"1"`
	Port int32  `protowire:"2"`
}

func TestUnmarshalOptionsValidatorViolation(t *testing.T) {
	data, err := pb.Marshal(&valMsg{Name: "x", Port: 99999})
	require.NoError(t, err)

	rep := &check.Report{Violations: []check.Violation{
		{Path: "port", RuleID: "test.range", Message: "port out of range"},
	}}
	fv := &optsFakeValidator{rep: rep}

	var got valMsg
	err = pb.UnmarshalOptions{Validator: fv}.Unmarshal(data, &got)

	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Same(t, rep, ce.Report)
	// The validator sees the same decoded struct pointer, fully populated.
	require.Same(t, &got, fv.got)
	assert.Equal(t, "x", got.Name)
	assert.Equal(t, int32(99999), got.Port)
}

func TestUnmarshalOptionsCleanPass(t *testing.T) {
	data, err := pb.Marshal(&valMsg{Name: "x"})
	require.NoError(t, err)

	var got valMsg
	err = pb.UnmarshalOptions{Validator: &optsFakeValidator{}}.Unmarshal(data, &got)
	assert.NoError(t, err)
	assert.Equal(t, "x", got.Name)
}

func TestUnmarshalOptionsEngineError(t *testing.T) {
	data, err := pb.Marshal(&valMsg{Name: "x"})
	require.NoError(t, err)

	engineErr := errors.New("engine failure")
	var got valMsg
	err = pb.UnmarshalOptions{Validator: &optsFakeValidator{err: engineErr}}.Unmarshal(data, &got)
	assert.ErrorIs(t, err, engineErr)
}

func TestUnmarshalOptionsZeroValue(t *testing.T) {
	data, err := pb.Marshal(&valMsg{Name: "x"})
	require.NoError(t, err)

	var got valMsg
	require.NoError(t, pb.UnmarshalOptions{}.Unmarshal(data, &got))
	assert.Equal(t, "x", got.Name)
}
