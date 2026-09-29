package ocinuke

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// resourcesImportPath identifies the resources/ package by its Go import path rather than a
// relative filesystem path, so this test does not depend on the test binary's working directory
// (go test always runs a test binary with cwd set to the package under test, but resolving via
// go/build.Import is still the more robust choice and matches how the Go toolchain itself locates
// packages).
const resourcesImportPath = "github.com/naviteq/oci-nuke/resources"

// registryImportPath is the libnuke package whose Register function resources/*.go must never
// call directly.
const registryImportPath = "github.com/ekristen/libnuke/pkg/registry"

// resourcesDir resolves the absolute directory of the resources/ package.
func resourcesDir(t *testing.T) string {
	t.Helper()
	pkg, err := build.Import(resourcesImportPath, ".", build.FindOnly)
	if err != nil {
		t.Fatalf("locating resources/ package directory: %v", err)
	}
	return pkg.Dir
}

// registryRegisterCallPositions walks f's AST and returns the position of every call expression
// that invokes Register on whatever local identifier this file uses for
// github.com/ekristen/libnuke/pkg/registry (resolved from the file's own import spec, the same
// way pkg/config/schema_test.go's TestNoOCISDKImport resolves import paths -- so this correctly
// follows an aliased import, e.g. `reg "github.com/ekristen/libnuke/pkg/registry"`).
func registryRegisterCallPositions(f *ast.File) []token.Pos {
	var registryAlias string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != registryImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			registryAlias = "registry" // package's own declared name, no alias in this file
		case imp.Name.Name != "_" && imp.Name.Name != ".":
			registryAlias = imp.Name.Name
		}
	}
	if registryAlias == "" {
		return nil // this file doesn't import registry at all -- nothing to check
	}

	var positions []token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Register" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == registryAlias {
			positions = append(positions, call.Pos())
		}
		return true
	})
	return positions
}

// TestNoDirectRegistryRegisterInResources is the structural enforcement for 02-CONTEXT.md's
// locked requirement 5 ("it must not be possible to disable [per-resource scope
// re-verification] -- no flag, no config key, no build tag"). It parses every non-test .go file
// under resources/ and fails if any calls registry.Register directly: resources/*.go files may
// legitimately import github.com/ekristen/libnuke/pkg/registry for its Scope/Registration types
// while still being required to reach registry.Register only through ocinuke.Register, which is
// what actually wraps the Lister in a scopedLister. Mirrors pkg/config/schema_test.go's
// TestNoOCISDKImport go/parser-based pattern.
//
// Passes trivially today: resources/ contains only doc.go, which imports nothing. It starts
// failing the moment Phase 4 adds a resources/*.go file that calls registry.Register directly.
func TestNoDirectRegistryRegisterInResources(t *testing.T) {
	dir := resourcesDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		for _, pos := range registryRegisterCallPositions(f) {
			t.Errorf(
				"%s: calls registry.Register directly -- resources/*.go must call ocinuke.Register "+
					"instead (locked requirement 5, 02-CONTEXT.md: per-resource scope re-verification "+
					"must not be possible to disable)",
				fset.Position(pos),
			)
		}
	}
}

// TestNoDirectRegistryRegisterInResources_CatchesViolation proves
// registryRegisterCallPositions -- the detection logic TestNoDirectRegistryRegisterInResources
// depends on -- actually flags a direct registry.Register call, rather than shipping a check that
// would pass today even if its own detection were broken. Uses an inline synthetic source string
// (not a real file under resources/, which must stay clean) parsed the same way the real test
// parses resources/*.go.
func TestNoDirectRegistryRegisterInResources_CatchesViolation(t *testing.T) {
	const src = `package resources

import "github.com/ekristen/libnuke/pkg/registry"

func init() {
	registry.Register(&registry.Registration{Name: "Evil"})
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic_violation.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing synthetic violation source: %v", err)
	}

	positions := registryRegisterCallPositions(f)
	if len(positions) != 1 {
		t.Fatalf(
			"registryRegisterCallPositions found %d violation(s) in a synthetic file with exactly "+
				"one direct registry.Register call, want 1 -- the detector must not be vacuous",
			len(positions),
		)
	}
}
