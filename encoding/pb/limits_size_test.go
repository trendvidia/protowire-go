// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pb

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

type blobHolder struct {
	B []byte `protowire:"1"`
}

type listHolder struct {
	Xs []int32          `protowire:"1"`
	M  map[string]int32 `protowire:"2"`
	Ss []string         `protowire:"3"`
}

// TestMaxMessageSize pins #112: the input to one decode call is capped at
// MaxMessageSize (HARDENING.md § Mandatory limits) before anything is
// read — at the default, and lowered per call, on Unmarshal and on the
// stream decoder alike.
func TestMaxMessageSize(t *testing.T) {
	t.Run("default rejects 64 MiB + tag", func(t *testing.T) {
		data, err := Marshal(&blobHolder{B: make([]byte, MaxMessageSize)})
		require.NoError(t, err)
		require.Greater(t, len(data), MaxMessageSize, "tag and length push it past the cap")
		err = Unmarshal(data, &blobHolder{})
		require.ErrorContains(t, err, "MaxMessageSize=67108864")
	})
	t.Run("default accepts 64 MiB", func(t *testing.T) {
		data, err := Marshal(&blobHolder{B: make([]byte, MaxMessageSize-16)})
		require.NoError(t, err)
		require.LessOrEqual(t, len(data), MaxMessageSize)
		require.NoError(t, Unmarshal(data, &blobHolder{}))
	})
	t.Run("lowered per call", func(t *testing.T) {
		data, err := Marshal(&blobHolder{B: make([]byte, 32)})
		require.NoError(t, err)
		require.NoError(t, Unmarshal(data, &blobHolder{}), "default")
		require.NoError(t, UnmarshalOptions{MaxMessageSize: len(data)}.Unmarshal(data, &blobHolder{}), "at the bound")
		err = UnmarshalOptions{MaxMessageSize: 16}.Unmarshal(data, &blobHolder{})
		require.ErrorContains(t, err, "MaxMessageSize=16")

		// A frame inside the stream's frame cap, payload past the call's cap.
		var buf bytes.Buffer
		require.NoError(t, NewEncoder(&buf).Encode(&blobHolder{B: make([]byte, 32)}))
		require.NoError(t, NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&blobHolder{}), "default")
		err = UnmarshalOptions{MaxMessageSize: 16}.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&blobHolder{})
		require.ErrorContains(t, err, "MaxMessageSize=16")
	})
}

// TestMaxRepeatedCount pins #112: the element count of a repeated or map
// field is capped at MaxRepeatedCount, lowered per call, on packed and
// unpacked elements and on map entries.
func TestMaxRepeatedCount(t *testing.T) {
	data, err := Marshal(&listHolder{
		Xs: []int32{1, 2, 3, 4, 5},
		M:  map[string]int32{"a": 1, "b": 2, "c": 3},
		Ss: []string{"a", "b", "c"},
	})
	require.NoError(t, err)
	require.NoError(t, Unmarshal(data, &listHolder{}), "default")
	require.NoError(t, UnmarshalOptions{MaxRepeatedCount: 5}.Unmarshal(data, &listHolder{}), "at the bound")
	err = UnmarshalOptions{MaxRepeatedCount: 4}.Unmarshal(data, &listHolder{})
	require.ErrorContains(t, err, "MaxRepeatedCount=4", "five packed int32s")
	err = UnmarshalOptions{MaxRepeatedCount: 2}.Unmarshal(data, &listHolder{})
	require.ErrorContains(t, err, "MaxRepeatedCount=2")

	// Unpacked strings and map entries hit the same bound.
	data, err = Marshal(&listHolder{Ss: []string{"a", "b", "c"}})
	require.NoError(t, err)
	require.ErrorContains(t, UnmarshalOptions{MaxRepeatedCount: 2}.Unmarshal(data, &listHolder{}), "MaxRepeatedCount=2")
	data, err = Marshal(&listHolder{M: map[string]int32{"a": 1, "b": 2, "c": 3}})
	require.NoError(t, err)
	require.ErrorContains(t, UnmarshalOptions{MaxRepeatedCount: 2}.Unmarshal(data, &listHolder{}), "MaxRepeatedCount=2")
	require.NoError(t, UnmarshalOptions{MaxRepeatedCount: 3}.Unmarshal(data, &listHolder{}), "three entries at the bound")
}
