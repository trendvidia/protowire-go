// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// mustTolerant parses src and asserts the number of reported errors.
func mustTolerant(t *testing.T, src string, wantErrs int) (*pxf.Document, []pxf.Error) {
	t.Helper()
	doc, errs := pxf.ParseTolerant([]byte(src))
	if doc == nil {
		t.Fatalf("ParseTolerant(%q) returned nil document", src)
	}
	if len(errs) != wantErrs {
		t.Fatalf("ParseTolerant(%q) reported %d errors, want %d:\n%v", src, len(errs), wantErrs, errs)
	}
	return doc, errs
}

// entryKeys flattens the top-level entry keys/names for shape assertions.
func entryKeys(doc *pxf.Document) []string {
	keys := make([]string, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		switch e := e.(type) {
		case *pxf.Assignment:
			keys = append(keys, e.Key)
		case *pxf.MapEntry:
			keys = append(keys, e.Key)
		case *pxf.Block:
			keys = append(keys, e.Name)
		}
	}
	return keys
}

func TestParseTolerantValidInputMatchesParse(t *testing.T) {
	src := `@type demo.v1.Config

# leading comment
name = "value"  # trailing
count = 42
weight = 3.14
when = 2024-01-15T10:30:00Z
wait = 1h30m
data = b"aGk="
tags = ["a", "b", "c"]
labels {
  "x": 1
  "y": 2
}
server {
  host = "h"
  tls {
    enabled = true
  }
}
`
	strict, err := pxf.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	doc, _ := mustTolerant(t, src, 0)
	if !reflect.DeepEqual(doc, strict) {
		t.Fatalf("tolerant AST differs from strict AST on valid input\ntolerant: %+v\nstrict:   %+v", doc, strict)
	}
}

func TestParseTolerantDanglingAssignment(t *testing.T) {
	// The exact mid-edit shape completion fires on: `key =` with the
	// next entry on the following line.
	doc, errs := mustTolerant(t, "port =\nhost = \"h\"\n", 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"port", "host"}) {
		t.Fatalf("entries = %v, want [port host]", got)
	}
	a := doc.Entries[0].(*pxf.Assignment)
	bad, ok := a.Value.(*pxf.BadVal)
	if !ok {
		t.Fatalf("port value = %T, want *pxf.BadVal", a.Value)
	}
	// The placeholder and the error anchor just past the dangling '=',
	// not at the next entry's key on the following line.
	if bad.Pos.Line != 1 || bad.Pos.Offset != len("port =") {
		t.Fatalf("BadVal.Pos = %v (offset %d), want just past '=' on line 1", bad.Pos, bad.Pos.Offset)
	}
	if errs[0].Pos.Line != 1 {
		t.Fatalf("error position = %v, want line 1", errs[0].Pos)
	}
	// The second entry must be intact.
	if h := doc.Entries[1].(*pxf.Assignment).Value.(*pxf.StringVal); h.Value != "h" {
		t.Fatalf("host = %q, want %q", h.Value, "h")
	}
}

func TestParseTolerantDanglingAssignmentAtEOF(t *testing.T) {
	doc, _ := mustTolerant(t, "port =", 1)
	a := doc.Entries[0].(*pxf.Assignment)
	if a.Key != "port" {
		t.Fatalf("key = %q", a.Key)
	}
	if _, ok := a.Value.(*pxf.BadVal); !ok {
		t.Fatalf("value = %T, want *pxf.BadVal", a.Value)
	}
}

func TestParseTolerantBareKey(t *testing.T) {
	doc, _ := mustTolerant(t, "flush\nb = 2\n", 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"flush", "b"}) {
		t.Fatalf("entries = %v, want [flush b]", got)
	}
	bad, ok := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.BadVal)
	if !ok {
		t.Fatalf("bare key should carry a BadVal value")
	}
	if bad.Pos.Offset != len("flush") {
		t.Fatalf("BadVal.Pos.Offset = %d, want just past the key (%d)", bad.Pos.Offset, len("flush"))
	}
}

func TestParseTolerantUnterminatedString(t *testing.T) {
	// Completion mid-keystroke on `type = "`.
	doc, _ := mustTolerant(t, `type = "`, 1)
	a := doc.Entries[0].(*pxf.Assignment)
	s, ok := a.Value.(*pxf.StringVal)
	if !ok {
		t.Fatalf("value = %T, want *pxf.StringVal", a.Value)
	}
	if s.Value != "" {
		t.Fatalf("value = %q, want empty", s.Value)
	}
}

