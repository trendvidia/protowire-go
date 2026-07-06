// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func rewrite(t *testing.T, src string, stage func(r *pxf.Rewriter) error) string {
	t.Helper()
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatalf("NewRewriter: %v", err)
	}
	if err := stage(r); err != nil {
		t.Fatalf("stage: %v", err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return string(out)
}

func TestRewriterSetScalarPreservesEverythingElse(t *testing.T) {
	src := `# heading comment

server {
	host = "prod"   # primary
	port = 8080     # keep me
}
`
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("server.port", &pxf.IntVal{Raw: "9090"})
	})
	want := `# heading comment

server {
	host = "prod"   # primary
	port = 9090     # keep me
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriterSetPreservesSeparatorAndQuoting(t *testing.T) {
	src := "labels {\n  \"team\":   \"core\"\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("labels.team", &pxf.StringVal{Value: "infra"})
	})
	want := "labels {\n  \"team\":   \"infra\"\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewriterSetInsertsIntoMultilineBlock(t *testing.T) {
	src := "server {\n\thost = \"h\"\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("server.port", &pxf.IntVal{Raw: "8080"})
	})
	want := "server {\n\thost = \"h\"\n\tport = 8080\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRewriterSetInsertsIntoInlineBlock(t *testing.T) {
	got := rewrite(t, "inline { a = 1 }\n", func(r *pxf.Rewriter) error {
		return r.Set("inline.b", &pxf.IntVal{Raw: "2"})
	})
	want := "inline { a = 1 b = 2 }\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriterSetInsertsIntoEmptyInlineBlock(t *testing.T) {
	got := rewrite(t, "empty {}\n", func(r *pxf.Rewriter) error {
		return r.Set("empty.a", &pxf.BoolVal{Value: true})
	})
	want := "empty { a = true }\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriterSetAppendsAtTopLevel(t *testing.T) {
	got := rewrite(t, "a = 1", func(r *pxf.Rewriter) error {
		return r.Set("b", &pxf.StringVal{Value: "x"})
	})
	want := "a = 1\nb = \"x\"\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriterSetCreatesMissingChain(t *testing.T) {
	src := "server {\n  host = \"h\"\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("server.tls.min_version", &pxf.StringVal{Value: "1.3"})
	})
	want := "server {\n  host = \"h\"\n  tls {\n    min_version = \"1.3\"\n  }\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRewriterSetInsertsMapEntryForm(t *testing.T) {
	src := "labels {\n  \"team\": \"core\"\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("labels.env", &pxf.StringVal{Value: "prod"})
	})
	want := "labels {\n  \"team\": \"core\"\n  env: \"prod\"\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRewriterSetMultilineValueIndents(t *testing.T) {
	src := "server {\n\ttags = [\"a\"]\n}\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Set("server.tags", &pxf.ListVal{Elements: []pxf.Value{
			&pxf.StringVal{Value: "x"},
			&pxf.StringVal{Value: "y"},
		}})
	})
	want := "server {\n\ttags = [\n\t  \"x\",\n\t  \"y\"\n\t]\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestRewriterRemoveWholeLineKeepsLeadingComments(t *testing.T) {
	src := "# section\n# about a\na = 1   # trailing\nb = 2\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Remove("a")
	})
	want := "# section\n# about a\nb = 2\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriterRemoveBlock(t *testing.T) {
	src := "a = 1\nserver {\n  host = \"h\"\n}\nb = 2\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Remove("server")
	})
	want := "a = 1\nb = 2\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriterRemoveInlineEntry(t *testing.T) {
	src := "inline { a = 1 b = 2 }\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Remove("inline.a")
	})
	want := "inline { b = 2 }\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriterRemoveTrailingBlockComment(t *testing.T) {
	// A /* ... */ that closes on the entry's own line goes with it.
	src := "x = 1 /* gone */\ny = 2\n"
	got := rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Remove("x")
	})
	if got != "y = 2\n" {
		t.Fatalf("got %q, want %q", got, "y = 2\n")
	}
	// A multi-line block comment is not the entry's trailing comment;
	// only the entry's exact span is removed and the comment survives.
	src = "x = 1 /* spans\nlines */\ny = 2\n"
	got = rewrite(t, src, func(r *pxf.Rewriter) error {
		return r.Remove("x")
	})
	if got != "/* spans\nlines */\ny = 2\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRewriterSetRejectsNestedBadVal(t *testing.T) {
	r, err := pxf.NewRewriter([]byte("xs = [1]\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Set("xs", &pxf.ListVal{Elements: []pxf.Value{
		&pxf.StringVal{Value: "a"}, &pxf.BadVal{},
	}})
	if err == nil {
		t.Fatal("Set with a nested BadVal should fail")
	}
	err = r.Set("m", &pxf.BlockVal{Entries: []pxf.Entry{
		&pxf.Assignment{Key: "k", Value: &pxf.BadVal{}},
	}})
	if err == nil {
		t.Fatal("Set with a BadVal inside a BlockVal should fail")
	}
}

func TestRewriterErrors(t *testing.T) {
	src := "server {\n  host = \"h\"\n}\nport = 1\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("nope"); err == nil {
		t.Error("Remove of missing path should fail")
	}
	if err := r.Set("server", &pxf.IntVal{Raw: "1"}); err == nil {
		t.Error("Set on a block should fail")
	}
	if err := r.Set("port.sub", &pxf.IntVal{Raw: "1"}); err == nil {
		t.Error("descending through a scalar should fail")
	}
	if err := r.Set("", &pxf.IntVal{Raw: "1"}); err == nil {
		t.Error("empty path should fail")
	}
	if err := r.Set("a", nil); err == nil {
		t.Error("nil value should fail")
	}
	if _, err := pxf.NewRewriter([]byte("port =")); err == nil {
		t.Error("NewRewriter on invalid source should fail")
	}
}

func TestRewriterConflictingEditsRejected(t *testing.T) {
	src := "server {\n  host = \"h\"\n}\n"
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("server"); err != nil {
		t.Fatal(err)
	}
	if err := r.Set("server.host", &pxf.StringVal{Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Bytes(); err == nil {
		t.Fatal("overlapping Remove(server) + Set(server.host) should fail")
	}
}

func TestRewriterSetSamePathLastWins(t *testing.T) {
	got := rewrite(t, "a = 1\n", func(r *pxf.Rewriter) error {
		if err := r.Set("a", &pxf.IntVal{Raw: "2"}); err != nil {
			return err
		}
		return r.Set("a", &pxf.IntVal{Raw: "3"})
	})
	if got != "a = 3\n" {
		t.Fatalf("got %q, want %q", got, "a = 3\n")
	}
}

func TestRewriterNoEditsRoundTripsExactly(t *testing.T) {
	for _, f := range rewriteCorpus(t) {
		r, err := pxf.NewRewriter(f.src)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		out, err := r.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if !bytes.Equal(out, f.src) {
			t.Fatalf("%s: Bytes() without edits is not byte-identical", f.name)
		}
	}
}

type corpusFile struct {
	name string
	src  []byte
}

func rewriteCorpus(t *testing.T) []corpusFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "rewrite", "*.pxf"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no rewrite corpus found: %v", err)
	}
	files := make([]corpusFile, 0, len(paths))
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, corpusFile{name: filepath.Base(p), src: src})
	}
	return files
}

// leafPaths walks a document and returns the dotted path of every
// addressable leaf entry (assignments and map entries whose path
// segments contain no dot).
func leafPaths(doc *pxf.Document) []string {
	var out []string
	var walk func(prefix string, entries []pxf.Entry)
	walk = func(prefix string, entries []pxf.Entry) {
		for _, e := range entries {
			switch n := e.(type) {
			case *pxf.Assignment:
				if strings.Contains(n.Key, ".") {
					continue
				}
				if bv, ok := n.Value.(*pxf.BlockVal); ok {
					walk(prefix+n.Key+".", bv.Entries)
				} else {
					out = append(out, prefix+n.Key)
				}
			case *pxf.MapEntry:
				if strings.Contains(n.Key, ".") {
					continue
				}
				if bv, ok := n.Value.(*pxf.BlockVal); ok {
					walk(prefix+n.Key+".", bv.Entries)
				} else {
					out = append(out, prefix+n.Key)
				}
			case *pxf.Block:
				if strings.Contains(n.Name, ".") {
					continue
				}
				walk(prefix+n.Name+".", n.Entries)
			}
		}
	}
	walk("", doc.Entries)
	return out
}

// findLeaf resolves a leaf path against a parsed document, returning
// its value span.
func findLeaf(t *testing.T, doc *pxf.Document, path string) (valStart, valEnd int) {
	t.Helper()
	segs := strings.Split(path, ".")
	entries := doc.Entries
	for i, seg := range segs {
		var found pxf.Entry
		for _, e := range entries {
			switch n := e.(type) {
			case *pxf.Assignment:
				if n.Key == seg {
					found = e
				}
			case *pxf.MapEntry:
				if n.Key == seg {
					found = e
				}
			case *pxf.Block:
				if n.Name == seg {
					found = e
				}
			}
			if found != nil {
				break
			}
		}
		if found == nil {
			t.Fatalf("path %s: segment %q not found", path, seg)
		}
		var val pxf.Value
		switch n := found.(type) {
		case *pxf.Assignment:
			val = n.Value
		case *pxf.MapEntry:
			val = n.Value
		case *pxf.Block:
			entries = n.Entries
			continue
		}
		if i == len(segs)-1 {
			start, end := pxf.ValueSpan(val)
			return start.Offset, end.Offset
		}
		entries = val.(*pxf.BlockVal).Entries
	}
	t.Fatalf("path %s did not terminate at a leaf", path)
	return 0, 0
}

// TestRewriterPropertySingleEditTouchesOnlyItsSpan is the property
// test from issue #24: for a corpus of hand-written PXF, a random
// single-field edit must leave every byte outside the edited value
// span untouched — comments, blank lines, ordering, indentation, and
// formatting quirks all survive verbatim.
func TestRewriterPropertySingleEditTouchesOnlyItsSpan(t *testing.T) {
	rng := rand.New(rand.NewSource(24)) // deterministic
	replacements := []func() pxf.Value{
		func() pxf.Value { return &pxf.StringVal{Value: "edited"} },
		func() pxf.Value { return &pxf.IntVal{Raw: "12345"} },
		func() pxf.Value { return &pxf.BoolVal{Value: false} },
		func() pxf.Value { return &pxf.FloatVal{Raw: "2.75"} },
		func() pxf.Value {
			return &pxf.ListVal{Elements: []pxf.Value{
				&pxf.IntVal{Raw: "1"}, &pxf.IntVal{Raw: "2"},
			}}
		},
	}
	for _, f := range rewriteCorpus(t) {
		doc, err := pxf.Parse(f.src)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		for _, path := range leafPaths(doc) {
			valStart, valEnd := findLeaf(t, doc, path)
			v := replacements[rng.Intn(len(replacements))]()

			r, err := pxf.NewRewriter(f.src)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Set(path, v); err != nil {
				t.Fatalf("%s: Set(%s): %v", f.name, path, err)
			}
			out, err := r.Bytes()
			if err != nil {
				t.Fatalf("%s: Set(%s): Bytes: %v", f.name, path, err)
			}

			prefix := f.src[:valStart]
			suffix := f.src[valEnd:]
			if !bytes.HasPrefix(out, prefix) {
				t.Errorf("%s: Set(%s) modified bytes before the value span", f.name, path)
			}
			if !bytes.HasSuffix(out, suffix) {
				t.Errorf("%s: Set(%s) modified bytes after the value span", f.name, path)
			}
			// And the rewritten value must actually be the new value.
			redoc, err := pxf.Parse(out)
			if err != nil {
				t.Fatalf("%s: Set(%s): output does not reparse: %v", f.name, path, err)
			}
			ns, ne := findLeaf(t, redoc, path)
			if got := string(out[ns:ne]); got == string(f.src[valStart:valEnd]) && !bytes.Equal(out, f.src) {
				t.Errorf("%s: Set(%s): value span unchanged (%q)", f.name, path, got)
			}
		}
	}
}

// TestRewriterPropertyRemoveIsContiguous checks the Remove side of the
// property: removing an entry deletes exactly one contiguous region of
// the original source and leaves both sides byte-identical.
func TestRewriterPropertyRemoveIsContiguous(t *testing.T) {
	for _, f := range rewriteCorpus(t) {
		doc, err := pxf.Parse(f.src)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		for _, path := range leafPaths(doc) {
			r, err := pxf.NewRewriter(f.src)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Remove(path); err != nil {
				t.Fatalf("%s: Remove(%s): %v", f.name, path, err)
			}
			out, err := r.Bytes()
			if err != nil {
				// Removing this entry can legitimately make the document
				// invalid only if it never was; our corpus is valid.
				t.Fatalf("%s: Remove(%s): Bytes: %v", f.name, path, err)
			}
			if len(out) >= len(f.src) {
				t.Fatalf("%s: Remove(%s) did not shrink the document", f.name, path)
			}
			// out must be src with one contiguous cut: find the split.
			i := 0
			for i < len(out) && out[i] == f.src[i] {
				i++
			}
			tail := len(out) - i
			if tail > 0 && !bytes.Equal(out[i:], f.src[len(f.src)-tail:]) {
				t.Errorf("%s: Remove(%s) is not a single contiguous deletion", f.name, path)
			}
		}
	}
}
