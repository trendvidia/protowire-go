// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Draft §3.8: for float and double fields, decoders MUST accept the
// identifiers "inf", "+inf", "-inf", "nan". (#47)

func TestNonFinite_DecodeIdentifiers(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	cases := []struct {
		literal string
		want    float64 // NaN checked via IsNaN
	}{
		{"inf", math.Inf(1)},
		{"+inf", math.Inf(1)},
		{"-inf", math.Inf(-1)},
		{"nan", math.NaN()},
	}
	for _, field := range []string{"float_field", "double_field"} {
		for _, tc := range cases {
			t.Run(field+"/"+tc.literal, func(t *testing.T) {
				msg, err := pxf.UnmarshalDescriptor([]byte(field+" = "+tc.literal), desc)
				require.NoError(t, err)
				got := msg.Get(desc.Fields().ByName(protoreflect.Name(field))).Float()
				if math.IsNaN(tc.want) {
					assert.True(t, math.IsNaN(got), "want NaN, got %v", got)
				} else {
					assert.Equal(t, tc.want, got)
				}
			})
		}
	}
}

func TestNonFinite_MarshalRoundTrip(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	floatFd := desc.Fields().ByName("float_field")
	doubleFd := desc.Fields().ByName("double_field")

	for name, v := range map[string]float64{
		"+inf": math.Inf(1),
		"-inf": math.Inf(-1),
		"nan":  math.NaN(),
	} {
		t.Run(name, func(t *testing.T) {
			msg := dynamicpb.NewMessage(desc)
			msg.Set(floatFd, protoreflect.ValueOfFloat32(float32(v)))
			msg.Set(doubleFd, protoreflect.ValueOfFloat64(v))

			out, err := pxf.Marshal(msg)
			require.NoError(t, err)

			back, err := pxf.UnmarshalDescriptor(out, desc)
			require.NoError(t, err, "own Marshal output must round-trip: %q", out)

			gotF := back.Get(floatFd).Float()
			gotD := back.Get(doubleFd).Float()
			if math.IsNaN(v) {
				assert.True(t, math.IsNaN(gotF), "float_field: want NaN, got %v", gotF)
				assert.True(t, math.IsNaN(gotD), "double_field: want NaN, got %v", gotD)
			} else {
				assert.Equal(t, v, gotF)
				assert.Equal(t, v, gotD)
			}
		})
	}
}

func TestNonFinite_RepeatedAndWrapper(t *testing.T) {
	fd := compileFiles(t, map[string]string{"nonfinite.proto": `
syntax = "proto3";
package nonfinite.v1;
import "google/protobuf/wrappers.proto";
message M {
  repeated double values = 1;
  google.protobuf.DoubleValue wrapped = 2;
  google.protobuf.FloatValue wrapped_f = 3;
}
`})
	desc := findMsg(t, fd, "M")

	in := `
values = [inf, -inf, nan, +inf, 1.5]
wrapped = -inf
wrapped_f = nan
`
	msg, err := pxf.UnmarshalDescriptor([]byte(in), desc)
	require.NoError(t, err)

	list := msg.Get(desc.Fields().ByName("values")).List()
	require.Equal(t, 5, list.Len())
	assert.Equal(t, math.Inf(1), list.Get(0).Float())
	assert.Equal(t, math.Inf(-1), list.Get(1).Float())
	assert.True(t, math.IsNaN(list.Get(2).Float()))
	assert.Equal(t, math.Inf(1), list.Get(3).Float())
	assert.Equal(t, 1.5, list.Get(4).Float())

	wrapped := msg.Get(desc.Fields().ByName("wrapped")).Message()
	assert.Equal(t, math.Inf(-1), wrapped.Get(wrapped.Descriptor().Fields().ByName("value")).Float())
	wrappedF := msg.Get(desc.Fields().ByName("wrapped_f")).Message()
	assert.True(t, math.IsNaN(wrappedF.Get(wrappedF.Descriptor().Fields().ByName("value")).Float()))
}

func TestNonFinite_DatasetCells(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	in := `@dataset test.v1.AllTypes (string_field, double_field, float_field)
("a", inf, -inf)
("b", -inf, nan)
("c", nan, +inf)`
	tr, err := pxf.NewDatasetReader(strings.NewReader(in))
	require.NoError(t, err)

	type row struct{ d, f float64 }
	var got []row
	for {
		msg := dynamicpb.NewMessage(allTypes)
		if err := tr.Scan(msg); errors.Is(err, io.EOF) {
			break
		} else {
			require.NoError(t, err)
		}
		got = append(got, row{
			d: msg.Get(allTypes.Fields().ByName("double_field")).Float(),
			f: msg.Get(allTypes.Fields().ByName("float_field")).Float(),
		})
	}
	require.Len(t, got, 3)
	assert.Equal(t, math.Inf(1), got[0].d)
	assert.Equal(t, math.Inf(-1), got[0].f)
	assert.Equal(t, math.Inf(-1), got[1].d)
	assert.True(t, math.IsNaN(got[1].f))
	assert.True(t, math.IsNaN(got[2].d))
	assert.Equal(t, math.Inf(1), got[2].f)
}

// The identifiers are exactly the four lowercase spellings; nothing
// looser gets through, and non-float fields still reject them.
func TestNonFinite_Rejections(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	cases := []struct {
		name  string
		input string
	}{
		{"uppercase Inf", "double_field = Inf"},
		{"uppercase NaN", "double_field = NaN"},
		{"infinity spelled out", "double_field = infinity"},
		{"negative infinity spelled out", "double_field = -infinity"},
		{"signed nan", "double_field = -nan"},
		{"plus signed nan", "double_field = +nan"},
		{"plus on a finite number", "double_field = +1.5"},
		{"inf-prefixed identifier", "double_field = -inf5"},
		{"overflow rounds to inf", "double_field = 1e999"},
		{"inf into int32", "int32_field = inf"},
		{"inf into string", "string_field = inf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pxf.UnmarshalDescriptor([]byte(tc.input), desc)
			assert.Error(t, err, "input %q must not decode", tc.input)
		})
	}
}
