// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// A rejected wrapper default leaves the parent unchanged (#135).
//
// applyMessageDefault's wrapper arm called msg.Mutable(fd) before it
// parsed the literal, so a google.protobuf.BoolValue field with
// `[(pxf.default) = "True"]` got the #90 error AND an allocated, empty
// BoolValue on the parent: Has(field) was true with no value inside.
// Every other arm of the same function (Timestamp, Duration, BigInt,
// Decimal, BigFloat) and every scalar arm of applyDefaultImpl parse
// first and touch the message only on success — the wrapper arm was the
// one that did not.
//
// Harmless to a caller that aborts on the error, which is what postDecode
// does; wrong for a layered-config consumer that reports the error and
// continues, since a presence-shaped side effect survives the rejection.
// Observed from chameleon's defaults.Apply against v1.8.0.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// TestApplyDefault_RejectedWrapperLeavesParentUnchanged covers the eight
// wrapper kinds whose inner literal can be rejected, one invalid literal
// each, and asserts the parent still reports the field absent.
func TestApplyDefault_RejectedWrapperLeavesParentUnchanged(t *testing.T) {
	desc := compileWKTDefaults(t)

	cases := []struct {
		field   string
		bad     string
		errKind string
	}{
		{"bv", "True", "invalid default bool"},
		{"i32v", "not-a-number", "invalid default int32"},
		{"i64v", "9223372036854775808", "invalid default int64"},
		{"u32v", "-1", "invalid default uint32"},
		{"u64v", "-1", "invalid default uint64"},
		{"fv", "1.5x", "invalid default float"},
		{"dv", "3.14.15", "invalid default double"},
		{"bytv", "not base64!", "invalid default bytes"},
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

			// The parent must look exactly as it did before the call: no
			// wrapper shell, and nothing in Range.
			assert.False(t, msg.Has(fd),
				"a rejected %s default must not leave a wrapper on the parent", fd.Message().FullName())
			msg.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
				t.Errorf("field %q is populated after a rejected default", fd.Name())
				return true
			})
		})
	}
}

// TestApplyDefault_StringValueAcceptsEveryLiteral is the ninth wrapper.
// A string literal cannot fail to parse, so there is no rejection to
// leave a shell behind: the row pins that the parent is present after
// the call, with the literal inside, for the spellings a bool or
// number wrapper would refuse.
func TestApplyDefault_StringValueAcceptsEveryLiteral(t *testing.T) {
	desc := compileWKTDefaults(t)
	fd := desc.Fields().ByName("sv")
	require.NotNil(t, fd)
	inner := fd.Message().Fields().ByName("value")

	for _, lit := range []string{"True", "not-a-number", "-1", "", "not base64!"} {
		t.Run("lit="+lit, func(t *testing.T) {
			msg := dynamicpb.NewMessage(desc)
			require.NoError(t, pxf.ApplyDefault(msg, fd, lit))
			assert.True(t, msg.Has(fd))
			assert.Equal(t, lit, msg.Get(fd).Message().Get(inner).String())
		})
	}
}

// TestApplyDefault_RejectedWrapperKeepsPriorValue: a rejected default
// must not disturb a wrapper the caller had already populated either —
// the layered-config shape, where a lower layer's value is on the
// message when the defaults pass runs.
func TestApplyDefault_RejectedWrapperKeepsPriorValue(t *testing.T) {
	desc := compileWKTDefaults(t)
	fd := desc.Fields().ByName("bv")
	require.NotNil(t, fd)
	inner := fd.Message().Fields().ByName("value")

	msg := dynamicpb.NewMessage(desc)
	msg.Mutable(fd).Message().Set(inner, protoreflect.ValueOfBool(true))

	require.Error(t, pxf.ApplyDefault(msg, fd, "True"))
	assert.True(t, msg.Has(fd))
	assert.True(t, msg.Get(fd).Message().Get(inner).Bool(),
		"the prior value must survive a rejected default")
}
