// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"encoding/base64"
	"time"
)

type parser struct {
	lex      *lexer
	current  Token
	comments []Comment // pending comments not yet attached to an entry
}

func newParser(input []byte) *parser {
	p := &parser{lex: newLexer(input)}
	p.advance()
	return p
}

// advance consumes the next token. Comments and newlines are collected
// into pending comments instead of being skipped, so they can be attached
// to the following entry.
func (p *parser) advance() {
	for {
		p.current = p.lex.Next()
		if p.current.Kind == NEWLINE {
			continue
		}
		if p.current.Kind == COMMENT {
			p.comments = append(p.comments, Comment{
				Pos:  p.current.Pos,
				Text: p.current.Value,
			})
			continue
		}
		return
	}
}

// flushComments returns and clears pending comments.
func (p *parser) flushComments() []Comment {
	if len(p.comments) == 0 {
		return nil
	}
	c := p.comments
	p.comments = nil
	return c
}

// Parse parses PXF source into an AST Document with comments attached.
func Parse(input []byte) (*Document, error) {
	return newParser(input).parseDocument()
}

func (p *parser) parseDocument() (*Document, error) {
	doc := &Document{}
	doc.LeadingComments = p.flushComments() // comments before any directive or body entry

	// Top-of-document directives. @type and @<name> may interleave in any
	// order; @type populates TypeURL, others append to Directives.
	// doc.BodyOffset is the byte right after the last directive's
	// closing `}` (block form) or last token (bare form). Stays 0 when
	// there are no directives, so chameleon hashes from byte 0.
directives:
	for {
		switch p.current.Kind {
		case AT_TYPE:
			p.advance() // consume @type
			if p.current.Kind != IDENT {
				return nil, errorf(p.current.Pos, "expected type name after @type, got %s", p.current.Kind)
			}
			doc.TypeURL = p.current.Value
			doc.BodyOffset = p.current.Pos.Offset + len(p.current.Value)
			p.advance()
		case AT_DIRECTIVE:
			d, end, err := p.parseDirective()
			if err != nil {
				return nil, err
			}
			doc.Directives = append(doc.Directives, *d)
			doc.BodyOffset = end
		default:
			break directives
		}
	}

	doc.Entries = make([]Entry, 0, 8)
	for p.current.Kind != EOF {
		// Top-level: only field_entry is allowed. The document represents a
		// proto message, never a map<K,V>; map_entry (`:` form) is reserved
		// for the inside of a `{ ... }` block. See docs/grammar.ebnf → document.
		entry, err := p.parseEntry(0, false)
		if err != nil {
			return nil, err
		}
		doc.Entries = append(doc.Entries, entry)
	}
	return doc, nil
}

// parseDirective reads `@<name> [<type>] [{ ... }]`. The AT_DIRECTIVE
// token is current on entry. Returns the directive plus the byte offset
// immediately after the directive's last token (the `}` for block form,
// the type name for bare form, or `@<name>` if neither is present).
func (p *parser) parseDirective() (*Directive, int, error) {
	leading := p.flushComments()
	atPos := p.current.Pos
	name := p.current.Value
	d := &Directive{
		Pos:             atPos,
		Name:            name,
		LeadingComments: leading,
	}
	endOffset := atPos.Offset + 1 + len(name) // `@` + name
	p.advance()                               // consume AT_DIRECTIVE

	// Optional type name.
	if p.current.Kind == IDENT {
		d.Type = p.current.Value
		endOffset = p.current.Pos.Offset + len(p.current.Value)
		p.advance()
	}

	// Optional inline block. Use parseBlockVal so the inner content is
	// validated (string / brace / comment well-formedness); then slice
	// the raw bytes between { and } from the input for Body.
	if p.current.Kind == LBRACE {
		open := p.current.Pos.Offset
		if _, err := p.parseBlockVal(0); err != nil {
			return nil, 0, err
		}
		close := findMatchingBrace(p.lex.input, open)
		if close < 0 {
			// parseBlockVal succeeded, so a matching brace must exist —
			// this is defensive belt-and-braces.
			return nil, 0, errorf(d.Pos, "directive @%s: unmatched '{'", d.Name)
		}
		d.Body = p.lex.input[open+1 : close]
		endOffset = close + 1
	}
	return d, endOffset, nil
}

