package plan

import (
	"strings"
	"testing"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	childOfDeferred = "child-of-deferred"
	clean           = "clean"
	grandchild      = "grandchild"
	kept            = "kept"
	pending         = "pending"
	protected       = "protected"
	parent          = "parent"
	gone            = "gone"

	typeVault       = "Vault"
	typeVaultSecret = "VaultSecret"
)

func compartmentEntry(id string) Entry {
	return Entry{ResourceType: compartmentEntryResourceType, ResourceID: id, CompartmentID: id, State: StateWouldRemove}
}

// TestDeferNonEmptyCompartments covers each kind of blocker, a clean compartment, and a deferral
// propagating up to the parent.
func TestDeferNonEmptyCompartments(t *testing.T) {
	tree := map[string][]string{
		parent:          {clean, pending, protected, kept, childOfDeferred, gone},
		childOfDeferred: {grandchild},
	}
	states := map[string]string{gone: "DELETED"}

	entries := []Entry{
		compartmentEntry(parent),
		compartmentEntry(clean),
		{ResourceType: fixtureTypeSubnet, ResourceID: "s", CompartmentID: clean, State: StateWouldRemove},
		{ResourceType: "BootVolume", ResourceID: "bv", CompartmentID: clean, State: StateFiltered, Detail: "TERMINATED"},
		compartmentEntry(pending),
		{ResourceType: typeVault, ResourceID: "v", CompartmentID: pending, State: StateWouldRemove},
		compartmentEntry(protected),
		{ResourceType: "Bucket", ResourceID: "b", CompartmentID: protected, State: StateSkipped, Reason: scope.ReasonProtectedByTag},
		compartmentEntry(kept),
		{ResourceType: "ObjectVersion", ResourceID: "o", CompartmentID: kept, State: StateFiltered, Detail: filteredByConfigDetail},
		compartmentEntry(childOfDeferred),
		compartmentEntry(grandchild),
		{ResourceType: typeVaultSecret, ResourceID: "sec", CompartmentID: grandchild, State: StateSkipped, Reason: scope.ReasonScheduledDeletion},
	}

	got := DeferNonEmptyCompartments(entries,
		func(id string) []string { return tree[id] },
		func(id string) (string, bool) { s, ok := states[id]; return s, ok })

	want := map[string]string{
		clean:           "",
		pending:         "Vault v (scheduled for deletion, not deleted, by this run)",
		protected:       "Bucket b (protected-by-tag)",
		kept:            "ObjectVersion o (filtered by config)",
		grandchild:      "VaultSecret sec (scheduled-deletion)",
		childOfDeferred: "Compartment grandchild",
		parent:          "Compartment child-of-deferred; Compartment kept; Compartment pending; Compartment protected",
	}
	for _, e := range got {
		if e.ResourceType != compartmentEntryResourceType {
			continue
		}
		blocker := want[e.ResourceID]
		if blocker == "" {
			if e.State != StateWouldRemove {
				t.Errorf("%s = %s (%s), want would-remove", e.ResourceID, e.State, e.Detail)
			}
			continue
		}
		if e.State != StateSkipped || e.Reason != scope.ReasonCompartmentNotEmpty || !strings.Contains(e.Detail, blocker) {
			t.Errorf("%s = %s/%s %q, want skipped compartment-not-empty naming %q", e.ResourceID, e.State, e.Reason, e.Detail, blocker)
		}
	}
}

// TestDeferNonEmptyCompartments_DetailIndependentOfScanOrder pins a spurious approval refusal: libnuke's
// concurrent scanners hand entries over in no fixed order, and a deferred compartment's detail
// named its blockers in whatever order they arrived -- so two scans of an unchanged compartment
// disagreed and the approved-plan gate refused the apply.
func TestDeferNonEmptyCompartments_DetailIndependentOfScanOrder(t *testing.T) {
	blockers := []Entry{
		{ResourceType: typeVault, ResourceID: "v", CompartmentID: pending, State: StateSkipped, Reason: scope.ReasonScheduledDeletion},
		{ResourceType: typeVaultSecret, ResourceID: "sec", CompartmentID: pending, State: StateSkipped, Reason: scope.ReasonScheduledDeletion},
		{ResourceType: "KmsKey", ResourceID: "k", CompartmentID: pending, State: StateSkipped, Reason: scope.ReasonScheduledDeletion},
	}
	detail := func(order ...int) string {
		entries := []Entry{compartmentEntry(pending)}
		for _, i := range order {
			entries = append(entries, blockers[i])
		}
		got := DeferNonEmptyCompartments(entries,
			func(string) []string { return nil },
			func(string) (string, bool) { return "", false })
		return got[0].Detail
	}

	first, second := detail(0, 1, 2), detail(1, 0, 2)
	if first != second {
		t.Fatalf("detail depends on scan order:\n%s\n%s", first, second)
	}
}
