package scope

import (
	"context"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// compartment is a small test helper that builds a synthetic identity.Compartment with the
// fields the resolver reads: Id, CompartmentId (parent), LifecycleState. Every synthetic tree
// in this file is built directly in-memory -- no fake IdentityClient needed for Resolve or
// AncestorBlocklisted, matching Q1's "trivially unit-testable with a synthetic map[string]
// Compartment fixture" framing.
func compartment(id, parent string, state identity.CompartmentLifecycleStateEnum) identity.Compartment {
	c := identity.Compartment{
		Id:             common.String(id),
		Name:           common.String(id),
		LifecycleState: state,
	}
	if parent != "" {
		c.CompartmentId = common.String(parent)
	}
	return c
}

const (
	root   = "ocid1.tenancy.oc1..root"
	target = "ocid1.compartment.oc1..target"
	nodeA  = "ocid1.compartment.oc1..a"
	nodeB  = "ocid1.compartment.oc1..b"
	nodeC  = "ocid1.compartment.oc1..c"
)

func TestResolve_PrunesBlocklistedDescendant(t *testing.T) {
	// target
	// ├── A (ACTIVE)
	// └── B (ACTIVE, blocklisted)
	//     └── C (ACTIVE) -- must never be visited, must never appear in inScope or skippedBlocklisted
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateActive),
		compartment(nodeB, target, identity.CompartmentLifecycleStateActive),
		compartment(nodeC, nodeB, identity.CompartmentLifecycleStateActive),
	})
	blocklist := map[string]struct{}{nodeB: {}}

	inScope, skipped, _ := tree.Resolve(target, blocklist)

	if _, ok := inScope[nodeB]; ok {
		t.Fatal("blocklisted node B must not be in the in-scope set")
	}
	if _, ok := inScope[nodeC]; ok {
		t.Fatal("B's child C must never be visited, must not be in the in-scope set")
	}
	if _, ok := inScope[target]; !ok {
		t.Fatal("target itself must be in-scope")
	}
	if _, ok := inScope[nodeA]; !ok {
		t.Fatal("A (target's non-blocklisted child) must be in-scope")
	}
	if len(skipped) != 1 || skipped[0] != nodeB {
		t.Fatalf("expected skippedBlocklisted to contain exactly [%s], got %v", nodeB, skipped)
	}
}

func TestResolve_ExcludesNonActiveLifecycle(t *testing.T) {
	// target
	// └── A (DELETED)
	//     └── B (ACTIVE) -- must never be visited since A is excluded and not descended into
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateDeleted),
		compartment(nodeB, nodeA, identity.CompartmentLifecycleStateActive),
	})

	inScope, _, skippedNonActive := tree.Resolve(target, map[string]struct{}{})

	if _, ok := inScope[nodeA]; ok {
		t.Fatal("DELETED node A must not be in the in-scope set")
	}

	// The prune is correct; being silent about it was not. A pruned subtree with no record of
	// itself reads exactly like one that was scanned and found clean (NR-787).
	if len(skippedNonActive) != 1 {
		t.Fatalf("Resolve reported %d non-ACTIVE prunes, want 1: %+v", len(skippedNonActive), skippedNonActive)
	}
	if skippedNonActive[0].ID != nodeA {
		t.Errorf("non-ACTIVE prune ID = %q, want %q", skippedNonActive[0].ID, nodeA)
	}
	if got, want := skippedNonActive[0].LifecycleState, string(identity.CompartmentLifecycleStateDeleted); got != want {
		t.Errorf("non-ACTIVE prune state = %q, want %q -- DELETING and CREATING call for "+
			"different operator actions, so the state has to travel with the event", got, want)
	}
	if _, ok := inScope[nodeB]; ok {
		t.Fatal("A's child B must never be visited since A is non-ACTIVE")
	}
	if _, ok := inScope[target]; !ok {
		t.Fatal("target itself must be in-scope")
	}
}

