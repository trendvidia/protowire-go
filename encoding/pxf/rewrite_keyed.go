// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Keyed-collection editing (issue #53). A keyed repeated field
// (draft -01 §3.13) is written as a block of named blocks — entry name
// = key, document order = list order. Dotted paths cannot address its
// elements reliably: entry names are atoms (dots included), so a name
// like "user.name" is one key, never two path segments, and names that
// are not identifier-shaped fall outside path syntax entirely. The
// methods here take the key as an opaque atom instead.
//
// Like the rest of the [Rewriter], these methods are schema-less: they
// operate on the block-of-named-blocks *shape*, whether or not a
// (pxf.key) annotation exists. fieldPath addresses the collection's
// block with ordinary first-match dotted-path resolution ([Rewriter.Set]);
// key / before / oldKey / newKey parameters are atoms and are never
// split on dots. An element is an entry of that block in either
// spelling — `name { ... }` or `name = { ... }` — and is matched by its
// unquoted name. Written entry names are quoted exactly when they are
// not identifier-safe.

import (
	"bytes"
	"fmt"
)

// keyedScope is a resolved keyed-collection block: its container entry,
// body entries, and source span.
type keyedScope struct {
	entries    []Entry
	scopeStart int // offset of the container entry's first byte
	closeOff   int // offset of the closing '}'
}

func (r *Rewriter) keyedScope(op, fieldPath string) (*keyedScope, error) {
	t, err := r.resolveIn(r.doc.Entries, nil, fieldPath)
	if err != nil {
		return nil, err
	}
	if t.entry == nil {
		return nil, fmt.Errorf("pxf: %s: no entry at %q", op, fieldPath)
	}
	sc := &keyedScope{scopeStart: t.entry.pos().Offset}
	switch n := t.entry.(type) {
	case *Block:
		sc.entries = n.Entries
		sc.closeOff = n.End.Offset - 1
	default:
		v, _ := entryValue(t.entry)
		bv, ok := v.(*BlockVal)
		if !ok {
			return nil, fmt.Errorf("pxf: %s: %q is not a block", op, fieldPath)
		}
		sc.entries = bv.Entries
		sc.closeOff = bv.End.Offset - 1
	}
	if sc.closeOff < sc.scopeStart || sc.closeOff >= len(r.src) || r.src[sc.closeOff] != '}' {
		return nil, fmt.Errorf("pxf: %s: block at %q has no source span", op, fieldPath)
	}
	return sc, nil
}

// keyedElementIn returns the element of entries named key — a [Block],
// or an [Assignment] holding a [BlockVal] (the `name = { ... }`
// spelling) — together with its body entries. Names compare by their
// unquoted value. Returns (nil, nil) when no element matches.
func keyedElementIn(entries []Entry, key string) (Entry, []Entry) {
	for _, e := range entries {
		switch n := e.(type) {
		case *Block:
			if n.Name == key {
				return n, n.Entries
			}
		case *Assignment:
			if n.Key == key {
				if bv, ok := n.Value.(*BlockVal); ok {
					return n, bv.Entries
				}
			}
		}
	}
	return nil, nil
}

// newKeyedElementBlock builds the AST node for a fresh element,
// quoting the entry name exactly when it is not identifier-safe.
func newKeyedElementBlock(key string, body []Entry) *Block {
	return &Block{Name: key, NameQuoted: !identSafeEntryName(key), Entries: body}
}

