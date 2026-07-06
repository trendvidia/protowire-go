// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"reflect"
	"testing"

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
