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

// FuzzRewriteKeyed exercises the keyed-collection editors' byte-offset
// surgery over both surface forms. It applies a fuzz-selected op with
// fuzzed key/subpath/value strings to a fixed valid document and
// asserts the Rewriter never panics and that Bytes() upholds its
// contract: it either errors or returns a document that reparses (the
// built-in reparse safety net makes silently-invalid output impossible,
// so this is really a panic / offset-math check on the surgery).
func FuzzRewriteKeyed(f *testing.F) {
	const blockDoc = `id = "root"
children {
  greeting {
    type = "Label"
    weight = 1
  }
  "us-east-1" {
    type = "Region"
  }
}
`
	const anonDoc = `service = "edge"
regions = [
  {
    name = "us-east-1"
    replicas = 3
  },
  {
    name = "eu-west-2"
    replicas = 1
  }
]
`
	f.Add(0, "greeting", "weight", "9", true)
	f.Add(1, "us-east-1", "", "", true)
	f.Add(2, "footer", "type", "X", true)
	f.Add(3, "greeting", "welcome", "", true)
	f.Add(4, "greeting", "", "", true)
	f.Add(0, "us-east-1", "replicas", "5", false)
	f.Add(2, "ap-south-1", "replicas", "2", false)
	f.Add(5, "us-east-1", "eu-west-2", "", false)

	f.Fuzz(func(t *testing.T, op int, key, arg, val string, block bool) {
		src := anonDoc
		field := "regions"
		if block {
			src = blockDoc
			field = "children"
		}
		r, err := pxf.NewRewriter([]byte(src))
		if err != nil {
			t.Fatalf("NewRewriter on a fixed valid doc failed: %v", err)
		}
		if !block {
			r.KeyedByField("regions", "name")
		}
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("keyed Rewriter panicked: op=%d key=%q arg=%q val=%q block=%v: %v", op, key, arg, val, block, rec)
			}
		}()
		switch op % 6 {
		case 0:
			_ = r.SetKeyed(field, key, arg, &pxf.StringVal{Value: val})
		case 1:
			_ = r.RemoveKeyedElement(field, key)
		case 2:
			_ = r.InsertKeyedElement(field, key, arg, []pxf.Entry{
				&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: val}},
			})
		case 3:
			_ = r.RenameKeyedElement(field, key, arg)
		case 4:
			_ = r.RemoveKeyed(field, key, "type")
		case 5:
			_ = r.MoveKeyedElement(field, key, arg)
		}
		// Contract: Bytes never panics; on success the result reparses.
		out, err := r.Bytes()
		if err != nil {
			return
		}
		if _, err := pxf.Parse(out); err != nil {
			t.Fatalf("Bytes() returned a document that does not reparse: %v\nop=%d key=%q arg=%q val=%q block=%v\nout=%q", err, op, key, arg, val, block, out)
		}
	})
}
