// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Keyed-collection editing (issues #53, #55). A keyed repeated field
// (draft -01 §3.13) is written as a block of named blocks — entry name
// = key, document order = list order. Dotted paths cannot address its
// elements reliably: entry names are atoms (dots included), so a name
// like "user.name" is one key, never two path segments, and names that
// are not identifier-shaped fall outside path syntax entirely. The
// methods here take the key as an opaque atom instead.
//
// Like the rest of the [Rewriter], these methods are schema-less: they
// operate on the collection's *shape*, whether or not a (pxf.key)
// annotation exists. fieldPath addresses the collection with ordinary
// first-match dotted-path resolution ([Rewriter.Set]); key / before /
// oldKey / newKey parameters are atoms and are never split on dots. An
// element is matched by its unquoted key. Written entry names are
// quoted exactly when they are not identifier-safe.
//
// Two facts about a keyed field's surface (§3.13) shape these editors
// beyond the single-keyed-block common case:
//
//   - Concatenation: a keyed field may be bound more than once, and the
//     bindings concatenate in document order. The editors search every
//     binding of the field for the target key; an append lands in the
//     last binding (the end of list order).
//   - Two surface forms: the keyed-block form (`children { greeting
//     { … } }`) needs no schema — the entry name is the key. The
//     anonymous list form (`children = [ { id = "greeting" } ]`) carries
//     the key as the value of the key field, so the editors can address
//     it only once the caller names that field with [Rewriter.KeyedByField].

import (
	"bytes"
	"fmt"
	"strconv"
)

// KeyedByField declares that the keyed collection at fieldPath uses
// keyField as its (pxf.key) field. This lets the keyed editors also
// address a collection written in the anonymous list form
// (`fieldPath = [ { <keyField> = "k" } ]`), where an element's key is
// the value of the key field rather than an entry name. The keyed-block
// form needs no declaration. Declarations are matched by exact
// fieldPath; a later call for the same fieldPath replaces the earlier
// one. Returns the Rewriter for chaining.
func (r *Rewriter) KeyedByField(fieldPath, keyField string) *Rewriter {
	if r.keyFields == nil {
		r.keyFields = make(map[string]string)
	}
	r.keyFields[fieldPath] = keyField
	return r
}

// keyedBinding is one binding of a keyed field — a keyed block, or an
// anonymous list — together with its source span.
type keyedBinding struct {
	anon       bool
	scopeStart int      // offset of the binding's container entry's first byte
	closeOff   int      // block form: '}' offset; anonymous form: ']' offset
	entries    []Entry  // block form: the block's entries
	list       *ListVal // anonymous form: the list value
	keyField   string   // anonymous form: the (pxf.key) field name
}

// keyedElem is one located element within a binding.
type keyedElem struct {
	b    *keyedBinding
	key  string
	node Entry     // block form: the *Block or *Assignment entry
	idx  int       // anonymous form: index into b.list.Elements
	bv   *BlockVal // element body as a block value (anonymous form; block form `name = {}`)
	body []Entry   // the element's body entries, for subpath resolution
}

// each visits every addressable element of the binding in document
// order until fn returns false.
func (b *keyedBinding) each(fn func(keyedElem) bool) {
	if b.anon {
		for i, v := range b.list.Elements {
			bv, ok := v.(*BlockVal)
			if !ok {
				continue
			}
			key, ok := explicitKeyOf(bv.Entries, b.keyField)
			if !ok || key == "" {
				continue
			}
			if !fn(keyedElem{b: b, key: key, idx: i, bv: bv, body: bv.Entries}) {
				return
			}
		}
		return
	}
	for _, e := range b.entries {
		switch n := e.(type) {
		case *Block:
			if !fn(keyedElem{b: b, key: n.Name, node: n, body: n.Entries}) {
				return
			}
		case *Assignment:
			if bv, ok := n.Value.(*BlockVal); ok {
				if !fn(keyedElem{b: b, key: n.Key, node: n, bv: bv, body: bv.Entries}) {
					return
				}
			}
		}
	}
}

// container returns the entry whose body holds the element, for subpath
// resolution and value edits. In the anonymous form (a bare block value
// with no owning entry) it is a synthetic assignment over that value —
// its span drives insertion, which is all the resolver needs.
func (e *keyedElem) container() Entry {
	if e.node != nil {
		return e.node
	}
	return &Assignment{Pos: e.bv.Pos, End: e.bv.End, Value: e.bv}
}

