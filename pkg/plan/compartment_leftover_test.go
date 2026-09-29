package plan

import (
	"testing"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	testEnrichCompartmentID = "ocid1.compartment.oc1..enrich-test"
	testEnrichOtherCompID   = "ocid1.compartment.oc1..other"

	// testEnrichInstanceType is the fixture blocker resource type reused across this file's
	// tests (goconst min-occurrences 3) -- any real, non-Compartment resource type name works
	// equally well here since EnrichCompartmentLeftover treats ResourceType as opaque data.
	testEnrichInstanceType = "Instance"
	// testEnrichInstanceOCID is the fixture blocker resource ID reused alongside
	// testEnrichInstanceType above, same goconst rationale.
	testEnrichInstanceOCID = "ocid1.instance.oc1..a"
)

// TestEnrichCompartmentLeftover_BlockersPresent_SetsReasonAndDetail is the plan's core behavior:
// a StateLeftover Compartment entry with two OTHER entries sharing its CompartmentID that never
// reached StateRemoved gets Reason=scope.ReasonCompartmentNotEmpty and a Detail naming both.
func TestEnrichCompartmentLeftover_BlockersPresent_SetsReasonAndDetail(t *testing.T) {
	entries := []Entry{
		{
			ResourceType: compartmentEntryResourceType, ResourceID: "ocid1.compartment.oc1..enrich-test",
			CompartmentID: testEnrichCompartmentID, State: StateLeftover, Reason: scope.ReasonAPIError,
		},
		{
			ResourceType: testEnrichInstanceType, ResourceID: testEnrichInstanceOCID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover,
		},
		{
			ResourceType: "BlockVolume", ResourceID: "ocid1.volume.oc1..b",
			CompartmentID: testEnrichCompartmentID, State: StateLeftover,
		},
		// A different compartment's own leftover entry must never be treated as a blocker.
		{
			ResourceType: testEnrichInstanceType, ResourceID: "ocid1.instance.oc1..unrelated",
			CompartmentID: testEnrichOtherCompID, State: StateLeftover,
		},
	}

	got := EnrichCompartmentLeftover(entries, testEnrichCompartmentID)

	compartmentEntry := got[0]
	if compartmentEntry.Reason != scope.ReasonCompartmentNotEmpty {
		t.Fatalf("Reason = %q, want %q", compartmentEntry.Reason, scope.ReasonCompartmentNotEmpty)
	}
	wantDetail := "still present: Instance ocid1.instance.oc1..a; BlockVolume ocid1.volume.oc1..b"
	if compartmentEntry.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", compartmentEntry.Detail, wantDetail)
	}
}

// TestEnrichCompartmentLeftover_QueueCannotExplain_LeavesReasonUnchanged proves T-06-04-01: when
// every OTHER entry sharing the Compartment entry's CompartmentID already reached StateRemoved,
// the generic scope.ReasonAPIError ClassifyLeftover already assigned is left exactly as-is --
// never silently overwritten with a specific-sounding but false ReasonCompartmentNotEmpty and an
// empty blocker list.
func TestEnrichCompartmentLeftover_QueueCannotExplain_LeavesReasonUnchanged(t *testing.T) {
	entries := []Entry{
		{
			ResourceType: compartmentEntryResourceType, ResourceID: testEnrichCompartmentID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover,
			Reason: scope.ReasonAPIError, Detail: "left in state waiting: max-wait-retries likely exhausted",
		},
		{
			ResourceType: testEnrichInstanceType, ResourceID: testEnrichInstanceOCID,
			CompartmentID: testEnrichCompartmentID, State: StateRemoved,
		},
	}

	got := EnrichCompartmentLeftover(entries, testEnrichCompartmentID)

	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("Reason = %q, want unchanged %q", got[0].Reason, scope.ReasonAPIError)
	}
	wantDetail := "left in state waiting: max-wait-retries likely exhausted"
	if got[0].Detail != wantDetail {
		t.Errorf("Detail = %q, want unchanged %q", got[0].Detail, wantDetail)
	}
}

// TestEnrichCompartmentLeftover_QueueCannotExplain_NoOtherEntriesAtAll is the degenerate form of
// the same case: a lone Compartment entry with no sibling entries at all for its CompartmentID
// still must not be treated as having zero blockers with an empty Detail.
func TestEnrichCompartmentLeftover_QueueCannotExplain_NoOtherEntriesAtAll(t *testing.T) {
	entries := []Entry{
		{
			ResourceType: compartmentEntryResourceType, ResourceID: testEnrichCompartmentID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover, Reason: scope.ReasonAPIError,
		},
	}

	got := EnrichCompartmentLeftover(entries, testEnrichCompartmentID)

	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("Reason = %q, want unchanged %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].Detail != "" {
		t.Errorf("Detail = %q, want unchanged empty string", got[0].Detail)
	}
}

