// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// decodeString round-trips a literal through the lexer and returns the decoded
// string_field value. Wraps it in a `string_field = "..."` assignment so we
// don't have to recompile the schema for every case.
func decodeString(t *testing.T, literal string) (string, error) {
	t.Helper()
	desc := msgDesc(t, "AllTypes")
	input := "string_field = " + literal
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	if err != nil {
		return "", err
	}
	fd := desc.Fields().ByName("string_field")
	return msg.ProtoReflect().Get(fd).String(), nil
}

func TestLexStringBasicEscapes(t *testing.T) {
	cases := []struct {
		name    string
		literal string
		want    string
	}{
		{"plain", `"hello"`, "hello"},
		{"escaped quote", `"he said \"hi\""`, `he said "hi"`},
		{"escaped backslash", `"a\\b"`, `a\b`},
		{"newline", `"a\nb"`, "a\nb"},
		{"tab", `"a\tb"`, "a\tb"},
		{"carriage return", `"a\rb"`, "a\rb"},
		{"bell", `"\a"`, "\x07"},
		{"backspace", `"\b"`, "\x08"},
		{"form feed", `"\f"`, "\x0c"},
		{"vertical tab", `"\v"`, "\x0b"},
		{"single quote", `"\'"`, `'`},
		{"question mark", `"\?"`, `?`},
		{"all controls", `"\a\b\f\n\r\t\v"`, "\x07\x08\x0c\n\r\t\x0b"},
		{"empty", `""`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeString(t, tc.literal)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLexStringHexAndOctal(t *testing.T) {
	cases := []struct {
		name    string
		literal string
		want    string
	}{
		{"hex ascii", `"\x41"`, "A"},
		{"hex lowercase", `"\x7f"`, "\x7f"},
		{"two utf-8 bytes via hex", `"\xc3\xa9"`, "é"},
		{"octal ascii", `"\101"`, "A"},
		{"octal nul", `"\000"`, "\x00"},
		{"hex nul", `"\x00"`, "\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeString(t, tc.literal)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// HARDENING.md § UTF-8 forbids \xHH and \NNN byte escapes from producing
// invalid UTF-8 when the surrounding literal targets a proto3 string field.
// Same bytes inside a b"…" bytes literal are fine (covered separately).
func TestStringInvalidUTF8Rejected(t *testing.T) {
	cases := []struct {
		name    string
		literal string
	}{
		{"lone hex 0xFF", `"\xFF"`},
		{"octal 0xFF", `"\377"`},
		{"hex pair invalid", `"\xFF\xFE"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeString(t, tc.literal)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid UTF-8")
		})
	}
}

func TestLexStringUnicodeEscapes(t *testing.T) {
	cases := []struct {
		name    string
		literal string
		want    string
	}{
		{"u BMP 2-byte", `"é"`, "é"},
		{"u BMP 3-byte", `"中"`, "中"},
		{"u uppercase hex", `"é"`, "é"},
		{"U supplementary", `"\U0001F600"`, "😀"},
		{"U BMP via 8-hex", `"\U0000004A"`, "J"},
		{"mixed", `"aéb"`, "aéb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeString(t, tc.literal)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLexStringLiteralUTF8(t *testing.T) {
	// Multi-byte UTF-8 written literally between quotes must round-trip
	// byte-for-byte (the lexer is byte-oriented).
	cases := []struct {
		name    string
		literal string
		want    string
	}{
		{"latin", `"café"`, "café"},
		{"cjk", `"日本語"`, "日本語"},
		{"emoji", `"😀"`, "😀"},
		{"mixed", `"café 日本 😀"`, "café 日本 😀"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeString(t, tc.literal)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLexStringErrors(t *testing.T) {
	// We only assert that bad input is rejected. The lexer's specific message
	// (e.g. `invalid \u escape`) is in the ILLEGAL token's Value but the
	// decoder generalizes it to "expected string for field …" — that's a
	// pre-existing behavior, not something this change introduces.
	cases := []struct {
		name    string
		literal string
	}{
		{"unknown escape", `"\z"`},
		{"truncated u", `"\u12"`},
		{"non-hex u", `"\u12gh"`},
		{"surrogate high", `"\uD800"`},
		{"surrogate low", `"\uDFFF"`},
		{"U out of range", `"\U00110000"`},
		{"truncated U", `"\U0001F60"`},
		{"truncated x", `"\x"`},
		{"single hex digit", `"\x4"`},
		{"non-hex x", `"\xZZ"`},
		{"truncated octal", `"\10"`},
		{"non-octal in octal", `"\18a"`},
		{"unterminated string", `"hello`},
		{"trailing backslash", `"hello\`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeString(t, tc.literal)
			assert.Error(t, err, "expected %q to be rejected", tc.literal)
		})
	}
}

// TestStringRoundTripMarshal verifies that any string we lex correctly
// is also emitted by the encoder in a form the lexer can re-read.
func TestStringRoundTripMarshal(t *testing.T) {
	desc := msgDesc(t, "AllTypes")
	values := []string{
		`he said "hi"`,
		"a\nb\tc\rd",
		"\a\b\f\v",
		"中 café 😀",
		"embedded \x00 nul",
		`back\slash`,
	}
	fd := desc.Fields().ByName("string_field")
	for _, v := range values {
		t.Run(v, func(t *testing.T) {
			// Build a message with this string, marshal, then unmarshal.
			input := `string_field = "` + escapeForLiteral(v) + `"`
			msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
			require.NoError(t, err, "initial decode of %q failed", v)
			require.Equal(t, v, msg.ProtoReflect().Get(fd).String())

			// Marshal back to PXF text.
			out, err := pxf.Marshal(msg)
			require.NoError(t, err)

			// Re-decode the marshaled output.
			msg2, err := pxf.UnmarshalDescriptor(out, desc)
			require.NoError(t, err, "re-decode of %q failed; marshaled output was %q", v, string(out))
			assert.Equal(t, v, msg2.ProtoReflect().Get(fd).String())
		})
	}
}

// TestStringRoundTripFormatter verifies that Parse → FormatDocument → Parse
// preserves string values. format.go uses fmt.Fprintf("%q", ...), which can
// emit \v \f \a \b \uXXXX \UXXXXXXXX — all of which the lexer must handle.
func TestStringRoundTripFormatter(t *testing.T) {
	values := []string{
		`he said "hi"`,
		"a\nb\tc\rd",
		"\a\b\f\v",
		"中 café 😀",
		"embedded \x00 nul",
		"\x7f delete",
		`back\slash`,
	}
	for _, v := range values {
		t.Run(v, func(t *testing.T) {
			input := `string_field = "` + escapeForLiteral(v) + `"`
			doc, err := pxf.Parse([]byte(input))
			require.NoError(t, err)
			out := pxf.FormatDocument(doc)

			doc2, err := pxf.Parse(out)
			require.NoError(t, err, "re-parse of formatter output failed; output was %q", string(out))

			// Re-decode through the schema to compare values.
			desc := msgDesc(t, "AllTypes")
			msg, err := pxf.UnmarshalDescriptor(out, desc)
			require.NoError(t, err)
			fd := desc.Fields().ByName("string_field")
			assert.Equal(t, v, msg.ProtoReflect().Get(fd).String())
			_ = doc2
		})
	}
}

// escapeForLiteral converts a Go string into PXF source by escaping the bytes
// that would otherwise terminate or break a "..."-delimited literal. Used
// only by tests to embed arbitrary values into a synthetic input.
func escapeForLiteral(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0x07:
			b.WriteString(`\a`)
		case 0x08:
			b.WriteString(`\b`)
		case 0x0b:
			b.WriteString(`\v`)
		case 0x0c:
			b.WriteString(`\f`)
		default:
			if c < 0x20 || c == 0x7f {
				const hex = "0123456789abcdef"
				b.WriteString(`\x`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}
