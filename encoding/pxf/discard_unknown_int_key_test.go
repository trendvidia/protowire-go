// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// An integer or bool key with '=' or '{' is a grammar error, not an
// unknown field, so DiscardUnknown does not skip it: the decoder reports
// what Parse reports, with or without the option, before or after a
// known field (#196).
func TestDiscardUnknownIntegerKey(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	cases := []struct{ name, src, want string }{
		{"assign first", "1 = 2\n", "field assignment with '=' requires an identifier or string key"},
		{"assign after known field", "string_field = \"x\"\n1 = 2\n", "field assignment with '=' requires an identifier or string key"},
		{"block first", "1 { }\n", "submessage block requires an identifier or string key"},
		{"block after known field", "string_field = \"x\"\n1 { }\n", "submessage block requires an identifier or string key"},
		{"bool assign", "true = 2\n", `got bool ("true")`},
		{"bool block", "string_field = \"x\"\nfalse { }\n", `got bool ("false")`},
		{"suffixed key quoted as written", "1p5 = 2\n", `got integer ("1p5")`},
		{"suffixed block key", "1p64 { }\n", `got integer ("1p64")`},
		{"no key", "= 2\n", "expected identifier, string, integer, or bool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, perr := pxf.Parse([]byte(c.src))
			require.Error(t, perr)
			assert.Contains(t, perr.Error(), c.want, "Parse")
			for _, discard := range []bool{false, true} {
				_, err := pxf.UnmarshalOptions{DiscardUnknown: discard}.UnmarshalDescriptor([]byte(c.src), desc)
				require.Error(t, err, "DiscardUnknown=%v", discard)
				assert.Equal(t, perr.Error(), err.Error(), "DiscardUnknown=%v", discard)
			}
		})
	}
}

// The value of a field DiscardUnknown discards is held to the grammar
// Parse holds it to (#196). The skipper used to count braces and take
// any other token as a scalar, so a truncated document, a lexical error
// or an integer key inside the discarded value decoded as success.
func TestDiscardUnknownValueIsParsed(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	discard := pxf.UnmarshalOptions{DiscardUnknown: true}
	for _, src := range []string{
		"nope {\n",
		"nope = {\n",
		"nope = [1, 2\n",
		"nope { k { }\n",
		"nope { 1 = 2 }\n",
		"nope = { k { 1 { } } }\n",
		"nope = [{ true = 1 }]\n",
		"nope = 5seconds\n",
		"nope = \"abc\n",
		"nope = =\n",
		"nope = }\n",
		"nope { a = 1 @type = \"x\" }\n",
		"string_field = \"x\"\nnope = [1 2\n",
		// lex.prec still holds 1p5's suffix when the bool key's '=' is
		// current; only an integer key reads it.
		"nope = 1p5\ntrue = 2\n",
	} {
		t.Run(src, func(t *testing.T) {
			_, perr := pxf.Parse([]byte(src))
			require.Error(t, perr)
			_, err := discard.UnmarshalDescriptor([]byte(src), desc)
			require.Error(t, err)
			assert.Equal(t, perr.Error(), err.Error())
		})
	}

	// What Parse accepts is discarded, and decoding resumes after it.
	for _, src := range []string{
		"nope = { a: 1 b { c = [1, { d = 2 }] } }\n",
		"nope = []\n",
		"nope { k { } \"q\" { } }\n",
		"nope = 1.5p53\n",
		"nope = [ # comment\n 1, 2 ]\n",
		"nope { @type = \"x\" a = 1 }\n",
	} {
		t.Run(src, func(t *testing.T) {
			_, err := pxf.Parse([]byte(src + "string_field = \"x\"\n"))
			require.NoError(t, err)
			for _, s := range []string{src + "string_field = \"x\"\n", "string_field = \"x\"\n" + src} {
				msg, err := discard.UnmarshalDescriptor([]byte(s), desc)
				require.NoError(t, err, "%q", s)
				assert.Equal(t, "x", msg.Get(desc.Fields().ByName("string_field")).String(), "%q", s)
			}
		})
	}
}

// A discarded value counts toward MaxNestingDepth as a decoded one does,
// at the same depth Parse counts it at: the skipper had no bound at all.
func TestDiscardUnknownValueNestingDepth(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	opts := pxf.UnmarshalOptions{DiscardUnknown: true, MaxNestingDepth: 2}
	popts := pxf.ParseOptions{MaxNestingDepth: 2}
	for _, c := range []struct {
		src string
		ok  bool
	}{
		{"nope { a { } }\n", true},
		{"nope { a { b { } } }\n", false},
		{"nope = [[1]]\n", true},
		{"nope = [[[1]]]\n", false},
		{"nested_field { nope { a { } } }\n", false},
	} {
		_, perr := popts.Parse([]byte(c.src))
		_, err := opts.UnmarshalDescriptor([]byte(c.src), desc)
		if c.ok {
			require.NoError(t, perr, "Parse %q", c.src)
			require.NoError(t, err, "%q", c.src)
			continue
		}
		require.Error(t, perr, "Parse %q", c.src)
		require.Error(t, err, "%q", c.src)
		assert.Equal(t, perr.Error(), err.Error(), "%q", c.src)
	}
}
