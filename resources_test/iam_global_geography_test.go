package ocinuke_test

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	// Blank-imported so the real, fully-populated resources package's registrations exist,
	// matching every other AST-walk test in this package's own convention (e.g.
	// filter_contract_test.go) even though this file's own walk over resources/*.go source does
	// not itself need the registry.
	_ "github.com/naviteq/oci-nuke/resources"
)

// This file is the structural enforcement 05-CONTEXT.md's IAM decision requires: "Policies,
// dynamic groups, tag namespaces and tag defaults are all Global. Every one of their List()
// implementations opens with opts.BeforeList(ocinuke.Global). A test fails the build if any type
// in this group is registered as Regional." IAM is the first production consumer of
// pkg/ocinuke.ListerOpts.BeforeList(ocinuke.Global) since it was scaffolded in Phase 3 with zero
// real callers -- a missing or wrong Global guard is this wave's headline correctness risk
// (05-05-PLAN.md threat T-05-05-02): every subscribed region's scanner would redundantly
// re-discover, and could concurrently race to delete, the same home-region-only resource.

// iamGlobalGeographyResourcesImportPath mirrors filter_contract_test.go's own
// filterContractResourcesImportPath constant, resolving resources/ by Go import path (robust to
// the test binary's working directory) rather than a relative filesystem path.
const iamGlobalGeographyResourcesImportPath = "github.com/naviteq/oci-nuke/resources"

// iamGlobalGeographyFiles is the reviewed, exhaustive set of files this test enforces
// BeforeList(ocinuke.Global) against -- Policy, DynamicGroup, TagNamespace, TagDefault
// (05-05-PLAN.md), plus Compartment (06-REVIEW.md CR-01): a compartment is a tenancy-wide
// Identity object exactly like the other four, and buildNukes registers one scanner per
// subscribed region onto the SAME *libnuke.Nuke for a compartment, so a Regional-geography
// regression here reintroduces the same "one BeforeList(ocinuke.Global) away from
// once-per-region duplicate DeleteCompartment calls against a single OCID" defect CR-01
// found. Adding a further Global-geography type to a future plan requires adding its filename
// here too.
var iamGlobalGeographyFiles = []string{
	"policy.go",
	"dynamic_group.go",
	"tag_namespace.go",
	"tag_default.go",
	"compartment.go",
}

// iamGlobalGeographyResourcesDir resolves the absolute directory of the resources/ package,
// mirroring pkg/ocinuke/registry_integrity_test.go's resourcesDir helper exactly.
func iamGlobalGeographyResourcesDir(t *testing.T) string {
	t.Helper()
	pkg, err := build.Import(iamGlobalGeographyResourcesImportPath, ".", build.FindOnly)
	if err != nil {
		t.Fatalf("locating resources/ package directory: %v", err)
	}
	return pkg.Dir
}

// findListFuncDecl returns f's List method declaration, matched by function name (not receiver
// type name) since every Wave-5 lister type (policyLister, dynamicGroupLister,
// tagNamespaceLister, tagDefaultLister) is unexported and this check must be robust to whatever
// the receiver happens to be named. Returns nil if no such method exists in f.
func findListFuncDecl(f *ast.File) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "List" {
			continue
		}
		return fn
	}
	return nil
}

// findBeforeListCall walks fn's body and returns the first `<x>.BeforeList(...)` *ast.CallExpr it
// finds -- the identifier <x> is deliberately not constrained (every real List() derives it from
// the opts type-assertion, e.g. `o, ok := opts.(*ocinuke.ListerOpts)`, but this walk does not
// depend on that variable being named `o`). Returns nil if no such call exists anywhere in fn's
// body.
func findBeforeListCall(fn *ast.FuncDecl) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "BeforeList" {
			return true
		}
		found = call
		return false
	})
	return found
}

// findFirstClientsCallPos walks fn's body and returns the source position of the first `<x>.
// Clients.<Method>(...)` call it finds (an *ast.CallExpr whose Fun is a selector, and whose
// selector's own X is itself a selector ending in `.Clients`) -- the position every real Wave-5
// List() constructs its client at, e.g. `o.Clients.Identity(o.Region)`. Returns token.NoPos if no
// such call exists.
func findFirstClientsCallPos(fn *ast.FuncDecl) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if pos != token.NoPos {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		innerSel, ok := sel.X.(*ast.SelectorExpr)
		if !ok || innerSel.Sel.Name != "Clients" {
			return true
		}
		pos = call.Pos()
		return false
	})
	return pos
}

