// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestFormatMapKeyForPath_KindBranches exercises every branch of
// formatMapKeyForPath. The public OnSecretField path only reaches the
// string branch (chameleon's tenant_keys schema uses map<string,
// pxf.Secret>); the other branches are dispatched on protoreflect
// map-key kinds that show up when consumers use non-string map keys,
// which is rare in practice but valid in the proto3 grammar
// (int32/int64/uint32/uint64/sint*/fixed*/sfixed*/bool keys are all
// permitted). Cover here so the helper stays honest if someone wires
// up a numeric-keyed secret map in the future.
func TestFormatMapKeyForPath_KindBranches(t *testing.T) {
	cases := []struct {
		name string
		key  protoreflect.MapKey
		want string
	}{
		{"string", protoreflect.ValueOfString("acme").MapKey(), `"acme"`},
		{"int32", protoreflect.ValueOfInt32(-42).MapKey(), "-42"},
		{"int64", protoreflect.ValueOfInt64(9_000_000_000).MapKey(), "9000000000"},
		{"uint32", protoreflect.ValueOfUint32(7).MapKey(), "7"},
		{"uint64", protoreflect.ValueOfUint64(1 << 40).MapKey(), "1099511627776"},
		{"bool", protoreflect.ValueOfBool(true).MapKey(), "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, formatMapKeyForPath(c.key))
		})
	}
}
