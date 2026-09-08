// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Cross-port wire-compatibility dumper, driven by protowire's
// scripts/cross_envelope_check.sh. Every port carries the same program and
// the script compares their output byte for byte.
//
// Four modes:
//
//	dump_envelope
//	    Constructs the canonical Envelope in code and prints its pb bytes as
//	    hex. The same value is constructed in every other port's dumper.
//
//	dump_envelope --pb  FDS MESSAGE DOC
//	dump_envelope --sbe FDS MESSAGE DOC
//	    Decodes the PXF document DOC against MESSAGE (a fully-qualified name)
//	    found in the FileDescriptorSet FDS, applying the PXF annotations the
//	    descriptor carries, and prints the pb (or SBE) bytes as hex. This is
//	    how the script proves every port reads the annotation extension
//	    numbers from a descriptor it did not compile itself (STABILITY.md
//	    promise 3, protowire#244): a port looking for the wrong number
//	    decodes to different bytes, or accepts a document it must reject.
//
//	dump_envelope --vector NAME
//	    Prints the pb bytes of a named wire vector from the spec repo's
//	    testdata/envelope/ (STABILITY.md promise 2, protowire#295). The
//	    script compares them with a checked-in golden rather than with the
//	    other ports, so a layout every port shares wrongly still fails.
//
// Exit status: 0 with hex on stdout; 1 with "reject: <reason>" on stderr
// when the schema rejects DOC (a missing (pxf.required) field, a syntax
// error, a value the field cannot hold); 2 for anything that is the
// harness's fault rather than the document's (bad arguments, unreadable
// files, an FDS that does not build, a message name that is not in it).
// Exit 3 with "not-implemented: <name>" for a vector this port has not built.
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protowire-go/encoding/pb"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"github.com/trendvidia/protowire-go/encoding/sbe"
	"github.com/trendvidia/protowire-go/envelope"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		dumpEnvelope()
		return
	}
	if len(args) == 2 && args[0] == "--vector" {
		dumpVector(args[1])
		return
	}
	if len(args) != 4 || (args[0] != "--pb" && args[0] != "--sbe") {
		fmt.Fprintln(os.Stderr, "usage: dump_envelope [--pb|--sbe FDS MESSAGE DOC | --vector NAME]")
		os.Exit(2)
	}
	dumpFixture(args[0], args[1], args[2], args[3])
}

func dumpEnvelope() {
	env := envelope.Err(402, "INSUFFICIENT_FUNDS", "balance too low",
		"$3.50", "$10.00")
	env.Data = []byte{0xDE, 0xAD, 0xBE, 0xEF}
	env.Error.
		WithField("amount", "MIN_VALUE", "below minimum", "10.00").
		WithMeta("request_id", "req-123")

	data, err := pb.Marshal(env)
	if err != nil {
		fatal(2, err)
	}
	fmt.Println(hex.EncodeToString(data))
}

// dumpVector prints a wire vector the gate checks against a golden. The
// named vectors are the spec repo's testdata/envelope/NAME.textproto.
func dumpVector(name string) {
	var env *envelope.Envelope
	switch name {
	case "zero-map-entry":
		env = &envelope.Envelope{Error: &envelope.AppError{Metadata: map[string]string{"": ""}}}
	default:
		fmt.Fprintf(os.Stderr, "not-implemented: %s\n", name)
		os.Exit(3)
	}
	data, err := pb.Marshal(env)
	if err != nil {
		fatal(2, err)
	}
	fmt.Println(hex.EncodeToString(data))
}

func dumpFixture(mode, fdsPath, message, docPath string) {
	fdsBytes, err := os.ReadFile(fdsPath)
	if err != nil {
		fatal(2, err)
	}
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(fdsBytes, &fds); err != nil {
		fatal(2, fmt.Errorf("%s: %w", fdsPath, err))
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		fatal(2, fmt.Errorf("%s: %w", fdsPath, err))
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(message))
	if err != nil {
		fatal(2, fmt.Errorf("%s: %w", fdsPath, err))
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		fatal(2, fmt.Errorf("%s: %s is not a message", fdsPath, message))
	}
	doc, err := os.ReadFile(docPath)
	if err != nil {
		fatal(2, err)
	}

	// The full decode is the one that validates (pxf.required) and applies
	// (pxf.default); the plain one leaves both to the caller.
	msg, _, err := pxf.UnmarshalFullDescriptor(doc, md)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reject: %v\n", err)
		os.Exit(1)
	}

	var out []byte
	switch mode {
	case "--pb":
		out, err = proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	case "--sbe":
		var codec *sbe.Codec
		codec, err = sbe.NewCodec(md.ParentFile())
		if err == nil {
			out, err = codec.Marshal(msg)
		}
	}
	if err != nil {
		fatal(2, err)
	}
	fmt.Println(hex.EncodeToString(out))
}

func fatal(code int, err error) {
	fmt.Fprintln(os.Stderr, "dump_envelope:", err)
	os.Exit(code)
}
