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
	"regexp"
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

// validateProtosModule is the BSR-generated Go code for the buf.validate
// protos. protowire-go imports none of it; protocompile requires it, and
// this module's go.mod records that requirement as a floor every
// consumer inherits.
const validateProtosModule = "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go"

// minValidateProtos is the proto commit time, not the version, of the
// oldest build the floor may name: the one buf.build/go/protovalidate
// v1.2.0 requires. Older builds lack FieldPath, Rule and the other types
// that release compiles against.
const minValidateProtos = "20260415201107"

// TestValidateProtosFloorIsNotStale pins issue #179. A BSR pseudo-version
// leads with the protobuf-go version it was generated with, so the 2024
// build at v1.36.12 sorts above the 2026 build at v1.36.11, and minimal
// version selection hands a consumer of protovalidate v1.2.0 the older
// protos. The comparison therefore reads the proto timestamp inside the
// version rather than trusting version order.
func TestValidateProtosFloorIsNotStale(t *testing.T) {
	version, ok := selectedVersion(t, validateProtosModule)
	if !ok {
		t.Skipf("%s is no longer in the module graph, so this module sets no floor on it", validateProtosModule)
	}

	stamp, ok := bsrTimestamp(version)
	require.Truef(t, ok, "%s@%s is not a BSR pseudo-version; the floor check cannot read its proto timestamp", validateProtosModule, version)
	assert.GreaterOrEqualf(t, stamp, minValidateProtos,
		"%s@%s carries buf.validate protos from %s, older than the %s build protovalidate v1.2.0 needs. "+
			"Consumers inherit this floor, and version order prefers it over newer protos at a lower protobuf-go version. "+
			"Raise it with go get. See issue #179.",
		validateProtosModule, version, stamp, minValidateProtos)
}

// selectedVersion reports the version of module that this module's own
// build list selects, outside any go.work, which is the graph a
// consumer inherits.
func selectedVersion(t *testing.T, module string) (string, bool) {
	t.Helper()

	cmd := exec.Command(goTool(t), "list", "-m", "-f", "{{.Path}} {{.Version}}", "all")
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "go list -m all failed: %s", stderr.String())

	for line := range strings.Lines(string(out)) {
		path, version, _ := strings.Cut(strings.TrimSpace(line), " ")
		if path == module {
			return version, true
		}
	}
	return "", false
}

// bsrPseudoVersion matches a BSR generated-SDK version:
// v<plugin version>-<proto commit time>-<commit>.<revision>.
var bsrPseudoVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-([0-9]{14})-[0-9a-f]{12}\.[0-9]+$`)

// bsrTimestamp returns the proto commit time inside a BSR version,
// reporting whether version has that shape.
func bsrTimestamp(version string) (string, bool) {
	m := bsrPseudoVersion.FindStringSubmatch(version)
	if m == nil {
		return "", false
	}
	return m[1], true
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

// TestBSRTimestamp pins the version parsing the floor check relies on,
// including the ordering trap from issue #179: the later proto build
// carries the lower plugin version.
func TestBSRTimestamp(t *testing.T) {
	tests := []struct {
		version string
		want    string
		wantOK  bool
	}{
		{version: "v1.36.12-20240920164238-5a7b106cbb87.1", want: "20240920164238", wantOK: true},
		{version: "v1.36.11-20260415201107-50325440f8f2.1", want: "20260415201107", wantOK: true},
		{version: "v1.36.12-20260825204119-511051f7f437.2", want: "20260825204119", wantOK: true},
		{version: "v1.36.12", wantOK: false},
		{version: "v0.0.0-20260113132338-7c7de50cc741", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, ok := bsrTimestamp(tt.version)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}

	older, _ := bsrTimestamp("v1.36.12-20240920164238-5a7b106cbb87.1")
	newer, _ := bsrTimestamp("v1.36.11-20260415201107-50325440f8f2.1")
	assert.Less(t, older, newer, "proto timestamps must order by time even where versions order the other way")
}
