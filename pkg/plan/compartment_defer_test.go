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
		{ResourceType: "Subnet", ResourceID: "s", CompartmentID: clean, State: StateWouldRemove},
		{ResourceType: "BootVolume", ResourceID: "bv", CompartmentID: clean, State: StateFiltered, Detail: "TERMINATED"},
		compartmentEntry(pending),
		{ResourceType: "Vault", ResourceID: "v", CompartmentID: pending, State: StateWouldRemove},
		compartmentEntry(protected),
		{ResourceType: "Bucket", ResourceID: "b", CompartmentID: protected, State: StateSkipped, Reason: scope.ReasonProtectedByTag},
		compartmentEntry(kept),
		{ResourceType: "ObjectVersion", ResourceID: "o", CompartmentID: kept, State: StateFiltered, Detail: filteredByConfigDetail},
		compartmentEntry(childOfDeferred),
		compartmentEntry(grandchild),
		{ResourceType: "VaultSecret", ResourceID: "sec", CompartmentID: grandchild, State: StateSkipped, Reason: scope.ReasonScheduledDeletion},
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
		parent:          "Compartment pending; Compartment protected; Compartment kept; Compartment child-of-deferred",
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
