// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- Parse: AST-level coverage ---

func TestParse_Directive_Basic(t *testing.T) {
	in := `@header chameleon.v1.LayerHeader
{
  id = "base"
  encrypted = false
}
string_field = "x"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)

	require.Len(t, doc.Directives, 1)
	d := doc.Directives[0]
	assert.Equal(t, "header", d.Name)
	assert.Equal(t, "chameleon.v1.LayerHeader", d.Type)
	assert.Contains(t, string(d.Body), `id = "base"`)
	assert.Contains(t, string(d.Body), `encrypted = false`)

	require.Len(t, doc.Entries, 1)
}

func TestParse_Directive_NoBlock(t *testing.T) {
	doc, err := pxf.Parse([]byte("@foo SomeType\nstring_field = \"x\""))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	assert.Equal(t, "foo", doc.Directives[0].Name)
	assert.Equal(t, "SomeType", doc.Directives[0].Type)
	assert.Nil(t, doc.Directives[0].Body)
}

func TestParse_Directive_NoType(t *testing.T) {
	doc, err := pxf.Parse([]byte("@bare { id = \"x\" }\nstring_field = \"y\""))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	assert.Equal(t, "bare", doc.Directives[0].Name)
	assert.Equal(t, "", doc.Directives[0].Type)
	assert.Contains(t, string(doc.Directives[0].Body), `id = "x"`)
}

func TestParse_Directive_Multiple(t *testing.T) {
	in := `@header H { id = "x" }
@trace T { sample = "0.1" }
string_field = "y"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 2)
	assert.Equal(t, "header", doc.Directives[0].Name)
	assert.Equal(t, "trace", doc.Directives[1].Name)
}

func TestParse_Directive_MixedWithAtType(t *testing.T) {
	in := `@type test.v1.AllTypes
@header H { id = "x" }
string_field = "y"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	assert.Equal(t, "test.v1.AllTypes", doc.TypeURL)
	require.Len(t, doc.Directives, 1)
	assert.Equal(t, "header", doc.Directives[0].Name)
}

func TestParse_Directive_NestedBraces(t *testing.T) {
	in := `@d X {
  sub { inner = "v" }
  another { a { b = 1 } }
}
string_field = "ok"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	body := string(doc.Directives[0].Body)
	assert.Contains(t, body, "sub {")
	assert.Contains(t, body, "another {")
}

func TestParse_Directive_BraceInString(t *testing.T) {
	in := `@d X {
  name = "has } brace"
  more = "{ also"
}
string_field = "ok"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	assert.Contains(t, string(doc.Directives[0].Body), `"has } brace"`)
	require.Len(t, doc.Entries, 1)
}

func TestParse_Directive_BraceInLineComment(t *testing.T) {
	for _, prefix := range []string{"#", "//"} {
		t.Run(prefix, func(t *testing.T) {
			in := `@d X {
  ` + prefix + ` inline { } braces in comment
  name = "value"
}
string_field = "ok"`
			doc, err := pxf.Parse([]byte(in))
			require.NoError(t, err)
			require.Len(t, doc.Directives, 1)
			require.Len(t, doc.Entries, 1)
		})
	}
}

func TestParse_Directive_BraceInBlockComment(t *testing.T) {
	in := `@d X {
  /* nested } and { braces */
  name = "value"
}
string_field = "ok"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	require.Len(t, doc.Entries, 1)
}

