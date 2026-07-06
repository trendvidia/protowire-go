// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// Rewriter applies targeted, format-preserving edits to a PXF
// document. Unlike [FormatDocument], which normalizes the whole
// document, a Rewriter splices replacement bytes into the original
// source, so everything outside the edited spans — comments, blank
// lines, key ordering, indentation, number and string formatting
// quirks — round-trips byte-for-byte. This is the write path for
// tools that machine-edit documents a human also hand-edits (editor
// settings writers, property panels, manifest updaters).
//
//	r, err := pxf.NewRewriter(src)
//	r.Set("server.port", &pxf.IntVal{Raw: "9090"})
//	r.Remove("server.debug")
//	out, err := r.Bytes()
//
// Fields are addressed by dotted path; each segment names an
// [Assignment] key, [Block] name, or [MapEntry] key in the enclosing
// scope, and the first match wins. Keys that themselves contain a
// dot cannot be addressed. Edits are computed against the document
// as originally parsed: staging an edit does not make new fields
// addressable by later calls, and two Sets that both create the same
// missing intermediate block will create it twice (stage one Set with
// a [BlockVal] value instead).
//
// A Rewriter is not safe for concurrent use.
type Rewriter struct {
	src   []byte
	doc   *Document
	edits []spanEdit
}

// spanEdit replaces src[start:end) with text. start == end is a pure
// insertion; text == nil is a pure deletion.
type spanEdit struct {
	path       string
	start, end int
	text       []byte
}

// NewRewriter parses src (strictly, via [Parse]) and returns a
// Rewriter over it. The source must be valid PXF: rewriting is a
// writer's concern, and a broken document has no stable spans to
// splice into — use [ParseTolerant] for read-side tooling on broken
// buffers.
func NewRewriter(src []byte) (*Rewriter, error) {
	doc, err := Parse(src)
	if err != nil {
		return nil, err
	}
	return &Rewriter{src: src, doc: doc}, nil
}

// Document returns the parsed document the Rewriter addresses paths
// against. It reflects the original source, not staged edits.
func (r *Rewriter) Document() *Document { return r.doc }

// Set stages an upsert of the field at path to the given value. If
// the path resolves to an existing [Assignment] or [MapEntry], only
// that entry's value span is replaced — the key, separator, alignment,
// and any trailing comment stay put. If the path (or a suffix of it)
// does not exist, the missing chain is inserted at the end of the
// deepest existing enclosing block, matching its sibling indentation.
//
// Setting a path that resolves to a [Block] is an error: replacing a
// block wholesale would discard the comments inside it. Set its leaf
// fields individually, or Remove it and Set a [BlockVal].
//
// A Rewriter works on syntax alone — it has no schema. An inserted
// leaf uses the ':' map form when an existing sibling is a map entry
// and the '=' field form otherwise, so inserting the first entry of a
// map into an empty block writes '=' where the schema may expect ':';
// prefer seeding empty map blocks with a [BlockVal] of [MapEntry]
// values.
//
// Calling Set again with the same path replaces the previously staged
// edit for that path.
func (r *Rewriter) Set(path string, v Value) error {
	if v == nil {
		return fmt.Errorf("pxf: Set %s: nil value", path)
	}
	if containsBadVal(v) {
		return fmt.Errorf("pxf: Set %s: cannot write a BadVal placeholder", path)
	}
	t, err := r.resolve(path)
	if err != nil {
		return err
	}
	if t.entry != nil {
		old, ok := entryValue(t.entry)
		if !ok {
			return fmt.Errorf("pxf: Set %s: path addresses a block; set its fields individually, or Remove it and Set a BlockVal", path)
		}
		start := old.pos().Offset
		end := old.end().Offset
		indent := r.lineIndentAt(t.entry.pos().Offset)
		r.stage(spanEdit{path: path, start: start, end: end, text: r.renderValue(v, indent)})
		return nil
	}
	return r.stageInsert(path, t, v)
}