// checkBeforeListGlobal parses the .go file at path and returns a non-nil, descriptive error if
// its List() method does not call BeforeList(ocinuke.Global) as the first statement before any
// Clients.* client construction. This is the detection logic
// TestIAMTypesRegisteredAsGlobal_NotRegional depends on, factored out so its own non-vacuousness
// can be proven against synthetic fixtures below (TestCheckBeforeListGlobal_Catches*), mirroring
// filter_contract_test.go's TestFindUnconditionalNilFilters_CatchesViolation self-proof
// discipline.
func checkBeforeListGlobal(path string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	fn := findListFuncDecl(f)
	if fn == nil {
		return fmt.Errorf("%s: no List method found", filepath.Base(path))
	}

	call := findBeforeListCall(fn)
	if call == nil {
		return fmt.Errorf(
			"%s: List() never calls BeforeList(...) -- every IAM type in this wave must call BeforeList(ocinuke.Global) as the first statement",
			filepath.Base(path),
		)
	}
	if len(call.Args) != 1 {
		return fmt.Errorf("%s: BeforeList(...) called with %d arguments, want exactly 1", filepath.Base(path), len(call.Args))
	}
	arg, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok {
		return fmt.Errorf("%s: BeforeList(...) argument is not a package-qualified selector expression", filepath.Base(path))
	}
	pkgIdent, ok := arg.X.(*ast.Ident)
	if !ok || pkgIdent.Name != "ocinuke" || arg.Sel.Name != "Global" {
		argPkg := "?"
		if ok {
			argPkg = pkgIdent.Name
		}
		return fmt.Errorf(
			"%s: List() calls BeforeList(%s.%s) -- every IAM type in this wave must be registered "+
				"as ocinuke.Global, never ocinuke.Regional or anything else (05-CONTEXT.md)",
			filepath.Base(path), argPkg, arg.Sel.Name,
		)
	}

	if clientsPos := findFirstClientsCallPos(fn); clientsPos != token.NoPos && clientsPos < call.Pos() {
		return fmt.Errorf(
			"%s: a Clients.* client-construction call appears BEFORE BeforeList(ocinuke.Global) in "+
				"source order -- the geography guard must run first, before any client is ever constructed",
			filepath.Base(path),
		)
	}

	return nil
}

// TestIAMTypesRegisteredAsGlobal_NotRegional is 05-CONTEXT.md's structural enforcement, quoted in
// full above this file's own package doc comment: parses each of the four real IAM resource
// files, locates its List() method's BeforeList(...) call, and fails by name -- naming the exact
// file and what it found -- for any file where the call is missing, has the wrong argument (in
// particular ocinuke.Regional), or runs after a client has already been constructed.
func TestIAMTypesRegisteredAsGlobal_NotRegional(t *testing.T) {
	dir := iamGlobalGeographyResourcesDir(t)

	for _, name := range iamGlobalGeographyFiles {
		if err := checkBeforeListGlobal(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
}

// writeSyntheticListerFile writes a minimal, syntactically valid Go source file to dir containing
// exactly one List method, whose body is bodySrc -- used by every TestCheckBeforeListGlobal_Catches*
// test below to construct a synthetic violation without ever touching a real resources/*.go file
// (which must stay clean).
func writeSyntheticListerFile(t *testing.T, dir, bodySrc string) string {
	t.Helper()
	src := "package resources\n\n" +
		"type syntheticLister struct{}\n\n" +
		"func (l *syntheticLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {\n" +
		bodySrc +
		"\n}\n"
	path := filepath.Join(dir, "synthetic_lister.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing synthetic lister source: %v", err)
	}
	return path
}

// TestCheckBeforeListGlobal_CatchesRegionalArgument proves checkBeforeListGlobal actually flags a
// List() that calls BeforeList(ocinuke.Regional) instead of ocinuke.Global -- the exact inversion
// this test exists to catch, not merely a hypothetical.
func TestCheckBeforeListGlobal_CatchesRegionalArgument(t *testing.T) {
	dir := t.TempDir()
	path := writeSyntheticListerFile(t, dir, `	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}
	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, err
	}
	_ = client
	return nil, nil`)

	if err := checkBeforeListGlobal(path); err == nil {
		t.Fatal("checkBeforeListGlobal() = nil, want a non-nil error for a Regional-geography List()")
	}
}

// TestCheckBeforeListGlobal_CatchesMissingCall proves checkBeforeListGlobal flags a List() that
// never calls BeforeList at all.
func TestCheckBeforeListGlobal_CatchesMissingCall(t *testing.T) {
	dir := t.TempDir()
	path := writeSyntheticListerFile(t, dir, `	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, err
	}
	_ = client
	return nil, nil`)

	if err := checkBeforeListGlobal(path); err == nil {
		t.Fatal("checkBeforeListGlobal() = nil, want a non-nil error for a List() that never calls BeforeList")
	}
}

// TestCheckBeforeListGlobal_CatchesClientConstructedFirst proves checkBeforeListGlobal flags a
// List() where the Clients.* call appears BEFORE BeforeList(ocinuke.Global) in source order --
// the ordering check, independent of the presence/argument check above.
func TestCheckBeforeListGlobal_CatchesClientConstructedFirst(t *testing.T) {
	dir := t.TempDir()
	path := writeSyntheticListerFile(t, dir, `	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, err
	}
	_ = client
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	return nil, nil`)

	if err := checkBeforeListGlobal(path); err == nil {
		t.Fatal("checkBeforeListGlobal() = nil, want a non-nil error when a Clients.* call precedes BeforeList(ocinuke.Global)")
	}
}

// TestCheckBeforeListGlobal_AcceptsCorrectShape is the non-vacuous control case: a List() shaped
// exactly like every real IAM file in this plan -- BeforeList(ocinuke.Global) first, Clients.*
// second -- must produce a nil error.
func TestCheckBeforeListGlobal_AcceptsCorrectShape(t *testing.T) {
	dir := t.TempDir()
	path := writeSyntheticListerFile(t, dir, `	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, err
	}
	_ = client
	return nil, nil`)

	if err := checkBeforeListGlobal(path); err != nil {
		t.Fatalf("checkBeforeListGlobal() = %v, want nil for the correct BeforeList(ocinuke.Global)-first shape", err)
	}
}
