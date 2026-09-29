package ocinuke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isSkipResourceCheck does not exist anywhere on the pinned oci-go-sdk/v65@v65.123.0 as of this
// session (06-RESEARCH.md Contradiction 4): the whole module tree was grepped for
// "isskipresourcecheck" (case-insensitive) and returned zero matches, anywhere --
// identity.DeleteCompartmentRequest has exactly three fields (CompartmentId, IfMatch,
// OpcRequestId), no emptiness-bypass flag among them. This file is therefore a TRIPWIRE against a
// FUTURE SDK bump reintroducing the parameter (Oracle's own public API docs do reference an
// isSkipResourceCheck query parameter on the DeleteCompartment REST operation in some SDK/CLI
// surfaces, even though the pinned Go SDK does not expose it as a struct field) -- it is not
// closing a currently-live risk. A future reader should not mistake a passing
// TestNoIsSkipResourceCheckInResources for evidence that a real bypass was ever removed from this
// codebase; none was ever present.

// containsIsSkipResourceCheck reports whether src contains the literal identifier
// "isSkipResourceCheck" anywhere, case-insensitively. A bare substring check, not an AST walk, is
// deliberate: the target is a single unconditional identifier that could appear anywhere in the
// source text (a struct field name, a query-parameter string, a comment referencing it) -- 06-
// CONTEXT.md's own wording is "a test fails the build if the identifier appears anywhere in the
// source tree", not "if it appears as a resolved Go identifier."
func containsIsSkipResourceCheck(src []byte) bool {
	return strings.Contains(strings.ToLower(string(src)), "isskipresourcecheck")
}

// TestNoIsSkipResourceCheckInResources scans every .go file under resources/ -- production AND
// _test.go files, since the tripwire is about the whole tree, not just production code -- and
// fails naming the file if the identifier ever appears. resourcesDir is reused directly from
// registry_integrity_test.go (same package, no redeclaration).
func TestNoIsSkipResourceCheckInResources(t *testing.T) {
	dir := resourcesDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		if containsIsSkipResourceCheck(src) {
			t.Errorf(
				"%s: contains isSkipResourceCheck -- this identifier does not exist on the pinned "+
					"oci-go-sdk/v65@v65.123.0 (06-RESEARCH.md Contradiction 4) and must never be "+
					"introduced; a future SDK bump reintroducing it must not be reached for under "+
					"retry pressure",
				path,
			)
		}
	}
}

// TestContainsIsSkipResourceCheck_CatchesViolation proves containsIsSkipResourceCheck -- the
// detection logic TestNoIsSkipResourceCheckInResources depends on -- actually flags a realistic
// violation and does not flag an unrelated snippet, mirroring
// TestNoDirectRegistryRegisterInResources_CatchesViolation's non-vacuousness discipline
// (registry_integrity_test.go, same package).
func TestContainsIsSkipResourceCheck_CatchesViolation(t *testing.T) {
	const violation = `package resources

func deleteCompartmentRequest(id string) identity.DeleteCompartmentRequest {
	return identity.DeleteCompartmentRequest{
		CompartmentId:       &id,
		isSkipResourceCheck: common.Bool(true),
	}
}
`
	if !containsIsSkipResourceCheck([]byte(violation)) {
		t.Fatal("containsIsSkipResourceCheck returned false for a source string containing " +
			"isSkipResourceCheck in a realistic struct-literal context -- the detector must not be vacuous")
	}

	const clean = `package resources

func deleteCompartmentRequest(id string) identity.DeleteCompartmentRequest {
	return identity.DeleteCompartmentRequest{
		CompartmentId: &id,
		IfMatch:       nil,
	}
}
`
	if containsIsSkipResourceCheck([]byte(clean)) {
		t.Fatal("containsIsSkipResourceCheck returned true for an unrelated Go source snippet of " +
			"similar length -- the detector must not false-positive on ordinary request-builder code")
	}
}

// TestNoIsSkipResourceCheckAnywhereInTree widens TestNoIsSkipResourceCheckInResources from
// resources/ to the whole repository. The narrower test covers where the DeleteCompartment call
// actually lives today, but 06-CONTEXT.md's wording is "anywhere in the source tree" for a reason:
// a bypass could just as easily be built in pkg/clients (a request-mutating wrapper) or in
// pkg/commands/run (a flag that threads the parameter down), and neither is under resources/.
//
// This file itself necessarily contains the identifier, so it is skipped by name; so is .planning/,
// where the research and context documents discuss the parameter on purpose.
func TestNoIsSkipResourceCheckAnywhereInTree(t *testing.T) {
	root := repoRootForTripwire(t)
	const selfName = "skip_resource_check_test.go"

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".planning", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || d.Name() == selfName {
			return nil
		}

		src, readErr := os.ReadFile(path) //nolint:gosec // path comes from WalkDir over the repo
		if readErr != nil {
			return readErr
		}
		if containsIsSkipResourceCheck(src) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			t.Errorf(
				"%s: contains isSkipResourceCheck -- this identifier does not exist on the pinned "+
					"oci-go-sdk/v65@v65.123.0 and must never be introduced anywhere in the tree; "+
					"it bypasses OCI's server-side compartment-emptiness check, which is the safety "+
					"net Phase 6's delete-last guarantee depends on",
				rel,
			)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repository from %s: %v", root, err)
	}
}

// repoRootForTripwire resolves the repository root from the resources/ package directory, reusing
// resourcesDir's build.Import lookup rather than assuming a relative path from the test's cwd.
func repoRootForTripwire(t *testing.T) string {
	t.Helper()
	return filepath.Dir(resourcesDir(t))
}