// Remove stages the deletion of the entry at path. When the entry sits
// alone on its line(s), the whole lines are removed, including a
// trailing comment on the entry's last line; leading comment lines
// above the entry are kept (they may describe the surrounding group).
// When the entry shares a line with other content, only its exact span
// is removed. Removing a path that does not exist is an error.
func (r *Rewriter) Remove(path string) error {
	t, err := r.resolve(path)
	if err != nil {
		return err
	}
	if t.entry == nil {
		return fmt.Errorf("pxf: Remove %s: no such entry", path)
	}
	start := t.entry.pos().Offset
	end := t.entry.end().Offset

	ls := lineStartOffset(r.src, start)
	if isBlank(r.src[ls:start]) {
		// The entry starts its line; try to consume through end of line.
		le := end
		for le < len(r.src) && (r.src[le] == ' ' || r.src[le] == '\t') {
			le++
		}
		if le < len(r.src) && (r.src[le] == '#' || (r.src[le] == '/' && le+1 < len(r.src) && r.src[le+1] == '/')) {
			for le < len(r.src) && r.src[le] != '\n' {
				le++
			}
		} else if le+1 < len(r.src) && r.src[le] == '/' && r.src[le+1] == '*' {
			// A trailing block comment is consumed only when it closes on
			// the same line; a multi-line /* ... */ is not the entry's own
			// trailing comment, so fall through to span-only removal.
			if close := bytes.Index(r.src[le:], []byte("*/")); close >= 0 {
				through := le + close + 2
				if !bytes.Contains(r.src[le:through], []byte("\n")) {
					le = through
					for le < len(r.src) && (r.src[le] == ' ' || r.src[le] == '\t') {
						le++
					}
				}
			}
		}
		if le >= len(r.src) || r.src[le] == '\n' {
			if le < len(r.src) {
				le++ // include the newline
			}
			r.stage(spanEdit{path: path, start: ls, end: le})
			return nil
		}
	}
	// The entry shares its line(s) with other content: remove its exact
	// span plus the spaces that separated it from what follows.
	for end < len(r.src) && (r.src[end] == ' ' || r.src[end] == '\t') {
		end++
	}
	r.stage(spanEdit{path: path, start: start, end: end})
	return nil
}

// Bytes applies the staged edits and returns the rewritten document.
// Edits must not overlap (e.g. a Remove of a block combined with a Set
// inside that block); overlapping edits are reported as an error. As a
// safety net for writer bugs, the result is reparsed before being
// returned — a rewrite that produces invalid PXF is an error, and the
// original source is never modified.
func (r *Rewriter) Bytes() ([]byte, error) {
	if len(r.edits) == 0 {
		return append([]byte(nil), r.src...), nil
	}
	edits := append([]spanEdit(nil), r.edits...)
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	for i := 1; i < len(edits); i++ {
		if edits[i-1].end > edits[i].start {
			return nil, fmt.Errorf("pxf: conflicting edits: %s overlaps %s", edits[i-1].path, edits[i].path)
		}
	}
	var out bytes.Buffer
	out.Grow(len(r.src) + 64)
	last := 0
	for _, e := range edits {
		out.Write(r.src[last:e.start])
		out.Write(e.text)
		last = e.end
	}
	out.Write(r.src[last:])
	result := out.Bytes()
	if _, err := Parse(result); err != nil {
		return nil, fmt.Errorf("pxf: rewrite produced an invalid document: %w", err)
	}
	return result, nil
}

// stage records an edit, replacing any previously staged edit for the
// same path (last call wins).
func (r *Rewriter) stage(e spanEdit) {
	for i := range r.edits {
		if r.edits[i].path == e.path {
			r.edits[i] = e
			return
		}
	}
	r.edits = append(r.edits, e)
}

// target is the result of resolving a dotted path: either a concrete
// entry, or the deepest existing scope plus the missing trailing
// segments.
type target struct {
	entry            Entry    // non-nil when the full path resolved
	container        Entry    // innermost resolved ancestor (*Block, or *Assignment/*MapEntry holding a *BlockVal); nil = document top level
	containerEntries []Entry  // the entry list the last lookup ran in
	missing          []string // unresolved trailing segments; empty when entry != nil
}

func (r *Rewriter) resolve(path string) (*target, error) {
	segs := strings.Split(path, ".")
	for _, seg := range segs {
		if seg == "" {
			return nil, fmt.Errorf("pxf: invalid path %q: empty segment", path)
		}
	}
	entries := r.doc.Entries
	var container Entry
	for i, seg := range segs {
		e := findEntry(entries, seg)
		if e == nil {
			return &target{container: container, containerEntries: entries, missing: segs[i:]}, nil
		}
		if i == len(segs)-1 {
			return &target{entry: e, container: container, containerEntries: entries}, nil
		}
		if b, ok := e.(*Block); ok {
			entries = b.Entries
		} else {
			val, _ := entryValue(e)
			bv, ok := val.(*BlockVal)
			if !ok {
				return nil, fmt.Errorf("pxf: path %q: segment %q is a scalar field, not a block", path, seg)
			}
			entries = bv.Entries
		}
		container = e
	}
	return nil, fmt.Errorf("pxf: invalid path %q", path) // unreachable: segs is never empty
}

