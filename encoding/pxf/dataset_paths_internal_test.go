// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

// BindRow checks the header on every row and builds every row's body,
// so neither may cost a row more than it must: a header that passes
// allocates nothing, and a row of plain columns is written without the
// tree dotted columns need.
func TestDatasetRowCosts(t *testing.T) {
	desc := (&descriptorpb.FileDescriptorProto{}).ProtoReflect().Descriptor()
	cols := []string{"name", "options.java_package", "options.go_package"}
	if got := testing.AllocsPerRun(100, func() {
		if msg := checkColumnPaths(desc, cols); msg != "" {
			t.Fatal(msg)
		}
	}); got != 0 {
		t.Errorf("checkColumnPaths on a passing header: %v allocations, want 0", got)
	}

	row := DatasetRow{Cells: []Value{&StringVal{Value: "a"}, nil, &IntVal{Raw: "1"}}}
	plain := []string{"name", "package", "syntax"}
	// The body's buffer, its spans and the quoted string.
	if got := testing.AllocsPerRun(100, func() {
		if _, _, err := rowToPXFBody(plain, row); err != nil {
			t.Fatal(err)
		}
	}); got > 3 {
		t.Errorf("rowToPXFBody on plain columns: %v allocations, want at most 3", got)
	}
}