func TestParse_Directive_TripleQuotedBody(t *testing.T) {
	in := `@d X {
  body = """
  this } has } braces
  and { too }
  """
}
string_field = "ok"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	assert.Contains(t, string(doc.Directives[0].Body), `"""`)
	require.Len(t, doc.Entries, 1)
}

func TestParse_Directive_BytesLiteralInBody(t *testing.T) {
	in := `@d X {
  blob = b"aGVsbG8="
}
string_field = "ok"`
	doc, err := pxf.Parse([]byte(in))
	require.NoError(t, err)
	require.Len(t, doc.Directives, 1)
	assert.Contains(t, string(doc.Directives[0].Body), `b"aGVsbG8="`)
}

func TestParse_Directive_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"unterminated_block", "@d X {\n  id = \"x\"\n"},
		{"unterminated_string", `@d X { name = "open`},
		{"unterminated_triple", `@d X { name = """open`},
		{"unterminated_block_comment", "@d X { /* open\n  id = 1\n"},
		{"unterminated_bytes", "@d X { blob = b\"abcd\n"},
		{"bare_at_sign", "@\nstring_field = \"x\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := pxf.Parse([]byte(c.in))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// --- UnmarshalFull: Result.Directives() coverage ---

func TestUnmarshalFull_RecordsDirectives(t *testing.T) {
	allTypes := msgDesc(t, "AllTypes")
	cfg := dynamicpb.NewMessage(allTypes)

	in := []byte(`@header test.v1.Nested
{
  name = "hdr"
  value = 42
}
string_field = "body"`)

	res, err := pxf.UnmarshalFull(in, cfg)
	require.NoError(t, err)

	dirs := res.Directives()
	require.Len(t, dirs, 1)
	assert.Equal(t, "header", dirs[0].Name)
	assert.Equal(t, "test.v1.Nested", dirs[0].Type)

	// Sub-parse: the recorded body decodes against the consumer's
	// chosen message — exactly chameleon's use case.
	nested := msgDesc(t, "Nested")
	hdr := dynamicpb.NewMessage(nested)
	_, err = pxf.UnmarshalFull(dirs[0].Body, hdr)
	require.NoError(t, err)
	nameFd := nested.Fields().ByName("name")
	valFd := nested.Fields().ByName("value")
	assert.Equal(t, "hdr", hdr.Get(nameFd).String())
	assert.Equal(t, int64(42), hdr.Get(valFd).Int())

	// Body decoded normally too.
	bodyFd := allTypes.Fields().ByName("string_field")
	assert.Equal(t, "body", cfg.Get(bodyFd).String())
}

func TestUnmarshalFull_DirectivesEmptyByDefault(t *testing.T) {
	cfg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	res, err := pxf.UnmarshalFull([]byte(`string_field = "x"`), cfg)
	require.NoError(t, err)
	assert.Empty(t, res.Directives())
}

func TestUnmarshalFull_MixedAtTypeAndDirectives(t *testing.T) {
	cfg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	in := []byte(`@type test.v1.AllTypes
@header test.v1.Nested { name = "h" }
string_field = "x"`)

	res, err := pxf.UnmarshalFull(in, cfg)
	require.NoError(t, err)
	require.Len(t, res.Directives(), 1)
	assert.Equal(t, "header", res.Directives()[0].Name)
}

func TestUnmarshalFull_DirectiveDoesNotConsumeBody(t *testing.T) {
	cfg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	in := []byte(`@d Anything { foo = 1  bar = 2 }
string_field = "after"`)

	res, err := pxf.UnmarshalFull(in, cfg)
	require.NoError(t, err)
	fd := cfg.Descriptor().Fields().ByName("string_field")
	assert.Equal(t, "after", cfg.Get(fd).String(),
		"body parses unaffected by leading directive")
	assert.Len(t, res.Directives(), 1)
}

func TestUnmarshalFull_DirectiveErrors(t *testing.T) {
	cfg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	cases := []struct {
		name string
		in   string
	}{
		{"unterminated_block", "@d X {\n  k = \"v\"\n"},
		{"unterminated_string", `@d X { name = "open`},
		{"unterminated_block_comment", "@d X { /* open\nk = 1\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := pxf.UnmarshalFull([]byte(c.in), cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestUnmarshalFull_DirectiveBodyTokenizationVariants(t *testing.T) {
	cfg := dynamicpb.NewMessage(msgDesc(t, "AllTypes"))
	// Each case exercises a different tokenization branch inside the
	// decoder's brace matcher.
	cases := []string{
		`@d X { name = "with } brace" }` + "\nstring_field = \"x\"",
		"@d X { # line comment with } in it\n  name = \"v\" }\nstring_field = \"x\"",
		"@d X { // also // a } comment\n  name = \"v\" }\nstring_field = \"x\"",
		"@d X { /* block } comment { */\n  name = \"v\" }\nstring_field = \"x\"",
		`@d X { blob = b"aGk=" }` + "\nstring_field = \"x\"",
		`@d X { body = """has } braces""" }` + "\nstring_field = \"x\"",
	}
	for i, in := range cases {
		t.Run(fmt.Sprintf("variant_%d", i), func(t *testing.T) {
			res, err := pxf.UnmarshalFull([]byte(in), cfg)
			require.NoError(t, err)
			require.Len(t, res.Directives(), 1)
		})
	}
}

// Position.Offset is populated.
func TestPositionOffset_Populated(t *testing.T) {
	doc, err := pxf.Parse([]byte("x = 1\ny = 2"))
	require.NoError(t, err)
	require.Len(t, doc.Entries, 2)
	a := doc.Entries[0].(*pxf.Assignment)
	b := doc.Entries[1].(*pxf.Assignment)
	assert.Equal(t, 0, a.Pos.Offset, "first assignment starts at offset 0")
	assert.Equal(t, len("x = 1\n"), b.Pos.Offset, "second assignment starts after newline")

	// And on directives:
	doc2, err := pxf.Parse([]byte("@hdr T { a = 1 }\nx = 2"))
	require.NoError(t, err)
	require.Len(t, doc2.Directives, 1)
	assert.Equal(t, 0, doc2.Directives[0].Pos.Offset)
	bodyPos := doc2.Entries[0].(*pxf.Assignment).Pos
	assert.Equal(t, byte('x'), []byte("@hdr T { a = 1 }\nx = 2")[bodyPos.Offset])
}

// Document.BodyOffset points at the byte right after the directive's
// closing `}` — INCLUDING any trailing whitespace / comments before
// the body's first entry. This makes body-offset-based hashing stable
// across edits to whitespace before the first body assignment.
func TestDocument_BodyOffset(t *testing.T) {
	in := []byte(`@header X { id = "x" }
string_field = "body"`)
	doc, err := pxf.Parse(in)
	require.NoError(t, err)
	// `}` is at some offset; the byte right after `}` is `\n`.
	assert.Equal(t, byte('\n'), in[doc.BodyOffset])
	// The body starts immediately after `}`.
	assert.Equal(t, "\nstring_field = \"body\"", string(in[doc.BodyOffset:]))
}

func TestDocument_BodyOffset_NoDirectives(t *testing.T) {
	doc, err := pxf.Parse([]byte(`string_field = "x"`))
	require.NoError(t, err)
	assert.Equal(t, 0, doc.BodyOffset)
}

func TestDocument_BodyOffset_DirectivesOnly(t *testing.T) {
	in := []byte(`@header X { id = "x" }`)
	doc, err := pxf.Parse(in)
	require.NoError(t, err)
	assert.Equal(t, len(in), doc.BodyOffset, "no body → offset == input length")
}

// Backward compat: existing `@type ...` documents continue to parse.
func TestBackwardCompat_AtTypeStillWorks(t *testing.T) {
	doc, err := pxf.Parse([]byte("@type test.v1.AllTypes\nstring_field = \"x\""))
	require.NoError(t, err)
	assert.Equal(t, "test.v1.AllTypes", doc.TypeURL)
	assert.Empty(t, doc.Directives)
}

// msgDesc, AllTypes, and Nested are defined in pxf_test.go.

var _ protoreflect.Message = (*dynamicpb.Message)(nil)