// keyedScopeEntries resolves the sibling scope that holds fieldPath's
// bindings and the collection's own key segment (the last path
// segment), so the caller can gather every binding of the field.
func (r *Rewriter) keyedScopeEntries(fieldPath string) (scope []Entry, key string, err error) {
	segs := splitPathSegs(fieldPath)
	if segs == nil {
		return nil, "", fmt.Errorf("invalid path %q", fieldPath)
	}
	key = segs[len(segs)-1]
	if len(segs) == 1 {
		return r.doc.Entries, key, nil
	}
	parent := joinPathSegs(segs[:len(segs)-1])
	t, err := r.resolveIn(r.doc.Entries, nil, parent)
	if err != nil {
		return nil, "", err
	}
	if t.entry == nil {
		return nil, "", fmt.Errorf("no entry at %q", parent)
	}
	if b, ok := t.entry.(*Block); ok {
		return b.Entries, key, nil
	}
	if v, ok := entryValue(t.entry); ok {
		if bv, ok := v.(*BlockVal); ok {
			return bv.Entries, key, nil
		}
	}
	return nil, "", fmt.Errorf("%q is not a block", parent)
}

// keyedBindings gathers every binding of the keyed field at fieldPath,
// in document order. Anonymous-list bindings are included only when the
// field's key field has been declared ([Rewriter.KeyedByField]).
func (r *Rewriter) keyedBindings(op, fieldPath string) ([]keyedBinding, error) {
	scope, key, err := r.keyedScopeEntries(fieldPath)
	if err != nil {
		return nil, fmt.Errorf("pxf: %s: %w", op, err)
	}
	keyField := r.keyFields[fieldPath]
	var bs []keyedBinding
	sawUndeclaredList := false
	for _, e := range scope {
		if entryKey(e) != key {
			continue
		}
		switch n := e.(type) {
		case *Block:
			bs = append(bs, keyedBinding{scopeStart: n.Pos.Offset, closeOff: n.End.Offset - 1, entries: n.Entries})
		case *Assignment:
			switch v := n.Value.(type) {
			case *BlockVal:
				bs = append(bs, keyedBinding{scopeStart: n.Pos.Offset, closeOff: v.End.Offset - 1, entries: v.Entries})
			case *ListVal:
				if keyField == "" {
					sawUndeclaredList = true
					continue
				}
				bs = append(bs, keyedBinding{anon: true, scopeStart: n.Pos.Offset, closeOff: v.End.Offset - 1, list: v, keyField: keyField})
			}
		}
	}
	if len(bs) == 0 {
		if sawUndeclaredList {
			return nil, fmt.Errorf("pxf: %s: %q is written in the anonymous list form; declare its key field with KeyedByField to edit it", op, fieldPath)
		}
		return nil, fmt.Errorf("pxf: %s: no keyed collection at %q", op, fieldPath)
	}
	for i := range bs {
		want := byte('}')
		if bs[i].anon {
			want = ']'
		}
		if bs[i].closeOff < bs[i].scopeStart || bs[i].closeOff >= len(r.src) || r.src[bs[i].closeOff] != want {
			return nil, fmt.Errorf("pxf: %s: binding at %q has no source span", op, fieldPath)
		}
	}
	return bs, nil
}

// findKeyedElem returns the first element named key across all bindings,
// in document order, or nil.
func findKeyedElem(bs []keyedBinding, key string) *keyedElem {
	for i := range bs {
		var found *keyedElem
		bs[i].each(func(e keyedElem) bool {
			if e.key == key {
				ee := e
				found = &ee
				return false
			}
			return true
		})
		if found != nil {
			return found
		}
	}
	return nil
}

// newKeyedElementBlock builds the AST node for a fresh keyed-block-form
// element, quoting the entry name exactly when it is not identifier-safe.
func newKeyedElementBlock(key string, body []Entry) *Block {
	return &Block{Name: key, NameQuoted: !identSafeEntryName(key), Entries: body}
}

