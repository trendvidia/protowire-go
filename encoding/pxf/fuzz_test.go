// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
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
