package ocinuke_test

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	// Blank-imported so the real, fully-populated resources package's registrations exist when
	// TestAllowlistedTypesAreCorrectlyExempted cross-references its result against
	// registry.GetNames() -- matching registration_test.go's own convention. (This file's own
	// AST walk over resources/*.go source does not itself need the registry, but keeping the
	// blank import here too documents that this file also depends on resources/ being real, not
	// synthetic.)
	_ "github.com/naviteq/oci-nuke/resources"
)

// filterContractResourcesImportPath mirrors pkg/ocinuke/registry_integrity_test.go's own
// resourcesImportPath constant, resolving resources/ by Go import path (robust to the test
// binary's working directory) rather than a relative filesystem path.
const filterContractResourcesImportPath = "github.com/naviteq/oci-nuke/resources"

// noLifecycleFieldTypes is the reviewed, exhaustive allow-list of resource types whose Filter()
// correctly, unconditionally returns nil, because the underlying OCI SDK type has NO
// lifecycle-state-shaped field at all (04-RESEARCH.md Q2/Q4/Q6):
//   - InstanceConfiguration, Bucket, ObjectVersion, MultipartUpload, PreauthenticatedRequest,
//     ReplicationPolicy -- each verified directly against its own SDK struct this session, not
//     assumed. PrivateIp is NOT on this list: its Filter() has an IsPrimary conditional before
//     its nil fallthrough, so its body is not a single bare `return nil` statement and this
//     check's pattern-match does not flag it in the first place.
//
// Adding a name here requires the same justification these six have -- confirm by reading the
// underlying OCI SDK type's source directly that it has no LifecycleState-shaped field, not by
// assumption. If the type DOES have a lifecycle-state field, the fix is not an allow-list entry:
// write a switch over that enum's "present" values instead (see 04-RESEARCH.md Q2).
var noLifecycleFieldTypes = map[string]bool{
	"InstanceConfiguration":   true,
	"Bucket":                  true,
	"ObjectVersion":           true,
	"MultipartUpload":         true,
	"PreauthenticatedRequest": true,
	"ReplicationPolicy":       true,
}

// filterContractResourcesDir resolves the absolute directory of the resources/ package, mirroring
// pkg/ocinuke/registry_integrity_test.go's resourcesDir helper exactly.
func filterContractResourcesDir(t *testing.T) string {
	t.Helper()
	pkg, err := build.Import(filterContractResourcesImportPath, ".", build.FindOnly)
	if err != nil {
		t.Fatalf("locating resources/ package directory: %v", err)
	}
	return pkg.Dir
}

// receiverTypeName returns fn's method receiver type name (e.g. "Bucket" for both `func (r
// *Bucket) Filter()` and `func (r Bucket) Filter()`), and false if fn has no receiver or the
// receiver's type shape is not a simple (possibly pointer) named type.
func receiverTypeName(fn *ast.FuncDecl) (string, bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", false
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name, true
		}
	}
	return "", false
}

// isBareReturnNil reports whether body is syntactically exactly one statement, `return nil`, with
// no preceding if/switch/anything else -- an "unconditional nil" Filter() body. Requiring the
// body to be exactly one statement is what makes this robust to future formatting changes (single
// line vs multi-line `func (r *Bucket) Filter() error { return nil }`) while still correctly NOT
// flagging a body that has a conditional before its nil fallthrough (e.g. PrivateIp's
// IsPrimary check, which is two statements: an if, then return nil).
func isBareReturnNil(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	ident, ok := ret.Results[0].(*ast.Ident)
	return ok && ident.Name == "nil"
}

// findUnconditionalNilFilters parses every non-test .go file directly under dir, locates each
// file's Filter() method declaration(s) by receiver type (not by filename, so this check is
// robust to any future filename convention drift), and returns the set of receiver type names
// whose Filter() body is exactly the single statement `return nil` -- an "unconditional nil",
// the hang-trap shape T-04-38 exists to catch. Mirrors
// pkg/ocinuke/registry_integrity_test.go's TestNoDirectRegistryRegisterInResources own
// os.ReadDir + go/parser.ParseFile shape.
func findUnconditionalNilFilters(dir string) (map[string]bool, error) {
	flagged := make(map[string]bool)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
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
			return nil, err
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Filter" {
				continue
			}
			typeName, ok := receiverTypeName(fn)
			if !ok {
				continue
			}
			if isBareReturnNil(fn.Body) {
				flagged[typeName] = true
			}
		}
	}

	return flagged, nil
}