func TestAncestorBlocklisted_TargetItselfBlocklisted(t *testing.T) {
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
	})
	blocklist := map[string]struct{}{target: {}}

	blockedOCID, blocked := tree.AncestorBlocklisted(target, blocklist)

	if !blocked {
		t.Fatal("expected blocked=true when target itself is blocklisted")
	}
	if blockedOCID != target {
		t.Fatalf("expected blockedOCID=%s, got %s", target, blockedOCID)
	}
}

func TestAncestorBlocklisted_AncestorBlocklisted(t *testing.T) {
	// root -> A (blocklisted) -> target
	tree := BuildTree([]identity.Compartment{
		compartment(nodeA, root, identity.CompartmentLifecycleStateActive),
		compartment(target, nodeA, identity.CompartmentLifecycleStateActive),
	})
	blocklist := map[string]struct{}{nodeA: {}}

	blockedOCID, blocked := tree.AncestorBlocklisted(target, blocklist)

	if !blocked {
		t.Fatal("expected blocked=true when target's parent is blocklisted")
	}
	if blockedOCID != nodeA {
		t.Fatalf("expected blockedOCID=%s (the blocklisted ancestor), got %s", nodeA, blockedOCID)
	}
}

func TestAncestorBlocklisted_NotBlocklisted(t *testing.T) {
	// 5+ levels above target, none blocklisted -- must terminate cleanly at the root (root is
	// never itself a key in byID, matching FetchTenancyTree's live-verified response shape).
	l1 := "ocid1.compartment.oc1..l1"
	l2 := "ocid1.compartment.oc1..l2"
	l3 := "ocid1.compartment.oc1..l3"
	l4 := "ocid1.compartment.oc1..l4"
	l5 := "ocid1.compartment.oc1..l5"
	tree := BuildTree([]identity.Compartment{
		compartment(l1, root, identity.CompartmentLifecycleStateActive),
		compartment(l2, l1, identity.CompartmentLifecycleStateActive),
		compartment(l3, l2, identity.CompartmentLifecycleStateActive),
		compartment(l4, l3, identity.CompartmentLifecycleStateActive),
		compartment(l5, l4, identity.CompartmentLifecycleStateActive),
		compartment(target, l5, identity.CompartmentLifecycleStateActive),
	})

	blockedOCID, blocked := tree.AncestorBlocklisted(target, map[string]struct{}{})

	if blocked {
		t.Fatalf("expected blocked=false, got blockedOCID=%s", blockedOCID)
	}
	if blockedOCID != "" {
		t.Fatalf("expected empty blockedOCID, got %s", blockedOCID)
	}
}

func TestExists_UnknownTargetReturnsFalse(t *testing.T) {
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
	})

	if tree.Exists("ocid1.compartment.oc1..unknown") {
		t.Fatal("expected Exists to return false for an OCID absent from the tree")
	}
	if !tree.Exists(target) {
		t.Fatal("expected Exists to return true for a known OCID")
	}
}

// fakeIdentityClient implements IdentityClient purely in-memory, returning canned responses in
// sequence -- proves FetchTenancyTree's pagination loop without any network or HTTP mocking.
type fakeIdentityClient struct {
	responses []identity.ListCompartmentsResponse
	calls     int
}

// ListCompartments signature must match the real identity.IdentityClient.ListCompartments (by
// value, not a pointer).
//
//nolint:gocritic
func (f *fakeIdentityClient) ListCompartments(
	_ context.Context,
	_ identity.ListCompartmentsRequest,
) (identity.ListCompartmentsResponse, error) {
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

func (f *fakeIdentityClient) GetCompartment(_ context.Context, _ identity.GetCompartmentRequest) (identity.GetCompartmentResponse, error) {
	return identity.GetCompartmentResponse{}, nil
}

func TestDeletionOrder_ChildrenBeforeAncestors(t *testing.T) {
	// target -> A -> B, all ACTIVE and all in inScope. Children must sort strictly before every
	// in-scope ancestor: B before A, A before target.
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateActive),
		compartment(nodeB, nodeA, identity.CompartmentLifecycleStateActive),
	})
	inScope := map[string]struct{}{target: {}, nodeA: {}, nodeB: {}}

	got := tree.DeletionOrder(inScope)

	want := []string{nodeB, nodeA, target}
	if len(got) != len(want) {
		t.Fatalf("DeletionOrder returned %d entries, want %d (got: %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DeletionOrder()[%d] = %s, want %s (full order: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDeletionOrder_TiebreakAscendingOCID(t *testing.T) {
	// target -> A, target -> C: two same-depth siblings. target itself is NOT in inScope, so the
	// tiebreak between A and C (both depth 1) is the only thing under test: ascending OCID.
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateActive),
		compartment(nodeC, target, identity.CompartmentLifecycleStateActive),
	})
	inScope := map[string]struct{}{nodeA: {}, nodeC: {}}

	got := tree.DeletionOrder(inScope)

	want := []string{nodeA, nodeC} // nodeA's OCID ("...a") sorts before nodeC's ("...c")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("DeletionOrder() = %v, want %v (ascending-OCID tiebreak among same-depth siblings)", got, want)
	}
}

