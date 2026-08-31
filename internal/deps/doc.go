// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

// Package deps holds protowire-go's dependency-shape invariants. It
// contains no code — only tests asserting properties of the module's
// import graph that no ordinary unit test would notice.
//
// # The library builds without a .proto compiler
//
// PXF annotations are ordinary protobuf. The binder reads three
// extension numbers off a descriptor (encoding/pxf/annotations.go) and
// never parses .proto source, so nothing on the published surface —
// check, encoding/pb, encoding/pxf, encoding/sbe, envelope — needs a
// schema compiler at build or run time. RFC-001 §8.1 makes the same
// claim for the ecosystem, that PXF needs no vendor toolchain, and this
// module's build closure is where that claim is either true or not.
//
// The property has a visible cost. github.com/trendvidia/protocompile
// is a direct require in go.mod, imported by the test suite and by the
// two scripts/ commands that compile fixture .proto files. It therefore
// reaches every consumer's go.sum and no consumer's binary, and the
// fork is not a light dependency: it carries the RFC-001 schema
// extensions and pulls ten further modules with it. That asymmetry is
// what makes the dependency tolerable, and it holds only while the
// library stays compiler-free.
//
// TestLibraryPackagesHaveNoProtoCompiler is what keeps it so rather
// than re-measuring it by hand. Hand-measurement is genuinely fragile
// here: a grep for "protocompile" across the tree matches
// encoding/pxf/annotations.go and encoding/sbe/annotations.go, where
// the word appears only in comments about how protocompile resolves
// extensions.
//
// The guard forbids upstream bufbuild/protocompile and jhump/protoreflect
// alongside the fork. Which compiler the tests use is a decision that has
// changed once already (issue #80) and may change again; that a library
// package pulls in none of them should not have to be re-litigated when
// it does.
package deps