// entryKey returns the key (assignment / map entry) or name (block)
// an entry is addressed by.
func entryKey(e Entry) string {
	switch n := e.(type) {
	case *Assignment:
		return n.Key
	case *MapEntry:
		return n.Key
	case *Block:
		return n.Name
	}
	return ""
}

// entryValue returns the value of a leaf entry; ok is false for a
// [Block], which has entries rather than a value.
func entryValue(e Entry) (Value, bool) {
	switch n := e.(type) {
	case *Assignment:
		return n.Value, true
	case *MapEntry:
		return n.Value, true
	}
	return nil, false
}

// findEntry returns the first entry in entries whose key or block name
// is key, or nil.
func findEntry(entries []Entry, key string) Entry {
	for _, e := range entries {
		if entryKey(e) == key {
			return e
		}
	}
	return nil
}

// containsBadVal reports whether v is or contains a [BadVal]
// placeholder anywhere in its value tree.
func containsBadVal(v Value) bool {
	switch n := v.(type) {
	case *BadVal:
		return true
	case *ListVal:
		for _, e := range n.Elements {
			if containsBadVal(e) {
				return true
			}
		}
	case *BlockVal:
		for _, e := range n.Entries {
			if val, ok := entryValue(e); ok && containsBadVal(val) {
				return true
			}
		}
	}
	return false
}

// stageInsert builds the text for the missing tail of a path and
// stages its insertion at the end of the deepest existing scope.
func (r *Rewriter) stageInsert(path string, t *target, v Value) error {
	// The intermediate (block-creating) segments must be bare
	// identifiers; the leaf may need quoting only in map (':') form.
	for _, seg := range t.missing[:len(t.missing)-1] {
		if needsQuoting(seg) {
			return fmt.Errorf("pxf: Set %s: segment %q is not a valid block name", path, seg)
		}
	}
	leaf := t.missing[len(t.missing)-1]
	mapForm := hasMapEntry(t.containerEntries)
	if needsQuoting(leaf) && !mapForm {
		return fmt.Errorf("pxf: Set %s: key %q needs a map (':') context", path, leaf)
	}

	// Locate the insertion scope: [open..close) of the container block,
	// or the end of the document for the top level.
	var closeOff int   // offset of the closing '}' (top level: len(src))
	var scopeStart int // offset the block's entry starts at, for indent fallback
	switch c := t.container.(type) {
	case nil:
		closeOff = len(r.src)
		scopeStart = -1
	case *Block:
		closeOff = c.End.Offset - 1
		scopeStart = c.Pos.Offset
	case *Assignment:
		closeOff = c.Value.(*BlockVal).End.Offset - 1
		scopeStart = c.Pos.Offset
	case *MapEntry:
		closeOff = c.Value.(*BlockVal).End.Offset - 1
		scopeStart = c.Pos.Offset
	}
	if scopeStart >= 0 && (closeOff < 0 || closeOff >= len(r.src) || r.src[closeOff] != '}') {
		return fmt.Errorf("pxf: Set %s: internal error: container span does not end at '}'", path)
	}

	if scopeStart >= 0 && !bytes.Contains(r.src[scopeStart:closeOff], []byte("\n")) {
		// Inline block `{ ... }` (or `{}`): splice ` key = value ` just
		// before the closing brace.
		var sb bytes.Buffer
		if closeOff > 0 && r.src[closeOff-1] != ' ' && r.src[closeOff-1] != '\t' {
			sb.WriteByte(' ')
		}
		sb.Write(r.renderChain(t.missing, "", mapForm, v, true))
		sb.WriteByte(' ')
		r.stage(spanEdit{path: path, start: closeOff, end: closeOff, text: sb.Bytes()})
		return nil
	}

	// Multi-line scope: insert full lines just above the closing brace
	// (top level: at end of input), indented like the existing siblings.
	indent := r.siblingIndent(t)
	insertAt := closeOff
	var prefix []byte
	if scopeStart >= 0 {
		ls := lineStartOffset(r.src, closeOff)
		if isBlank(r.src[ls:closeOff]) {
			insertAt = ls
		} else {
			// The '}' shares a line with the last entry; break the line.
			prefix = []byte("\n")
		}
	} else if len(r.src) > 0 && r.src[len(r.src)-1] != '\n' {
		prefix = []byte("\n")
	}
	var sb bytes.Buffer
	sb.Write(prefix)
	sb.Write(r.renderChain(t.missing, indent, mapForm, v, false))
	sb.WriteByte('\n')
	r.stage(spanEdit{path: path, start: insertAt, end: insertAt, text: sb.Bytes()})
	return nil
}

