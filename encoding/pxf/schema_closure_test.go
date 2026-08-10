// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Bind-time checks cover the import closure of the bound descriptor, not
// the file declaring it (#71; draft -01 §schema-constraints, "Scope of
// Bind-Time Checks"). The tests here cover the shapes the closure adds:
// depth, diamonds, cross-file ordering, and the per-file memo.

// compileClosure compiles srcs and returns the message named msg,
// resolved by fully-qualified name.
func compileClosure(t testing.TB, root string, srcs map[string]string, msg string) protoreflect.MessageDescriptor {
	t.Helper()
	full := make(map[string]string, len(srcs)+1)
	for k, v := range srcs {
		full[k] = v
	}
	full["pxf/annotations.proto"] = annotationsProtoSrc
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(full)},
		),
	}
	files, err := comp.Compile(context.Background(), root)
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName(msg))
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// The reserved-name check has been file-scoped since it landed, well
// before (pxf.default) existed — #71 widens it too, and this is the case
// its "scope is wider than (pxf.default)" section named.
func TestValidateDescriptor_ReservedNameInImportedFile(t *testing.T) {
	desc := compileClosure(t, "root.proto", map[string]string{
		"leaf.proto": `
syntax = "proto3";
package closure_reserved_test.leaf;
message Inner {
  string null = 1;
}
enum Side {
  SIDE_UNSPECIFIED = 0;
  false = 1;
}
`,
		"root.proto": `
syntax = "proto3";
package closure_reserved_test.root;
import "leaf.proto";
message Root {
  closure_reserved_test.leaf.Inner inner = 1;
}
`,
	}, "closure_reserved_test.root.Root")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 2)
	for _, v := range vs {
		assert.Equal(t, "leaf.proto", v.File)
	}
	assert.Equal(t, pxf.ViolationField, vs[0].Kind)
	assert.Equal(t, "closure_reserved_test.leaf.Inner.null", vs[0].Element)
	// Enum values are scoped to the enum's parent, not to the enum, so
	// this is "…leaf.false" and not "…leaf.Side.false".
	assert.Equal(t, pxf.ViolationEnumValue, vs[1].Kind)
	assert.Equal(t, "closure_reserved_test.leaf.false", vs[1].Element)

	// The enum is not referenced by any field of Root. The closure is the
	// import closure, not the set of types a document can reach.
	require.Nil(t, desc.Fields().ByName("side"))
}

// Depth: the violation is three imports down, and no field of Root is
// typed by the file that declares it.
func TestValidateDescriptor_TransitiveImportDepth(t *testing.T) {
	desc := compileClosure(t, "a.proto", map[string]string{
		"c.proto": `
syntax = "proto3";
package closure_depth_test.c;
import "pxf/annotations.proto";
message Leaf {
  map<string, string> labels = 1 [(pxf.default) = "nope"];
}
`,
		"b.proto": `
syntax = "proto3";
package closure_depth_test.b;
import "c.proto";
message Mid { string note = 1; }
`,
		"a.proto": `
syntax = "proto3";
package closure_depth_test.a;
import "b.proto";
message Root { closure_depth_test.b.Mid mid = 1; }
`,
	}, "closure_depth_test.a.Root")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 1)
	assert.Equal(t, pxf.ViolationDefaultOption, vs[0].Kind)
	assert.Equal(t, "c.proto", vs[0].File)
	assert.Equal(t, "closure_depth_test.c.Leaf.labels", vs[0].Element)
	assert.Contains(t, vs[0].Detail, "not valid on map fields")
}

// A diamond reaches the offending file twice. It is checked once.
func TestValidateDescriptor_DiamondReportsOnce(t *testing.T) {
	desc := compileClosure(t, "top.proto", map[string]string{
		"shared.proto": `
syntax = "proto3";
package closure_diamond_test.shared;
import "pxf/annotations.proto";
message Shared {
  repeated string tags = 1 [(pxf.default) = "dup"];
}
`,
		"left.proto": `
syntax = "proto3";
package closure_diamond_test.left;
import "shared.proto";
message Left { closure_diamond_test.shared.Shared s = 1; }
`,
		"right.proto": `
syntax = "proto3";
package closure_diamond_test.right;
import "shared.proto";
message Right { closure_diamond_test.shared.Shared s = 1; }
`,
		"top.proto": `
syntax = "proto3";
package closure_diamond_test.top;
import "left.proto";
import "right.proto";
message Top {
  closure_diamond_test.left.Left  l = 1;
  closure_diamond_test.right.Right r = 2;
}
`,
	}, "closure_diamond_test.top.Top")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 1, "shared.proto is reached through both arms and must be reported once: %v", vs)
	assert.Equal(t, "shared.proto", vs[0].File)
}

// Violations from several files sort by declaring file first, then by
// element, so one file's violations stay together in the error text.
func TestValidateDescriptor_SortedByFileThenElement(t *testing.T) {
	desc := compileClosure(t, "zz_root.proto", map[string]string{
		"mm_mid.proto": `
syntax = "proto3";
package closure_sort_test.mid;
message Mid {
  string null = 1;
  string true = 2;
}
`,
		"zz_root.proto": `
syntax = "proto3";
package closure_sort_test.root;
import "mm_mid.proto";
message Root {
  closure_sort_test.mid.Mid mid = 1;
  string false = 2;
}
`,
	}, "closure_sort_test.root.Root")

	vs := pxf.ValidateDescriptor(desc)
	require.Len(t, vs, 3)
	got := make([]string, len(vs))
	for i, v := range vs {
		got[i] = fmt.Sprintf("%s|%s", v.File, v.Element)
	}
	assert.Equal(t, []string{
		"mm_mid.proto|closure_sort_test.mid.Mid.null",
		"mm_mid.proto|closure_sort_test.mid.Mid.true",
		"zz_root.proto|closure_sort_test.root.Root.false",
	}, got, "mm_mid.proto sorts before zz_root.proto even though the root file was the one bound")
}