// newAnonElement builds the block value for a fresh anonymous-form
// element: the key field written explicitly first, then the body.
func newAnonElement(keyField, key string, body []Entry) *BlockVal {
	entries := make([]Entry, 0, len(body)+1)
	entries = append(entries, &Assignment{Key: keyField, Value: &StringVal{Value: key}})
	entries = append(entries, body...)
	return &BlockVal{Entries: entries}
}

// SetKeyed stages an upsert of the field at subpath inside the element
// named key of the keyed collection at fieldPath — the keyed analogue
// of [Rewriter.Set], with the same value-span and missing-chain
// semantics inside the element. When no element named key exists, a new
// element is appended to the collection's last binding, in its form
// (a `key { <subpath chain> }` block, or a `{ <keyField> = "key"
// <chain> }` anonymous element). As with Set, edits are computed against
// the document as parsed, so two SetKeyed calls that both create the
// same missing element create it twice.
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
	bs, err := r.keyedBindings(op, fieldPath)
	if err != nil {
		return err
	}
	pathKey := fmt.Sprintf("\x00keyed:set:%s\x00%s\x00%s", fieldPath, key, subpath)
	elem := findKeyedElem(bs, key)
	if elem == nil {
		chain, err := buildEntryChain(op, subpath, v)
		if err != nil {
			return err
		}
		r.stageSplice(pathKey, r.renderAppendKeyedElement(&bs[len(bs)-1], key, chain))
		return nil
	}
	t, err := r.resolveIn(elem.body, elem.container(), subpath)
	if err != nil {
		return err
	}
	return r.setResolved(op, pathKey, t, v)
}

// renderAppendKeyedElement builds the insertion that appends a new
// element with the given key and body to a binding, in the binding's
// form.
func (r *Rewriter) renderAppendKeyedElement(b *keyedBinding, key string, body []Entry) listSplice {
	if b.anon {
		return r.renderAppendListElement(b, newAnonElement(b.keyField, key, body))
	}
	at, text := r.renderAppendEntry(b.scopeStart, b.closeOff, b.entries, newKeyedElementBlock(key, body))
	return listSplice{at: at, text: text, commaAt: -1}
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
	bs, err := r.keyedBindings(op, fieldPath)
	if err != nil {
		return err
	}
	elem := findKeyedElem(bs, key)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, key)
	}
	t, err := r.resolveIn(elem.body, elem.container(), subpath)
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
// from the keyed collection at fieldPath. In the keyed-block form the
// element is removed like any entry ([Rewriter.Remove]'s whole-line
// handling); in the anonymous list form the element, its separating
// comma and a trailing comment on its last line are removed, leaving the
// surrounding list well-formed. As with Remove, comments on the lines
// around the element are kept, and so is a trailing comma on the list.
func (r *Rewriter) RemoveKeyedElement(fieldPath, key string) error {
	op := fmt.Sprintf("RemoveKeyedElement %s[%q]", fieldPath, key)
	bs, err := r.keyedBindings(op, fieldPath)
	if err != nil {
		return err
	}
	elem := findKeyedElem(bs, key)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, key)
	}
	pathKey := fmt.Sprintf("\x00keyed:rmel:%s\x00%s", fieldPath, key)
	if elem.b.anon {
		r.stageListRemoval(pathKey, r.listElementRemovalSpans(elem.b.list, elem.idx, false))
		return nil
	}
	start, end, _ := r.entryRemovalSpan(elem.node)
	r.stage(spanEdit{path: pathKey, start: start, end: end})
	return nil
}

// InsertKeyedElement stages the insertion of a new element named key
// with the given body entries into the keyed collection at fieldPath.
// Document order is list order for keyed collections, so placement
// matters: with before == "" the element is appended to the last
// binding; otherwise it is inserted immediately before the existing
// element named before (in whatever binding holds it, and in that
// binding's form — above the anchor's glued doc comments). Inserting a key that already names an element,
// anywhere across the field's bindings, is an error — duplicate keys
// have no keyed-form representation.
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
	bs, err := r.keyedBindings(op, fieldPath)
	if err != nil {
		return err
	}
	if dup := findKeyedElem(bs, key); dup != nil {
		return fmt.Errorf("pxf: %s: element %q already exists", op, key)
	}
	pathKey := fmt.Sprintf("\x00keyed:ins:%s\x00%s", fieldPath, key)

	if before == "" {
		r.stageSplice(pathKey, r.renderAppendKeyedElement(&bs[len(bs)-1], key, body))
		return nil
	}
	anchor := findKeyedElem(bs, before)
	if anchor == nil {
		return fmt.Errorf("pxf: %s: no element %q to insert before", op, before)
	}
	if anchor.b.anon {
		r.stageSplice(pathKey, r.renderInsertListElementBefore(anchor.b, anchor.idx, newAnonElement(anchor.b.keyField, key, body)))
		return nil
	}
	at, text := r.renderInsertBlockElementBefore(anchor.b, anchor.node, newKeyedElementBlock(key, body))
	r.stage(spanEdit{path: pathKey, start: at, end: at, text: text})
	return nil
}

