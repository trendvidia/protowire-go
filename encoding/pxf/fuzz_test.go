// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// FuzzParse feeds arbitrary bytes through pxf.Parse. The lexer +
// recursive-descent parser must always return an error on malformed
// input — never panic, never loop indefinitely.
func FuzzParse(f *testing.F) {
	seeds := []string{
		``,
		`name = "value"`,
		`count = 42`,
		`weight = 3.14`,
		`flag = true`,
		`tags = ["a", "b", "c"]`,
		`nested {
  x = 1
  y = 2
}`,
		`# leading comment
name = "value"  # trailing`,
		`s = """
multi
line
"""`,
		`m = { key: "value", other: 7 }`,
		`type_url: "demo.v1.Config"
config { hostname = "h" }`,
		// Pathological: nested braces, deep maps, etc.
		`a { b { c { d { e = 1 } } } }`,
		// Keyed repeated fields (draft -01 §3.13): quoted entry names.
		`children { greeting { type = "Label" } }`,
		`regions { "us-east-1" { replicas = 3 } primary { replicas = 2 } }`,
		`children { "a" = { x = 1 } "" { } }`,
		`"quoted" = 1`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	// Adversarial seeds that previously made parsers crash:
	f.Add([]byte(`s = "`))               // unterminated string
	f.Add([]byte(`s = """`))             // unterminated triple-quote
	f.Add([]byte(`{{{{{{{{{{{{{{{{{{{`)) // unbalanced opens
	f.Add([]byte(`}}}}}}}}}}}}}}}}}}}`)) // unbalanced closes
	f.Add([]byte(`x = 0x`))              // truncated hex literal
	f.Add([]byte("\xff\xfe\xfd\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("pxf.Parse panicked: %v\ninput=%q", r, data)
			}
		}()
		_, _ = pxf.Parse(data)
	})
}

// FuzzParseTolerant feeds arbitrary bytes through pxf.ParseTolerant.
// Tolerant mode must always terminate with a non-nil document — never
// panic, never loop indefinitely — and when it reports zero errors the
// input must be one pxf.Parse accepts with the identical AST. (The
// other direction does not hold: a raw newline inside a simple-quoted
// string is accepted by Parse but flagged by ParseTolerant, which ends
// unterminated strings at the newline for mid-edit buffers.)
func FuzzParseTolerant(f *testing.F) {
	seeds := []string{
		``,
		`name = "value"`,
		`name = "`,      // unterminated string mid-keystroke
		`port =`,        // dangling assignment
		`server {`,      // unclosed block
		`xs = [1, 2`,    // unclosed list
		`a = 1 } b = 2`, // stray close
		"@dataset t (a) (1)",
		"@proto { message M {} }",
		"@type demo.v1.Config\nx = 1",
		`m { "a": "b": 2 }`,
		`children { "dup" { } "dup" { } }`,
		`regions { "us-east-1" { replicas = `,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Add([]byte(`{{{{{{{{{{{{{{{{{{{`))
	f.Add([]byte(`}}}}}}}}}}}}}}}}}}}`))
	f.Add([]byte("\xff\xfe\xfd\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("pxf.ParseTolerant panicked: %v\ninput=%q", r, data)
			}
		}()
		doc, errs := pxf.ParseTolerant(data)
		if doc == nil {
			t.Fatalf("pxf.ParseTolerant returned nil document\ninput=%q", data)
		}
		if len(errs) == 0 {
			strict, err := pxf.Parse(data)
			if err != nil {
				t.Fatalf("pxf.ParseTolerant reported no errors but pxf.Parse rejects the input: %v\ninput=%q", err, data)
			}
			if !reflect.DeepEqual(doc, strict) {
				t.Fatalf("pxf.ParseTolerant and pxf.Parse disagree on a valid input\ninput=%q", data)
			}
		}
	})
}

// FuzzUnmarshalKeyed drives the schema-aware decoder over the keyed
// repeated-field grammar (draft -01 §3.13), bound to the keyed.v1.Node
// fixture schema. Invariants: the decoder never panics, every accepted
// document re-encodes ([Marshal] chooses keyed vs anonymous form), and
// the canonical encoding decodes back to an equal message.
func FuzzUnmarshalKeyed(f *testing.F) {
	src, err := os.ReadFile(filepath.Join("testdata", "keyed", "keyed.proto"))
	if err != nil {
		f.Fatal(err)
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					"keyed.proto":           string(src),
					"pxf/annotations.proto": annotationsProtoSrc,
				}),
			},
		),
	}
	files, err := comp.Compile(context.Background(), "keyed.proto")
	if err != nil {
		f.Fatal(err)
	}
	var desc protoreflect.MessageDescriptor
	for _, fl := range files {
		if fl.Path() == "keyed.proto" {
			desc = fl.Messages().ByName("Node")
		}
	}
	if desc == nil {
		f.Fatal("keyed.v1.Node not found")
	}

	fixtures, err := filepath.Glob(filepath.Join("testdata", "keyed", "*.pxf"))
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range fixtures {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	seeds := []string{
		`id = "root"` + "\n" + `children { a { type = "L" } "b-1" { weight = 2 } }`,
		`children = [ { type = "no key" } ]`,
		`children { a { children { b { } } } }`,
		`children { a { } } children { a { } }`,
		`children { "" { } }`,
		`children { a { id = "mismatch" } }`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("keyed unmarshal panicked: %v\ninput=%q", r, data)
			}
		}()
		msg, err := pxf.UnmarshalDescriptor(data, desc)
		if err != nil {
			return
		}
		out, err := pxf.Marshal(msg)
		if err != nil {
			t.Fatalf("Marshal of accepted document failed: %v\ninput=%q", err, data)
		}
		msg2, err := pxf.UnmarshalDescriptor(out, desc)
		if err != nil {
			t.Fatalf("re-decode of canonical encoding failed: %v\nencoded=%q\ninput=%q", err, out, data)
		}
		if !proto.Equal(msg, msg2) {
			t.Fatalf("canonical roundtrip changed the message\nencoded=%q\ninput=%q", out, data)
		}
	})
}
