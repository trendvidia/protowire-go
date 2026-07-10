// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- #37: FormatValue / AppendValue ---------------------------------

func TestFormatValueScalars(t *testing.T) {
	cases := []struct {
		name string
		v    pxf.Value
		want string
	}{
		{"string", &pxf.StringVal{Value: `he said "hi"`}, `"he said \"hi\""`},
		{"int", &pxf.IntVal{Raw: "0x1F"}, "0x1F"},
		{"float", &pxf.FloatVal{Raw: "1.5e3"}, "1.5e3"},
		{"bool", &pxf.BoolVal{Value: true}, "true"},
		{"null", &pxf.NullVal{}, "null"},
		{"ident", &pxf.IdentVal{Name: "ENABLED"}, "ENABLED"},
		{"bytes", &pxf.BytesVal{Value: []byte("hi")}, `b"aGk="`},
		{"duration", &pxf.DurationVal{Raw: "30s"}, "30s"},
		{"timestamp", &pxf.TimestampVal{Raw: "2020-01-01T00:00:00Z"}, "2020-01-01T00:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(pxf.FormatValue(c.v)); got != c.want {
				t.Fatalf("FormatValue = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatValueMultiline(t *testing.T) {
	v := &pxf.ListVal{Elements: []pxf.Value{
		&pxf.IntVal{Raw: "1"}, &pxf.IntVal{Raw: "2"},
	}}
	want := "[\n  1,\n  2\n]"
	if got := string(pxf.FormatValue(v)); got != want {
		t.Fatalf("FormatValue = %q, want %q", got, want)
	}
}

func TestFormatValueMatchesRewriter(t *testing.T) {
	// The exported formatter must render the same literal the Rewriter
	// splices for an equivalent value.
	got := rewrite(t, "a = 0\n", func(r *pxf.Rewriter) error {
		return r.Set("a", &pxf.StringVal{Value: "x\ny"})
	})
	want := "a = " + string(pxf.FormatValue(&pxf.StringVal{Value: "x\ny"})) + "\n"
	if got != want {
		t.Fatalf("Rewriter Set = %q, want %q", got, want)
	}
}

func TestAppendValuePreservesDst(t *testing.T) {
	dst := []byte("prefix=")
	got := string(pxf.AppendValue(dst, &pxf.IntVal{Raw: "7"}))
	if got != "prefix=7" {
		t.Fatalf("AppendValue = %q, want %q", got, "prefix=7")
	}
}

// --- #43: renderValue / renderChain honor the document's indent step -

func TestSetCreatesChainWithDocumentStep(t *testing.T) {
	// The synthesized block scaffolding steps by the document's width
	// (4 spaces here), not a hard-coded 2.
	src := "server {\n    host = \"h\"\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("server.tls.min_version", &pxf.StringVal{Value: "1.3"})
	})
	want := "server {\n    host = \"h\"\n    tls {\n        min_version = \"1.3\"\n    }\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSetMultilineValueUsesDocumentStep(t *testing.T) {
	// A multi-line value written onto an existing entry steps its body by
	// the document width (4 spaces), via renderValue.
	src := "server {\n    tags = [\"a\"]\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("server.tags", &pxf.ListVal{Elements: []pxf.Value{
			&pxf.StringVal{Value: "x"}, &pxf.StringVal{Value: "y"},
		}})
	})
	want := "server {\n    tags = [\n        \"x\",\n        \"y\"\n    ]\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// --- #38: ReplaceValue / SetSpan ------------------------------------

// findAssignment returns the first assignment with the given key,
// walking nested blocks, the way a real editor would grab a node it
// holds. It fails the test when no match exists.
func findAssignment(t *testing.T, entries []pxf.Entry, key string) *pxf.Assignment {
	t.Helper()
	for _, e := range entries {
		switch n := e.(type) {
		case *pxf.Assignment:
			if n.Key == key {
				return n
			}
		case *pxf.Block:
			if a := findAssignmentIn(n.Entries, key); a != nil {
				return a
			}
		}
	}
	t.Fatalf("no assignment %q found", key)
	return nil
}