// renderInsertBlockElementBefore builds the insertion that places nb
// immediately before anchor in a keyed-block binding — above the
// anchor's glued leading comments when the anchor starts its own line,
// or inline before it otherwise.
func (r *Rewriter) renderInsertBlockElementBefore(b *keyedBinding, anchor Entry, nb *Block) (at int, text []byte) {
	aStart := anchor.pos().Offset
	ls := lineStartOffset(r.src, aStart)
	if isBlank(r.src[ls:aStart]) {
		indent := string(r.src[ls:aStart])
		at = r.extendOverLeadingComments(ls)
		text = append(renderEntryLines(nb, indent, r.stepFor(b.entries)), '\n')
		return at, text
	}
	var sb bytes.Buffer
	writeEntryInline(&sb, nb)
	sb.WriteByte(' ')
	return aStart, sb.Bytes()
}

// RenameKeyedElement stages a retargeting of the element named oldKey to
// newKey. In the keyed-block form only the name token is replaced,
// quoted or unquoted per the canonical rule (§3.13); the element's body,
// comments, and layout stay byte-for-byte. In the anonymous list form
// the key field's value literal is replaced instead. Renaming to a key
// that already names a sibling element is an error; renaming an element
// to its current key stages nothing.
func (r *Rewriter) RenameKeyedElement(fieldPath, oldKey, newKey string) error {
	op := fmt.Sprintf("RenameKeyedElement %s[%q]", fieldPath, oldKey)
	if newKey == "" {
		return fmt.Errorf("pxf: %s: empty new key", op)
	}
	if newKey == oldKey {
		return nil
	}
	bs, err := r.keyedBindings(op, fieldPath)
	if err != nil {
		return err
	}
	elem := findKeyedElem(bs, oldKey)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, oldKey)
	}
	if dup := findKeyedElem(bs, newKey); dup != nil {
		return fmt.Errorf("pxf: %s: element %q already exists", op, newKey)
	}
	pathKey := fmt.Sprintf("\x00keyed:ren:%s\x00%s", fieldPath, oldKey)

	if elem.b.anon {
		// The key is the value of the key-field assignment; replace that
		// value literal (always a quoted string in the anonymous form).
		ka := anonKeyAssignment(elem.bv.Entries, elem.b.keyField)
		if ka == nil {
			return fmt.Errorf("pxf: %s: element has no %q assignment", op, elem.b.keyField)
		}
		start := ka.Value.pos().Offset
		end := ka.Value.end().Offset
		r.stage(spanEdit{path: pathKey, start: start, end: end, text: []byte(strconv.Quote(newKey))})
		return nil
	}

	start := elem.node.pos().Offset
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
	r.stage(spanEdit{path: pathKey, start: start, end: end, text: text})
	return nil
}

// anonKeyAssignment returns the element's key-field assignment (the
// `keyField = "…"` entry), or nil.
func anonKeyAssignment(entries []Entry, keyField string) *Assignment {
	for _, e := range entries {
		if a, ok := e.(*Assignment); ok && !a.KeyQuoted && a.Key == keyField {
			if _, ok := a.Value.(*StringVal); ok {
				return a
			}
		}
	}
	return nil
}