// SetKeyed stages an upsert of the field at subpath inside the element
// named key of the keyed collection at fieldPath — the keyed analogue
// of [Rewriter.Set], with the same value-span and missing-chain
// semantics inside the element. When no element named key exists, a new
// `key { <subpath chain> }` element is appended to the collection; as
// with Set, edits are computed against the document as parsed, so two
// SetKeyed calls that both create the same missing element create it
// twice.
//
// Calling SetKeyed again with the same fieldPath, key, and subpath
// replaces the previously staged edit.
func (r *Rewriter) SetKeyed(fieldPath, key, subpath string, v Value) error {
	op := fmt.Sprintf("SetKeyed %s[%q].%s", fieldPath, key, subpath)
	if v == nil {
		return fmt.Errorf("pxf: %s: nil value", op)
	}
	if containsBadVal(v) {
		return fmt.Errorf("pxf: %s: cannot write a BadVal placeholder", op)
	}
	if key == "" {
		return fmt.Errorf("pxf: %s: empty key", op)
	}
	sc, err := r.keyedScope(op, fieldPath)
	if err != nil {
		return err
	}
	pathKey := fmt.Sprintf("\x00keyed:set:%s\x00%s\x00%s", fieldPath, key, subpath)
	elem, body := keyedElementIn(sc.entries, key)
	if elem == nil {
		chain, err := buildEntryChain(op, subpath, v)
		if err != nil {
			return err
		}
		at, text := r.renderAppendEntry(sc.scopeStart, sc.closeOff, sc.entries, newKeyedElementBlock(key, chain))
		r.stage(spanEdit{path: pathKey, start: at, end: at, text: text})
		return nil
	}
	t, err := r.resolveIn(body, elem, subpath)
	if err != nil {
		return err
	}
	return r.setResolved(op, pathKey, t, v)
}

// buildEntryChain turns a dotted subpath and a leaf value into nested
// block entries: `a.b.c` becomes `a { b { c = v } }`. Every segment
// must be a bare identifier — a fresh element has no map context to
// justify a quoted key.
func buildEntryChain(op, subpath string, v Value) ([]Entry, error) {
	segs := splitPathSegs(subpath)
	if segs == nil {
		return nil, fmt.Errorf("pxf: %s: invalid subpath %q: empty segment", op, subpath)
	}
	for _, seg := range segs {
		if needsQuoting(seg) {
			return nil, fmt.Errorf("pxf: %s: segment %q is not a valid field name", op, seg)
		}
	}
	var e Entry = &Assignment{Key: segs[len(segs)-1], Value: v}
	for i := len(segs) - 2; i >= 0; i-- {
		e = &Block{Name: segs[i], Entries: []Entry{e}}
	}
	return []Entry{e}, nil
}

// RemoveKeyed stages the deletion of the field at subpath inside the
// element named key — the keyed analogue of [Rewriter.Remove], with the
// same whole-line and trailing-comment handling.
func (r *Rewriter) RemoveKeyed(fieldPath, key, subpath string) error {
	op := fmt.Sprintf("RemoveKeyed %s[%q].%s", fieldPath, key, subpath)
	sc, err := r.keyedScope(op, fieldPath)
	if err != nil {
		return err
	}
	elem, body := keyedElementIn(sc.entries, key)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, key)
	}
	t, err := r.resolveIn(body, elem, subpath)
	if err != nil {
		return err
	}
	if t.entry == nil {
		return fmt.Errorf("pxf: %s: no such entry", op)
	}
	start, end, _ := r.entryRemovalSpan(t.entry)
	r.stage(spanEdit{path: fmt.Sprintf("\x00keyed:rm:%s\x00%s\x00%s", fieldPath, key, subpath), start: start, end: end})
	return nil
}

// RemoveKeyedElement stages the deletion of the whole element named key
// from the keyed collection at fieldPath, with [Rewriter.Remove]'s
// whole-line handling.
func (r *Rewriter) RemoveKeyedElement(fieldPath, key string) error {
	op := fmt.Sprintf("RemoveKeyedElement %s[%q]", fieldPath, key)
	sc, err := r.keyedScope(op, fieldPath)
	if err != nil {
		return err
	}
	elem, _ := keyedElementIn(sc.entries, key)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, key)
	}
	start, end, _ := r.entryRemovalSpan(elem)
	r.stage(spanEdit{path: fmt.Sprintf("\x00keyed:rmel:%s\x00%s", fieldPath, key), start: start, end: end})
	return nil
}