func TestDeletionOrder_EmptyInScopeReturnsEmpty(t *testing.T) {
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
	})

	got := tree.DeletionOrder(map[string]struct{}{})

	if len(got) != 0 {
		t.Fatalf("DeletionOrder(empty inScope) = %v, want an empty (or nil) slice", got)
	}
}

func TestHasBlocklistedDescendant_DirectChild(t *testing.T) {
	// target -> A (blocklisted)
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateActive),
	})
	blocklist := map[string]struct{}{nodeA: {}}

	if !tree.HasBlocklistedDescendant(target, blocklist) {
		t.Fatal("expected true: target's direct child A is blocklisted")
	}
}

func TestHasBlocklistedDescendant_Grandchild(t *testing.T) {
	// target -> A -> B (blocklisted): proves the walk is genuinely recursive, not one level deep.
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateActive),
		compartment(nodeB, nodeA, identity.CompartmentLifecycleStateActive),
	})
	blocklist := map[string]struct{}{nodeB: {}}

	if !tree.HasBlocklistedDescendant(target, blocklist) {
		t.Fatal("expected true: target's grandchild B (via A) is blocklisted")
	}
}

func TestHasBlocklistedDescendant_NoChildren(t *testing.T) {
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
	})

	if tree.HasBlocklistedDescendant(target, map[string]struct{}{target: {}}) {
		t.Fatal("expected false: target has no children at all")
	}
}

func TestHasBlocklistedDescendant_SelfBlocklistedNeverChecked(t *testing.T) {
	// target itself is blocklisted, but its one child A is not -- HasBlocklistedDescendant must
	// deliberately never check id itself, only its descendants (callers needing that answer
	// already have it from Resolve/AncestorBlocklisted).
	tree := BuildTree([]identity.Compartment{
		compartment(target, root, identity.CompartmentLifecycleStateActive),
		compartment(nodeA, target, identity.CompartmentLifecycleStateActive),
	})
	blocklist := map[string]struct{}{target: {}}

	if tree.HasBlocklistedDescendant(target, blocklist) {
		t.Fatal("expected false: only target itself is blocklisted, no descendant is")
	}
}

func TestFetchTenancyTree_Paginates(t *testing.T) {
	page1Items := []identity.Compartment{compartment(nodeA, root, identity.CompartmentLifecycleStateActive)}
	page2Items := []identity.Compartment{compartment(nodeB, root, identity.CompartmentLifecycleStateActive)}
	client := &fakeIdentityClient{
		responses: []identity.ListCompartmentsResponse{
			{Items: page1Items, OpcNextPage: common.String("page-2-token")},
			{Items: page2Items, OpcNextPage: nil},
		},
	}

	all, err := FetchTenancyTree(context.Background(), client, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("expected exactly 2 ListCompartments calls, got %d", client.calls)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 combined items across both pages, got %d", len(all))
	}
	if *all[0].Id != nodeA || *all[1].Id != nodeB {
		t.Fatalf("expected combined items [%s, %s] in order, got [%s, %s]", nodeA, nodeB, *all[0].Id, *all[1].Id)
	}
}
