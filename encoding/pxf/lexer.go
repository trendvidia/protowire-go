// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"
)

type lexer struct {
	input []byte
	pos   int
	line  int
	col   int
	// sawEnd records that the lexer stopped where more input could
	// still change the token it returned: it ran out of input inside the
	// token, or it stopped a byte or two short of the end, where its
	// lookahead ('.' then a digit, "µ", `""` then '"', '/' then '/' or
	// '*') could not see. Each token sets it where it stops, once, not
	// in the per-byte loops, which Parse runs on every byte. The dataset
	// scan (DatasetReader.nextToken) reads it, with a lexer per token, to
	// tell a token a read cut short from a finished one; the parser
	// never reads it.
	sawEnd bool

	// noValue skips copying a string literal's plain characters into the
	// token's Value: the dataset row scan (DatasetReader.nextToken) only
	// needs where a token ends, not what it holds. Escapes are still
	// decoded, so every error is the same as with values.
	noValue bool

	// base is added to every reported Offset. It is non-zero only when
	// the input is a window into a larger document (the streaming
	// dataset reader parses one row, or a chained table's header, at a
	// time), so positions stay document-relative. A Pos.Offset indexes
	// input only after base is subtracted.
	base int

	// tolerant enables the error-recovering behaviors used by
	// [ParseTolerant]: unterminated strings end at the newline (or EOF),
	// unterminated triple-quoted strings / block comments / bytes
	// literals end at EOF, invalid escape sequences are kept literally,
	// and bytes literals skip base64 pre-validation (the parser reports
	// it instead). Each recovery reports a positioned error via onErr.
	tolerant bool
	onErr    func(pos Position, msg string)

	// maxBytesLiteral bounds the decoded length of a b"…" literal, checked
	// from the literal's length while it is scanned (HARDENING.md
	// MaxBytesLiteralLength); the decoder sets it from its per-call limits.
	maxBytesLiteral int
}

// reportErr records a recoverable lexical error in tolerant mode.
func (l *lexer) reportErr(pos Position, msg string) {
	if l.onErr != nil {
		l.onErr(pos, msg)
	}
}

func newLexer(input []byte) *lexer {
	return &lexer{input: input, line: 1, col: 1, maxBytesLiteral: MaxBytesLiteralLength}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.input) {
		return 0
	}
	return l.input[l.pos]
}

func (l *lexer) peekAt(offset int) byte {
	i := l.pos + offset
	if i >= len(l.input) {
		return 0
	}
	return l.input[i]
}

// more reports whether the input holds a byte n bytes past the cursor,
// recording in sawEnd when it does not. For the lexer's cold paths
// (signs, escapes); the hot ones set sawEnd where the token stops.
func (l *lexer) more(n int) bool {
	if l.pos+n < len(l.input) {
		return true
	}
	l.sawEnd = true
	return false
}

// runCut reports whether a token that runs until a byte that cannot
// continue it (a number, duration, timestamp, identifier or directive
// name) and stopped at the cursor could still go on with more input:
// the input ends at the cursor, or the cursor is on its last byte and
// that byte is one whose meaning the lexer reads from the byte after it
// ('.' before a digit, the first byte of "µ", '/' before '/' or '*',
// 'p' before a digit, which makes a number's precision suffix).
func (l *lexer) runCut() bool {
	switch len(l.input) - l.pos {
	case 0:
		return true
	case 1:
		ch := l.input[l.pos]
		return ch == '.' || ch == 0xC2 || ch == '/' || ch == 'p'
	}
	return false
}

func (l *lexer) advance() byte {
	if l.pos >= len(l.input) {
		return 0
	}
	ch := l.input[l.pos]
	l.pos++
	if ch == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return ch
}

func (l *lexer) currentPos() Position {
	return Position{Line: l.line, Column: l.col, Offset: l.base + l.pos}
}

func (l *lexer) skipSpaces() {
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' {
			l.advance()
		} else {
			break
		}
	}
}

