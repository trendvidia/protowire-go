// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Surface-form coverage for the keyed-collection editors (issue #55):
// concatenated (split) bindings and the anonymous list form.

import (
	"strings"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// --- Split (concatenated) bindings ----------------------------------

// splitSrc binds `children` twice; §3.13 concatenates the blocks in
// document order.
const splitSrc = `children {
  a {
    type = "L"
  }
  b {
    type = "H"
  }
}
children {
  c {
    type = "V"
  }
}
`

func TestSplitBindingFindsElementInSecondBinding(t *testing.T) {
	got := rewrite(t, splitSrc, func(r *pxf.Rewriter) error {
		return r.SetKeyed("children", "c", "type", &pxf.StringVal{Value: "Grid"})
	})
	if !strings.Contains(got, "c {\n    type = \"Grid\"\n  }") {
		t.Fatalf("element in second binding not edited:\n%s", got)
	}
}

func TestSplitBindingRemoveFromSecondBinding(t *testing.T) {
	got := rewrite(t, splitSrc, func(r *pxf.Rewriter) error {
		return r.RemoveKeyedElement("children", "c")
	})
	if strings.Contains(got, "c {") {
		t.Fatalf("element c not removed:\n%s", got)
	}
	// The now-empty second binding stays (removal is per-element).
	if !strings.Contains(got, "a {") || !strings.Contains(got, "b {") {
		t.Fatalf("first binding damaged:\n%s", got)
	}
}

func TestSplitBindingRenameAcrossBindings(t *testing.T) {
	got := rewrite(t, splitSrc, func(r *pxf.Rewriter) error {
		return r.RenameKeyedElement("children", "c", "z")
	})
	if !strings.Contains(got, "z {\n    type = \"V\"\n  }") {
		t.Fatalf("rename in second binding failed:\n%s", got)
	}
}

func TestSplitBindingDuplicateDetectionSpansBindings(t *testing.T) {
	r, err := pxf.NewRewriter([]byte(splitSrc))
	if err != nil {
		t.Fatal(err)
	}
	// `a` lives in the first binding; inserting it (targeting the second)
	// must still be rejected.
	if err := r.InsertKeyedElement("children", "a", "", nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want cross-binding duplicate error, got %v", err)
	}
}

func TestSplitBindingAppendLandsInLastBinding(t *testing.T) {
	got := rewrite(t, splitSrc, func(r *pxf.Rewriter) error {
		return r.InsertKeyedElement("children", "d", "", []pxf.Entry{
			&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "X"}},
		})
	})
	// `d` must land in the second (last) binding — the end of list order —
	// not the first.
	firstClose := strings.Index(got, "}\nchildren")
	dAt := strings.Index(got, "d {")
	if dAt < 0 || dAt < firstClose {
		t.Fatalf("append did not land in the last binding:\n%s", got)
	}
}

func TestSplitBindingInsertBeforeAnchorInItsBinding(t *testing.T) {
	got := rewrite(t, splitSrc, func(r *pxf.Rewriter) error {
		return r.InsertKeyedElement("children", "a2", "b", []pxf.Entry{
			&pxf.Assignment{Key: "type", Value: &pxf.StringVal{Value: "X"}},
		})
	})
	a2 := strings.Index(got, "a2 {")
	b := strings.Index(got, "b {")
	a := strings.Index(got, "a {")
	if !(a < a2 && a2 < b) {
		t.Fatalf("a2 not inserted between a and b in the first binding:\n%s", got)
	}
}

func TestSplitBindingMoveAcrossBindings(t *testing.T) {
	got := rewrite(t, splitSrc, func(r *pxf.Rewriter) error {
		// Move `c` (second binding) before `a` (first binding).
		return r.MoveKeyedElement("children", "c", "a")
	})
	c := strings.Index(got, "c {")
	a := strings.Index(got, "a {")
	if c < 0 || a < 0 || c > a {
		t.Fatalf("c not moved before a across bindings:\n%s", got)
	}
	if _, err := pxf.Parse([]byte(got)); err != nil {
		t.Fatalf("result does not reparse: %v\n%s", err, got)
	}
}

// --- Anonymous list form --------------------------------------------

const anonSrc = `service = "edge"
regions = [
  {
    name = "us-east-1"
    replicas = 3
  },
  {
    name = "eu-west-2"
    replicas = 1
  }
]
`

func TestAnonRequiresKeyFieldDeclaration(t *testing.T) {
	r, err := pxf.NewRewriter([]byte(anonSrc))
	if err != nil {
		t.Fatal(err)
	}
	// Without a declaration the anonymous form is not addressable.
	if err := r.SetKeyed("regions", "us-east-1", "replicas", &pxf.IntVal{Raw: "5"}); err == nil || !strings.Contains(err.Error(), "KeyedByField") {
		t.Fatalf("want KeyedByField guidance, got %v", err)
	}
}

