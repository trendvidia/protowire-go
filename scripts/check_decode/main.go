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
//	check_decode --format <pxf|pxf-pb|pxf-fmt|pb|pb-render|pb-text|sbe|envelope> \
//	             --schema <fully.qualified.MessageType> \
//	             --proto  <path-to-adversarial.proto> \
//	             --input  <path>
//
//	Exit 0 → input was accepted
//	Exit 1 → input was rejected (clean error)
//	Exit 2 → the harness cannot judge the row (bad flags, or a pb mirror
//	         that does not match --proto), which the gate reports as a crash
//	Other  → bug in the decoder (panic / abort / OOM / hang / ...)
//
// pb-text and pxf-pb also write their output on stdout, for the text gate
// and the write gate (protowire/scripts/cross_text_check.sh and
// cross_write_check.sh). pxf-fmt writes the formatted document on stdout, for
// the format gate (cross_fmt_check.sh); it needs no --schema or --proto.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	pwpb "github.com/trendvidia/protowire-go/encoding/pb"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"github.com/trendvidia/protowire-go/encoding/sbe"
)

// Hand-mirrored Go types for adversarial.proto. protowire-go's pb.Unmarshal
// reads `protowire:"<num>"` struct tags via reflect — it does not consume
// FileDescriptor descriptors — so the PB path needs concrete Go types here.
// The PXF path uses descriptors compiled from --proto at runtime.
//
// Mirror of protowire/testdata/adversarial/adversarial.proto. pbDecode
// compares the row's mirror with the message --proto compiles to, field
// number by field number, and exits 2 on any difference. A row's verdict
// alone cannot catch drift: pb.Unmarshal skips a field the struct does not
// declare, so a missing field is accepted at any depth (#285).
type Tree struct {
	Child    *Tree             `protowire:"1"`
	Label    string            `protowire:"2"`
	Children []*Tree           `protowire:"3"`
	Branches map[string]*Tree  `protowire:"4"`
	Tags     map[string]string `protowire:"5"`
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

type ListHolder struct {
	Values []int32 `protowire:"1"`
}

// BigNumHolder mirrors adversarial.v1.BigNumHolder (protowire#279): the
// arbitrary-precision carriers, so the corpus can prove the digit cap on a
// field that only the cap can reject, and the Decimal.scale bound.
type BigNumHolder struct {
	BigInt   *big.Int   `protowire:"1"`
	Decimal  *big.Rat   `protowire:"2"`
	BigFloat *big.Float `protowire:"3"`
}

func main() {
	var format, schema, proto, input string
	flag.StringVar(&format, "format", "", "pxf|pxf-pb|pxf-fmt|pb|pb-render|pb-text|sbe|envelope")
	flag.StringVar(&schema, "schema", "", "fully.qualified.MessageType")
	flag.StringVar(&proto, "proto", "", "path to adversarial.proto")
	flag.StringVar(&input, "input", "", "path to corpus file")
	var limits limitFlags
	flag.Var(&limits, "limit", "NAME=VALUE, repeatable: lower a HARDENING limit for this run — MaxMessageSize, MaxNestingDepth, MaxNumericLiteralDigits, MaxBytesLiteralLength, MaxRepeatedCount, MaxBigFloatExponent")
	flag.Parse()

	// Formatting reads no schema, so pxf-fmt alone may leave --schema out.
	if format == "" || (schema == "" && format != "pxf-fmt") || input == "" {
		fmt.Fprintln(os.Stderr, "usage: check_decode --format ... --schema ... --input ... [--proto ...]")
		os.Exit(2)
	}

	if err := run(format, schema, proto, input, limits); err != nil {
		var he harnessError
		if errors.As(err, &he) {
			fmt.Fprintln(os.Stderr, "check_decode:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "reject:", err)
		os.Exit(1)
	}
}

// harnessError is a failure of the harness, not a verdict on the input,
// so main exits 2 for it rather than 1. Exiting 1 would count it as a
// rejection, and a reject row would pass without its input being read.
type harnessError struct{ error }

func harnessErrorf(format string, a ...any) error {
	return harnessError{fmt.Errorf(format, a...)}
}

// checkMirror reports a difference between the field numbers a pb mirror
// declares in its protowire tags and the fields of md, in either direction.
func checkMirror(md protoreflect.MessageDescriptor, mirror any) error {
	declared := map[protoreflect.FieldNumber]string{}
	rt := reflect.TypeOf(mirror).Elem()
	for i := 0; i < rt.NumField(); i++ {
		tag, ok := rt.Field(i).Tag.Lookup("protowire")
		if !ok {
			continue
		}
		num, err := strconv.Atoi(strings.SplitN(tag, ",", 2)[0])
		if err != nil {
			return fmt.Errorf("pb mirror %s.%s: bad tag %q", rt.Name(), rt.Field(i).Name, tag)
		}
		declared[protoreflect.FieldNumber(num)] = rt.Field(i).Name
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if _, ok := declared[fd.Number()]; !ok {
			return fmt.Errorf("pb mirror %s has no field %d (%s) of %s", rt.Name(), fd.Number(), fd.Name(), md.FullName())
		}
		delete(declared, fd.Number())
	}
	for num, name := range declared {
		return fmt.Errorf("pb mirror %s.%s is field %d, which %s does not have", rt.Name(), name, num, md.FullName())
	}
	return nil
}

func run(format, schema, protoPath, input string, lim limitFlags) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return harnessErrorf("read input: %w", err)
	}
	switch format {
	case "pxf":
		return pxfDecode(data, schema, protoPath, lim)
	case "pxf-pb":
		return pxfPb(data, schema, protoPath, lim)
	case "pxf-fmt":
		return pxfFmt(data)
	case "pb":
		return pbDecode(data, schema, protoPath, lim)
	case "pb-render":
		return pbRender(data, schema, protoPath)
	case "pb-text":
		return pbText(data, schema, protoPath)
	case "envelope":
		return harnessErrorf("envelope decode not yet implemented in this reference")
	case "sbe":
		return sbeDecode(data, schema, protoPath, lim)
	default:
		return harnessErrorf("unsupported format: %s", format)
	}
}

func pxfDecode(data []byte, schema, protoPath string, lim limitFlags) error {
	if protoPath == "" {
		return harnessErrorf("--proto is required for format=pxf")
	}
	desc, err := loadDescriptor(protoPath, schema)
	if err != nil {
		return err
	}
	_, err = pxf.UnmarshalOptions{
		MaxMessageSize:          lim["MaxMessageSize"],
		MaxNestingDepth:         lim["MaxNestingDepth"],
		MaxNumericLiteralDigits: lim["MaxNumericLiteralDigits"],
		MaxBytesLiteralLength:   lim["MaxBytesLiteralLength"],
		MaxRepeatedCount:        lim["MaxRepeatedCount"],
		MaxBigFloatExponent:     lim["MaxBigFloatExponent"],
	}.UnmarshalDescriptor(data, desc)
	return err
}

// pxfPb writes the PB bytes for a PXF document on stdout, for the write gate
// (protowire/scripts/cross_write_check.sh). It decodes as dump_envelope does,
// with the full decode that validates (pxf.required) and applies
// (pxf.default), and writes deterministically, so map entries are sorted by
// key. Text and security gates never compare these bytes (protowire#509).
func pxfPb(data []byte, schema, protoPath string, lim limitFlags) error {
	if protoPath == "" {
		return harnessErrorf("--proto is required for format=pxf-pb")
	}
	desc, err := loadDescriptor(protoPath, schema)
	if err != nil {
		return err
	}
	msg, _, err := pxf.UnmarshalOptions{
		MaxMessageSize:          lim["MaxMessageSize"],
		MaxNestingDepth:         lim["MaxNestingDepth"],
		MaxNumericLiteralDigits: lim["MaxNumericLiteralDigits"],
		MaxBytesLiteralLength:   lim["MaxBytesLiteralLength"],
		MaxRepeatedCount:        lim["MaxRepeatedCount"],
		MaxBigFloatExponent:     lim["MaxBigFloatExponent"],
	}.UnmarshalFullDescriptor(data, desc)
	if err != nil {
		return err
	}
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}

// pxfFmt writes the formatted document on stdout, for the format gate
// (protowire/scripts/cross_fmt_check.sh, protowire#584). Formatting works on
// the parsed document, not a message, so it keeps comments, directives and
// dataset rows and needs no schema. A document that does not parse is a
// rejection.
func pxfFmt(data []byte) error {
	doc, err := pxf.Parse(data)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(pxf.FormatDocument(doc))
	return err
}

func pbDecode(data []byte, schema, protoPath string, lim limitFlags) error {
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
	case "adversarial.v1.ListHolder":
		msg = &ListHolder{}
	case "adversarial.v1.BigNumHolder":
		msg = &BigNumHolder{}
	default:
		return harnessErrorf("unknown schema for pb: %s", schema)
	}
	if protoPath != "" {
		desc, err := loadDescriptor(protoPath, schema)
		if err != nil {
			return err
		}
		if err := checkMirror(desc, msg); err != nil {
			return harnessError{err}
		}
	}
	return pwpb.UnmarshalOptions{
		MaxMessageSize:          lim["MaxMessageSize"],
		MaxNestingDepth:         lim["MaxNestingDepth"],
		MaxNumericLiteralDigits: lim["MaxNumericLiteralDigits"],
		MaxRepeatedCount:        lim["MaxRepeatedCount"],
		MaxBigFloatExponent:     lim["MaxBigFloatExponent"],
	}.Unmarshal(data, msg)
}