// Next returns the next token from the input.
func (l *lexer) Next() Token {
	l.skipSpaces()
	if l.pos >= len(l.input) {
		return Token{Kind: EOF, Pos: l.currentPos()}
	}

	pos := l.currentPos()
	ch := l.peek()

	switch {
	case ch == '\n':
		l.advance()
		return Token{Kind: NEWLINE, Pos: pos}

	case ch == '#':
		return l.lexLineComment(pos)
	case ch == '/' && l.peekAt(1) == '/':
		return l.lexLineComment(pos)
	case ch == '/' && l.peekAt(1) == '*':
		return l.lexBlockComment(pos)

	case ch == '"':
		if l.peekAt(1) == '"' {
			if l.peekAt(2) == '"' {
				return l.lexTripleString(pos)
			}
			// `""` is the empty string unless a third '"' follows.
			l.sawEnd = l.pos+2 >= len(l.input)
		}
		return l.lexString(pos)
	case ch == 'b' && l.peekAt(1) == '"':
		return l.lexBytes(pos)

	case ch == '{':
		l.advance()
		return Token{Kind: LBRACE, Value: "{", Pos: pos}
	case ch == '}':
		l.advance()
		return Token{Kind: RBRACE, Value: "}", Pos: pos}
	case ch == '[':
		l.advance()
		return Token{Kind: LBRACKET, Value: "[", Pos: pos}
	case ch == ']':
		l.advance()
		return Token{Kind: RBRACKET, Value: "]", Pos: pos}
	case ch == '(':
		l.advance()
		return Token{Kind: LPAREN, Value: "(", Pos: pos}
	case ch == ')':
		l.advance()
		return Token{Kind: RPAREN, Value: ")", Pos: pos}
	case ch == '=':
		l.advance()
		return Token{Kind: EQUALS, Value: "=", Pos: pos}
	case ch == ':':
		l.advance()
		return Token{Kind: COLON, Value: ":", Pos: pos}
	case ch == ',':
		l.advance()
		return Token{Kind: COMMA, Value: ",", Pos: pos}
	case ch == '@':
		return l.lexDirective(pos)

	case ch == '-' || ch == '+' || isDigit(ch):
		return l.lexNumber(pos)

	case isIdentStart(ch):
		return l.lexIdent(pos)

	default:
		return l.lexIllegalRune(pos)
	}
}

// lexIllegalRune consumes the character no token can start with and
// reports it by name. It consumes a whole UTF-8 rune rather than a single
// byte so a multi-byte character yields one diagnostic naming what the
// author actually typed — "unexpected character 'μ'" for the U+03BC GREEK
// SMALL LETTER MU that the micro-us grammar does not admit (§3.3, see
// atMicroSign) — instead of one report per byte it decomposes into.
// An invalid encoding names no character, so its lead byte is reported
// as a hex value.
func (l *lexer) lexIllegalRune(pos Position) Token {
	if l.input[l.pos] == '/' && l.pos+1 == len(l.input) {
		l.sawEnd = true // a '/' may open a comment
	}
	r, size := utf8.DecodeRune(l.input[l.pos:])
	if r == utf8.RuneError && size <= 1 {
		if !utf8.FullRune(l.input[l.pos:]) {
			l.sawEnd = true // the rest of the rune may still arrive
		}
		return Token{Kind: ILLEGAL, Value: fmt.Sprintf("unexpected byte %#02x", l.advance()), Pos: pos}
	}
	for i := 0; i < size; i++ {
		l.advance()
	}
	return Token{Kind: ILLEGAL, Value: fmt.Sprintf("unexpected character %q", r), Pos: pos}
}

func (l *lexer) lexLineComment(pos Position) Token {
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != '\n' {
		l.advance()
	}
	l.sawEnd = l.pos == len(l.input)
	return Token{Kind: COMMENT, Value: string(l.input[start:l.pos]), Pos: pos}
}

func (l *lexer) lexBlockComment(pos Position) Token {
	start := l.pos
	l.advance() // /
	l.advance() // *
	for l.pos+1 < len(l.input) {
		if l.input[l.pos] == '*' && l.input[l.pos+1] == '/' {
			l.advance() // *
			l.advance() // /
			return Token{Kind: COMMENT, Value: string(l.input[start:l.pos]), Pos: pos}
		}
		l.advance()
	}
	l.sawEnd = true
	if l.tolerant {
		for l.pos < len(l.input) {
			l.advance()
		}
		l.reportErr(pos, "unterminated block comment")
		return Token{Kind: COMMENT, Value: string(l.input[start:l.pos]), Pos: pos}
	}
	return Token{Kind: ILLEGAL, Value: "unterminated block comment", Pos: pos}
}

