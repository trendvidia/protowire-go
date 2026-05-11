// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import "time"

// Comment represents a comment in source text.
type Comment struct {
	Pos  Position
	Text string // raw text including the comment prefix (# or // or /* */)
}

// Document is the root AST node of a PXF file.
type Document struct {
	TypeURL         string      // from @type directive, may be empty
	Directives      []Directive // @<name> [<type>] [{ ... }] entries before the body, in source order; excludes @type
	BodyOffset      int         // byte offset in the input where the schema-typed body begins (after all leading directives)
	Entries         []Entry
	LeadingComments []Comment // comments before the first entry (or after @type)
}

// Directive is a top-of-document `@<name> *(<prefix-id>) [{ ... }]`
// entry. The canonical use is side-channel metadata that sits alongside
// the schema-typed body — e.g. chameleon's `@header
// chameleon.v1.LayerHeader { id = "x" }` — but the grammar is open-ended:
// any name except `type` is accepted, followed by zero-or-more prefix
// identifiers and an optional inline block.
//
// Prefix identifiers are positional and per-directive. The two
// registrations defined by the protowire spec:
//
//   - One prefix identifier (v0.72.0 conventional shape) — the
//     identifier names the inner block's message type, dotted. Used by
//     `@header` and similar.
//   - `@entry` (draft §3.4.3) — zero, one, or two prefix identifiers
//     (label, type); a single prefix is disambiguated by the presence
//     of a `.` (dotted ⇒ type; bare ⇒ label).
//
// Body holds the RAW bytes between the opening `{` and matching `}`
// (both exclusive), suitable for handing back to [UnmarshalFull] /
// [Unmarshal] against the consumer's chosen message. Body is nil when
// the directive has no inline block.
type Directive struct {
	Pos      Position
	Name     string // e.g. "header"; never "type" (those go to Document.TypeURL)
	Prefixes []string // identifiers between @<name> and the optional `{ ... }`, in source order
	// Type is preserved for v0.72.0-era consumers: when exactly one
	// prefix identifier was supplied, Type holds it (matching the
	// previous single-Type field's behavior). For zero or two-plus
	// prefixes, Type is empty and callers MUST read Prefixes directly.
	// New code should use Prefixes; Type is retained to avoid churning
	// downstream consumers that haven't migrated.
	Type            string
	Body            []byte // raw inner bytes of the block; nil if the directive has no `{ ... }`
	LeadingComments []Comment
}

// Entry is a node that can appear in a message or map body.
type Entry interface {
	entryNode()
	pos() Position
}

// Assignment represents "key = value" (field assignment in message context).
type Assignment struct {
	Pos             Position
	Key             string
	Value           Value
	LeadingComments []Comment // comments on lines before this entry
	TrailingComment string    // inline comment after value on same line
}

func (*Assignment) entryNode()      {}
func (a *Assignment) pos() Position { return a.Pos }

// MapEntry represents "key: value" (entry in map context).
type MapEntry struct {
	Pos             Position
	Key             string
	Value           Value
	LeadingComments []Comment
	TrailingComment string
}

func (*MapEntry) entryNode()      {}
func (e *MapEntry) pos() Position { return e.Pos }

// Block represents "name { entries }" (nested message).
type Block struct {
	Pos             Position
	Name            string
	Entries         []Entry
	LeadingComments []Comment
}

func (*Block) entryNode()      {}
func (b *Block) pos() Position { return b.Pos }

// Value is an expression on the right side of = or :.
type Value interface {
	valueNode()
	pos() Position
}

// StringVal is a quoted string literal.
type StringVal struct {
	Pos   Position
	Value string
}

func (*StringVal) valueNode()      {}
func (v *StringVal) pos() Position { return v.Pos }

// IntVal is an integer literal (raw text, decoded later by schema).
type IntVal struct {
	Pos Position
	Raw string
}

func (*IntVal) valueNode()      {}
func (v *IntVal) pos() Position { return v.Pos }

// FloatVal is a floating-point literal.
type FloatVal struct {
	Pos Position
	Raw string
}

func (*FloatVal) valueNode()      {}
func (v *FloatVal) pos() Position { return v.Pos }

// BoolVal is a boolean literal (true / false).
type BoolVal struct {
	Pos   Position
	Value bool
}

func (*BoolVal) valueNode()      {}
func (v *BoolVal) pos() Position { return v.Pos }

// BytesVal is a base64-encoded bytes literal (b"...").
type BytesVal struct {
	Pos   Position
	Value []byte
}

func (*BytesVal) valueNode()      {}
func (v *BytesVal) pos() Position { return v.Pos }

// NullVal represents an explicit null literal.
type NullVal struct {
	Pos Position
}

func (*NullVal) valueNode()      {}
func (v *NullVal) pos() Position { return v.Pos }

// IdentVal is an unquoted identifier used as a value (enum names).
type IdentVal struct {
	Pos  Position
	Name string
}

func (*IdentVal) valueNode()      {}
func (v *IdentVal) pos() Position { return v.Pos }

// TimestampVal is an RFC 3339 timestamp literal.
type TimestampVal struct {
	Pos   Position
	Value time.Time
	Raw   string
}

func (*TimestampVal) valueNode()      {}
func (v *TimestampVal) pos() Position { return v.Pos }

// DurationVal is a Go-style duration literal (e.g. 30s, 1h30m).
type DurationVal struct {
	Pos   Position
	Value time.Duration
	Raw   string
}

func (*DurationVal) valueNode()      {}
func (v *DurationVal) pos() Position { return v.Pos }

// ListVal is a list value: [elem, elem, ...].
type ListVal struct {
	Pos      Position
	Elements []Value
}

func (*ListVal) valueNode()      {}
func (v *ListVal) pos() Position { return v.Pos }

// BlockVal is an anonymous block value: { entries }.
// Used for maps (key: value pairs) and inline messages in lists.
type BlockVal struct {
	Pos     Position
	Entries []Entry
}

func (*BlockVal) valueNode()      {}
func (v *BlockVal) pos() Position { return v.Pos }
