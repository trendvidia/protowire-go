// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package pxf

// Result captures field-level presence metadata from PXF decoding.
// It tracks which fields were explicitly set, which were set to null,
// and which were absent from the input.
// Fields are identified by dotted paths (e.g., "name", "nested.value").
type Result struct {
	nullFields    map[string]struct{}
	presentFields map[string]struct{}
}

func newResult() *Result {
	return &Result{
		nullFields:    make(map[string]struct{}),
		presentFields: make(map[string]struct{}),
	}
}

func (r *Result) markNull(path string) {
	r.nullFields[path] = struct{}{}
	r.presentFields[path] = struct{}{}
}

func (r *Result) markPresent(path string) {
	r.presentFields[path] = struct{}{}
}

// IsNull reports whether the field at the given path was explicitly set to null.
// Use dotted paths for nested fields: "nested.value".
func (r *Result) IsNull(path string) bool {
	_, ok := r.nullFields[path]
	return ok
}

// IsAbsent reports whether the field at the given path was not mentioned in the input.
func (r *Result) IsAbsent(path string) bool {
	_, ok := r.presentFields[path]
	return !ok
}

// IsSet reports whether the field at the given path was set to a concrete (non-null) value.
func (r *Result) IsSet(path string) bool {
	_, inPresent := r.presentFields[path]
	_, inNull := r.nullFields[path]
	return inPresent && !inNull
}

// NullFields returns the paths of all fields explicitly set to null.
func (r *Result) NullFields() []string {
	paths := make([]string, 0, len(r.nullFields))
	for p := range r.nullFields {
		paths = append(paths, p)
	}
	return paths
}

// PresentFields returns the paths of all fields encountered during
// parsing — both fields set to a concrete value and fields set to null.
// Equivalent to the union of [Result.IsSet] and [Result.IsNull] paths.
//
// Useful for layered-config systems (chameleon) that union per-layer
// presence into a merged-result presence set, then run defaults /
// required-validation against that union.
func (r *Result) PresentFields() []string {
	paths := make([]string, 0, len(r.presentFields))
	for p := range r.presentFields {
		paths = append(paths, p)
	}
	return paths
}