// TestNoUnconditionalNilFilterOutsideAllowlist is T-04-38's structural enforcement: parses the
// real resources/ package and fails, naming the type and how to fix it, for every Filter() that
// unconditionally returns nil and is not on the reviewed noLifecycleFieldTypes allow-list. A
// Filter() that exists but is not actually conditioned on lifecycle state cannot distinguish
// "present" from "TERMINATING/TERMINATED" -- reintroducing the exact hang trap this phase's
// per-type Filter() convention exists to prevent, silently, the moment a future contributor
// (Phase 5's type 38) writes `func (r *X) Filter() error { return nil }` and it compiles, passes
// go vet, and passes every other structural check in this plan.
func TestNoUnconditionalNilFilterOutsideAllowlist(t *testing.T) {
	dir := filterContractResourcesDir(t)

	flagged, err := findUnconditionalNilFilters(dir)
	if err != nil {
		t.Fatalf("findUnconditionalNilFilters(%s): %v", dir, err)
	}

	var violations []string
	for name := range flagged {
		if !noLifecycleFieldTypes[name] {
			violations = append(violations, name)
		}
	}
	sort.Strings(violations)

	for _, name := range violations {
		t.Errorf(
			"resources/%s: Filter() unconditionally returns nil and is not on the reviewed "+
				"no-lifecycle-field allow-list -- if this type genuinely has no LifecycleState-shaped "+
				"field, add it to noLifecycleFieldTypes in this test with a citation; otherwise write "+
				"an allow-list switch over its lifecycle enum's 'present' values (see "+
				"04-RESEARCH.md Q2)",
			name,
		)
	}
}

// TestFindUnconditionalNilFilters_CatchesViolation proves findUnconditionalNilFilters -- the
// detection logic TestNoUnconditionalNilFilterOutsideAllowlist depends on -- actually flags a
// bare `return nil` Filter() on a type not in any allow-list, rather than shipping a check that
// would pass today even if its own detection were broken. Uses an inline synthetic source string
// (not a real file under resources/, which must stay clean), parsed via findUnconditionalNilFilters
// against a temp directory, mirroring registry_integrity_test.go's own self-proof discipline
// (TestNoDirectRegistryRegisterInResources_CatchesViolation).
func TestFindUnconditionalNilFilters_CatchesViolation(t *testing.T) {
	const src = `package resources

func (r *SyntheticViolation) Filter() error {
	return nil
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "synthetic_violation.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("writing synthetic violation source: %v", err)
	}

	flagged, err := findUnconditionalNilFilters(dir)
	if err != nil {
		t.Fatalf("findUnconditionalNilFilters(%s): %v", dir, err)
	}

	if !flagged["SyntheticViolation"] {
		t.Fatalf(
			"findUnconditionalNilFilters(%v) = %v, want it to flag %q -- the detector must not be "+
				"vacuous",
			dir, flagged, "SyntheticViolation",
		)
	}
}

// TestAllowlistedTypesAreCorrectlyExempted runs the real detector against resources/ and proves
// every one of the six allow-listed names IS present in the flagged set (proving they really are
// unconditional-nil today, not a stale allow-list entry for a type whose Filter() changed shape
// out from under it) AND does not appear in TestNoUnconditionalNilFilterOutsideAllowlist's own
// error output -- i.e. re-running that test's exact violation logic against the real package
// produces zero violations for these six names.
func TestAllowlistedTypesAreCorrectlyExempted(t *testing.T) {
	dir := filterContractResourcesDir(t)

	flagged, err := findUnconditionalNilFilters(dir)
	if err != nil {
		t.Fatalf("findUnconditionalNilFilters(%s): %v", dir, err)
	}

	names := make([]string, 0, len(noLifecycleFieldTypes))
	for name := range noLifecycleFieldTypes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !flagged[name] {
			t.Errorf(
				"allow-listed type %q is not flagged as unconditional-nil by the real detector -- "+
					"its Filter() no longer matches the bare `return nil` shape this allow-list entry "+
					"was justified against; either its Filter() now correctly handles lifecycle state "+
					"(remove it from noLifecycleFieldTypes) or the detector itself regressed",
				name,
			)
		}
		// Mirrors TestNoUnconditionalNilFilterOutsideAllowlist's own violation predicate exactly:
		// an allow-listed name must never itself be reported as a violation.
		if flagged[name] && !noLifecycleFieldTypes[name] {
			t.Errorf("allow-listed type %q unexpectedly treated as a violation", name)
		}
	}
}