// findMatchingBrace returns the offset of the `}` that matches the `{`
// at openOffset. Returns -1 on unterminated input. Mirrors the lexer's
// string / comment handling so braces inside literals don't confuse
// the brace count.
func findMatchingBrace(input []byte, openOffset int) int {
	depth := 1
	i := openOffset + 1
	for i < len(input) {
		ch := input[i]
		switch {
		case ch == '{':
			depth++
			i++
		case ch == '}':
			depth--
			if depth == 0 {
				return i
			}
			i++
		case ch == '"':
			i = skipDirString(input, i)
			if i < 0 {
				return -1
			}
		case ch == 'b' && i+1 < len(input) && input[i+1] == '"':
			i = skipDirBytes(input, i)
			if i < 0 {
				return -1
			}
		case ch == '#':
			i = skipDirEOL(input, i+1)
		case ch == '/' && i+1 < len(input) && input[i+1] == '/':
			i = skipDirEOL(input, i+2)
		case ch == '/' && i+1 < len(input) && input[i+1] == '*':
			j := i + 2
			closed := false
			for j+1 < len(input) {
				if input[j] == '*' && input[j+1] == '/' {
					j += 2
					closed = true
					break
				}
				j++
			}
			if !closed {
				return -1
			}
			i = j
		default:
			i++
		}
	}
	return -1
}

func skipDirString(input []byte, i int) int {
	if i+2 < len(input) && input[i+1] == '"' && input[i+2] == '"' {
		j := i + 3
		for j+2 < len(input) {
			if input[j] == '"' && input[j+1] == '"' && input[j+2] == '"' {
				return j + 3
			}
			j++
		}
		return -1
	}
	j := i + 1
	for j < len(input) {
		if input[j] == '\\' {
			if j+1 >= len(input) {
				return -1
			}
			j += 2
			continue
		}
		if input[j] == '"' {
			return j + 1
		}
		if input[j] == '\n' {
			return -1
		}
		j++
	}
	return -1
}

func skipDirBytes(input []byte, i int) int {
	j := i + 2 // past `b"`
	for j < len(input) {
		if input[j] == '"' {
			return j + 1
		}
		if input[j] == '\n' {
			return -1
		}
		j++
	}
	return -1
}

func skipDirEOL(input []byte, i int) int {
	for i < len(input) && input[i] != '\n' {
		i++
	}
	return i
}

// parseEntry, parseValue, parseList, parseBlockVal, and parseBody thread an
// explicit depth counter for HARDENING.md § Recursion. depth is the number of
// open '{' / '[' the parser has descended into; rejection happens when a fresh
// descent would push depth past MaxNestingDepth.
//
// allowMapEntry gates the `:` (map-entry) form: false at document top level,
// true inside any '{ ... }' block where the surrounding field could be a
// map<K,V>. See docs/grammar.ebnf → field_entry, map_entry.
func (p *parser) parseEntry(depth int, allowMapEntry bool) (Entry, error) {
	leading := p.flushComments()

	pos := p.current.Pos
	if p.current.Kind != IDENT && p.current.Kind != STRING && p.current.Kind != INT {
		return nil, errorf(pos, "expected identifier, string, or integer, got %s (%q)", p.current.Kind, p.current.Value)
	}
	keyKind := p.current.Kind
	key := p.current.Value
	p.advance()

	switch p.current.Kind {
	case EQUALS:
		// `=` denotes a field assignment on a proto message; the key must
		// be an identifier (= proto field name). Map-style keys (string /
		// integer) are only valid with `:`.
		if keyKind != IDENT {
			return nil, errorf(pos,
				"field assignment with '=' requires an identifier key, got %s (%q); use ':' for map entries",
				keyKind, key)
		}
		p.advance()
		val, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		return &Assignment{Pos: pos, Key: key, Value: val, LeadingComments: leading}, nil

	case COLON:
		// Map entry. Only allowed inside a '{ ... }' block, never at
		// document top level.
		if !allowMapEntry {
			return nil, errorf(pos,
				"map entry (':' form) is only allowed inside a '{ … }' block; use '=' for top-level field assignments")
		}
		p.advance()
		val, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		return &MapEntry{Pos: pos, Key: key, Value: val, LeadingComments: leading}, nil

	case LBRACE:
		// `{ ... }` denotes a submessage field; same identifier-only rule
		// as `=` applies.
		if keyKind != IDENT {
			return nil, errorf(pos,
				"submessage block requires an identifier key, got %s (%q)",
				keyKind, key)
		}
		if depth+1 > MaxNestingDepth {
			return nil, errorf(p.current.Pos, "nesting depth exceeds MaxNestingDepth=%d", MaxNestingDepth)
		}
		p.advance() // consume {
		entries, err := p.parseBody(depth + 1)
		if err != nil {
			return nil, err
		}
		return &Block{Pos: pos, Name: key, Entries: entries, LeadingComments: leading}, nil

	default:
		return nil, errorf(p.current.Pos, "expected '=', ':', or '{' after %q, got %s", key, p.current.Kind)
	}
}

