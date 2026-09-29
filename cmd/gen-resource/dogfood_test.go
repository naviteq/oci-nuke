package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// natGatewayDogfoodInput starts from natGatewayInput() (the exact base flags Plan 04-03's own
// golden-file test already uses: --name/--sdk-package/--sdk-type/--client-accessor/--list-*/
// --delete-*/--lifecycle-field) and extends it with the two facts the golden test's narrower
// scope never needed: the real core.NatGatewayLifecycleStateEnum has TWO present states
// (PROVISIONING and AVAILABLE, both verified directly against core/nat_gateway.go by Plan
// 04-06 -- not just AVAILABLE, which is all the golden test's own self-referential input ever
// exercised), and the two extra Properties() entries (vcn_id, block_traffic) Plan 04-06 added
// beyond baseProperties. This is this plan's Task 3 in code: the golden-file test alone never
// caught either gap, because it only ever diffs the generator against its own output.
func natGatewayDogfoodInput() templateDataInput {
	in := natGatewayInput()
	in.LifecyclePresent = []string{"Provisioning", lifecyclePresentAvailable}
	in.ExtraProperty = []string{"vcn_id=VcnId", "block_traffic=BlockTraffic"}
	return in
}

// TestGeneratedNatGateway_MatchesLandedFile is T-04-39's mitigation and this plan's Task 3: the
// generator's ONLY end-to-end fidelity check against a real, reviewed, landed file this phase
// (every other domain plan, 04-04 through 04-10, hand-authored its types from 04-RESEARCH.md
// Q1's prose template directly, never invoking cmd/gen-resource itself -- see this plan's own
// objective). Rendering with natGatewayDogfoodInput() and diffing byte-for-byte against the real,
// checked-in resources/nat_gateway.go (NOT testdata/nat_gateway.go.golden, which only proves the
// generator agrees with its own prior output) must produce an EMPTY diff -- any divergence found
// while writing this test was reconciled as part of this same task (see 04-12-SUMMARY.md's
// Decisions section for which direction each reconciliation went), not left in place with a
// loosened assertion.
func TestGeneratedNatGateway_MatchesLandedFile(t *testing.T) {
	in := natGatewayDogfoodInput()
	data, err := newTemplateData(&in)
	if err != nil {
		t.Fatalf("newTemplateData() error = %v", err)
	}

	got, err := renderAndFormat("resource", &data)
	if err != nil {
		t.Fatalf("renderAndFormat(resource) error = %v", err)
	}

	// resources/nat_gateway.go is resolved relative to this package's own directory
	// (cmd/gen-resource), two levels up to the module root, then into resources/ -- mirroring
	// this project's other tests that need to read a real, checked-in file (e.g.
	// TestRenderAndFormat_CompilesCleanly's own resources/support.go read a few lines below in
	// this same package).
	wantPath := filepath.Join("..", "..", "resources", "nat_gateway.go")
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("reading %s: %v", wantPath, err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"cmd/gen-resource's output for NatGateway does not byte-for-byte match the checked-in "+
				"%s -- the generator has drifted from what the team actually reviewed and shipped; "+
				"reconcile by adjusting either template.go.tmpl or %s, whichever is the actual "+
				"divergence, per this test's own doc comment\n--- got (generated) ---\n%s\n"+
				"--- want (resources/nat_gateway.go) ---\n%s",
			wantPath, wantPath, got, want,
		)
	}
}
