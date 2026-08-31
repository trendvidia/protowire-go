// Copyright 2026 TrendVidia LLC
// SPDX-License-Identifier: MIT

package deps_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// protoCompilers are the Go .proto compilers, by module path. A
// package that reaches one of these has stopped reading descriptors
// and started parsing schemas.
var protoCompilers = []string{
	"github.com/bufbuild/protocompile",
	"github.com/trendvidia/protocompile",
	"github.com/jhump/protoreflect",
}

// toolPrefix is the sole exemption. The commands under scripts/ compile
// fixture .proto files — that is their job — and they are package main,
// so no consumer's build reaches them.
const toolPrefix = "github.com/trendvidia/protowire-go/scripts/"

// publishedPackages is the library surface the guard must cover. It is
// spelled out rather than inferred so that moving or renaming a package
// fails this test instead of quietly dropping it from the check.
var publishedPackages = []string{
	"github.com/trendvidia/protowire-go/check",
	"github.com/trendvidia/protowire-go/encoding/pb",
	"github.com/trendvidia/protowire-go/encoding/pxf",
	"github.com/trendvidia/protowire-go/encoding/sbe",
	"github.com/trendvidia/protowire-go/envelope",
}

// TestLibraryPackagesHaveNoProtoCompiler pins the assumption behind
// carrying a .proto compiler as a direct require: it is reachable from
// the tests and from scripts/, and from nothing a consumer builds. See
// the package doc and issue #80.
func TestLibraryPackagesHaveNoProtoCompiler(t *testing.T) {
	var violations []string
	covered := make(map[string]bool)

	for _, pkg := range listPackages(t) {
		if strings.HasPrefix(pkg.ImportPath, toolPrefix) {
			continue
		}
		covered[pkg.ImportPath] = true

		// One line per compiler, not per dependency: importing a
		// compiler's entry package drags a dozen of its subpackages
		// into the closure, and listing them all buries the finding.
		for _, compiler := range protoCompilers {
			if dep, ok := firstDepFrom(pkg.Deps, compiler); ok {
				violations = append(violations,
					fmt.Sprintf("%s reaches %s (via %s)", pkg.ImportPath, compiler, dep))
			}
		}
	}

	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("library packages must build without a .proto compiler:\n  %s\n\n"+
			"The binder reads annotations off descriptors and parses no schema source. "+
			"Move the code that needs a compiler under %s or into a _test.go file. "+
			"See issue #80.",
			strings.Join(violations, "\n  "), toolPrefix)
	}

	for _, pkg := range publishedPackages {
		require.True(t, covered[pkg],
			"published package %s was not checked — did it move or get renamed? "+
				"Update publishedPackages so the guard keeps covering the library surface.",
			pkg)
	}
}

// firstDepFrom returns the first entry of deps belonging to the module
// at compiler, reporting whether one was found.
func firstDepFrom(deps []string, compiler string) (string, bool) {
	for _, dep := range deps {
		if dep == compiler || strings.HasPrefix(dep, compiler+"/") {
			return dep, true
		}
	}
	return "", false
}

// listedPackage is the subset of `go list -json` output the guard uses.
// Deps is the full transitive closure of a package's non-test imports,
// which is exactly the graph a consumer's build walks.
type listedPackage struct {
	ImportPath string
	Deps       []string
}

func listPackages(t *testing.T) []listedPackage {
	t.Helper()

	root := moduleRoot(t)
	cmd := exec.Command(goTool(t), "list", "-json", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "go list -json ./... in %s failed: %s", root, stderr.String())

	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg listedPackage
		if err := dec.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err, "decoding go list output")
		}
		pkgs = append(pkgs, pkg)
	}
	require.NotEmpty(t, pkgs, "go list reported no packages in %s", root)
	return pkgs
}

// goTool locates the go command, preferring GOROOT so the guard
// measures the same toolchain that is running the test.
func goTool(t *testing.T) string {
	t.Helper()

	if goroot := build.Default.GOROOT; goroot != "" {
		exe := filepath.Join(goroot, "bin", "go")
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	path, err := exec.LookPath("go")
	require.NoError(t, err, "no go command found in GOROOT or PATH")
	return path
}

// moduleRoot walks up from the test's working directory to the
// directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod found above the test's directory")
		dir = parent
	}
}

// TestFirstDepFrom pins the module-path matching, including the
// boundary the guard would otherwise get wrong in both directions: a
// module whose path merely starts with a compiler's path is a
// different module, and a compiler's subpackages are not.
func TestFirstDepFrom(t *testing.T) {
	tests := []struct {
		name     string
		deps     []string
		compiler string
		want     string
		wantOK   bool
	}{
		{
			name:     "exact module path",
			deps:     []string{"fmt", "github.com/bufbuild/protocompile"},
			compiler: "github.com/bufbuild/protocompile",
			want:     "github.com/bufbuild/protocompile",
			wantOK:   true,
		},
		{
			name:     "subpackage only",
			deps:     []string{"fmt", "github.com/bufbuild/protocompile/parser"},
			compiler: "github.com/bufbuild/protocompile",
			want:     "github.com/bufbuild/protocompile/parser",
			wantOK:   true,
		},
		{
			name:     "the fork counts too",
			deps:     []string{"github.com/trendvidia/protocompile/linker"},
			compiler: "github.com/trendvidia/protocompile",
			want:     "github.com/trendvidia/protocompile/linker",
			wantOK:   true,
		},
		{
			name:     "shared prefix is a different module",
			deps:     []string{"github.com/bufbuild/protocompile-extras"},
			compiler: "github.com/bufbuild/protocompile",
			wantOK:   false,
		},
		{
			name:     "unrelated deps",
			deps:     []string{"fmt", "google.golang.org/protobuf/reflect/protoreflect"},
			compiler: "github.com/bufbuild/protocompile",
			wantOK:   false,
		},
		{
			name:     "no deps",
			compiler: "github.com/bufbuild/protocompile",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := firstDepFrom(tt.deps, tt.compiler)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestToolPrefixIsPathBounded pins that the scripts/ exemption cannot
// be claimed by a sibling package whose name merely starts with the
// same letters.
func TestToolPrefixIsPathBounded(t *testing.T) {
	assert.True(t, strings.HasPrefix("github.com/trendvidia/protowire-go/scripts/check_decode", toolPrefix))
	assert.False(t, strings.HasPrefix("github.com/trendvidia/protowire-go/scriptsy", toolPrefix))
}