func findAssignmentIn(entries []pxf.Entry, key string) *pxf.Assignment {
	for _, e := range entries {
		switch n := e.(type) {
		case *pxf.Assignment:
			if n.Key == key {
				return n
			}
		case *pxf.Block:
			if a := findAssignmentIn(n.Entries, key); a != nil {
				return a
			}
		}
	}
	return nil
}

func TestReplaceValueTargetsSpecificSibling(t *testing.T) {
	src := `children {
    a { type = "Button"  text = "@one" }
    b { type = "Button"  text = "@two" }
}
`
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	// Reach the `text` assignment inside node b — first-match paths can't.
	children := r.Document().Entries[0].(*pxf.Block)
	nodeB := children.Entries[1].(*pxf.Block)
	textB := findAssignment(t, nodeB.Entries, "text")
	if err := r.ReplaceValue(textB.Value, &pxf.StringVal{Value: "@changed"}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `children {
    a { type = "Button"  text = "@one" }
    b { type = "Button"  text = "@changed" }
}
`
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestReplaceValueReindentsMultiline(t *testing.T) {
	src := "server {\n    tags = [\"a\"]\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	tags := findAssignment(t, r.Document().Entries, "tags")
	if err := r.ReplaceValue(tags.Value, &pxf.ListVal{Elements: []pxf.Value{
		&pxf.StringVal{Value: "x"}, &pxf.StringVal{Value: "y"},
	}}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	// Source indents 4 spaces, so the replacement list body steps by 4.
	want := "server {\n    tags = [\n        \"x\",\n        \"y\"\n    ]\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestReplaceValueSameNodeLastWins(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("a = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	v := findAssignment(t, r.Document().Entries, "a").Value
	if err := r.ReplaceValue(v, &pxf.IntVal{Raw: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceValue(v, &pxf.IntVal{Raw: "3"}); err != nil {
		t.Fatal(err)
	}
	out, _ := r.Bytes()
	if string(out) != "a = 3\n" {
		t.Fatalf("got %q, want %q", out, "a = 3\n")
	}
}

func TestReplaceValueErrors(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("a = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	v := findAssignment(t, r.Document().Entries, "a").Value
	if err := r.ReplaceValue(nil, v); err == nil {
		t.Error("nil old should fail")
	}
	if err := r.ReplaceValue(v, nil); err == nil {
		t.Error("nil replacement should fail")
	}
	if err := r.ReplaceValue(v, &pxf.BadVal{}); err == nil {
		t.Error("BadVal replacement should fail")
	}
	// A hand-built value has no span.
	if err := r.ReplaceValue(&pxf.IntVal{Raw: "9"}, v); err == nil {
		t.Error("spanless old value should fail")
	}
}

func TestSetSpanRawSplice(t *testing.T) {
	src := "a = 1\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	v := findAssignment(t, r.Document().Entries, "a").Value
	start, end := pxf.ValueSpan(v)
	if err := r.SetSpan(start.Offset, end.Offset, []byte("42")); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a = 42\n" {
		t.Fatalf("got %q, want %q", out, "a = 42\n")
	}
}

func TestSetSpanOutOfRange(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("a = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetSpan(-1, 2, []byte("x")); err == nil {
		t.Error("negative start should fail")
	}
	if err := r.SetSpan(3, 2, []byte("x")); err == nil {
		t.Error("end < start should fail")
	}
	if err := r.SetSpan(0, 999, []byte("x")); err == nil {
		t.Error("end past source should fail")
	}
}

func TestSetSpanCopiesText(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("a = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	text := []byte("42")
	if err := r.SetSpan(4, 5, text); err != nil {
		t.Fatal(err)
	}
	text[0], text[1] = '9', '9' // mutate after staging; must not affect output
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a = 42\n" {
		t.Fatalf("got %q, want %q (text was not copied)", out, "a = 42\n")
	}
}

// --- #39: AppendEntry -----------------------------------------------

func TestAppendEntryMultilineMatchesSiblingIndent(t *testing.T) {
	src := "server {\n    host = \"h\"\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	err = r.AppendEntry(block, &pxf.Assignment{Key: "port", Value: &pxf.IntVal{Raw: "8080"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "server {\n    host = \"h\"\n    port = 8080\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestAppendEntryBlockNode(t *testing.T) {
	src := "children {\n    a { type = \"Button\" }\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	child := &pxf.Block{Name: "b", Entries: []pxf.Entry{
		&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "Label"}},
	}}
	if err := r.AppendEntry(block, child); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	// The source steps in by 4 (a is 4 spaces inside children), so the
	// appended block's body steps by 4 too, not a fixed 2.
	want := "children {\n    a { type = \"Button\" }\n    b {\n        type = \"Label\"\n    }\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestAppendEntryBlockBodyMatchesDocumentStep(t *testing.T) {
	// #41: a nested body must step by the document's own indent width,
	// inferred from a sibling block's body (4 spaces here), not a fixed 2.
	src := "root {\n    children {\n        greeting {\n            type = \"Label\"\n        }\n    }\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	children := r.Document().Entries[0].(*pxf.Block).Entries[0].(*pxf.Block)
	child := &pxf.Block{Name: "button", Entries: []pxf.Entry{
		&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "Button"}},
	}}
	if err := r.AppendEntry(children, child); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "root {\n    children {\n        greeting {\n            type = \"Label\"\n        }\n" +
		"        button {\n            type = \"Button\"\n        }\n    }\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestAppendEntryEmptyBlockUsesDocumentStep(t *testing.T) {
	// An empty target block reveals no local step; the width is inferred
	// document-wide (4 spaces from sub's body) for both the new node's
	// own line and its body.
	src := "root {\n    sub {\n        x = 1\n    }\n    children {\n    }\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	children := r.Document().Entries[0].(*pxf.Block).Entries[1].(*pxf.Block)
	child := &pxf.Block{Name: "button", Entries: []pxf.Entry{
		&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "Button"}},
	}}
	if err := r.AppendEntry(children, child); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "root {\n    sub {\n        x = 1\n    }\n    children {\n" +
		"        button {\n            type = \"Button\"\n        }\n    }\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestAppendEntryEmptyInlineBlock(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("children { }\n"))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	child := &pxf.Block{Name: "a", Entries: []pxf.Entry{
		&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "Button"}},
	}}
	if err := r.AppendEntry(block, child); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "children { a { type = \"Button\" } }\n"
	if string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestAppendEntryInlineBlock(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("inline { a = 1 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	if err := r.AppendEntry(block, &pxf.Assignment{Key: "b", Value: &pxf.IntVal{Raw: "2"}}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "inline { a = 1 b = 2 }\n" {
		t.Fatalf("got %q", out)
	}
}