func TestAnonSetKeyedEditsInPlace(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.SetKeyed("regions", "us-east-1", "replicas", &pxf.IntVal{Raw: "5"})
	})
	want := strings.Replace(anonSrc, "replicas = 3", "replicas = 5", 1)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonSetKeyedInsertsMissingField(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.SetKeyed("regions", "eu-west-2", "tier", &pxf.StringVal{Value: "edge"})
	})
	if !strings.Contains(got, "name = \"eu-west-2\"\n    replicas = 1\n    tier = \"edge\"\n  }") {
		t.Fatalf("field not inserted into anonymous element:\n%s", got)
	}
}

func TestAnonRemoveKeyedField(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.RemoveKeyed("regions", "us-east-1", "replicas")
	})
	if strings.Contains(got, "replicas = 3") {
		t.Fatalf("replicas not removed from us-east-1:\n%s", got)
	}
	if !strings.Contains(got, "replicas = 1") {
		t.Fatalf("wrong element edited:\n%s", got)
	}
}

func TestAnonRenameRewritesKeyFieldValue(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.RenameKeyedElement("regions", "us-east-1", "us-east-2")
	})
	want := strings.Replace(anonSrc, `name = "us-east-1"`, `name = "us-east-2"`, 1)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonRemoveElementFirst(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.RemoveKeyedElement("regions", "us-east-1")
	})
	want := `service = "edge"
regions = [
  {
    name = "eu-west-2"
    replicas = 1
  }
]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonRemoveElementLast(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.RemoveKeyedElement("regions", "eu-west-2")
	})
	want := `service = "edge"
regions = [
  {
    name = "us-east-1"
    replicas = 3
  }
]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonInsertElementAppend(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.InsertKeyedElement("regions", "ap-south-1", "", []pxf.Entry{
			&pxf.Assignment{Key: "replicas", Value: &pxf.IntVal{Raw: "2"}},
		})
	})
	want := `service = "edge"
regions = [
  {
    name = "us-east-1"
    replicas = 3
  },
  {
    name = "eu-west-2"
    replicas = 1
  },
  {
    name = "ap-south-1"
    replicas = 2
  }
]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonInsertElementBeforeAnchor(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.InsertKeyedElement("regions", "ca-central-1", "eu-west-2", []pxf.Entry{
			&pxf.Assignment{Key: "replicas", Value: &pxf.IntVal{Raw: "4"}},
		})
	})
	ca := strings.Index(got, `name = "ca-central-1"`)
	eu := strings.Index(got, `name = "eu-west-2"`)
	us := strings.Index(got, `name = "us-east-1"`)
	if !(us < ca && ca < eu) {
		t.Fatalf("ca-central-1 not inserted between us-east-1 and eu-west-2:\n%s", got)
	}
	if _, err := pxf.Parse([]byte(got)); err != nil {
		t.Fatalf("result does not reparse: %v\n%s", err, got)
	}
}

func TestAnonMoveElement(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.MoveKeyedElement("regions", "eu-west-2", "us-east-1")
	})
	want := `service = "edge"
regions = [
  {
    name = "eu-west-2"
    replicas = 1
  },
  {
    name = "us-east-1"
    replicas = 3
  }
]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonMoveElementToEnd(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.MoveKeyedElement("regions", "us-east-1", "")
	})
	want := `service = "edge"
regions = [
  {
    name = "eu-west-2"
    replicas = 1
  },
  {
    name = "us-east-1"
    replicas = 3
  }
]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnonMoveNoOpBeforeSuccessor(t *testing.T) {
	got := rewrite(t, anonSrc, func(r *pxf.Rewriter) error {
		r.KeyedByField("regions", "name")
		return r.MoveKeyedElement("regions", "us-east-1", "eu-west-2")
	})
	if got != anonSrc {
		t.Fatalf("no-op move changed the document:\n%s", got)
	}
}

func TestAnonCrossFormMoveIsError(t *testing.T) {
	// A collection bound once as a block and once anonymously.
	src := `children {
  a { type = "L" }
}
children = [
  {
    id = "b"
  }
]
`
	r, err := pxf.NewRewriter([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	r.KeyedByField("children", "id")
	if err := r.MoveKeyedElement("children", "a", "b"); err == nil || !strings.Contains(err.Error(), "different surface forms") {
		t.Fatalf("want cross-form move error, got %v", err)
	}
	// But lookup/edit across the mixed forms works.
	if err := r.SetKeyed("children", "b", "type", &pxf.StringVal{Value: "H"}); err != nil {
		t.Fatalf("edit in anonymous binding failed: %v", err)
	}
	out, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `type = "H"`) {
		t.Fatalf("anonymous-binding edit not applied:\n%s", out)
	}
}

func TestKeyedByFieldChains(t *testing.T) {
	r, err := pxf.NewRewriter([]byte(anonSrc))
	if err != nil {
		t.Fatal(err)
	}
	if r.KeyedByField("regions", "name") != r {
		t.Fatal("KeyedByField should return the Rewriter for chaining")
	}
}
