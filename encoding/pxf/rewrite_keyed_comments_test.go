// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

// Comments around the elements of an anonymous keyed list (#221): an
// edit takes an element's own trailing comment or none, never a neighbour's.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func keyedEdit(t *testing.T, src string, edit func(r *pxf.Rewriter) error) string {
	t.Helper()
	return rewrite(t, src, func(r *pxf.Rewriter) error {
		r.KeyedByField("f", "k")
		return edit(r)
	})
}

func TestAnonRemoveKeepsCommentsOfOtherElements(t *testing.T) {
	rm := func(key string) func(*pxf.Rewriter) error {
		return func(r *pxf.Rewriter) error { return r.RemoveKeyedElement("f", key) }
	}
	for _, tc := range []struct {
		name, src, key, want string
	}{
		{
			"the issue: first element",
			"f = [\n  { k = \"a\" }, # after a\n  // second\n  { k = \"b\" }\n]\n", "a",
			"f = [\n  // second\n  { k = \"b\" }\n]\n",
		},
		{
			"last element keeps the previous element's comment",
			"f = [\n  { k = \"a\" }, # after a\n  { k = \"b\" } # after b\n]\n", "b",
			"f = [\n  { k = \"a\" } # after a\n]\n",
		},
		{
			"last element leaves the comment between",
			"f = [\n  { k = \"a\" }, # after a\n  // second\n  { k = \"b\" }\n]\n", "b",
			"f = [\n  { k = \"a\" } # after a\n  // second\n]\n",
		},
		{
			"middle element",
			"f = [\n  { k = \"a\" }, # after a\n  // b\n  { k = \"b\" }, # after b\n  // c\n  { k = \"c\" }\n]\n", "b",
			"f = [\n  { k = \"a\" }, # after a\n  // b\n  // c\n  { k = \"c\" }\n]\n",
		},
		{
			"a trailing comma stays",
			"f = [\n  { k = \"a\" },\n  { k = \"b\" },\n]\n", "b",
			"f = [\n  { k = \"a\" },\n]\n",
		},
		{
			"sole element with a comma and a comment",
			"f = [\n  { k = \"a\" }, # after a\n]\n", "a",
			"f = [\n]\n",
		},
		{
			"block comment between inline elements",
			"f = [ { k = \"a\" }, /* b */ { k = \"b\" } ]\n", "b",
			"f = [ { k = \"a\" } /* b */ ]\n",
		},
		{
			"a comma in a comment is not the separator",
			"f = [\n  { k = \"a\" } # x, y\n  , { k = \"b\" }\n]\n", "a",
			"f = [\n  { k = \"b\" }\n]\n",
		},
		{
			"an element sharing its line keeps the next one's indent",
			"f = [\n  { k = \"a\" }, { k = \"b\" }\n]\n", "a",
			"f = [\n  { k = \"b\" }\n]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, keyedEdit(t, tc.src, rm(tc.key)))
		})
	}
}

