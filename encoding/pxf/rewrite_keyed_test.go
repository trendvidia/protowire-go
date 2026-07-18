// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Keyed-collection editing (issue #53): format-preserving edits to
// draft -01 §3.13 blocks of named blocks, addressed by element key.

import (
	"strings"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// keyedSrc is the shared fixture document: a keyed collection with
// comments, a quoted non-identifier key, and a dotted key — the shapes
// dotted-path addressing cannot reach.
const keyedSrc = `id = "root"
children {
  # the welcome banner
  greeting {
    type = "Label"   # widget class
    weight = 1
  }
  "us-east-1" {
    type = "Region"
  }
  user.name {
    type = "Field"
  }
}
`

func TestSetKeyedReplacesValueInElement(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.SetKeyed("children", "greeting", "weight", &pxf.IntVal{Raw: "5"})
	})
	want := strings.Replace(keyedSrc, "weight = 1", "weight = 5", 1)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetKeyedReachesQuotedAndDottedKeys(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		if err := r.SetKeyed("children", "us-east-1", "type", &pxf.StringVal{Value: "Zone"}); err != nil {
			return err
		}
		return r.SetKeyed("children", "user.name", "type", &pxf.StringVal{Value: "Input"})
	})
	if !strings.Contains(got, "\"us-east-1\" {\n    type = \"Zone\"\n") {
		t.Fatalf("quoted-key element not edited:\n%s", got)
	}
	if !strings.Contains(got, "user.name {\n    type = \"Input\"\n") {
		t.Fatalf("dotted-key element not edited:\n%s", got)
	}
}

func TestSetKeyedInsertsMissingFieldIntoElement(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.SetKeyed("children", "us-east-1", "weight", &pxf.IntVal{Raw: "3"})
	})
	if !strings.Contains(got, "\"us-east-1\" {\n    type = \"Region\"\n    weight = 3\n  }") {
		t.Fatalf("missing field not inserted:\n%s", got)
	}
}

func TestSetKeyedCreatesMissingElement(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.SetKeyed("children", "footer", "type", &pxf.StringVal{Value: "HBox"})
	})
	if !strings.Contains(got, "  footer {\n    type = \"HBox\"\n  }\n}") {
		t.Fatalf("element not created at end of collection:\n%s", got)
	}
}

func TestSetKeyedCreatesQuotedElementAndNestedChain(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.SetKeyed("children", "eu-west-2", "geo.zone", &pxf.StringVal{Value: "b"})
	})
	if !strings.Contains(got, "  \"eu-west-2\" {\n    geo {\n      zone = \"b\"\n    }\n  }\n}") {
		t.Fatalf("quoted element with nested chain not created:\n%s", got)
	}
}

func TestSetKeyedLastCallWins(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		if err := r.SetKeyed("children", "greeting", "weight", &pxf.IntVal{Raw: "7"}); err != nil {
			return err
		}
		return r.SetKeyed("children", "greeting", "weight", &pxf.IntVal{Raw: "9"})
	})
	if !strings.Contains(got, "weight = 9") || strings.Contains(got, "weight = 7") {
		t.Fatalf("second SetKeyed did not replace the first:\n%s", got)
	}
}

func TestSetKeyedAddressesAssignmentSpelledElement(t *testing.T) {
	src := "children {\n  greeting = { weight = 1 }\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.SetKeyed("children", "greeting", "weight", &pxf.IntVal{Raw: "4"})
	})
	want := "children {\n  greeting = { weight = 4 }\n}\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRemoveKeyedFieldRemovesWholeLineWithComment(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.RemoveKeyed("children", "greeting", "type")
	})
	if strings.Contains(got, "widget class") || strings.Contains(got, "type = \"Label\"") {
		t.Fatalf("field line not fully removed:\n%s", got)
	}
	if !strings.Contains(got, "greeting {\n    weight = 1\n  }") {
		t.Fatalf("element structure damaged:\n%s", got)
	}
}