func TestAppendEntryMapForm(t *testing.T) {
	src := "labels {\n  \"team\": \"core\"\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	err = r.AppendEntry(block, &pxf.MapEntry{Key: "env", Value: &pxf.StringVal{Value: "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "labels {\n  \"team\": \"core\"\n  env: \"prod\"\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestAppendEntryMultipleAccumulate(t *testing.T) {
	src := "server {\n  host = \"h\"\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	if err := r.AppendEntry(block, &pxf.Assignment{Key: "port", Value: &pxf.IntVal{Raw: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.AppendEntry(block, &pxf.Assignment{Key: "debug", Value: &pxf.BoolVal{Value: true}}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "server {\n  host = \"h\"\n  port = 1\n  debug = true\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestAppendEntryEmptyMultilineBlock(t *testing.T) {
	src := "server {\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	if err := r.AppendEntry(block, &pxf.Assignment{Key: "port", Value: &pxf.IntVal{Raw: "8080"}}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "server {\n  port = 8080\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", out, want)
	}
}

func TestAppendEntryErrors(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("server {\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	block := r.Document().Entries[0].(*pxf.Block)
	if err := r.AppendEntry(nil, &pxf.Assignment{Key: "a", Value: &pxf.IntVal{Raw: "1"}}); err == nil {
		t.Error("nil block should fail")
	}
	if err := r.AppendEntry(block, nil); err == nil {
		t.Error("nil entry should fail")
	}
	if err := r.AppendEntry(block, &pxf.Assignment{Key: "a", Value: &pxf.BadVal{}}); err == nil {
		t.Error("BadVal entry should fail")
	}
	if err := r.AppendEntry(&pxf.Block{Name: "x"}, &pxf.Assignment{Key: "a", Value: &pxf.IntVal{Raw: "1"}}); err == nil {
		t.Error("hand-built block with no span should fail")
	}
}
