// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// nestHolder nests itself: nestTo(n) is n structs deep.
type nestHolder struct {
	Child *nestHolder `protowire:"1"`
	V     int32       `protowire:"2"`
}

func nestTo(depth int) *nestHolder {
	root := &nestHolder{V: 1}
	cur := root
	for i := 1; i < depth; i++ {
		cur.Child = &nestHolder{V: int32(i + 1)} //nolint:gosec
		cur = cur.Child
	}
	return root
}

// TestUnmarshalOptionsLimits pins #101: UnmarshalOptions carries the
// draft's per-call limits with the package constants as their zero-value
// defaults, and a lowered value rejects input the default accepts — on
// Unmarshal and on the length-prefixed stream decoder alike.
func TestUnmarshalOptionsLimits(t *testing.T) {
	t.Run("MaxNestingDepth", func(t *testing.T) {
		data, err := Marshal(nestTo(3))
		require.NoError(t, err)
		var out nestHolder
		require.NoError(t, Unmarshal(data, &out), "default")
		require.NoError(t, UnmarshalOptions{}.Unmarshal(data, &out), "zero options are the default")
		require.NoError(t, UnmarshalOptions{MaxNestingDepth: 3}.Unmarshal(data, &out), "at the bound")
		err = UnmarshalOptions{MaxNestingDepth: 2}.Unmarshal(data, &out)
		require.ErrorContains(t, err, "MaxNestingDepth=2")
		require.False(t, strings.Contains(err.Error(), "MaxNestingDepth=100"),
			"the error names the effective limit, not the constant: %s", err)
	})

	t.Run("MaxNumericLiteralDigits", func(t *testing.T) {
		wire := decimalWire(10, false)
		require.NoError(t, Unmarshal(wire, &decimalHolder{}), "default")
		require.NoError(t, UnmarshalOptions{MaxNumericLiteralDigits: 10}.Unmarshal(wire, &decimalHolder{}), "at the bound")
		err := UnmarshalOptions{MaxNumericLiteralDigits: 9}.Unmarshal(wire, &decimalHolder{})
		require.ErrorContains(t, err, "MaxNumericLiteralDigits=9")
		// Both signs of the scale are bounded by the call's value.
		err = UnmarshalOptions{MaxNumericLiteralDigits: 9}.Unmarshal(decimalWire(-10, false), &decimalHolder{})
		require.ErrorContains(t, err, "MaxNumericLiteralDigits=9")
	})

	t.Run("stream decoder", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, NewEncoder(&buf).Encode(nestTo(3)))
		framed := buf.Bytes()
		var out nestHolder
		require.NoError(t, NewDecoder(bytes.NewReader(framed)).Decode(&out), "default")
		require.NoError(t, UnmarshalOptions{}.NewDecoder(bytes.NewReader(framed)).Decode(&out), "zero options")
		err := UnmarshalOptions{MaxNestingDepth: 2}.NewDecoder(bytes.NewReader(framed)).Decode(&out)
		require.ErrorContains(t, err, "MaxNestingDepth=2")
	})
}

// TestNestingDepthBoundary pins where pb's counter sits (#111): the root
// struct is depth 1, as protobuf-go and prost count their recursion
// limits, so MaxNestingDepth structs deep including the root is the last
// accepted shape.
func TestNestingDepthBoundary(t *testing.T) {
	for _, tc := range []struct {
		depth int
		ok    bool
	}{{MaxNestingDepth, true}, {MaxNestingDepth + 1, false}} {
		data, err := Marshal(nestTo(tc.depth))
		require.NoError(t, err)
		err = Unmarshal(data, &nestHolder{})
		if tc.ok {
			require.NoError(t, err, "%d structs deep, root included", tc.depth)
		} else {
			require.ErrorContains(t, err, "MaxNestingDepth=100", "%d structs deep", tc.depth)
		}
	}
}