// A conformant closure is still conformant: importing files with no
// violations reports nothing, and in particular pxf/annotations.proto
// and google/protobuf/descriptor.proto — which every annotated schema
// drags in — are clean.
func TestValidateDescriptor_ConformantClosureIsClean(t *testing.T) {
	desc := compileClosure(t, "ok.proto", map[string]string{
		"ok.proto": `
syntax = "proto3";
package closure_clean_test;
import "pxf/annotations.proto";
import "google/protobuf/timestamp.proto";
message Order {
  string venue = 1 [(pxf.default) = "XNAS"];
  google.protobuf.Timestamp ts = 2;
}
`,
	}, "closure_clean_test.Order")

	assert.Empty(t, pxf.ValidateDescriptor(desc))

	// The closure really does include descriptor.proto — otherwise the
	// assertion above proves nothing about walking it.
	var paths []string
	imps := desc.ParentFile().Imports()
	for i := range imps.Len() {
		fd := imps.Get(i).FileDescriptor
		paths = append(paths, fd.Path())
		inner := fd.Imports()
		for j := range inner.Len() {
			paths = append(paths, inner.Get(j).FileDescriptor.Path())
		}
	}
	assert.Contains(t, paths, "google/protobuf/descriptor.proto")
}

// The per-file memo is keyed by descriptor identity, not by path. Two
// independently-compiled descriptor sets can hold different files at the
// same path, and must not see each other's results.
func TestValidateFile_MemoIsPerDescriptorNotPerPath(t *testing.T) {
	const dirty = `
syntax = "proto3";
package closure_memo_test.a;
import "pxf/annotations.proto";
message M { repeated string tags = 1 [(pxf.default) = "x"]; }
`
	const clean = `
syntax = "proto3";
package closure_memo_test.b;
message M { repeated string tags = 1; }
`
	// Same path, different content, different compilations.
	dirtyDesc := compileClosure(t, "same.proto", map[string]string{"same.proto": dirty}, "closure_memo_test.a.M")
	cleanDesc := compileClosure(t, "same.proto", map[string]string{"same.proto": clean}, "closure_memo_test.b.M")
	require.Equal(t, dirtyDesc.ParentFile().Path(), cleanDesc.ParentFile().Path())

	require.Len(t, pxf.ValidateDescriptor(dirtyDesc), 1)
	assert.Empty(t, pxf.ValidateDescriptor(cleanDesc),
		"the clean file must not inherit the dirty file's cached result")
	// And back again, now that both are cached.
	require.Len(t, pxf.ValidateDescriptor(dirtyDesc), 1)
	assert.Empty(t, pxf.ValidateDescriptor(cleanDesc))
}

// Repeated calls return equal results, and the caller may modify what it
// gets back without corrupting the memo for the next caller.
func TestValidateFile_ResultIsCallerOwned(t *testing.T) {
	desc := compileClosure(t, "own.proto", map[string]string{
		"own.proto": `
syntax = "proto3";
package closure_own_test;
message M {
  string null = 1;
  string true = 2;
}
`,
	}, "closure_own_test.M")

	first := pxf.ValidateDescriptor(desc)
	require.Len(t, first, 2)
	want := append([]pxf.Violation(nil), first...)

	// Scribble on it the way a caller filtering or re-sorting would.
	first[0], first[1] = first[1], first[0]
	first[0].Element = "clobbered"

	assert.Equal(t, want, pxf.ValidateDescriptor(desc))
}

// The memo is shared across goroutines; decoding concurrently is the
// normal case for a server. Run with -race.
func TestValidateFile_ConcurrentUse(t *testing.T) {
	desc := compileClosure(t, "conc.proto", map[string]string{
		"leaf.proto": `
syntax = "proto3";
package closure_conc_test.leaf;
import "pxf/annotations.proto";
message Leaf { repeated string tags = 1 [(pxf.default) = "c"]; }
`,
		"conc.proto": `
syntax = "proto3";
package closure_conc_test;
import "leaf.proto";
message Root { closure_conc_test.leaf.Leaf leaf = 1; }
`,
	}, "closure_conc_test.Root")

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 16 {
				vs := pxf.ValidateFile(desc.ParentFile())
				if len(vs) != 1 || vs[0].File != "leaf.proto" {
					t.Errorf("got %v", vs)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkValidateClosure(b *testing.B) {
	desc := compileClosure(b, "bench.proto", map[string]string{
		"bench.proto": `
syntax = "proto3";
package closure_bench_test;
import "pxf/annotations.proto";
import "google/protobuf/timestamp.proto";
message Order {
  string id = 1;
  string venue = 2 [(pxf.default) = "XNAS"];
  google.protobuf.Timestamp ts = 3;
  repeated Leg legs = 4 [(pxf.key) = "sym"];
  Nested nested = 5;
}
message Leg { string sym = 1; int64 qty = 2; }
message Nested { string a = 1; int32 b = 2; bool c = 3; }
`,
	}, "closure_bench_test.Order")

	b.ReportAllocs()
	for b.Loop() {
		if vs := pxf.ValidateDescriptor(desc); len(vs) != 0 {
			b.Fatal(vs)
		}
	}
}