// InsertKeyedElement stages the insertion of a new element named key
// with the given body entries into the keyed collection at fieldPath.
// Document order is list order for keyed collections, so placement
// matters: with before == "" the element is appended at the end;
// otherwise it is inserted immediately before the existing element
// named before — above any leading comment lines glued to it, so the
// anchor's own doc comment stays attached to the anchor. Inserting a
// key that already names an element is an error — duplicate keys have
// no keyed-form representation.
func (r *Rewriter) InsertKeyedElement(fieldPath, key, before string, body []Entry) error {
	op := fmt.Sprintf("InsertKeyedElement %s[%q]", fieldPath, key)
	if key == "" {
		return fmt.Errorf("pxf: %s: empty key", op)
	}
	for _, e := range body {
		if e == nil {
			return fmt.Errorf("pxf: %s: nil body entry", op)
		}
		if entryContainsBadVal(e) {
			return fmt.Errorf("pxf: %s: cannot write a BadVal placeholder", op)
		}
	}
	sc, err := r.keyedScope(op, fieldPath)
	if err != nil {
		return err
	}
	if dup, _ := keyedElementIn(sc.entries, key); dup != nil {
		return fmt.Errorf("pxf: %s: element %q already exists", op, key)
	}
	nb := newKeyedElementBlock(key, body)
	pathKey := fmt.Sprintf("\x00keyed:ins:%s\x00%s", fieldPath, key)

	if before == "" {
		at, text := r.renderAppendEntry(sc.scopeStart, sc.closeOff, sc.entries, nb)
		r.stage(spanEdit{path: pathKey, start: at, end: at, text: text})
		return nil
	}
	anchor, _ := keyedElementIn(sc.entries, before)
	if anchor == nil {
		return fmt.Errorf("pxf: %s: no element %q to insert before", op, before)
	}
	aStart := anchor.pos().Offset
	ls := lineStartOffset(r.src, aStart)
	if isBlank(r.src[ls:aStart]) {
		// The anchor starts its own line: insert full lines above it —
		// and above any leading comment lines glued to it, so the
		// anchor's own doc comment stays attached to the anchor.
		indent := string(r.src[ls:aStart])
		at := r.extendOverLeadingComments(ls)
		text := append(renderEntryLines(nb, indent, r.stepFor(sc.entries)), '\n')
		r.stage(spanEdit{path: pathKey, start: at, end: at, text: text})
		return nil
	}
	// Inline layout: splice `<entry> ` right before the anchor.
	var sb bytes.Buffer
	writeEntryInline(&sb, nb)
	sb.WriteByte(' ')
	r.stage(spanEdit{path: pathKey, start: aStart, end: aStart, text: sb.Bytes()})
	return nil
}

// RenameKeyedElement stages a retargeting of the element named oldKey
// to newKey: only the name token is replaced — the element's body,
// comments, and layout stay byte-for-byte. The written spelling follows
// the canonical rule (quoted exactly when newKey is not
// identifier-safe), so renaming can add or drop quotes as needed.
// Renaming to a key that already names a sibling element is an error;
// renaming an element to its current key stages nothing.
func (r *Rewriter) RenameKeyedElement(fieldPath, oldKey, newKey string) error {
	op := fmt.Sprintf("RenameKeyedElement %s[%q]", fieldPath, oldKey)
	if newKey == "" {
		return fmt.Errorf("pxf: %s: empty new key", op)
	}
	if newKey == oldKey {
		return nil
	}
	sc, err := r.keyedScope(op, fieldPath)
	if err != nil {
		return err
	}
	elem, _ := keyedElementIn(sc.entries, oldKey)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, oldKey)
	}
	if dup, _ := keyedElementIn(sc.entries, newKey); dup != nil {
		return fmt.Errorf("pxf: %s: element %q already exists", op, newKey)
	}
	start := elem.pos().Offset
	end := nameTokenEnd(r.src, start)
	if end < 0 {
		return fmt.Errorf("pxf: %s: element has no source span", op)
	}
	var text []byte
	if identSafeEntryName(newKey) {
		text = []byte(newKey)
	} else {
		text = []byte(fmt.Sprintf("%q", newKey))
	}
	r.stage(spanEdit{path: fmt.Sprintf("\x00keyed:ren:%s\x00%s", fieldPath, oldKey), start: start, end: end, text: text})
	return nil
}