func TestParseTolerantUnterminatedStringEndsAtNewline(t *testing.T) {
	doc, errs := mustTolerant(t, "name = \"ab\nport = 1\n", 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"name", "port"}) {
		t.Fatalf("entries = %v, want [name port]", got)
	}
	if s := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.StringVal); s.Value != "ab" {
		t.Fatalf("name = %q, want %q", s.Value, "ab")
	}
	if !strings.Contains(errs[0].Msg, "unterminated string") {
		t.Fatalf("error = %q, want unterminated string", errs[0].Msg)
	}
}

func TestParseTolerantUnclosedBlock(t *testing.T) {
	doc, errs := mustTolerant(t, "server {\n  port = 8080\n", 1)
	b := doc.Entries[0].(*pxf.Block)
	if b.Name != "server" || len(b.Entries) != 1 {
		t.Fatalf("block = %+v", b)
	}
	if p := b.Entries[0].(*pxf.Assignment); p.Key != "port" {
		t.Fatalf("inner entry = %+v", p)
	}
	if !strings.Contains(errs[0].Msg, "unclosed block") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

func TestParseTolerantUnclosedNestedBlocks(t *testing.T) {
	// Every open block is closed at EOF; one error per unclosed brace.
	doc, _ := mustTolerant(t, "a {\n b {\n  c = 1\n", 2)
	a := doc.Entries[0].(*pxf.Block)
	b := a.Entries[0].(*pxf.Block)
	if c := b.Entries[0].(*pxf.Assignment); c.Key != "c" {
		t.Fatalf("innermost entry = %+v", c)
	}
}

func TestParseTolerantStrayCloseBrace(t *testing.T) {
	doc, _ := mustTolerant(t, "a = 1\n}\nb = 2\n", 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("entries = %v, want [a b]", got)
	}
}

func TestParseTolerantUnclosedList(t *testing.T) {
	doc, errs := mustTolerant(t, "xs = [1, 2", 1)
	xs := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.ListVal)
	if len(xs.Elements) != 2 {
		t.Fatalf("elements = %d, want 2", len(xs.Elements))
	}
	if !strings.Contains(errs[0].Msg, "']'") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

func TestParseTolerantMapMissingValue(t *testing.T) {
	doc, _ := mustTolerant(t, "m {\n  \"a\":\n  \"b\": 2\n}\n", 1)
	m := doc.Entries[0].(*pxf.Block)
	if len(m.Entries) != 2 {
		t.Fatalf("map entries = %d, want 2", len(m.Entries))
	}
	if _, ok := m.Entries[0].(*pxf.MapEntry).Value.(*pxf.BadVal); !ok {
		t.Fatalf(`"a" value = %T, want *pxf.BadVal`, m.Entries[0].(*pxf.MapEntry).Value)
	}
	if v := m.Entries[1].(*pxf.MapEntry).Value.(*pxf.IntVal); v.Raw != "2" {
		t.Fatalf(`"b" value = %+v`, v)
	}
}

func TestParseTolerantMapEntryAtTopLevel(t *testing.T) {
	// Recorded as an error but kept as a map entry so tooling sees it.
	doc, _ := mustTolerant(t, "a: 1\n", 1)
	if _, ok := doc.Entries[0].(*pxf.MapEntry); !ok {
		t.Fatalf("entry = %T, want *pxf.MapEntry", doc.Entries[0])
	}
}

func TestParseTolerantIllegalToken(t *testing.T) {
	doc, _ := mustTolerant(t, "a = $\nb = 2\n", 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("entries = %v, want [a b]", got)
	}
	if _, ok := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.BadVal); !ok {
		t.Fatalf("value = %T, want *pxf.BadVal", doc.Entries[0].(*pxf.Assignment).Value)
	}
}

func TestParseTolerantBadBase64(t *testing.T) {
	doc, errs := mustTolerant(t, `k = b"!!"`, 1)
	if _, ok := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.BadVal); !ok {
		t.Fatalf("value = %T, want *pxf.BadVal", doc.Entries[0].(*pxf.Assignment).Value)
	}
	if !strings.Contains(errs[0].Msg, "base64") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

func TestParseTolerantUnterminatedBytes(t *testing.T) {
	doc, _ := mustTolerant(t, "k = b\"aGk=\nb = 2\n", 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"k", "b"}) {
		t.Fatalf("entries = %v, want [k b]", got)
	}
}

func TestParseTolerantUnterminatedBlockComment(t *testing.T) {
	doc, errs := mustTolerant(t, "a = 1\n/* dangling", 1)
	if len(doc.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(doc.Entries))
	}
	if !strings.Contains(errs[0].Msg, "unterminated block comment") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

