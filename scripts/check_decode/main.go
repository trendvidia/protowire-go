// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// check_decode is the Go reference for the per-port `check-decode` binary
// driven by the protowire HARDENING conformance corpus. See:
//
//	protowire/docs/HARDENING.md
//	protowire/scripts/cross_security_check.sh
//	protowire/testdata/adversarial/README.md
//
// Contract:
//
//	check_decode --format <pxf|pb|sbe|envelope> \
//	             --schema <fully.qualified.MessageType> \
//	             --proto  <path-to-adversarial.proto> \
//	             --input  <path>
//
//	Exit 0 → input was accepted
//	Exit 1 → input was rejected (clean error)
//	Other  → bug in the decoder (panic / abort / OOM / hang / ...)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"

	pwpb "github.com/trendvidia/protowire-go/encoding/pb"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Hand-mirrored Go types for adversarial.proto. protowire-go's pb.Unmarshal
// reads `protowire:"<num>"` struct tags via reflect — it does not consume
// FileDescriptor descriptors — so the PB path needs concrete Go types here.
// The PXF path uses descriptors compiled from --proto at runtime.
//
// Mirror of protowire/testdata/adversarial/adversarial.proto. Drift between
// this file and the .proto must be caught by the conformance run itself: a
// missing or wrong tag will fail the manifest's accept/reject expectations.
type Tree struct {
	Child *Tree  `protowire:"1"`
	Label string `protowire:"2"`
}

type StringHolder struct {
	Value string `protowire:"1"`
}

type BytesHolder struct {
	Value []byte `protowire:"1"`
}

type BigIntHolder struct {
	Value int64 `protowire:"1"`
}

func main() {
	var format, schema, proto, input string
	flag.StringVar(&format, "format", "", "pxf|pb|sbe|envelope")
	flag.StringVar(&schema, "schema", "", "fully.qualified.MessageType")
	flag.StringVar(&proto, "proto", "", "path to adversarial.proto")
	flag.StringVar(&input, "input", "", "path to corpus file")
	flag.Parse()

	if format == "" || schema == "" || input == "" {
		fmt.Fprintln(os.Stderr, "usage: check_decode --format ... --schema ... --input ... [--proto ...]")
		os.Exit(2)
	}

	if err := run(format, schema, proto, input); err != nil {
		fmt.Fprintln(os.Stderr, "reject:", err)
		os.Exit(1)
	}
}

func run(format, schema, protoPath, input string) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	switch format {
	case "pxf":
		return pxfDecode(data, schema, protoPath)
	case "pb":
		return pbDecode(data, schema)
	case "envelope":
		return errors.New("envelope decode not yet implemented in this reference")
	case "sbe":
		return errors.New("sbe decode not yet implemented in this reference")
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func pxfDecode(data []byte, schema, protoPath string) error {
	if protoPath == "" {
		return errors.New("--proto is required for format=pxf")
	}
	desc, err := loadDescriptor(protoPath, schema)
	if err != nil {
		return err
	}
	_, err = pxf.UnmarshalDescriptor(data, desc)
	return err
}

func pbDecode(data []byte, schema string) error {
	var msg any
	switch schema {
	case "adversarial.v1.Tree":
		msg = &Tree{}
	case "adversarial.v1.StringHolder":
		msg = &StringHolder{}
	case "adversarial.v1.BytesHolder":
		msg = &BytesHolder{}
	case "adversarial.v1.BigIntHolder":
		msg = &BigIntHolder{}
	default:
		return fmt.Errorf("unknown schema for pb: %s", schema)
	}
	return pwpb.Unmarshal(data, msg)
}

func loadDescriptor(protoPath, schema string) (protoreflect.MessageDescriptor, error) {
	abs, err := filepath.Abs(protoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve proto path: %w", err)
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	// adversarial.proto imports `sbe/annotations.proto` from the spec
	// repo's canonical proto root at <protowire>/proto/. The corpus lives
	// at <protowire>/testdata/adversarial/, so the root is two levels up.
	importPaths := []string{dir}
	if specProto := filepath.Join(dir, "..", "..", "proto"); dirExists(specProto) {
		importPaths = append(importPaths, specProto)
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: importPaths,
		}),
	}
	files, err := compiler.Compile(context.Background(), base)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", protoPath, err)
	}
	if len(files) == 0 {
		return nil, errors.New("compile produced no files")
	}
	fd := files[0]

	for i := 0; i < fd.Messages().Len(); i++ {
		m := fd.Messages().Get(i)
		if string(m.FullName()) == schema {
			return m, nil
		}
	}
	return nil, fmt.Errorf("schema %q not found in %s", schema, protoPath)
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