// MoveKeyedElement stages a reorder of the element named key within the
// keyed collection at fieldPath: its source text moves verbatim. In the
// keyed-block form its glued leading `#` / `//` comment lines travel
// with it (a group header separated by a blank line stays put); in the
// anonymous list form the element value moves, with a trailing comment
// on its line when the new position is a line of its own. A comment that
// cannot go there stays where it was.
// With before == "" the element moves to the end of the collection;
// otherwise it lands immediately before the existing element named
// before. An element and its anchor must share the same surface form.
// Moving an element before its current successor reproduces the input.
func (r *Rewriter) MoveKeyedElement(fieldPath, key, before string) error {
	op := fmt.Sprintf("MoveKeyedElement %s[%q]", fieldPath, key)
	if before == key {
		return fmt.Errorf("pxf: %s: cannot move an element before itself", op)
	}
	bs, err := r.keyedBindings(op, fieldPath)
	if err != nil {
		return err
	}
	elem := findKeyedElem(bs, key)
	if elem == nil {
		return fmt.Errorf("pxf: %s: no element %q", op, key)
	}
	var anchor *keyedElem
	if before != "" {
		anchor = findKeyedElem(bs, before)
		if anchor == nil {
			return fmt.Errorf("pxf: %s: no element %q to move before", op, before)
		}
		if anchor.b.anon != elem.b.anon {
			return fmt.Errorf("pxf: %s: element %q and anchor %q are in different surface forms", op, key, before)
		}
	}
	if elem.b.anon {
		return r.moveListElement(fieldPath, elem, anchor)
	}
	return r.moveBlockElement(op, fieldPath, elem, anchor)
}

// moveBlockElement reorders a keyed-block-form element by capturing its
// source (plus glued comments) and reinserting it at a line boundary.
func (r *Rewriter) moveBlockElement(op, fieldPath string, elem, anchor *keyedElem) error {
	start, end, wholeLines := r.entryRemovalSpan(elem.node)
	if wholeLines {
		start = r.extendOverLeadingComments(start)
	}
	text := append([]byte(nil), r.src[start:end]...)
	delKey := fmt.Sprintf("\x00keyed:mv:%s\x00%s", fieldPath, elem.key)
	insKey := delKey + "\x00ins"

	if !wholeLines {
		if len(text) == 0 || text[len(text)-1] != ' ' {
			text = append(text, ' ')
		}
		at := elem.b.closeOff
		if anchor != nil {
			at = anchor.node.pos().Offset
		} else if at > 0 && r.src[at-1] != ' ' && r.src[at-1] != '\t' {
			text = append([]byte(" "), text...)
		}
		r.stage(spanEdit{path: delKey, start: start, end: end})
		r.stage(spanEdit{path: insKey, start: at, end: at, text: text})
		return nil
	}

	if len(text) == 0 || text[len(text)-1] != '\n' {
		text = append(text, '\n')
	}
	var at int
	if anchor != nil {
		aStart := anchor.node.pos().Offset
		ls := lineStartOffset(r.src, aStart)
		if !isBlank(r.src[ls:aStart]) {
			return fmt.Errorf("pxf: %s: anchor element does not start its own line", op)
		}
		at = r.extendOverLeadingComments(ls)
	} else {
		at = elem.b.closeOff
		if ls := lineStartOffset(r.src, elem.b.closeOff); isBlank(r.src[ls:elem.b.closeOff]) {
			at = ls
		} else {
			text = append([]byte("\n"), bytes.TrimSuffix(text, []byte("\n"))...)
			text = append(text, '\n')
		}
	}
	r.stage(spanEdit{path: delKey, start: start, end: end})
	r.stage(spanEdit{path: insKey, start: at, end: at, text: text})
	return nil
}

// moveListElement reorders an anonymous-form element by capturing its
// value span and reinserting it before the anchor (or at the list end).
func (r *Rewriter) moveListElement(fieldPath string, elem, anchor *keyedElem) error {
	lv := elem.b.list
	// Already where it was asked to go: reproduce the input.
	if anchor == nil && elem.idx == len(lv.Elements)-1 {
		return nil
	}
	if anchor != nil && anchor.b.list == lv && anchor.idx == elem.idx+1 {
		return nil
	}
	text := append([]byte(nil), r.src[elem.bv.Pos.Offset:elem.bv.End.Offset]...)
	delKey := fmt.Sprintf("\x00keyed:mv:%s\x00%s", fieldPath, elem.key)

	// A line comment would swallow whatever followed it on an inline
	// target, so the comment only goes along to a line of the element's own.
	to := elem.b
	if anchor != nil {
		to = anchor.b
	}
	onOwnLine := r.listMultiline(to.list)
	if anchor != nil {
		p := to.list.Elements[anchor.idx].pos().Offset
		onOwnLine = onOwnLine && isBlank(r.src[lineStartOffset(r.src, p):p])
	}
	var comment []byte
	if onOwnLine {
		if cs, ce, _ := r.trailingComment(r.listElemTail(lv, elem.idx)); cs >= 0 {
			comment = r.src[cs:ce]
		}
	}

	var ins listSplice
	if anchor != nil {
		ins = r.spliceListElementBefore(anchor.b, anchor.idx, text, comment)
	} else {
		ins = r.spliceListElementAppend(elem.b, text, comment)
	}
	r.stageListRemoval(delKey, r.listElementRemovalSpans(lv, elem.idx, !onOwnLine))
	r.stageSplice(delKey+"\x00ins", ins)
	return nil
}

