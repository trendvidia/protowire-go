// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package check_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/check"
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

func TestReportOK(t *testing.T) {
	var nilReport *check.Report
	assert.True(t, nilReport.OK())
	assert.True(t, (&check.Report{}).OK())
	assert.False(t, (&check.Report{Violations: []check.Violation{{Message: "bad"}}}).OK())
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		rep  *check.Report
		want string
	}{
		{"nil report", nil, "check: validation failed"},
		{"empty report", &check.Report{}, "check: validation failed"},
		{
			"single full violation",
			&check.Report{Violations: []check.Violation{
				{Path: "port", RuleID: "rfc001.required", Message: "value is required"},
			}},
			"check: port: value is required (rfc001.required)",
		},
		{
			"message-level violation without path or rule",
			&check.Report{Violations: []check.Violation{{Message: "inconsistent"}}},
			"check: inconsistent",
		},
		{
			"multiple violations",
			&check.Report{Violations: []check.Violation{
				{Path: "a", Message: "first"},
				{Path: "b", Message: "second"},
			}},
			"check: 2 violations; first: a: first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, (&check.Error{Report: tt.rep}).Error())
		})
	}
}

func TestValidateNilValidator(t *testing.T) {
	rep, err := check.Validate(nil, "anything")
	assert.Nil(t, rep)
	assert.NoError(t, err)
}

func TestValidateEngineError(t *testing.T) {
	engineErr := errors.New("engine exploded")
	fv := &fakeValidator{err: engineErr}
	rep, err := check.Validate(fv, 42)
	assert.Nil(t, rep)
	assert.ErrorIs(t, err, engineErr)
	var ce *check.Error
	assert.False(t, errors.As(err, &ce), "engine errors must not be check.Error")
	assert.Equal(t, 42, fv.got)
}

func TestValidateViolations(t *testing.T) {
	rep := &check.Report{Violations: []check.Violation{{Path: "x", Message: "bad"}}}
	got, err := check.Validate(&fakeValidator{rep: rep}, nil)
	assert.Same(t, rep, got)
	var ce *check.Error
	require.ErrorAs(t, err, &ce)
	assert.Same(t, rep, ce.Report)
}

func TestValidateCleanPass(t *testing.T) {
	rep := &check.Report{}
	got, err := check.Validate(&fakeValidator{rep: rep}, nil)
	assert.NoError(t, err)
	assert.Same(t, rep, got)
}