func TestRemoveKeyedElementKeepsSiblings(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.RemoveKeyedElement("children", "greeting")
	})
	if strings.Contains(got, "greeting") || strings.Contains(got, "Label") {
		t.Fatalf("element not removed:\n%s", got)
	}
	// The element's leading comment stays (it may describe the group);
	// siblings and layout are untouched.
	if !strings.Contains(got, "# the welcome banner") {
		t.Fatalf("leading comment should be kept:\n%s", got)
	}
	if !strings.Contains(got, "\"us-east-1\" {\n    type = \"Region\"\n  }") {
		t.Fatalf("sibling damaged:\n%s", got)
	}
}

func TestInsertKeyedElementAppends(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.InsertKeyedElement("children", "footer", "", []pxf.Entry{
			&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "HBox"}},
			&pxf.Assignment{Key: "weight", Value: &pxf.IntVal{Raw: "2"}},
		})
	})
	if !strings.Contains(got, "  footer {\n    type = \"HBox\"\n    weight = 2\n  }\n}") {
		t.Fatalf("element not appended:\n%s", got)
	}
}

func TestInsertKeyedElementBeforeAnchor(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.InsertKeyedElement("children", "header", "greeting", []pxf.Entry{
			&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "Banner"}},
		})
	})
	headerAt := strings.Index(got, "header {")
	greetingAt := strings.Index(got, "greeting {")
	if headerAt < 0 || greetingAt < 0 || headerAt > greetingAt {
		t.Fatalf("header not inserted before greeting:\n%s", got)
	}
	if !strings.Contains(got, "  header {\n    type = \"Banner\"\n  }\n") {
		t.Fatalf("inserted element misformatted:\n%s", got)
	}
	// The anchor's glued doc comment stays attached to the anchor: the
	// new element lands above the comment line.
	if !strings.Contains(got, "  }\n  # the welcome banner\n  greeting {") {
		t.Fatalf("anchor's doc comment detached from anchor:\n%s", got)
	}
}

func TestInsertKeyedElementQuotedKey(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.InsertKeyedElement("children", "ap-south-1", "", []pxf.Entry{
			&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "Region"}},
		})
	})
	if !strings.Contains(got, "  \"ap-south-1\" {\n    type = \"Region\"\n  }\n}") {
		t.Fatalf("non-identifier key not quoted:\n%s", got)
	}
}

func TestInsertKeyedElementDuplicateIsError(t *testing.T) {
	r, err := pxf.NewRewriter([]byte(keyedSrc))
	if err != nil {
		t.Fatal(err)
	}
	err = r.InsertKeyedElement("children", "greeting", "", nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want duplicate-key error, got %v", err)
	}
	// Spelling equivalence: "us-east-1" exists quoted; inserting the
	// same unquoted value collides.
	err = r.InsertKeyedElement("children", "us-east-1", "", nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want duplicate-key error for quoted sibling, got %v", err)
	}
}

func TestRenameKeyedElementBareToBare(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.RenameKeyedElement("children", "greeting", "welcome")
	})
	want := strings.Replace(keyedSrc, "greeting {", "welcome {", 1)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenameKeyedElementQuoteHandling(t *testing.T) {
	// Bare → quoted: the new key is not identifier-safe.
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.RenameKeyedElement("children", "greeting", "welcome-1")
	})
	if !strings.Contains(got, "\"welcome-1\" {\n    type = \"Label\"") {
		t.Fatalf("rename to non-identifier key should quote:\n%s", got)
	}
	// Quoted → bare: identifier-safe names drop their quotes.
	got = rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.RenameKeyedElement("children", "us-east-1", "primary")
	})
	if !strings.Contains(got, "  primary {\n    type = \"Region\"\n  }") {
		t.Fatalf("rename to identifier key should unquote:\n%s", got)
	}
}

