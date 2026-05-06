// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// These tests cover adversarial wire input: malformed payloads must
// return an error (or in the View constructor's case), never panic.
// The bare `recover()` in each helper catches any regression where a
// bounds check is removed and the decoder reaches an out-of-range slice.

func mustNotPanic(t *testing.T, name string, fn func() error) error {
	t.Helper()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s panicked on adversarial input: %v", name, r)
			}
		}()
		err = fn()
	}()
	return err
}

// TestUnmarshal_BlockLengthTooSmall — wire blockLength < schema's must
// be rejected before any field read. Without the guard the decoder
// would slice past the truncated block and panic.
func TestUnmarshal_BlockLengthTooSmall(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	// Simple has two int32 fields → schema blockLength is 8 bytes.
	// Hand-craft a header that lies, claiming blockLength=4.
	desc := fd.Messages().ByName("Simple")
	require.NotNil(t, desc)

	buf := make([]byte, 8+8)                  // header + claimed-but-undersized payload room
	binary.LittleEndian.PutUint16(buf[0:], 4) // blockLength: lie
	binary.LittleEndian.PutUint16(buf[2:], 2) // templateID: Simple
	binary.LittleEndian.PutUint16(buf[4:], 1) // schemaID
	binary.LittleEndian.PutUint16(buf[6:], 0) // version

	msg := dynamicpb.NewMessage(desc)
	err = mustNotPanic(t, "Unmarshal(undersized blockLength)", func() error {
		return codec.Unmarshal(buf, msg)
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "blockLength")
}

// TestUnmarshal_GroupBlockLengthTooSmall — wire group blockLength <
// schema's must be rejected before any entry-field read.
func TestUnmarshal_GroupBlockLengthTooSmall(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	// Order has a Fill group whose schema blockLength is 8+4+8 = 20 bytes
	// (int64 fill_price + uint32 fill_qty + uint64 fill_id).
	desc := fd.Messages().ByName("Order")
	require.NotNil(t, desc)

	// Marshal a valid Order with one fill, then patch the group header to
	// claim a block size of 4 (smaller than the 20 bytes required).
	src := dynamicpb.NewMessage(desc)
	good := mustMarshalOrder(t, codec, src)

	// Find the group header: it sits right after the root block.
	rootBlock := int(binary.LittleEndian.Uint16(good[0:]))
	groupHeader := 8 + rootBlock
	require.True(t, len(good) >= groupHeader+4, "good payload too short")

	bad := make([]byte, len(good))
	copy(bad, good)
	binary.LittleEndian.PutUint16(bad[groupHeader:], 4) // blockLength: lie

	dst := dynamicpb.NewMessage(desc)
	err = mustNotPanic(t, "Unmarshal(undersized group blockLength)", func() error {
		return codec.Unmarshal(bad, dst)
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "blockLength")
}

// TestUnmarshal_GroupNumInGroupOverflow — a group header that claims
// more entries than the remaining bytes can hold must be rejected
// without computing numInGroup*blockLength (which can overflow int on
// 32-bit platforms when both factors approach uint16 max).
func TestUnmarshal_GroupNumInGroupOverflow(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Order")
	require.NotNil(t, desc)

	src := dynamicpb.NewMessage(desc)
	good := mustMarshalOrder(t, codec, src)

	rootBlock := int(binary.LittleEndian.Uint16(good[0:]))
	groupHeader := 8 + rootBlock

	bad := make([]byte, groupHeader+4) // header only — no room for entries
	copy(bad, good[:groupHeader])
	binary.LittleEndian.PutUint16(bad[groupHeader:], 20)     // schema-correct blockLength
	binary.LittleEndian.PutUint16(bad[groupHeader+2:], 1000) // numInGroup: lie

	dst := dynamicpb.NewMessage(desc)
	err = mustNotPanic(t, "Unmarshal(numInGroup overflow)", func() error {
		return codec.Unmarshal(bad, dst)
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "entries")
}

// TestUnmarshal_TruncatedHeader — buffer shorter than the message
// header is already rejected; sanity check that the path is panic-free.
func TestUnmarshal_TruncatedHeader(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	desc := fd.Messages().ByName("Simple")
	msg := dynamicpb.NewMessage(desc)

	for n := 0; n < 8; n++ {
		buf := make([]byte, n)
		err = mustNotPanic(t, "Unmarshal(truncated header)", func() error {
			return codec.Unmarshal(buf, msg)
		})
		require.Error(t, err)
	}
}

// TestView_BlockLengthTooSmall — View constructor must also reject
// undersized blockLength so subsequent accessor calls have a buffer
// large enough for every template offset.
func TestView_BlockLengthTooSmall(t *testing.T) {
	fd := compileTestProto(t)
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)

	buf := make([]byte, 8+8)
	binary.LittleEndian.PutUint16(buf[0:], 4) // blockLength: lie
	binary.LittleEndian.PutUint16(buf[2:], 2) // templateID: Simple

	err = mustNotPanic(t, "Codec.View(undersized blockLength)", func() error {
		_, e := codec.View(buf)
		return e
	})
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "blockLength") ||
			strings.Contains(err.Error(), "too short"),
		"unexpected error: %v", err)
}

func mustMarshalOrder(t *testing.T, codec *sbe.Codec, msg *dynamicpb.Message) []byte {
	t.Helper()
	out, err := codec.Marshal(msg)
	require.NoError(t, err)
	return out
}