// MoveKeyedElement stages a reorder of the element named key within the
// keyed collection at fieldPath: its source text moves verbatim,
// together with any leading `#` / `//` comment lines glued directly
// above it (no blank line in between) — an element's own doc comment
// travels with it, while a group header separated by a blank line stays
// put. With before == "" the element moves to the end of the
// collection; otherwise it lands immediately before the existing
// element named before (above that element's own glued comments).
// Moving an element before its current successor stages an edit pair
// that reproduces the input.
func (r *Rewriter) MoveKeyedElement(fieldPath, key, before string) error {
	op := fmt.Sprintf("MoveKeyedElement %s[%q]", fieldPath, key)
	if before == key {
		return fmt.Errorf("pxf: %s: cannot move an element before itself", op)
	}
	sc, err := r.keyedScope(op, fieldPath)
	if err != nil {
		return err
	}
	elem, _ := keyedElementIn(sc.entries, key)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, key)
	}
	var anchor Entry
	if before != "" {
		anchor, _ = keyedElementIn(sc.entries, before)
		if anchor == nil {
			return fmt.Errorf("pxf: %s: no element %q to move before", op, before)
		}
	}
	start, end, wholeLines := r.entryRemovalSpan(elem)
	if wholeLines {
		start = r.extendOverLeadingComments(start)
	}
	text := append([]byte(nil), r.src[start:end]...)
	delKey := fmt.Sprintf("\x00keyed:mv:%s\x00%s", fieldPath, key)
	insKey := delKey + "\x00ins"

	if !wholeLines {
		// Inline layout: the captured span is the bare entry plus its
		// separating spaces; splice it before the anchor (or the closing
		// brace), keeping one space on each side.
		if len(text) == 0 || text[len(text)-1] != ' ' {
			text = append(text, ' ')
		}
		at := sc.closeOff
		if anchor != nil {
			at = anchor.pos().Offset
		} else if at > 0 && r.src[at-1] != ' ' && r.src[at-1] != '\t' {
			text = append([]byte(" "), text...)
		}
		r.stage(spanEdit{path: delKey, start: start, end: end})
		r.stage(spanEdit{path: insKey, start: at, end: at, text: text})
		return nil
	}

	// Whole-line layout: the captured text carries its own indentation
	// and trailing newline; re-insert it at a line boundary.
	if len(text) == 0 || text[len(text)-1] != '\n' {
		text = append(text, '\n')
	}
	var at int
	if anchor != nil {
		aStart := anchor.pos().Offset
		ls := lineStartOffset(r.src, aStart)
		if !isBlank(r.src[ls:aStart]) {
			return fmt.Errorf("pxf: %s: element %q does not start its own line", op, before)
		}
		at = r.extendOverLeadingComments(ls)
	} else {
		at = sc.closeOff
		if ls := lineStartOffset(r.src, sc.closeOff); isBlank(r.src[ls:sc.closeOff]) {
			at = ls
		} else {
			// The '}' shares a line with the last entry; break the line.
			text = append([]byte("\n"), bytes.TrimSuffix(text, []byte("\n"))...)
			text = append(text, '\n')
		}
	}
	r.stage(spanEdit{path: delKey, start: start, end: end})
	r.stage(spanEdit{path: insKey, start: at, end: at, text: text})
	return nil
}

// extendOverLeadingComments walks a line-start offset upward over
// contiguous whole-line `#` / `//` comment lines, returning the start
// of the topmost one. A blank line (or any non-comment line) stops the
// walk, so a comment block separated from the entry by a blank line —
// a group header — is not included.
func (r *Rewriter) extendOverLeadingComments(start int) int {
	for start > 0 {
		ls := lineStartOffset(r.src, start-1)
		line := bytes.TrimSpace(r.src[ls : start-1])
		if len(line) == 0 || (line[0] != '#' && !bytes.HasPrefix(line, []byte("//"))) {
			break
		}
		start = ls
	}
	return start
}

// nameTokenEnd returns the offset just past the entry-name token that
// starts at start: a string literal (quoted entry name) or an
// identifier run. Returns -1 when no token is recognizable there.
func nameTokenEnd(src []byte, start int) int {
	if start >= len(src) {
		return -1
	}
	if src[start] == '"' {
		return skipDirString(src, start)
	}
	i := start
	for i < len(src) && isIdentPart(src[i]) {
		i++
	}
	if i == start {
		return -1
	}
	return i
}

// splitPathSegs splits a dotted path into segments, returning nil when
// the path is empty or has an empty segment.
func splitPathSegs(path string) []string {
	if path == "" {
		return nil
	}
	segs := []string{}
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			if i == start {
				return nil
			}
			segs = append(segs, path[start:i])
			start = i + 1
		}
	}
	return segs
}
