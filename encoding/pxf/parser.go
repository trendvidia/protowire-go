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
	doc.LeadingComments = p.flushComments() // comments before @type

	if p.current.Kind == AT_TYPE {
		p.advance() // consume @type
		if p.current.Kind != IDENT {
			return nil, errorf(p.current.Pos, "expected type name after @type, got %s", p.current.Kind)
		}
		doc.TypeURL = p.current.Value
		p.advance()
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
