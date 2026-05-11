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

// peekKind returns the kind of the next significant token (skipping
// newlines and comments) without consuming it or disturbing pending-
// comment accumulation. Used by parseDirective to disambiguate "this
// IDENT is a directive prefix" from "this IDENT is a body field key".
func (p *parser) peekKind() TokenKind {
	pos, line, col := p.lex.pos, p.lex.line, p.lex.col
	saved := p.current
	nComments := len(p.comments)
	p.advance()
	next := p.current.Kind
	p.lex.pos, p.lex.line, p.lex.col = pos, line, col
	p.current = saved
	p.comments = p.comments[:nComments]
	return next
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
		case AT_TABLE:
			tbl, end, err := p.parseTableDirective()
			if err != nil {
				return nil, err
			}
			doc.Tables = append(doc.Tables, *tbl)
			doc.BodyOffset = end
		default:
			break directives
		}
	}

	// Standalone constraint (draft §3.4.4): a document containing any
	// @table directive MUST NOT also carry @type or top-level field
	// entries — the @table header IS the document's type declaration.
	if len(doc.Tables) > 0 {
		if doc.TypeURL != "" {
			return nil, errorf(doc.Tables[0].Pos,
				"@table directive cannot coexist with @type; the @table header declares the document's type (draft §3.4.4)")
		}
		if p.current.Kind != EOF {
			return nil, errorf(p.current.Pos,
				"@table directive cannot coexist with top-level field entries; the document's payload is the @table rows (draft §3.4.4)")
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

// parseTableDirective reads `@table <type> ( col1, col2, ... ) row*`.
// AT_TABLE is current on entry. Returns the table plus the byte offset
// immediately after the directive's last token (the `)` of the last
// row, the `)` of the column list when there are no rows, or earlier
// on error). See draft §3.4.4.
func (p *parser) parseTableDirective() (*TableDirective, int, error) {
	leading := p.flushComments()
	atPos := p.current.Pos
	tbl := &TableDirective{
		Pos:             atPos,
		LeadingComments: leading,
	}
	p.advance() // consume @table

	// Required: row message type (dotted identifier).
	if p.current.Kind != IDENT {
		return nil, 0, errorf(p.current.Pos, "expected row message type after @table, got %s", p.current.Kind)
	}
	tbl.Type = p.current.Value
	p.advance()

	// Required: column list in `( ... )`. At least one column.
	if p.current.Kind != LPAREN {
		return nil, 0, errorf(p.current.Pos, "expected '(' to start @table column list, got %s", p.current.Kind)
	}
	p.advance() // consume (

	if p.current.Kind != IDENT {
		return nil, 0, errorf(p.current.Pos, "@table column list must contain at least one field name, got %s", p.current.Kind)
	}
	for {
		if p.current.Kind != IDENT {
			return nil, 0, errorf(p.current.Pos, "expected column field name, got %s", p.current.Kind)
		}
		colName := p.current.Value
		// v1: column entries are unqualified field names; dotted paths
		// reserved for a future revision.
		if containsDot(colName) {
			return nil, 0, errorf(p.current.Pos, "@table column %q: dotted column paths are not supported in v1 (draft §3.4.4)", colName)
		}
		tbl.Columns = append(tbl.Columns, colName)
		p.advance()
		if p.current.Kind == COMMA {
			p.advance()
			continue
		}
		if p.current.Kind == RPAREN {
			break
		}
		return nil, 0, errorf(p.current.Pos, "expected ',' or ')' in @table column list, got %s", p.current.Kind)
	}
	endOffset := p.current.Pos.Offset + 1 // past `)`
	p.advance()                           // consume )

	// Zero or more rows.
	for p.current.Kind == LPAREN {
		row, rowEnd, err := p.parseTableRow(len(tbl.Columns))
		if err != nil {
			return nil, 0, err
		}
		tbl.Rows = append(tbl.Rows, *row)
		endOffset = rowEnd
	}
	return tbl, endOffset, nil
}

// parseTableRow reads `( cell ( ',' cell )* )` with an arity check
// against expected. LPAREN is current on entry. Returns the row plus
// the byte offset immediately past the closing `)`.
func (p *parser) parseTableRow(expected int) (*TableRow, int, error) {
	pos := p.current.Pos
	p.advance() // consume (

	row := &TableRow{Pos: pos, Cells: make([]Value, 0, expected)}
	// First cell.
	cell, err := p.parseRowCell()
	if err != nil {
		return nil, 0, err
	}
	row.Cells = append(row.Cells, cell)
	// Remaining cells.
	for p.current.Kind == COMMA {
		p.advance()
		cell, err := p.parseRowCell()
		if err != nil {
			return nil, 0, err
		}
		row.Cells = append(row.Cells, cell)
	}
	if p.current.Kind != RPAREN {
		return nil, 0, errorf(p.current.Pos, "expected ',' or ')' in @table row, got %s", p.current.Kind)
	}
	endOffset := p.current.Pos.Offset + 1
	p.advance() // consume )

	if len(row.Cells) != expected {
		return nil, 0, errorf(pos, "@table row has %d cells, expected %d (column count)", len(row.Cells), expected)
	}
	return row, endOffset, nil
}

// parseRowCell consumes one cell of a @table row. Returns nil for an
// empty cell (no value between two commas, or at row start/end).
// Rejects list ('[ ... ]') and block ('{ ... }') values per v1
// cell-grammar (draft §3.4.4).
func (p *parser) parseRowCell() (Value, error) {
	switch p.current.Kind {
	case COMMA, RPAREN:
		return nil, nil
	case LBRACKET:
		return nil, errorf(p.current.Pos, "@table cells cannot contain list values in v1 (draft §3.4.4)")
	case LBRACE:
		return nil, errorf(p.current.Pos, "@table cells cannot contain block values in v1 (draft §3.4.4)")
	}
	return p.parseValue(0)
}

// containsDot reports whether s has a '.' rune. Inlined here so we
// don't pull strings.Contains into parser.go for one call site.
func containsDot(s string) bool {
	for i := range len(s) {
		if s[i] == '.' {
			return true
		}
	}
	return false
}

// parseDirective reads `@<name> *(<prefix-id>) [{ ... }]`. The
// AT_DIRECTIVE token is current on entry. Returns the directive plus
// the byte offset immediately after the directive's last token (the
// `}` for block form, the last prefix identifier for bare form, or
// `@<name>` if neither is present).
//
// The grammar accepts zero-or-more prefix identifiers between `@<name>`
// and the optional `{ ... }` block (draft §3.4.2). Specific directive
// registrations may impose a cardinality; the parser does not.
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

	// Zero-or-more prefix identifiers. PXF is whitespace-insignificant,
	// so we can't end the prefix run at a newline. Instead, one-token
	// lookahead disambiguates: an IDENT followed by `=` or `:` is a
	// body field key, not a directive prefix.
	for p.current.Kind == IDENT {
		switch p.peekKind() {
		case EQUALS, COLON:
			// p.current is the first body entry's key; leave it for
			// the body parser.
			goto prefixesDone
		}
		d.Prefixes = append(d.Prefixes, p.current.Value)
		endOffset = p.current.Pos.Offset + len(p.current.Value)
		p.advance()
	}
prefixesDone:
	// Back-compat: a single prefix identifier populates the legacy
	// Type field, matching v0.72.0's single-Type shape so existing
	// consumers (e.g. chameleon's `@header T { ... }` reader) keep
	// working unchanged.
	if len(d.Prefixes) == 1 {
		d.Type = d.Prefixes[0]
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