// pbRender is the producer's side of the bounds (HARDENING.md §
// Arbitrary-precision magnitudes): the bytes are read by the stock protobuf
// runtime, which stores the fields and bounds nothing, and then written as
// PXF. The hardened decoder is bypassed on purpose, or it would refuse the row
// and the encoder would go untested (protowire#448).
// pbText writes the PXF for data on stdout, for the text gate
// (protowire/scripts/cross_text_check.sh). The stock runtime reads the bytes,
// as in pbRender, so the comparison is of the marshaller alone.
func pbText(data []byte, schema, protoPath string) error {
	if protoPath == "" {
		return harnessErrorf("--proto is required for format=pb-text")
	}
	desc, err := loadDescriptor(protoPath, schema)
	if err != nil {
		return err
	}
	msg := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	text, err := pxf.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(text)
	return err
}

func pbRender(data []byte, schema, protoPath string) error {
	if protoPath == "" {
		return harnessErrorf("--proto is required for format=pb-render")
	}
	desc, err := loadDescriptor(protoPath, schema)
	if err != nil {
		return err
	}
	msg := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(data, msg); err != nil {
		return err
	}
	_, err = pxf.Marshal(msg)
	return err
}

// sbeDecode binds the codec from the same runtime-compiled proto the PXF
// path uses (adversarial.proto carries the sbe options), so the corpus's
// SBE rows are decoded rather than refused for want of a codec.
func sbeDecode(data []byte, schema, protoPath string, lim limitFlags) error {
	if protoPath == "" {
		return harnessErrorf("--proto is required for format=sbe")
	}
	desc, err := loadDescriptor(protoPath, schema)
	if err != nil {
		return err
	}
	codec, err := sbe.CodecOptions{
		MaxMessageSize:   lim["MaxMessageSize"],
		MaxRepeatedCount: lim["MaxRepeatedCount"],
	}.NewCodec(desc.ParentFile())
	if err != nil {
		return harnessError{err}
	}
	_, err = codec.UnmarshalDescriptor(data, desc)
	return err
}