// TestEnrichCompartmentLeftover_CompartmentEntryNotLeftover_ReturnsUnchanged proves a Compartment
// entry that is StateRemoved (or any state other than StateLeftover) is never touched, even if
// other entries for the same CompartmentID are themselves still leftover.
func TestEnrichCompartmentLeftover_CompartmentEntryNotLeftover_ReturnsUnchanged(t *testing.T) {
	entries := []Entry{
		{
			ResourceType: compartmentEntryResourceType, ResourceID: testEnrichCompartmentID,
			CompartmentID: testEnrichCompartmentID, State: StateRemoved,
		},
		{
			ResourceType: testEnrichInstanceType, ResourceID: testEnrichInstanceOCID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover,
		},
	}

	got := EnrichCompartmentLeftover(entries, testEnrichCompartmentID)

	if got[0].Reason != "" {
		t.Errorf("Reason = %q, want unchanged empty (Compartment entry is StateRemoved, not StateLeftover)", got[0].Reason)
	}
	if got[0].Detail != "" {
		t.Errorf("Detail = %q, want unchanged empty", got[0].Detail)
	}
}

// TestEnrichCompartmentLeftover_SecondCompartmentEntry_NeverListedAsBlocker proves 06-REVIEW.md
// WR-03's fix: should CR-01 (or any future regression) reintroduce a SECOND Compartment entry for
// the same CompartmentID -- distinct from the one EnrichCompartmentLeftover already identified by
// pointer as compartmentEntry -- that sibling must never be listed among the "still present"
// blockers in the enriched Detail. Before the fix, the blocker-exclusion loop only skipped the
// entries[i] whose ADDRESS equaled compartmentEntry, so a second, sibling Compartment entry
// (different ResourceID, same CompartmentID, also StateLeftover) would have been included in the
// blocker set, producing a confusing, self-referential "Compartment X still blocked by:
// Compartment Y" report instead of naming only the genuine blocker.
func TestEnrichCompartmentLeftover_SecondCompartmentEntry_NeverListedAsBlocker(t *testing.T) {
	const siblingCompartmentEntryID = "ocid1.compartment.oc1..enrich-test-sibling-region-dup"

	entries := []Entry{
		{
			ResourceType: compartmentEntryResourceType, ResourceID: testEnrichCompartmentID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover, Reason: scope.ReasonAPIError,
		},
		// A duplicate-entry regression: a second Compartment entry for the SAME CompartmentID,
		// distinct ResourceID, also StateLeftover -- exactly the shape CR-01 (Regional geography)
		// used to produce (one entry per region for the same compartment).
		{
			ResourceType: compartmentEntryResourceType, ResourceID: siblingCompartmentEntryID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover,
		},
		{
			ResourceType: testEnrichInstanceType, ResourceID: testEnrichInstanceOCID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover,
		},
	}

	got := EnrichCompartmentLeftover(entries, testEnrichCompartmentID)

	compartmentEntry := got[0]
	if compartmentEntry.Reason != scope.ReasonCompartmentNotEmpty {
		t.Fatalf("Reason = %q, want %q", compartmentEntry.Reason, scope.ReasonCompartmentNotEmpty)
	}
	wantDetail := "still present: " + testEnrichInstanceType + " " + testEnrichInstanceOCID
	if compartmentEntry.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q (the sibling Compartment entry must never be listed as a blocker)", compartmentEntry.Detail, wantDetail)
	}
}

// TestEnrichCompartmentLeftover_NoCompartmentEntryAtAll_ReturnsUnchanged proves entries with no
// Compartment entry at all pass through untouched, and that EnrichCompartmentLeftover never
// panics or fabricates one.
func TestEnrichCompartmentLeftover_NoCompartmentEntryAtAll_ReturnsUnchanged(t *testing.T) {
	entries := []Entry{
		{
			ResourceType: testEnrichInstanceType, ResourceID: testEnrichInstanceOCID,
			CompartmentID: testEnrichCompartmentID, State: StateLeftover, Reason: scope.ReasonAPIError,
		},
	}

	got := EnrichCompartmentLeftover(entries, testEnrichCompartmentID)

	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (unchanged)", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("Reason = %q, want unchanged %q", got[0].Reason, scope.ReasonAPIError)
	}
}