// --- Anonymous-list element surgery ---------------------------------

// listMultiline reports whether the list spans more than one source
// line (the canonical formatter's shape), as opposed to an inline
// `[ a, b ]`.
func (r *Rewriter) listMultiline(lv *ListVal) bool {
	return bytes.Contains(r.src[lv.Pos.Offset:lv.End.Offset], []byte("\n"))
}

// listElementIndent returns the indentation of the list's elements: the
// first element's own-line indent, else the list's line plus one step.
func (r *Rewriter) listElementIndent(lv *ListVal, scopeStart int) string {
	if len(lv.Elements) > 0 {
		p := lv.Elements[0].pos().Offset
		ls := lineStartOffset(r.src, p)
		if isBlank(r.src[ls:p]) {
			return string(r.src[ls:p])
		}
	}
	return r.lineLeadingIndent(scopeStart) + r.indentStep()
}

// listComma returns the offset of the first comma in src[from:to), which
// must be the gap between two list tokens, or -1. Comments in the gap are
// stepped over, since one may hold a comma of its own.
func (r *Rewriter) listComma(from, to int) int {
	for i := from; i < to; {
		switch {
		case r.src[i] == ',':
			return i
		case r.src[i] == '#', bytes.HasPrefix(r.src[i:to], []byte("//")):
			for i < to && r.src[i] != '\n' {
				i++
			}
		case bytes.HasPrefix(r.src[i:to], []byte("/*")):
			n := bytes.Index(r.src[i:to], []byte("*/"))
			if n < 0 {
				return -1
			}
			i += n + 2
		default:
			i++
		}
	}
	return -1
}

// listElemComma returns the offset of the comma following element idx of
// lv, or -1 for a last element with no trailing comma.
func (r *Rewriter) listElemComma(lv *ListVal, idx int) int {
	limit := lv.End.Offset - 1
	if idx < len(lv.Elements)-1 {
		limit = lv.Elements[idx+1].pos().Offset
	}
	return r.listComma(lv.Elements[idx].end().Offset, limit)
}

// listElemTail returns the offset where a trailing comment of element
// idx of lv would start to look: past its comma when that sits on the
// element's last line, else just past the element.
func (r *Rewriter) listElemTail(lv *ListVal, idx int) int {
	end := lv.Elements[idx].end().Offset
	if c := r.listElemComma(lv, idx); c >= 0 && !bytes.Contains(r.src[end:c], []byte("\n")) {
		return c + 1
	}
	return end
}

// trailingComment finds the comment that ends the line at off. It returns
// the comment's span, or -1s when there is none, and the offset of the
// line's end, or -1 when something other than a comment follows off on
// the line. A block comment counts only when it closes on the line, as in
// [Rewriter.entryRemovalSpan].
func (r *Rewriter) trailingComment(off int) (start, end, lineEnd int) {
	i := off + r.trailingListPad(off)
	start, end = -1, -1
	switch {
	case i >= len(r.src):
	case r.src[i] == '#', bytes.HasPrefix(r.src[i:], []byte("//")):
		start = i
		for i < len(r.src) && r.src[i] != '\n' {
			i++
		}
		end = i
	case bytes.HasPrefix(r.src[i:], []byte("/*")):
		n := bytes.Index(r.src[i:], []byte("*/"))
		if n >= 0 && !bytes.Contains(r.src[i:i+n], []byte("\n")) {
			start, end = i, i+n+2
			i = end + r.trailingListPad(end)
		}
	}
	if i >= len(r.src) || r.src[i] == '\n' {
		return start, end, i
	}
	return -1, -1, -1
}

