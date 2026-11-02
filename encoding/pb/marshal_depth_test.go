// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

// Marshal refuses a value deeper than a decoder at the same limit accepts,
// so nothing it writes is refused for depth when read back (#234).

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type depthNode struct {
	Next   *depthNode            `protowire:"1"`
	Kids   []*depthNode          `protowire:"2"`
	ByKey  map[string]*depthNode `protowire:"3"`
	Scalar map[string]int32      `protowire:"4"`
	V      int32                 `protowire:"5"`
}

// Each wrap adds levels to the chain; each leaf sits at its bottom.
func TestMarshal_DepthAgreesWithUnmarshal(t *testing.T) {
	wraps := map[string]func(*depthNode) *depthNode{
		"singular": func(c *depthNode) *depthNode { return &depthNode{Next: c} },
		"list":     func(c *depthNode) *depthNode { return &depthNode{Kids: []*depthNode{c}} },
		"map":      func(c *depthNode) *depthNode { return &depthNode{ByKey: map[string]*depthNode{"k": c}} },
	}
	leaves := map[string]func() *depthNode{
		"scalar":     func() *depthNode { return &depthNode{V: 1} },
		"scalar map": func() *depthNode { return &depthNode{Scalar: map[string]int32{"a": 1}} },
	}
	for wn, wrap := range wraps {
		for ln, leaf := range leaves {
			written, refused := 0, 0
			for n := 1; n <= MaxNestingDepth+2; n++ {
				m := leaf()
				for range n {
					m = wrap(m)
				}
				out, err := Marshal(m)
				if err != nil {
					require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100", "%s/%s %d", wn, ln, n)
					refused++
					continue
				}
				written++
				require.NoError(t, Unmarshal(out, &depthNode{}), "%s/%s %d", wn, ln, n)
			}
			assert.Positive(t, written, "%s/%s", wn, ln)
			assert.Positive(t, refused, "%s/%s", wn, ln)
		}
	}
}

func deepNode(n int) *depthNode {
	m := &depthNode{V: 1}
	for range n {
		m = &depthNode{Next: m}
	}
	return m
}

func TestMarshal_DepthBound(t *testing.T) {
	out, err := Marshal(deepNode(MaxNestingDepth))
	require.NoError(t, err)
	require.NoError(t, Unmarshal(out, &depthNode{}))
	_, err = Marshal(deepNode(MaxNestingDepth + 1))
	require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100")
}

func TestMarshalOptions_MaxNestingDepth(t *testing.T) {
	m := deepNode(150)
	out, err := MarshalOptions{MaxNestingDepth: 150}.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, UnmarshalOptions{MaxNestingDepth: 150}.Unmarshal(out, &depthNode{}))
	_, err = MarshalOptions{MaxNestingDepth: 149}.Marshal(m)
	require.ErrorContains(t, err, "MaxNestingDepth=149")
	_, err = MarshalOptions{MaxNestingDepth: 2}.Marshal(deepNode(3))
	require.ErrorContains(t, err, "MaxNestingDepth=2")
}

func TestEncoder_Depth(t *testing.T) {
	var buf bytes.Buffer
	require.ErrorContains(t, NewEncoder(&buf).Encode(deepNode(101)), "MaxNestingDepth=100")
	require.Zero(t, buf.Len(), "a refused message writes no frame")
	require.NoError(t, MarshalOptions{MaxNestingDepth: 101}.NewEncoder(&buf).Encode(deepNode(101)))
	require.NotZero(t, buf.Len())
}

// A pointer cycle used to overflow the stack, which no recover catches.
func TestMarshal_PointerCycle(t *testing.T) {
	m := &depthNode{}
	m.Next = m
	_, err := Marshal(m)
	require.ErrorContains(t, err, "nesting depth exceeds MaxNestingDepth=100")
}
