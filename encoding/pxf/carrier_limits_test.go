// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// defaultCarrier builds a 1327 carrier holding one @default whose value
// argument is the given AnnotationArg member (decimal_value = 17,
// big_float_value = 18) with sub as the nested message's bytes — by
// hand, because the compiler never writes the values these tests need.
func defaultCarrier(member protowire.Number, sub []byte) []byte {
	var arg []byte
	arg = protowire.AppendTag(arg, member, protowire.BytesType)
	arg = protowire.AppendBytes(arg, sub)
	var ann []byte
	ann = protowire.AppendTag(ann, 1, protowire.BytesType) // Annotation.name
	ann = protowire.AppendString(ann, "protowire.schema.v1.default")
	ann = protowire.AppendTag(ann, 2, protowire.BytesType) // Annotation.args
	ann = protowire.AppendBytes(ann, arg)
	var list []byte
	list = protowire.AppendTag(list, 1, protowire.BytesType) // AnnotationList.entries
	list = protowire.AppendBytes(list, ann)
	var b []byte
	b = protowire.AppendTag(b, 1327, protowire.BytesType)
	return protowire.AppendBytes(b, list)
}

// decimalArg is a pxf.Decimal{unscaled: 25, scale: scale}.
func decimalArg(scale int32) []byte {
	var sub []byte
	sub = protowire.AppendTag(sub, 1, protowire.BytesType)
	sub = protowire.AppendBytes(sub, []byte{25})
	sub = protowire.AppendTag(sub, 2, protowire.VarintType)
	sub = protowire.AppendVarint(sub, uint64(int64(scale)))
	return sub
}

// bigFloatArg is a pxf.BigFloat{mantissa: 25, exponent: exp, prec: prec}.
func bigFloatArg(exp int32, prec uint32) []byte {
	var sub []byte
	sub = protowire.AppendTag(sub, 1, protowire.BytesType)
	sub = protowire.AppendBytes(sub, []byte{25})
	sub = protowire.AppendTag(sub, 2, protowire.VarintType)
	sub = protowire.AppendVarint(sub, uint64(int64(exp)))
	sub = protowire.AppendTag(sub, 3, protowire.VarintType)
	sub = protowire.AppendVarint(sub, uint64(prec))
	return sub
}

// readCarrier reads the field's default through the public surface:
// the literal when the carrier yields one, otherwise every bind-time
// diagnostic the closure reports, joined. Called under a deadline, so
// it must not touch t.
func readCarrier(fd protoreflect.FieldDescriptor) (def string, problems string) {
	if d, ok := pxf.Default(fd); ok {
		return d, ""
	}
	var details []string
	for _, v := range pxf.ValidateFile(fd.ParentFile()) {
		details = append(details, v.Detail)
	}
	return "", strings.Join(details, "\n")
}

// TestCarrier_DecimalScaleIsBounded: the @default carrier reader
// materialises 10^|scale| exactly as the PB decoder does, from a scale
// the descriptor's producer chose, so it carries the same bound (#95).
// The negative arm also negated before widening, so MinInt32 read as a
// scale of zero; past the bound that is unreachable, and the small
// negative case pins the arm's correct output.
func TestCarrier_DecimalScaleIsBounded(t *testing.T) {
	const max = pxf.MaxNumericLiteralDigits
	cases := []struct {
		name        string
		scale       int32
		wantDef     string
		wantProblem string
	}{
		{"at the bound", max, "0." + strings.Repeat("0", max-2) + "25", ""},
		{"at the negative bound", -max, "25" + strings.Repeat("0", max), ""},
		{"trailing zeros", -5, "2500000", ""},
		{"one past", max + 1, "", "MaxNumericLiteralDigits"},
		{"one past, negative", -max - 1, "", "MaxNumericLiteralDigits"},
		{"MaxInt32", math.MaxInt32, "", "MaxNumericLiteralDigits"},
		{"MinInt32", math.MinInt32, "", "MaxNumericLiteralDigits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := synthField(t, defaultCarrier(17, decimalArg(tc.scale)))
			var def, problems string
			promptly(t, 5*time.Second, func() error { def, problems = readCarrier(fd); return nil })
			if tc.wantProblem != "" {
				require.Empty(t, def, "scale %d must not yield a default", tc.scale)
				require.Contains(t, problems, tc.wantProblem)
				return
			}
			require.Equal(t, tc.wantDef, def)
		})
	}
}

// TestCarrier_BigFloatOverflowIsDiagnosed: a big_float_value past
// big.Float's range came back as +Inf and was rendered as a literal no
// parser takes; protowire#278 makes it a bind-time diagnostic. The
// exponent itself is not bounded here — the cost of rendering a huge
// finite one is protowire#281's question — so only the overflow and a
// large finite value are pinned.
func TestCarrier_BigFloatOverflowIsDiagnosed(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		fd := synthField(t, defaultCarrier(18, bigFloatArg(math.MaxInt32, 64)))
		var def, problems string
		promptly(t, 5*time.Second, func() error { def, problems = readCarrier(fd); return nil })
		require.Empty(t, def)
		require.Contains(t, problems, "above big.Float's range")
	})
	t.Run("large finite", func(t *testing.T) {
		fd := synthField(t, defaultCarrier(18, bigFloatArg(10_000, 64)))
		var def, problems string
		promptly(t, 5*time.Second, func() error { def, problems = readCarrier(fd); return nil })
		require.Empty(t, problems)
		require.True(t, strings.HasSuffix(def, "e+3011"), "25 × 2^10000 renders in exponent form, got %q", def)
	})
}
