// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Dumps a canonical SBE-stream (SOFH-framed) as hex, for cross-port
// wire-compat checking. Three frames of `stream.v1.Simple`.
//
// The proto source is inlined here (mirroring the encoding/sbe test
// helper) until the spec repo lands testdata/stream/sbe-stream.binpb;
// once it does, switch this dumper to load that FileDescriptorSet
// the way scripts/bench_sbe does.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/sbe"
)

const sbeAnnotationsSrc = `
syntax = "proto3";
package sbe;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FileOptions {
  uint32 schema_id = 50100;
  uint32 version = 50101;
}
extend google.protobuf.MessageOptions {
  uint32 template_id = 50200;
}
extend google.protobuf.FieldOptions {
  uint32 length = 50300;
  string encoding = 50301;
}
`

const streamProtoSrc = `
syntax = "proto3";
package stream.v1;
import "sbe/annotations.proto";

option (sbe.schema_id) = 1;
option (sbe.version) = 0;

message Simple {
  option (sbe.template_id) = 2;
  uint32 id = 1;
  int32 value = 2;
}
`

func main() {
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"sbe/annotations.proto": sbeAnnotationsSrc,
					"stream.proto":          streamProtoSrc,
				}),
			},
		),
	}
	files, err := comp.Compile(context.Background(), "stream.proto")
	must(err, "compile")

	var fd protoreflect.FileDescriptor
	for _, f := range files {
		if f.Path() == "stream.proto" {
			fd = f
			break
		}
	}
	if fd == nil {
		fail("stream.proto not found")
	}

	codec, err := sbe.NewCodec(fd)
	must(err, "build codec")

	desc := fd.Messages().ByName("Simple")
	idF := desc.Fields().ByName("id")
	valF := desc.Fields().ByName("value")

	mk := func(id uint32, value int32) *dynamicpb.Message {
		m := dynamicpb.NewMessage(desc)
		m.Set(idF, protoreflect.ValueOfUint32(id))
		m.Set(valF, protoreflect.ValueOfInt32(value))
		return m
	}

	var buf bytes.Buffer
	enc := codec.NewEncoder(&buf)
	for i, m := range []*dynamicpb.Message{
		mk(1, -100),
		mk(2, 0),
		mk(1<<20, 2147483647),
	} {
		if err := enc.Encode(m); err != nil {
			fmt.Fprintf(os.Stderr, "frame %d: %v\n", i, err)
			os.Exit(1)
		}
	}
	fmt.Println(hex.EncodeToString(buf.Bytes()))
}

func must(err error, what string) {
	if err != nil {
		fail(what + ": " + err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "dump-stream-sbe:", msg)
	os.Exit(1)
}