func TestAnonInsertKeepsComments(t *testing.T) {
	body := []pxf.Entry{}
	for _, tc := range []struct {
		name, src, before, want string
	}{
		{
			"the issue: trailing comma",
			"f = [\n  { k = \"a\" }, # after a\n]\n", "",
			"f = [\n  { k = \"a\" }, # after a\n  {\n    k = \"b\"\n  },\n]\n",
		},
		{
			"no trailing comma, a comment",
			"f = [\n  { k = \"a\" } # after a\n]\n", "",
			"f = [\n  { k = \"a\" }, # after a\n  {\n    k = \"b\"\n  }\n]\n",
		},
		{
			"block comment",
			"f = [\n  { k = \"a\" } /* after a */\n]\n", "",
			"f = [\n  { k = \"a\" }, /* after a */\n  {\n    k = \"b\"\n  }\n]\n",
		},
		{
			"no comment, as before",
			"f = [\n  { k = \"a\" }\n]\n", "",
			"f = [\n  { k = \"a\" },\n  {\n    k = \"b\"\n  }\n]\n",
		},
		{
			"inline with a trailing comma",
			"f = [ { k = \"a\" } /* a */, ]\n", "",
			"f = [ { k = \"a\" } /* a */, {\n    k = \"b\"\n  }, ]\n",
		},
		{
			"before an anchor, above its comment",
			"f = [\n  { k = \"a\" }, # after a\n  // second\n  { k = \"c\" }\n]\n", "c",
			"f = [\n  { k = \"a\" }, # after a\n  {\n    k = \"b\"\n  },\n  // second\n  { k = \"c\" }\n]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := keyedEdit(t, tc.src, func(r *pxf.Rewriter) error {
				return r.InsertKeyedElement("f", "b", tc.before, body)
			})
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAnonMoveKeepsComments(t *testing.T) {
	const src = "f = [\n  { k = \"a\" }, # after a\n  // second\n  { k = \"b\" }, # after b\n  { k = \"c\" } # after c\n]\n"
	for _, tc := range []struct {
		name, key, before, want string
	}{
		{"to the end", "a", "",
			"f = [\n  // second\n  { k = \"b\" }, # after b\n  { k = \"c\" }, # after c\n  { k = \"a\" } # after a\n]\n"},
		{"last to first", "c", "a",
			"f = [\n  { k = \"c\" }, # after c\n  { k = \"a\" }, # after a\n  // second\n  { k = \"b\" } # after b\n]\n"},
		{"before its successor", "a", "b", src},
		{"last to the end", "c", "", src},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := keyedEdit(t, src, func(r *pxf.Rewriter) error {
				return r.MoveKeyedElement("f", tc.key, tc.before)
			})
			assert.Equal(t, tc.want, got)
		})
	}

	// A line comment cannot follow an element into an inline list.
	t.Run("into an inline position it stays behind", func(t *testing.T) {
		const src = "f = [\n  { k = \"a\" }, { k = \"b\" },\n  { k = \"c\" } # after c\n]\n"
		got := keyedEdit(t, src, func(r *pxf.Rewriter) error {
			return r.MoveKeyedElement("f", "c", "b")
		})
		assert.Contains(t, got, "# after c")
		_, err := pxf.Parse([]byte(got))
		require.NoError(t, err)
		assert.Equal(t, 3, strings.Count(got, "k ="))
	})
}

// The anchor of a move may sit in another binding of the field, and its
// index counts in that binding's list.
func TestAnonMoveToAnchorInAnotherBinding(t *testing.T) {
	const src = "f = [\n  { k = \"a\" },\n  { k = \"b\" },\n  { k = \"c\" }\n]\nf = [\n  { k = \"d\" }\n]\n"
	got := keyedEdit(t, src, func(r *pxf.Rewriter) error {
		return r.MoveKeyedElement("f", "a", "d")
	})
	assert.Equal(t, "f = [\n  { k = \"b\" },\n  { k = \"c\" }\n]\nf = [\n  { k = \"a\" },\n  { k = \"d\" }\n]\n", got)
}

// With the shorter list holding the element, that index is past its end.
func TestAnonMoveToAnchorPastTheElementsList(t *testing.T) {
	const src = "f = [\n  { k = \"a\" }\n]\nf = [\n  { k = \"c\" },\n  { k = \"d\" }\n]\n"
	got := keyedEdit(t, src, func(r *pxf.Rewriter) error {
		return r.MoveKeyedElement("f", "a", "d")
	})
	assert.Equal(t, "f = [\n]\nf = [\n  { k = \"c\" },\n  { k = \"a\" },\n  { k = \"d\" }\n]\n", got)
}