func loadDescriptor(protoPath, schema string) (protoreflect.MessageDescriptor, error) {
	abs, err := filepath.Abs(protoPath)
	if err != nil {
		return nil, harnessErrorf("resolve proto path: %w", err)
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
		return nil, harnessErrorf("compile %s: %w", protoPath, err)
	}
	if len(files) == 0 {
		return nil, harnessErrorf("compile produced no files")
	}
	fd := files[0]

	for i := 0; i < fd.Messages().Len(); i++ {
		m := fd.Messages().Get(i)
		if string(m.FullName()) == schema {
			return m, nil
		}
	}
	return nil, harnessErrorf("schema %q not found in %s", schema, protoPath)
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// limitFlags collects --limit NAME=VALUE flags: a HARDENING limit lowered
// for this run, so the conformance corpus can prove a limit with a small
// fixture (protowire#299) rather than a 64 MiB one. Zero means the
// package default; every name the draft's § Mandatory Limits lists but
// MaxVarintBytes is accepted, and MaxBytesLiteralLength only reaches the
// PXF decoder.
type limitFlags map[string]int

func (l limitFlags) String() string { return fmt.Sprint(map[string]int(l)) }

func (l *limitFlags) Set(s string) error {
	name, value, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("--limit wants NAME=VALUE, got %q", s)
	}
	switch name {
	case "MaxMessageSize", "MaxNestingDepth", "MaxNumericLiteralDigits", "MaxBytesLiteralLength", "MaxRepeatedCount", "MaxBigFloatExponent":
	default:
		return fmt.Errorf("--limit: unknown limit %q", name)
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fmt.Errorf("--limit %s: want a positive integer, got %q", name, value)
	}
	if *l == nil {
		*l = limitFlags{}
	}
	(*l)[name] = n
	return nil
}