func (l *lexer) lexString(pos Position) Token {
	l.advance() // opening "
	var sb strings.Builder
	var buf [utf8.UTFMax]byte
	tolerant := l.tolerant // hoisted: checked once per byte below
	noValue := l.noValue
	for l.pos < len(l.input) {
		if l.peek() == '\n' {
			// A simple string MUST NOT hold an unescaped LF (draft
			// § Strings; #143). Tolerant mode recovers for mid-edit
			// buffers by ending the string at the newline, leaving the
			// newline for the next token.
			if tolerant {
				l.reportErr(pos, "unterminated string: ended at newline")
				return Token{Kind: STRING, Value: sb.String(), Pos: pos}
			}
			return Token{Kind: ILLEGAL, Value: `newline in string literal: write it as \n, or use a """triple-quoted""" string`, Pos: pos}
		}
		ch := l.advance()
		if ch == '"' {
			return Token{Kind: STRING, Value: sb.String(), Pos: pos}
		}
		if ch != '\\' {
			if !noValue {
				sb.WriteByte(ch)
			}
			continue
		}
		if l.pos >= len(l.input) {
			l.sawEnd = true
			if tolerant {
				l.reportErr(pos, "unterminated escape sequence")
				return Token{Kind: STRING, Value: sb.String(), Pos: pos}
			}
			return Token{Kind: ILLEGAL, Value: "unterminated escape sequence", Pos: pos}
		}
		var escPos Position
		if tolerant { // strict mode never reads it; skip the capture
			escPos = l.currentPos()
		}
		esc := l.advance()
		switch esc {
		case '"', '\\', '\'', '?':
			sb.WriteByte(esc)
		case 'a':
			sb.WriteByte(0x07)
		case 'b':
			sb.WriteByte(0x08)
		case 'f':
			sb.WriteByte(0x0C)
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'v':
			sb.WriteByte(0x0B)
		case 'x':
			b, ok := l.readHexByte()
			if !ok {
				if tolerant {
					l.reportErr(escPos, `invalid \x escape: expected 2 hex digits`)
					sb.WriteString(`\x`)
					continue
				}
				return Token{Kind: ILLEGAL, Value: `invalid \x escape: expected 2 hex digits`, Pos: pos}
			}
			sb.WriteByte(b)
		case '0', '1', '2', '3':
			b, ok := l.readOctRest(esc)
			if !ok {
				if tolerant {
					l.reportErr(escPos, `invalid octal escape: expected 3 octal digits`)
					sb.WriteByte('\\')
					sb.WriteByte(esc)
					continue
				}
				return Token{Kind: ILLEGAL, Value: `invalid octal escape: expected 3 octal digits`, Pos: pos}
			}
			sb.WriteByte(b)
		case 'u':
			r, ok := l.readHexRune(4)
			if !ok || !utf8.ValidRune(r) {
				if tolerant {
					l.reportErr(escPos, `invalid \u escape: expected 4 hex digits forming a valid codepoint`)
					sb.WriteString(`\u`)
					continue
				}
				return Token{Kind: ILLEGAL, Value: `invalid \u escape: expected 4 hex digits forming a valid codepoint`, Pos: pos}
			}
			n := utf8.EncodeRune(buf[:], r)
			sb.Write(buf[:n])
		case 'U':
			r, ok := l.readHexRune(8)
			if !ok || !utf8.ValidRune(r) {
				if tolerant {
					l.reportErr(escPos, `invalid \U escape: expected 8 hex digits forming a valid codepoint`)
					sb.WriteString(`\U`)
					continue
				}
				return Token{Kind: ILLEGAL, Value: `invalid \U escape: expected 8 hex digits forming a valid codepoint`, Pos: pos}
			}
			n := utf8.EncodeRune(buf[:], r)
			sb.Write(buf[:n])
		default:
			if tolerant {
				l.reportErr(escPos, `unknown escape sequence \`+string(esc))
				sb.WriteByte('\\')
				sb.WriteByte(esc)
				continue
			}
			return Token{Kind: ILLEGAL, Value: `unknown escape sequence \` + string(esc), Pos: pos}
		}
	}
	l.sawEnd = true
	if tolerant {
		l.reportErr(pos, "unterminated string")
		return Token{Kind: STRING, Value: sb.String(), Pos: pos}
	}
	return Token{Kind: ILLEGAL, Value: "unterminated string", Pos: pos}
}

func (l *lexer) lexTripleString(pos Position) Token {
	l.advance() // "
	l.advance() // "
	l.advance() // "
	start := l.pos
	for l.pos+2 < len(l.input) {
		if l.input[l.pos] == '"' && l.input[l.pos+1] == '"' && l.input[l.pos+2] == '"' {
			raw := string(l.input[start:l.pos])
			l.advance() // "
			l.advance() // "
			l.advance() // "
			return Token{Kind: STRING, Value: dedent(raw), Pos: pos}
		}
		l.advance()
	}
	l.sawEnd = true
	if l.tolerant {
		for l.pos < len(l.input) {
			l.advance()
		}
		l.reportErr(pos, "unterminated triple-quoted string")
		return Token{Kind: STRING, Value: dedent(string(l.input[start:l.pos])), Pos: pos}
	}
	return Token{Kind: ILLEGAL, Value: "unterminated triple-quoted string", Pos: pos}
}

// dedent processes a triple-quoted string body:
//   - strips leading newline after opening """
//   - uses closing line indent as base indent to strip from each line
//   - strips trailing whitespace-only line (before closing """)
func dedent(s string) string {
	if len(s) > 0 && s[0] == '\n' {
		s = s[1:]
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return ""
	}
	last := lines[len(lines)-1]
	if strings.TrimSpace(last) == "" {
		indent := last
		lines = lines[:len(lines)-1]
		for i, line := range lines {
			lines[i] = strings.TrimPrefix(line, indent)
		}
	}
	return strings.Join(lines, "\n")
}

func (l *lexer) lexBytes(pos Position) Token {
	l.advance() // b
	if l.pos >= len(l.input) || l.peek() != '"' {
		return Token{Kind: ILLEGAL, Value: `expected '"' after b`, Pos: pos}
	}
	l.advance() // opening "
	start := l.pos
	for l.pos < len(l.input) {
		ch := l.peek()
		if bytesLiteralOver(l.pos-start, l.maxBytesLiteral) {
			// Decoded base64 is three bytes per four characters; judged from
			// the length so the literal is neither scanned to its end nor
			// decoded before it is refused.
			return Token{Kind: ILLEGAL, Value: fmt.Sprintf("bytes literal decodes to more than MaxBytesLiteralLength=%d bytes", l.maxBytesLiteral), Pos: pos}
		}
		if ch == '"' {
			raw := string(l.input[start:l.pos])
			l.advance() // closing "
			// Accept both standard and URL-safe base64, with or without
			// padding, per RFC 4648 §5 (referenced by draft §3.7).
			// Tolerant mode defers validation to the parser, which turns
			// a decode failure into a positioned error plus a BadVal.
			if !l.tolerant {
				if _, err := decodeBase64Lenient(raw); err != nil {
					return Token{Kind: ILLEGAL, Value: "invalid base64 in bytes literal", Pos: pos}
				}
			}
			return Token{Kind: BYTES, Value: raw, Pos: pos}
		}
		if ch == '\n' {
			if l.tolerant {
				// Leave the newline for the next token.
				l.reportErr(pos, "unterminated bytes literal: ended at newline")
				return Token{Kind: BYTES, Value: string(l.input[start:l.pos]), Pos: pos}
			}
			return Token{Kind: ILLEGAL, Value: "unterminated bytes literal", Pos: pos}
		}
		l.advance()
	}
	l.sawEnd = true
	if l.tolerant {
		l.reportErr(pos, "unterminated bytes literal")
		return Token{Kind: BYTES, Value: string(l.input[start:l.pos]), Pos: pos}
	}
	return Token{Kind: ILLEGAL, Value: "unterminated bytes literal", Pos: pos}
}

func (l *lexer) lexDirective(pos Position) Token {
	l.advance() // @
	start := l.pos
	for l.pos < len(l.input) && isIdentPart(l.input[l.pos]) {
		l.advance()
	}
	l.sawEnd = l.pos == len(l.input)
	name := string(l.input[start:l.pos])
	if name == "" {
		return Token{Kind: ILLEGAL, Value: `"@" must be followed by a directive name`, Pos: pos}
	}
	if name == "type" {
		return Token{Kind: AT_TYPE, Value: "@type", Pos: pos}
	}
	if name == "dataset" {
		return Token{Kind: AT_DATASET, Value: "@dataset", Pos: pos}
	}
	if name == "proto" {
		return Token{Kind: AT_PROTO, Value: "@proto", Pos: pos}
	}
	return Token{Kind: AT_DIRECTIVE, Value: name, Pos: pos}
}

func (l *lexer) lexNumber(pos Position) Token {
	start := l.pos
	neg := false
	if sign := l.peek(); sign == '-' || sign == '+' {
		l.advance()
		// Spec §3.8: "+inf" and "-inf" are the only signed identifier
		// literals. A '+' sign is valid for nothing else.
		if l.hasIdent("inf") {
			l.advance()
			l.advance()
			l.advance()
			return Token{Kind: FLOAT, Value: l.viewString(start, l.pos), Pos: pos}
		}
		if sign == '+' {
			return Token{Kind: ILLEGAL, Value: `"+" is valid only in "+inf"`, Pos: pos}
		}
		if l.pos >= len(l.input) || !isDigit(l.peek()) {
			return Token{Kind: ILLEGAL, Value: `"-" must be followed by a digit or "inf"`, Pos: pos}
		}
		neg = true
	}

	digitStart := l.pos
	for l.pos < len(l.input) && isDigit(l.peek()) {
		l.advance()
	}
	digitCount := l.pos - digitStart

	// Timestamp: exactly 4 digits followed by '-', only non-negative
	if !neg && digitCount == 4 && l.pos < len(l.input) && l.peek() == '-' {
		return l.lexTimestamp(pos, start)
	}
	// Fraction: '.' followed by at least one digit. Floats and durations
	// both admit one (draft §3.3: duration-segment = 1*DIGIT [ "." 1*DIGIT ]
	// time-unit), so it is consumed here and the two are told apart by
	// what follows it. A '.' with no digit after it is not a
	// duration-segment; the float branch below keeps it as it always has.
	frac := false
	if l.peek() == '.' && isDigit(l.peekAt(1)) {
		frac = true
		l.advance() // .
		for l.pos < len(l.input) && isDigit(l.peek()) {
			l.advance()
		}
	}
	// Duration: magnitude followed by a time unit (§3.10). Checked before
	// the float branch so "1.5ms" is one DURATION token rather than FLOAT
	// "1.5" followed by IDENT "ms" — which is what pxf.Marshal writes for
	// any Duration that is not a whole multiple of its largest unit (#75).
	if l.atDurationUnit() {
		return l.lexDuration(pos, start)
	}
	// Float: fraction, or 'e'/'E' exponent, or a bare trailing '.'
	if frac || (l.pos < len(l.input) && (l.peek() == '.' || l.peek() == 'e' || l.peek() == 'E')) {
		return l.lexFloat(pos, start)
	}

	value := l.viewString(start, l.pos)
	var prec string
	if l.peek() == 'p' {
		prec = l.lexPrecision()
	}
	l.sawEnd = l.runCut()
	return Token{Kind: INT, Value: value, Pos: pos, Prec: prec}
}

// lexPrecision consumes a number's precision suffix, "p" and one or more
// digits (the precision production of draft -01), and returns its digits,
// or "" when no digit follows the 'p' at the cursor. The caller checks
// for the 'p', so a number without one pays no call. Any field but a
// pxf.BigFloat rejects a suffix; that is the decoder's call, not the
// lexer's.
func (l *lexer) lexPrecision() string {
	if !isDigit(l.peekAt(1)) {
		return ""
	}
	l.advance() // p
	start := l.pos
	for l.pos < len(l.input) && isDigit(l.peek()) {
		l.advance()
	}
	return l.viewString(start, l.pos)
}

func (l *lexer) lexFloat(pos Position, start int) Token {
	if l.peek() == '.' {
		l.advance()
		for l.pos < len(l.input) && isDigit(l.peek()) {
			l.advance()
		}
	}
	if l.pos < len(l.input) && (l.peek() == 'e' || l.peek() == 'E') {
		l.advance()
		if l.pos < len(l.input) && (l.peek() == '+' || l.peek() == '-') {
			l.advance()
		}
		for l.pos < len(l.input) && isDigit(l.peek()) {
			l.advance()
		}
	}
	value := l.viewString(start, l.pos)
	var prec string
	if l.peek() == 'p' {
		prec = l.lexPrecision()
	}
	l.sawEnd = l.runCut()
	return Token{Kind: FLOAT, Value: value, Pos: pos, Prec: prec}
}

func (l *lexer) lexTimestamp(pos Position, start int) Token {
	// Read characters that can be part of an RFC 3339 timestamp.
	for l.pos < len(l.input) {
		ch := l.peek()
		if endsRun(ch) {
			break
		}
		if ch == '/' && (l.peekAt(1) == '/' || l.peekAt(1) == '*') {
			break
		}
		l.advance()
	}
	l.sawEnd = l.runCut()
	raw := l.viewString(start, l.pos)
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		if _, err2 := time.Parse(time.RFC3339, raw); err2 != nil {
			return Token{Kind: ILLEGAL, Value: "invalid timestamp: " + raw, Pos: pos}
		}
	}
	return Token{Kind: TIMESTAMP, Value: raw, Pos: pos}
}

// atDurationUnit reports whether the input at the current position
// begins a time-unit (draft §3.3): one of the ASCII unit letters, or the
// two-byte UTF-8 encoding of "µ" (U+00B5 MICRO SIGN, %xC2.B5) that opens
// micro-us. Only the first byte(s) are inspected; lexDuration consumes the
// candidate and time.ParseDuration decides whether it was a unit at all.
// Does not advance.
func (l *lexer) atDurationUnit() bool {
	if l.pos >= len(l.input) {
		return false
	}
	return isDurationUnit(l.peek()) || l.atMicroSign()
}

// atMicroSign reports whether the next two bytes are the UTF-8 encoding
// of U+00B5 MICRO SIGN. This is the only non-ASCII byte sequence the
// duration grammar admits (micro-us = %xC2.B5 %x73); U+03BC GREEK SMALL
// LETTER MU, which time.ParseDuration would also accept, is deliberately
// not recognised. Does not advance.
func (l *lexer) atMicroSign() bool {
	return l.peek() == 0xC2 && l.peekAt(1) == 0xB5
}

// lexDuration consumes a duration literal — one or more segments of
// digits, an optional "." fraction, and a unit (§3.3) — starting from
// start, and validates the whole with time.ParseDuration. The scan is
// deliberately loose (any run of digits, unit letters, "." followed by a
// digit, and "µ") so that a malformed literal such as "5min" is reported
// as one invalid duration rather than tokenised as a duration plus an
// identifier.
func (l *lexer) lexDuration(pos Position, start int) Token {
scan:
	for l.pos < len(l.input) {
		switch {
		case isDigit(l.peek()) || isLowerAlpha(l.peek()):
			l.advance()
		case l.peek() == '.' && isDigit(l.peekAt(1)):
			l.advance()
		case l.atMicroSign():
			l.advance()
			l.advance()
		default:
			break scan
		}
	}
	l.sawEnd = l.runCut()
	raw := l.viewString(start, l.pos)
	if _, err := time.ParseDuration(raw); err != nil {
		return Token{Kind: ILLEGAL, Value: "invalid duration: " + raw, Pos: pos}
	}
	return Token{Kind: DURATION, Value: raw, Pos: pos}
}

func (l *lexer) lexIdent(pos Position) Token {
	start := l.pos
	for l.pos < len(l.input) && isIdentPart(l.input[l.pos]) {
		l.advance()
	}
	l.sawEnd = l.pos == len(l.input)
	val := l.viewString(start, l.pos)
	if val == "true" || val == "false" {
		return Token{Kind: BOOL, Value: val, Pos: pos}
	}
	if val == "null" {
		return Token{Kind: NULL, Value: val, Pos: pos}
	}
	return Token{Kind: IDENT, Value: val, Pos: pos}
}

// hasIdent reports whether the input at the current position reads
// exactly the identifier word (not a prefix of a longer identifier).
// Does not advance. Input that ends inside word, or right after it,
// sets sawEnd: more of it could still make, or unmake, the word.
func (l *lexer) hasIdent(word string) bool {
	end := l.pos + len(word)
	if end > len(l.input) {
		l.sawEnd = strings.HasPrefix(word, l.viewString(l.pos, len(l.input)))
		return false
	}
	if string(l.input[l.pos:end]) != word {
		return false
	}
	if end == len(l.input) {
		l.sawEnd = true
		return true
	}
	return !isIdentPart(l.input[end])
}

// viewString returns a zero-copy string view into the input buffer.
// Safe only for values that are NOT stored in proto messages (field names,
// numbers, timestamps, durations — all consumed during decode and discarded).
func (l *lexer) viewString(start, end int) string {
	if start >= end {
		return ""
	}
	return unsafe.String(&l.input[start], end-start)
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }
func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}
func isIdentPart(ch byte) bool { return isIdentStart(ch) || isDigit(ch) || ch == '.' }
func isDurationUnit(ch byte) bool {
	return ch == 'h' || ch == 'm' || ch == 's' || ch == 'n' || ch == 'u'
}
func isLowerAlpha(ch byte) bool { return ch >= 'a' && ch <= 'z' }

// endsRun reports whether ch ends a timestamp: whitespace, a separator
// or closer, or '#'. lexTimestamp stops at these (and at "//" or "/*");
// every other run token (number, duration, identifier, directive name)
// stops at a superset of them. The dataset scan reads the same set to
// know when a timestamp a read cut short can have ended.
func endsRun(ch byte) bool {
	switch ch {
	case ' ', '\n', '\t', '\r', ',', ']', '}', ')', '#':
		return true
	}
	return false
}

// bytesLiteralOver reports whether a bytes literal whose base64 body has
// n characters decodes to more than limit bytes (three per four
// characters); limit 0 means no limit. lexBytes refuses the literal on
// it, and the dataset scan stops waiting for one on the same rule.
func bytesLiteralOver(n, limit int) bool {
	return limit > 0 && n/4*3 > limit
}

func hexVal(ch byte) (int, bool) {
	switch {
	case ch >= '0' && ch <= '9':
		return int(ch - '0'), true
	case ch >= 'a' && ch <= 'f':
		return int(ch-'a') + 10, true
	case ch >= 'A' && ch <= 'F':
		return int(ch-'A') + 10, true
	}
	return 0, false
}

// readHexByte reads exactly 2 hex digits and returns the assembled byte.
func (l *lexer) readHexByte() (byte, bool) {
	if !l.more(1) {
		return 0, false
	}
	hi, ok1 := hexVal(l.input[l.pos])
	lo, ok2 := hexVal(l.input[l.pos+1])
	if !ok1 || !ok2 {
		return 0, false
	}
	l.advance()
	l.advance()
	return byte(hi<<4 | lo), true
}

// readHexRune reads exactly n hex digits and returns the assembled rune.
// Validity (range, surrogates) is checked by the caller via utf8.ValidRune.
func (l *lexer) readHexRune(n int) (rune, bool) {
	if !l.more(n - 1) {
		return 0, false
	}
	var r rune
	for i := 0; i < n; i++ {
		v, ok := hexVal(l.input[l.pos])
		if !ok {
			return 0, false
		}
		r = r<<4 | rune(v)
		l.advance()
	}
	return r, true
}

// readOctRest reads two more octal digits after the leading one already consumed
// as part of the escape (\nnn — exactly 3 octal digits total). Restricted to
// leading 0-3 by the caller so the value can never overflow a byte.
func (l *lexer) readOctRest(first byte) (byte, bool) {
	if !l.more(1) {
		return 0, false
	}
	d1, ok1 := octVal(l.input[l.pos])
	d2, ok2 := octVal(l.input[l.pos+1])
	if !ok1 || !ok2 {
		return 0, false
	}
	l.advance()
	l.advance()
	return byte(int(first-'0')<<6 | d1<<3 | d2), true
}

func octVal(ch byte) (int, bool) {
	if ch >= '0' && ch <= '7' {
		return int(ch - '0'), true
	}
	return 0, false
}