// Every edit of every layout: the output parses, each surviving element
// keeps its own comment on its own line, and a comment between elements
// is never lost. The removed element's trailing comment is the only one
// that may go.
func TestAnonCommentsSurviveEveryEdit(t *testing.T) {
	elem := func(k string, comma bool, trail string) string {
		s := fmt.Sprintf("  { k = %q }", k)
		if comma {
			s += ","
		}
		if trail != "" {
			s += " " + trail + " after " + k
			if trail == "/*" {
				s += " */"
			}
		}
		return s + "\n"
	}
	keys := []string{"a", "b", "c"}
	var checked int
	for _, trail := range []string{"", "#", "//", "/*"} {
		for _, between := range []bool{false, true} {
			for _, trailingComma := range []bool{false, true} {
				var sb strings.Builder
				sb.WriteString("f = [\n")
				for i, k := range keys {
					if between {
						sb.WriteString("  // before " + k + "\n")
					}
					sb.WriteString(elem(k, i < len(keys)-1 || trailingComma, trail))
				}
				sb.WriteString("]\n")
				src := sb.String()

				check := func(name string, gone string, edit func(r *pxf.Rewriter) error) {
					t.Helper()
					got := keyedEdit(t, src, edit)
					_, err := pxf.Parse([]byte(got))
					require.NoError(t, err, "%s of\n%s", name, src)
					checked++
					for _, k := range keys {
						if between {
							assert.Contains(t, got, "// before "+k+"\n", "%s of\n%s\ngave\n%s", name, src, got)
						}
						if trail == "" || k == gone {
							continue
						}
						ok := false
						for line := range strings.SplitSeq(got, "\n") {
							if strings.Contains(line, "after "+k) {
								ok = strings.Contains(line, fmt.Sprintf("k = %q", k))
							}
						}
						assert.True(t, ok, "comment on %s moved or lost in %s of\n%s\ngave\n%s", k, name, src, got)
					}
				}
				for _, k := range keys {
					check("remove "+k, k, func(r *pxf.Rewriter) error { return r.RemoveKeyedElement("f", k) })
					check("insert before "+k, "", func(r *pxf.Rewriter) error {
						return r.InsertKeyedElement("f", "new", k, nil)
					})
					check("move to end "+k, "", func(r *pxf.Rewriter) error { return r.MoveKeyedElement("f", k, "") })
					for _, to := range keys {
						if to != k {
							check("move "+k+" before "+to, "", func(r *pxf.Rewriter) error { return r.MoveKeyedElement("f", k, to) })
						}
					}
				}
				check("append", "", func(r *pxf.Rewriter) error { return r.InsertKeyedElement("f", "new", "", nil) })
			}
		}
	}
	assert.Equal(t, 4*2*2*(3*5+1), checked)
}

func TestSetRefusesNestedBadVal(t *testing.T) {
	block := func(e pxf.Entry) *pxf.BlockVal { return &pxf.BlockVal{Entries: []pxf.Entry{e}} }
	bad := &pxf.Assignment{Key: "x", Value: &pxf.BadVal{}}
	for name, v := range map[string]pxf.Value{
		"assignment":          block(bad),
		"block":               block(&pxf.Block{Name: "b", Entries: []pxf.Entry{bad}}),
		"block in a block":    block(&pxf.Block{Name: "b", Entries: []pxf.Entry{&pxf.Block{Name: "c", Entries: []pxf.Entry{bad}}}}),
		"map entry in a list": &pxf.ListVal{Elements: []pxf.Value{block(&pxf.MapEntry{Key: "m", Value: block(&pxf.Block{Name: "b", Entries: []pxf.Entry{bad}})})}},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := pxf.NewRewriter([]byte("a = 1\n"))
			require.NoError(t, err)
			require.ErrorContains(t, r.Set("a", v), "cannot write a BadVal placeholder")
			require.ErrorContains(t, r.ReplaceValue(r.Document().Entries[0].(*pxf.Assignment).Value, v), "cannot write a BadVal placeholder")
		})
	}
}
