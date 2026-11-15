// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// TestElementLiteralRecordsOnlyTheElement pins the presence rule the owner
// kept in #298: a well-known-type literal in a list element or map value
// records the field only, while the block form records the inner field it
// writes. pxf.Secret is the one exception, and its literal records `value`
// in every position (#289, TestSecretElementLiteral_MarksInnerValuePresent).
// A field's literal records its inner fields; that is pinned in
// TestMarkInnerPresent_TimestampScalarShorthand.
func TestElementLiteralRecordsOnlyTheElement(t *testing.T) {
	types := []struct {
		name    string // the message type
		literal string // a literal of it
		block   string // a block of it that writes one inner field
		inner   string // that inner field
	}{
		{"google.protobuf.Timestamp", "2024-01-01T00:00:00Z", "{ seconds = 1 }", "seconds"},
		{"google.protobuf.Duration", "2s", "{ seconds = 2 }", "seconds"},
		{"google.protobuf.DoubleValue", "1.5", "{ value = 1.5 }", "value"},
		{"google.protobuf.FloatValue", "1.5", "{ value = 1.5 }", "value"},
		{"google.protobuf.Int64Value", "7", "{ value = 7 }", "value"},
		{"google.protobuf.UInt64Value", "7", "{ value = 7 }", "value"},
		{"google.protobuf.Int32Value", "7", "{ value = 7 }", "value"},
		{"google.protobuf.UInt32Value", "7", "{ value = 7 }", "value"},
		{"google.protobuf.BoolValue", "true", "{ value = true }", "value"},
		{"google.protobuf.StringValue", `"x"`, `{ value = "x" }`, "value"},
		{"google.protobuf.BytesValue", `b"AQID"`, `{ value = b"AQID" }`, "value"},
		{"pxf.BigInt", "-12", "{ negative = true }", "negative"},
		{"pxf.Decimal", "-1.5", "{ negative = true }", "negative"},
		{"pxf.BigFloat", "-2.5", "{ negative = true }", "negative"},
	}

	var msgs strings.Builder
	for i, ty := range types {
		fmt.Fprintf(&msgs, "message T%d { repeated %s l = 1; map<string, %s> m = 2; }\n", i, ty.name, ty.name)
	}
	src := `syntax = "proto3";
package elempres.v1;
import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";
import "pxf/bignum.proto";
` + msgs.String()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{
				"test.proto":       src,
				"pxf/bignum.proto": bignumProtoSrc,
			}),
		}),
	}
	files, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)

	for i, ty := range types {
		md := files[0].Messages().ByName(protoreflect.Name(fmt.Sprintf("T%d", i)))
		for _, tc := range []struct {
			doc  string
			want []string
		}{
			{"l = [" + ty.literal + "]", []string{"l"}},
			{`m = { "a": ` + ty.literal + " }", []string{"m"}},
			{"l = [" + ty.block + "]", []string{"l", "l[0]." + ty.inner}},
			{`m = { "a": ` + ty.block + " }", []string{"m", `m["a"].` + ty.inner}},
		} {
			t.Run(ty.name+"/"+tc.doc, func(t *testing.T) {
				_, res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFullDescriptor([]byte(tc.doc), md)
				require.NoError(t, err)
				require.ElementsMatch(t, tc.want, res.PresentFields())
			})
		}
	}
}