// byteSpan is a half-open range of source offsets.
type byteSpan struct{ start, end int }

// listElementRemovalSpans returns the spans to delete for element idx of
// lv: the element, the comma that separates it from a neighbour so the
// list stays well-formed, and a trailing comment on its last line unless
// keepComment is set. Those can be apart, with a comment on the element
// that stays between them, which is why this is more than one span.
//
// An element on lines of its own goes with those lines, so the lines
// around it survive. A last element with no trailing comma gives up the
// comma before it, which is then not its own: a list with a trailing
// comma keeps that shape.
func (r *Rewriter) listElementRemovalSpans(lv *ListVal, idx int, keepComment bool) []byteSpan {
	elems := lv.Elements
	start := elems[idx].pos().Offset
	end := elems[idx].end().Offset
	comma := r.listElemComma(lv, idx)
	tail := r.listElemTail(lv, idx)
	commentStart, _, lineEnd := r.trailingComment(tail)
	if keepComment && commentStart >= 0 {
		lineEnd = -1
	}

	var out []byteSpan
	switch {
	case comma >= tail: // on a later line than the element
		out = append(out, byteSpan{comma, comma + 1 + r.trailingListPad(comma+1)})
	case comma < 0 && idx > 0:
		prevEnd := elems[idx-1].end().Offset
		prev := r.listComma(prevEnd, start)
		if commentStart < 0 && prev >= 0 && !r.gapHasComment(prevEnd, start) {
			// Nothing to keep between the two elements: they close up.
			return []byteSpan{{prevEnd, end + r.trailingListPad(end)}}
		}
		if prev >= 0 {
			out = append(out, byteSpan{prev, prev + 1})
		}
	}

	ls := lineStartOffset(r.src, start)
	switch {
	case lineEnd < 0: // shares its last line with what follows
		return append(out, byteSpan{start, tail + r.trailingListPad(tail)})
	case !isBlank(r.src[ls:start]):
		for start > ls && (r.src[start-1] == ' ' || r.src[start-1] == '\t') {
			start--
		}
		return append(out, byteSpan{start, lineEnd})
	case lineEnd < len(r.src):
		return append(out, byteSpan{ls, lineEnd + 1})
	}
	return append(out, byteSpan{ls, lineEnd})
}

// gapHasComment reports whether src[from:to), the gap between two list
// tokens, holds a comment.
func (r *Rewriter) gapHasComment(from, to int) bool {
	for _, c := range r.src[from:to] {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != ',' {
			return true
		}
	}
	return false
}

// stageListRemoval stages the deletion of spans under one identity.
func (r *Rewriter) stageListRemoval(pathKey string, spans []byteSpan) {
	for i, s := range spans {
		r.stage(spanEdit{path: fmt.Sprintf("%s\x00%d", pathKey, i), start: s.start, end: s.end})
	}
}

// listSplice is an insertion into a list, and the comma it needs after
// the element before it when that is not next to the insertion: a comment
// on that element must stay on it. commaAt is -1 when there is none.
type listSplice struct {
	at      int
	text    []byte
	commaAt int
}

// stageSplice stages an insertion under one identity.
func (r *Rewriter) stageSplice(pathKey string, s listSplice) {
	r.stage(spanEdit{path: pathKey, start: s.at, end: s.at, text: s.text})
	if s.commaAt >= 0 {
		r.stage(spanEdit{path: pathKey + "\x00comma", start: s.commaAt, end: s.commaAt, text: []byte(",")})
	}
}

// trailingListPad returns the count of spaces/tabs immediately after off
// (used to tidy an inline last-element removal). Newlines are left for
// the bracket line.
func (r *Rewriter) trailingListPad(off int) int {
	n := 0
	for off+n < len(r.src) && (r.src[off+n] == ' ' || r.src[off+n] == '\t') {
		n++
	}
	return n
}

// renderAppendListElement builds the insertion that appends bv as the
// last element of the anonymous list b.
func (r *Rewriter) renderAppendListElement(b *keyedBinding, bv *BlockVal) listSplice {
	return r.spliceListElementAppend(b, r.renderListElement(b, bv), nil)
}