// renderChain renders the missing path tail: zero or more nested block
// opens, then `leaf = value` (or `leaf: value` in map form).
// baseIndent is the indent of the first rendered line; nested lines
// add two spaces per level. In inline form everything renders on one
// line and baseIndent is ignored.
func (r *Rewriter) renderChain(missing []string, baseIndent string, mapForm bool, v Value, inline bool) []byte {
	var sb bytes.Buffer
	// Only the leaf's immediate container decides '=' vs ':'; synthesized
	// intermediate blocks are messages, so their leaf uses '='.
	leafMap := mapForm && len(missing) == 1
	for i, seg := range missing[:len(missing)-1] {
		if !inline {
			sb.WriteString(indentAt(baseIndent, i))
		}
		sb.WriteString(seg)
		if inline {
			sb.WriteString(" { ")
		} else {
			sb.WriteString(" {\n")
		}
	}
	leaf := missing[len(missing)-1]
	depth := len(missing) - 1
	if !inline {
		sb.WriteString(indentAt(baseIndent, depth))
	}
	if leafMap && needsQuoting(leaf) {
		fmt.Fprintf(&sb, "%q", leaf)
	} else {
		sb.WriteString(leaf)
	}
	if leafMap {
		sb.WriteString(": ")
	} else {
		sb.WriteString(" = ")
	}
	sb.Write(r.renderValue(v, indentAt(baseIndent, depth)))
	for i := len(missing) - 2; i >= 0; i-- {
		if inline {
			sb.WriteString(" }")
		} else {
			sb.WriteByte('\n')
			sb.WriteString(indentAt(baseIndent, i))
			sb.WriteByte('}')
		}
	}
	return sb.Bytes()
}

// indentAt returns base plus depth levels of two-space indent.
func indentAt(base string, depth int) string {
	return base + strings.Repeat("  ", depth)
}

// renderValue formats v on its own (via the [FormatDocument]
// formatter) and re-indents any continuation lines with linePrefix so
// multi-line values (lists, blocks) align under the entry they are
// spliced into.
func (r *Rewriter) renderValue(v Value, linePrefix string) []byte {
	var buf bytes.Buffer
	f := &formatter{buf: &buf, indent: "  "}
	f.formatValue(v, 0)
	out := buf.Bytes()
	if linePrefix != "" && bytes.IndexByte(out, '\n') >= 0 {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\n"+linePrefix))
	}
	return out
}

// siblingIndent returns the indentation for a line inserted into the
// target's container: the indent of the container's first entry when
// there is one on its own line, otherwise the container's own indent
// plus one level (top level: none).
func (r *Rewriter) siblingIndent(t *target) string {
	if len(t.containerEntries) > 0 {
		first := t.containerEntries[0].pos().Offset
		ls := lineStartOffset(r.src, first)
		if isBlank(r.src[ls:first]) {
			return string(r.src[ls:first])
		}
	}
	if t.container == nil {
		return ""
	}
	return r.lineIndentAt(t.container.pos().Offset) + "  "
}

// lineIndentAt returns the whitespace prefix of the line containing
// off, or "" when non-whitespace precedes off on its line.
func (r *Rewriter) lineIndentAt(off int) string {
	ls := lineStartOffset(r.src, off)
	if isBlank(r.src[ls:off]) {
		return string(r.src[ls:off])
	}
	return ""
}

// hasMapEntry reports whether any sibling is a ':'-form map entry,
// which decides the separator for inserted leaves.
func hasMapEntry(entries []Entry) bool {
	for _, e := range entries {
		if _, ok := e.(*MapEntry); ok {
			return true
		}
	}
	return false
}

// lineStartOffset returns the offset of the first byte of the line
// containing off.
func lineStartOffset(src []byte, off int) int {
	i := off
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return i
}

// isBlank reports whether b is all spaces and tabs.
func isBlank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}