func TestParseTolerantUnterminatedTripleString(t *testing.T) {
	doc, errs := mustTolerant(t, "s = \"\"\"\nhello\n", 1)
	if s := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.StringVal); !strings.Contains(s.Value, "hello") {
		t.Fatalf("value = %q", s.Value)
	}
	if !strings.Contains(errs[0].Msg, "unterminated triple-quoted string") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

func TestParseTolerantBadEscapeKeepsRest(t *testing.T) {
	doc, errs := mustTolerant(t, `s = "a\qb"`+"\n"+`t = 2`, 1)
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"s", "t"}) {
		t.Fatalf("entries = %v, want [s t]", got)
	}
	if s := doc.Entries[0].(*pxf.Assignment).Value.(*pxf.StringVal); s.Value != `a\qb` {
		t.Fatalf("value = %q, want %q", s.Value, `a\qb`)
	}
	if !strings.Contains(errs[0].Msg, "escape") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

func TestParseTolerantDirectiveErrors(t *testing.T) {
	// A malformed directive is skipped up to the next directive or the
	// body; both survive.
	doc, _ := mustTolerant(t, "@dataset (\n@type demo.v1.Config\nx = 1\n", 1)
	if len(doc.Datasets) != 0 {
		t.Fatalf("datasets = %d, want 0", len(doc.Datasets))
	}
	if doc.TypeURL != "demo.v1.Config" {
		t.Fatalf("TypeURL = %q", doc.TypeURL)
	}
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("entries = %v, want [x]", got)
	}
}

func TestParseTolerantTypeMissingName(t *testing.T) {
	doc, _ := mustTolerant(t, "@type\nx = 1\n", 1)
	if doc.TypeURL != "" {
		t.Fatalf("TypeURL = %q, want empty", doc.TypeURL)
	}
	if got := entryKeys(doc); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("entries = %v, want [x]", got)
	}
}

func TestParseTolerantErrorsSortedByPosition(t *testing.T) {
	_, errs := mustTolerant(t, "a =\nb = $\nc {\n", 3)
	for i := 1; i < len(errs); i++ {
		if errs[i].Pos.Offset < errs[i-1].Pos.Offset {
			t.Fatalf("errors not sorted by offset: %v", errs)
		}
	}
}

// TestParseTolerantEveryTruncation exercises the terminate-and-recover
// guarantees on every prefix of a rich document — the exact stream of
// states an editor buffer passes through while the document is typed
// top to bottom.
func TestParseTolerantEveryTruncation(t *testing.T) {
	src := []byte(`@type demo.v1.Config
@header chameleon.v1.LayerHeader { id = "x" }

# comment
name = "value"
data = b"aGk="
tags = ["a", "b"]
labels { "x": 1 }
server {
  host = "h"
  tls { enabled = true }
  timeout = 30s
}
`)
	for i := 0; i <= len(src); i++ {
		doc, _ := pxf.ParseTolerant(src[:i])
		if doc == nil {
			t.Fatalf("nil document at truncation %d", i)
		}
	}
}

// TestParseTolerantDepthRecoverySpan pins that a block or list beyond
// MaxNestingDepth is skipped with an End just past its own closing
// token, not at whatever token follows.
func TestParseTolerantDepthRecoverySpan(t *testing.T) {
	src := "xs = " + strings.Repeat("[", 101) + "1" + strings.Repeat(" ]", 101) + "\nnext = 1\n"
	doc, errs := mustTolerant(t, src, 1)
	v := doc.Entries[0].(*pxf.Assignment).Value
	for i := 0; i < 100; i++ {
		v = v.(*pxf.ListVal).Elements[0]
	}
	recovered := v.(*pxf.ListVal)
	_, end := pxf.ValueSpan(recovered)
	wantEnd := strings.IndexByte(src, ']') + 1
	if end.Offset != wantEnd {
		t.Fatalf("recovered list End.Offset = %d, want %d (just past its own ']')", end.Offset, wantEnd)
	}
	if !strings.Contains(errs[0].Msg, "MaxNestingDepth") {
		t.Fatalf("error = %q", errs[0].Msg)
	}
}

// TestParseTolerantStrictUnchanged pins that Parse keeps its
// all-or-nothing contract on the shapes ParseTolerant recovers from.
func TestParseTolerantStrictUnchanged(t *testing.T) {
	for _, src := range []string{
		"port =",
		`type = "`,
		"server {\n  port = 8080\n",
		"xs = [1, 2",
		"a = 1\n}\nb = 2\n",
		"a: 1\n",
	} {
		if _, err := pxf.Parse([]byte(src)); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", src)
		}
	}
}