func TestRenameKeyedElementGuards(t *testing.T) {
	r, err := pxf.NewRewriter([]byte(keyedSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RenameKeyedElement("children", "greeting", "us-east-1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want collision error, got %v", err)
	}
	if err := r.RenameKeyedElement("children", "missing", "x"); err == nil || !strings.Contains(err.Error(), "no element") {
		t.Fatalf("want no-element error, got %v", err)
	}
	if err := r.RenameKeyedElement("children", "greeting", ""); err == nil || !strings.Contains(err.Error(), "empty new key") {
		t.Fatalf("want empty-key error, got %v", err)
	}
	// Same-key rename is a no-op, not an error.
	if err := r.RenameKeyedElement("children", "greeting", "greeting"); err != nil {
		t.Fatalf("same-key rename should be a no-op, got %v", err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != keyedSrc {
		t.Fatalf("no-op rename changed the document")
	}
}

func TestMoveKeyedElementToEnd(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.MoveKeyedElement("children", "greeting", "")
	})
	// The element moves verbatim — comments and trailing comment
	// included — after user.name.
	if !strings.Contains(got, "user.name {\n    type = \"Field\"\n  }\n  # the welcome banner\n  greeting {\n    type = \"Label\"   # widget class\n    weight = 1\n  }\n}") {
		t.Fatalf("element not moved verbatim to end:\n%s", got)
	}
}

func TestMoveKeyedElementBeforeAnchor(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.MoveKeyedElement("children", "user.name", "greeting")
	})
	userAt := strings.Index(got, "user.name {")
	greetingAt := strings.Index(got, "greeting {")
	if userAt < 0 || greetingAt < 0 || userAt > greetingAt {
		t.Fatalf("user.name not moved before greeting:\n%s", got)
	}
}

func TestMoveKeyedElementNoOpBeforeSuccessor(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		return r.MoveKeyedElement("children", "greeting", "us-east-1")
	})
	if got != keyedSrc {
		t.Fatalf("moving before the current successor should reproduce the input:\ngot:\n%s", got)
	}
}

func TestKeyedEditorsOnInlineBlock(t *testing.T) {
	src := "children { a { x = 1 } b { x = 2 } }\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.InsertKeyedElement("children", "c", "b", []pxf.Entry{
			&pxf.Assignment{Key: "x", Value: &pxf.IntVal{Raw: "3"}},
		})
	})
	want := "children { a { x = 1 } c { x = 3 } b { x = 2 } }\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.MoveKeyedElement("children", "a", "")
	})
	want = "children { b { x = 2 } a { x = 1 } }\n"
	if got != want {
		t.Fatalf("move inline: got %q, want %q", got, want)
	}
}

func TestKeyedEditorsComposeInOneBatch(t *testing.T) {
	got := rewrite(t, keyedSrc, func(r *pxf.Rewriter) error {
		if err := r.SetKeyed("children", "greeting", "weight", &pxf.IntVal{Raw: "10"}); err != nil {
			return err
		}
		if err := r.RemoveKeyedElement("children", "user.name"); err != nil {
			return err
		}
		return r.InsertKeyedElement("children", "footer", "", []pxf.Entry{
			&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "HBox"}},
		})
	})
	if !strings.Contains(got, "weight = 10") || strings.Contains(got, "user.name") || !strings.Contains(got, "footer {") {
		t.Fatalf("batch edit incomplete:\n%s", got)
	}
	if _, err := pxf.Parse([]byte(got)); err != nil {
		t.Fatalf("batch result does not reparse: %v", err)
	}
}

func TestKeyedEditorErrors(t *testing.T) {
	r, err := pxf.NewRewriter([]byte(keyedSrc))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetKeyed("nope", "k", "x", &pxf.IntVal{Raw: "1"}); err == nil {
		t.Fatal("want error for missing field path")
	}
	if err := r.SetKeyed("id", "k", "x", &pxf.IntVal{Raw: "1"}); err == nil || !strings.Contains(err.Error(), "not a block") {
		t.Fatalf("want not-a-block error, got %v", err)
	}
	if err := r.SetKeyed("children", "", "x", &pxf.IntVal{Raw: "1"}); err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Fatalf("want empty-key error, got %v", err)
	}
	if err := r.RemoveKeyed("children", "missing", "x"); err == nil || !strings.Contains(err.Error(), "no element") {
		t.Fatalf("want no-element error, got %v", err)
	}
	if err := r.RemoveKeyed("children", "greeting", "missing"); err == nil || !strings.Contains(err.Error(), "no such entry") {
		t.Fatalf("want no-such-entry error, got %v", err)
	}
	if err := r.MoveKeyedElement("children", "greeting", "greeting"); err == nil || !strings.Contains(err.Error(), "before itself") {
		t.Fatalf("want before-itself error, got %v", err)
	}
	if err := r.InsertKeyedElement("children", "x", "missing", nil); err == nil || !strings.Contains(err.Error(), "to insert before") {
		t.Fatalf("want missing-anchor error, got %v", err)
	}
}