func (p *parser) parseValue(depth int) (Value, error) {
	pos := p.current.Pos

	switch p.current.Kind {
	case STRING:
		v := &StringVal{Pos: pos, Value: p.current.Value}
		p.advance()
		return v, nil

	case INT:
		v := &IntVal{Pos: pos, Raw: p.current.Value}
		p.advance()
		return v, nil

	case FLOAT:
		v := &FloatVal{Pos: pos, Raw: p.current.Value}
		p.advance()
		return v, nil

	case BOOL:
		v := &BoolVal{Pos: pos, Value: p.current.Value == "true"}
		p.advance()
		return v, nil

	case BYTES:
		decoded, err := base64.StdEncoding.DecodeString(p.current.Value)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(p.current.Value)
			if err != nil {
				return nil, errorf(pos, "invalid base64: %v", err)
			}
		}
		v := &BytesVal{Pos: pos, Value: decoded}
		p.advance()
		return v, nil

	case TIMESTAMP:
		t, err := time.Parse(time.RFC3339Nano, p.current.Value)
		if err != nil {
			t, err = time.Parse(time.RFC3339, p.current.Value)
			if err != nil {
				return nil, errorf(pos, "invalid timestamp %q: %v", p.current.Value, err)
			}
		}
		v := &TimestampVal{Pos: pos, Value: t, Raw: p.current.Value}
		p.advance()
		return v, nil

	case DURATION:
		d, err := time.ParseDuration(p.current.Value)
		if err != nil {
			return nil, errorf(pos, "invalid duration %q: %v", p.current.Value, err)
		}
		v := &DurationVal{Pos: pos, Value: d, Raw: p.current.Value}
		p.advance()
		return v, nil

	case NULL:
		v := &NullVal{Pos: pos}
		p.advance()
		return v, nil

	case IDENT:
		v := &IdentVal{Pos: pos, Name: p.current.Value}
		p.advance()
		return v, nil

	case LBRACKET:
		return p.parseList(depth)

	case LBRACE:
		return p.parseBlockVal(depth)

	default:
		return nil, errorf(pos, "expected value, got %s (%q)", p.current.Kind, p.current.Value)
	}
}

func (p *parser) parseList(depth int) (Value, error) {
	if depth+1 > MaxNestingDepth {
		return nil, errorf(p.current.Pos, "nesting depth exceeds MaxNestingDepth=%d", MaxNestingDepth)
	}
	pos := p.current.Pos
	p.advance() // consume [

	elems := make([]Value, 0, 4)
	for p.current.Kind != RBRACKET && p.current.Kind != EOF {
		elem, err := p.parseValue(depth + 1)
		if err != nil {
			return nil, err
		}
		elems = append(elems, elem)
		if p.current.Kind == COMMA {
			p.advance()
		}
	}
	if p.current.Kind != RBRACKET {
		return nil, errorf(p.current.Pos, "expected ']', got %s", p.current.Kind)
	}
	p.advance()
	return &ListVal{Pos: pos, Elements: elems}, nil
}

func (p *parser) parseBlockVal(depth int) (Value, error) {
	if depth+1 > MaxNestingDepth {
		return nil, errorf(p.current.Pos, "nesting depth exceeds MaxNestingDepth=%d", MaxNestingDepth)
	}
	pos := p.current.Pos
	p.advance() // consume {
	entries, err := p.parseBody(depth + 1)
	if err != nil {
		return nil, err
	}
	return &BlockVal{Pos: pos, Entries: entries}, nil
}

func (p *parser) parseBody(depth int) ([]Entry, error) {
	entries := make([]Entry, 0, 4)
	for p.current.Kind != RBRACE && p.current.Kind != EOF {
		// Inside a '{ ... }' block we don't know whether the surrounding
		// field is a message or a map<K,V>; both forms are accepted and
		// disambiguated by the schema layer.
		entry, err := p.parseEntry(depth, true)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if p.current.Kind != RBRACE {
		return nil, errorf(p.current.Pos, "expected '}', got %s", p.current.Kind)
	}
	p.advance()
	return entries, nil
}
