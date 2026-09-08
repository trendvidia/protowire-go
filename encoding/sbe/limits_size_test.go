// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package sbe_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// TestSizeLimits pins #112 for SBE: the input to one decode call is capped
// at MaxMessageSize before the header is read, on Unmarshal and View alike,
// and a group's numInGroup at MaxRepeatedCount before any entry is
// allocated — each fixed at codec construction, zero meaning the constant.
func TestSizeLimits(t *testing.T) {
	fd := compileProtoSource(t, `syntax = "proto3";
package lim;
import "sbe/annotations.proto";
option (sbe.schema_id) = 7;
option (sbe.version) = 0;
message G {
  option (sbe.template_id) = 1;
  uint32 root = 1;
  message E { uint32 v = 1; }
  repeated E entries = 2;
}`)
	md := fd.Messages().ByName("G")
	require.NotNil(t, md)
	msg := dynamicpb.NewMessage(md)
	msg.Set(md.Fields().ByName("root"), protoreflect.ValueOfUint32(7))
	list := msg.Mutable(md.Fields().ByName("entries")).List()
	vFd := md.Messages().ByName("E").Fields().ByName("v")
	for i := range 5 {
		e := list.NewElement()
		e.Message().Set(vFd, protoreflect.ValueOfUint32(uint32(i))) //nolint:gosec
		list.Append(e)
	}
	codec, err := sbe.NewCodec(fd)
	require.NoError(t, err)
	data, err := codec.Marshal(msg)
	require.NoError(t, err)
	_, err = codec.UnmarshalDescriptor(data, md)
	require.NoError(t, err, "default")

	t.Run("MaxMessageSize", func(t *testing.T) {
		small, err := sbe.CodecOptions{MaxMessageSize: 8}.NewCodec(fd)
		require.NoError(t, err)
		_, err = small.UnmarshalDescriptor(data, md)
		require.ErrorContains(t, err, "MaxMessageSize=8")
		_, err = small.View(data)
		require.ErrorContains(t, err, "MaxMessageSize=8")
		exact, err := sbe.CodecOptions{MaxMessageSize: len(data)}.NewCodec(fd)
		require.NoError(t, err)
		_, err = exact.UnmarshalDescriptor(data, md)
		require.NoError(t, err, "at the bound")

		big := make([]byte, sbe.MaxMessageSize+1)
		copy(big, data)
		_, err = codec.UnmarshalDescriptor(big, md)
		require.ErrorContains(t, err, "MaxMessageSize=67108864", "the default")
	})
	t.Run("MaxRepeatedCount", func(t *testing.T) {
		four, err := sbe.CodecOptions{MaxRepeatedCount: 4}.NewCodec(fd)
		require.NoError(t, err)
		_, err = four.UnmarshalDescriptor(data, md)
		require.ErrorContains(t, err, "MaxRepeatedCount=4")
		five, err := sbe.CodecOptions{MaxRepeatedCount: 5}.NewCodec(fd)
		require.NoError(t, err)
		_, err = five.UnmarshalDescriptor(data, md)
		require.NoError(t, err, "at the bound")
	})
}