// renderInsertListElementBefore builds the insertion that places bv
// immediately before element idx of the anonymous list b.
func (r *Rewriter) renderInsertListElementBefore(b *keyedBinding, idx int, bv *BlockVal) listSplice {
	return r.spliceListElementBefore(b, idx, r.renderListElement(b, bv), nil)
}

// renderListElement formats bv as a list element (no surrounding comma
// or newline), indented for b's list.
func (r *Rewriter) renderListElement(b *keyedBinding, bv *BlockVal) []byte {
	indent := r.listElementIndent(b.list, b.scopeStart)
	return r.renderValue(bv, indent, r.indentStep())
}

// spliceListElementAppend returns the insertion that adds elemText as
// the last element of b's list, with comment after it on its line. It
// goes after the last element's line, so a comment there stays on that
// element, and after a trailing comma, which the new element then ends
// with too: the list keeps its shape.
func (r *Rewriter) spliceListElementAppend(b *keyedBinding, elemText, comment []byte) listSplice {
	lv := b.list
	indent := r.listElementIndent(lv, b.scopeStart)
	if len(lv.Elements) == 0 {
		// Empty list: place the element on its own line before ']'.
		base := r.lineLeadingIndent(b.scopeStart)
		var sb bytes.Buffer
		sb.WriteByte('\n')
		sb.WriteString(indent)
		sb.Write(elemText)
		sb.WriteByte('\n')
		sb.WriteString(base)
		return listSplice{at: b.closeOff, text: sb.Bytes(), commaAt: -1}
	}
	last := len(lv.Elements) - 1
	lastEnd := lv.Elements[last].end().Offset
	comma := r.listElemComma(lv, last)
	multiline := r.listMultiline(lv)

	var sb bytes.Buffer
	if !multiline {
		if comma >= 0 {
			sb.WriteByte(' ')
			sb.Write(elemText)
			sb.WriteByte(',')
			return listSplice{at: comma + 1, text: sb.Bytes(), commaAt: -1}
		}
		sb.WriteString(", ")
		sb.Write(elemText)
		return listSplice{at: lastEnd, text: sb.Bytes(), commaAt: -1}
	}

	after := lastEnd
	if comma >= 0 {
		after = comma + 1
	}
	_, commentEnd, _ := r.trailingComment(after)
	at := after
	if commentEnd >= 0 {
		at = commentEnd
	}
	commaAt := -1
	switch {
	case comma >= 0:
	case commentEnd >= 0:
		commaAt = lastEnd
	default:
		sb.WriteByte(',')
	}
	sb.WriteByte('\n')
	sb.WriteString(indent)
	sb.Write(elemText)
	if comma >= 0 {
		sb.WriteByte(',')
	}
	if len(comment) > 0 {
		sb.WriteByte(' ')
		sb.Write(comment)
	}
	return listSplice{at: at, text: sb.Bytes(), commaAt: commaAt}
}

// spliceListElementBefore returns the insertion that places elemText
// immediately before element idx of b's list, with comment after it on
// its line. On a list of lines it goes above the target's glued leading
// comments, which belong to the target.
func (r *Rewriter) spliceListElementBefore(b *keyedBinding, idx int, elemText, comment []byte) listSplice {
	lv := b.list
	indent := r.listElementIndent(lv, b.scopeStart)
	target := lv.Elements[idx].pos().Offset
	var sb bytes.Buffer
	if r.listMultiline(lv) {
		ls := lineStartOffset(r.src, target)
		if isBlank(r.src[ls:target]) {
			// Insert at a line start: the new element takes the target's
			// indent, then the comma and newline, leaving the lines after
			// it intact.
			sb.WriteString(indent)
			sb.Write(elemText)
			sb.WriteByte(',')
			if len(comment) > 0 {
				sb.WriteByte(' ')
				sb.Write(comment)
			}
			sb.WriteByte('\n')
			return listSplice{at: r.extendOverLeadingComments(ls), text: sb.Bytes(), commaAt: -1}
		}
	}
	sb.Write(elemText)
	sb.WriteString(", ")
	return listSplice{at: target, text: sb.Bytes(), commaAt: -1}
}

// --- shared helpers -------------------------------------------------

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

// joinPathSegs rejoins path segments with dots.
func joinPathSegs(segs []string) string {
	out := ""
	for i, s := range segs {
		if i > 0 {
			out += "."
		}
		out += s
	}
	return out
}
