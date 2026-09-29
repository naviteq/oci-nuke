package plan_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/queue"

	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	fixtureRegion        = "eu-frankfurt-1"
	fixtureCompartmentID = "ocid1.compartment.oc1..x"
	fixtureOwner         = fixtureRegion + "/" + fixtureCompartmentID

	resourceTypeVCN   = "VCN"
	resourceTypeVault = "Vault"
)

type fakeResource struct{}

func (fakeResource) Remove(context.Context) error { return nil }

// fakeKeyedResource implements both resource.UniqueKeyGetter and resource.LegacyStringer with
// deliberately distinct values, so TestBuildFromQueue_UniqueKeyGetterPreferredOverFallback can
// prove BuildFromQueue prefers UniqueKey() over the LegacyStringer fallback.
type fakeKeyedResource struct{}

func (fakeKeyedResource) Remove(context.Context) error { return nil }
func (fakeKeyedResource) UniqueKey() string            { return "unique-key-value" }
func (fakeKeyedResource) String() string               { return "legacy-string-value" }

func newItem(itemType string, state queue.ItemState) *queue.Item {
	return &queue.Item{
		Type:     itemType,
		Owner:    fixtureOwner,
		State:    state,
		Resource: fakeResource{},
	}
}

func TestBuildFromQueue_DryRunStates(t *testing.T) {
	items := []*queue.Item{
		newItem(resourceTypeVCN, queue.ItemStateNew),
		newItem("Subnet", queue.ItemStateNewDependency),
		newItem("Bucket", queue.ItemStateFiltered),
	}

	entries := plan.BuildFromQueue(items, false, nil)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].State != plan.StateWouldRemove {
		t.Errorf("New item: expected StateWouldRemove, got %s", entries[0].State)
	}
	if entries[1].State != plan.StateWouldRemove {
		t.Errorf("NewDependency item: expected StateWouldRemove, got %s", entries[1].State)
	}
	if entries[2].State != plan.StateFiltered {
		t.Errorf("Filtered item: expected StateFiltered, got %s", entries[2].State)
	}

	if entries[0].Region != fixtureRegion {
		t.Errorf("expected Region %q, got %q", fixtureRegion, entries[0].Region)
	}
	if entries[0].CompartmentID != fixtureCompartmentID {
		t.Errorf("expected CompartmentID %q, got %q", fixtureCompartmentID, entries[0].CompartmentID)
	}
}

func TestBuildFromQueue_DestructiveRunStates(t *testing.T) {
	items := []*queue.Item{
		newItem(resourceTypeVCN, queue.ItemStateFinished),
		newItem("Subnet", queue.ItemStateWaiting),
		newItem("Bucket", queue.ItemStateFailed),
	}

	leftoverCalls := 0
	leftoverReason := func(*queue.Item) scope.RefusalReason {
		leftoverCalls++
		return scope.ReasonAPIError
	}

	entries := plan.BuildFromQueue(items, true, leftoverReason)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].State != plan.StateRemoved {
		t.Errorf("Finished item: expected StateRemoved, got %s", entries[0].State)
	}
	if entries[1].State != plan.StateLeftover || entries[1].Reason != scope.ReasonAPIError {
		t.Errorf("Waiting item: expected StateLeftover/%s, got %s/%s", scope.ReasonAPIError, entries[1].State, entries[1].Reason)
	}
	if entries[2].State != plan.StateLeftover || entries[2].Reason != scope.ReasonAPIError {
		t.Errorf("Failed item: expected StateLeftover/%s, got %s/%s", scope.ReasonAPIError, entries[2].State, entries[2].Reason)
	}
	if leftoverCalls != 2 {
		t.Errorf("expected leftoverReason invoked twice (Waiting, Failed), got %d", leftoverCalls)
	}
}

func TestBuildFromQueue_UniqueKeyGetterPreferredOverFallback(t *testing.T) {
	item := &queue.Item{
		Type:     resourceTypeVCN,
		Owner:    fixtureOwner,
		State:    queue.ItemStateNew,
		Resource: fakeKeyedResource{},
	}

	entries := plan.BuildFromQueue([]*queue.Item{item}, false, nil)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ResourceID != "unique-key-value" {
		t.Fatalf("expected ResourceID from UniqueKey(), got %q (LegacyStringer fallback value, wrong preference order)", entries[0].ResourceID)
	}
}

func TestMergeSkipEvents_OverwritesLeftoverReasonWhenMoreSpecific(t *testing.T) {
	// Forward-reference the constant Plan 03-03 adds; pkg/scope's RefusalReason is
	// already open/string-based, so a plain literal here does not require that plan to have
	// landed yet.
	scheduledDeletion := scope.RefusalReason("scheduled-deletion")

	entries := []plan.Entry{
		{
			ResourceType:  resourceTypeVault,
			ResourceID:    "ocid1.vault.oc1..x",
			CompartmentID: fixtureCompartmentID,
			Region:        fixtureRegion,
			State:         plan.StateLeftover,
			Reason:        scope.ReasonAPIError,
		},
	}

	skips := []scope.SkipEvent{
		{
			Reason:        scheduledDeletion,
			ResourceType:  resourceTypeVault,
			ResourceID:    "ocid1.vault.oc1..x",
			CompartmentID: fixtureCompartmentID,
			Detail:        "scheduled for deletion in 7 days",
		},
	}

	merged := plan.MergeSkipEvents(entries, skips)
	if len(merged) != 1 {
		t.Fatalf("expected 1 entry (overwrite in place, not append), got %d", len(merged))
	}
	if merged[0].Reason != scheduledDeletion {
		t.Fatalf("expected Reason overwritten to %q, got %q", scheduledDeletion, merged[0].Reason)
	}
	if merged[0].Detail != "scheduled for deletion in 7 days" {
		t.Fatalf("expected Detail overwritten, got %q", merged[0].Detail)
	}
	if merged[0].State != plan.StateLeftover {
		t.Fatalf("expected State to remain StateLeftover, got %s", merged[0].State)
	}
}

// TestMergeSkipEvents_ConvertsFilteredEntryToSingleSkippedEntry proves 06-REVIEW.md WR-01's fix:
// a StateFiltered entry (Filter() returned a non-nil error, e.g. Compartment's
// blocklisted-descendant branch or LoadBalancer's FAILED-state branch) whose accompanying
// scope.SkipEvent matches it by ResourceID must be converted IN PLACE to StateSkipped, never left
// alongside a second, separately appended StateSkipped entry for the same resource. Before the
// fix, mergeSkipEvent only matched StateLeftover entries, so this exact scenario produced two
// entries for one resource.
func TestMergeSkipEvents_ConvertsFilteredEntryToSingleSkippedEntry(t *testing.T) {
	entries := []plan.Entry{
		{
			ResourceType:  "Compartment",
			ResourceID:    fixtureCompartmentID,
			CompartmentID: fixtureCompartmentID,
			Region:        fixtureRegion,
			State:         plan.StateFiltered,
			Detail:        "Compartment " + fixtureCompartmentID + " has a blocklisted descendant",
		},
	}

	compartmentNotEmpty := scope.RefusalReason("compartment-not-empty")
	skips := []scope.SkipEvent{
		{
			Reason:        compartmentNotEmpty,
			ResourceType:  "Compartment",
			ResourceID:    fixtureCompartmentID,
			CompartmentID: fixtureCompartmentID,
			Detail:        "a descendant compartment is blocklisted; this compartment can never physically empty",
		},
	}

	merged := plan.MergeSkipEvents(entries, skips)
	if len(merged) != 1 {
		t.Fatalf("expected exactly 1 entry (converted in place, not a second appended entry), got %d: %+v", len(merged), merged)
	}
	if merged[0].State != plan.StateSkipped {
		t.Fatalf("expected State converted to %q, got %q", plan.StateSkipped, merged[0].State)
	}
	if merged[0].Reason != compartmentNotEmpty {
		t.Fatalf("expected Reason %q, got %q", compartmentNotEmpty, merged[0].Reason)
	}
	if merged[0].Detail != skips[0].Detail {
		t.Fatalf("expected Detail overwritten to %q, got %q", skips[0].Detail, merged[0].Detail)
	}
}

func TestMergeSkipEvents_AppendsNewSkippedEntryWhenNoMatch(t *testing.T) {
	var entries []plan.Entry

	skips := []scope.SkipEvent{
		{
			Reason:        scope.ReasonOutOfScope,
			ResourceType:  resourceTypeVCN,
			ResourceID:    "ocid1.vcn.oc1..y",
			CompartmentID: "ocid1.compartment.oc1..y",
		},
	}

	merged := plan.MergeSkipEvents(entries, skips)
	if len(merged) != 1 {
		t.Fatalf("expected 1 appended entry, got %d", len(merged))
	}
	if merged[0].State != plan.StateSkipped {
		t.Fatalf("expected StateSkipped, got %s", merged[0].State)
	}
	if merged[0].Reason != scope.ReasonOutOfScope {
		t.Fatalf("expected Reason %q, got %q", scope.ReasonOutOfScope, merged[0].Reason)
	}
}

func TestRenderTable_UsesLiteralStateAndReasonStrings(t *testing.T) {
	entries := []plan.Entry{
		{
			ResourceType: resourceTypeVCN, ResourceID: "a", CompartmentID: "c",
			Region: fixtureRegion, State: plan.StateWouldRemove,
		},
		{
			ResourceType: resourceTypeVault, ResourceID: "b", CompartmentID: "c",
			Region: fixtureRegion, State: plan.StateLeftover, Reason: scope.ReasonAPIError,
		},
	}

	out := plan.RenderTable(entries)

	for _, want := range []string{"would-remove", "leftover", "api-error"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected rendered table to contain literal %q, got:\n%s", want, out)
		}
	}
}

// TestRenderTable_ShowsDetail is NR-787's third defect at the surface an operator actually reads.
// Entry.Detail already carried OCI's own words into the JSON artifact; the table printed the
// classification alone, so a bucket refused with "409 BucketNotEmpty" showed up as "api-error"
// and diagnosing it took half an hour and a separate CLI.
func TestRenderTable_ShowsDetail(t *testing.T) {
	const detail = "HTTP 409 BucketNotEmpty: The bucket is not empty."
	entries := []plan.Entry{
		{
			ResourceType: resourceTypeVault, ResourceID: "b", CompartmentID: "c",
			Region: fixtureRegion, State: plan.StateLeftover, Reason: scope.ReasonAPIError,
			Detail: detail,
		},
	}

	out := plan.RenderTable(entries)

	if !strings.Contains(out, "DETAIL") {
		t.Errorf("expected a DETAIL column header, got:\n%s", out)
	}
	if !strings.Contains(out, detail) {
		t.Errorf("expected the table to print Entry.Detail %q, got:\n%s", detail, out)
	}
	// A separate column, not folded into REASON: REASON is a closed vocabulary tooling matches
	// on, DETAIL is free text from OCI.
	if !strings.Contains(out, "REASON\tDETAIL") && !strings.Contains(out, "REASON") {
		t.Errorf("expected REASON to survive alongside DETAIL, got:\n%s", out)
	}
	if !strings.Contains(out, string(scope.ReasonAPIError)) {
		t.Errorf("expected the classification %q to still be printed, got:\n%s", scope.ReasonAPIError, out)
	}
}

// TestRenderTable_CollapsesMultilineDetail proves one embedded newline in an API message cannot
// break the tabwriter's column alignment for its whole group.
func TestRenderTable_CollapsesMultilineDetail(t *testing.T) {
	entries := []plan.Entry{
		{
			ResourceType: resourceTypeVault, ResourceID: "b", CompartmentID: "c",
			Region: fixtureRegion, State: plan.StateLeftover, Reason: scope.ReasonAPIError,
			Detail: "first line\nsecond line\n\tthird",
		},
	}

	out := plan.RenderTable(entries)

	// Group header, column header, one row -- and no fourth line, which is what a surviving
	// newline inside Detail would produce.
	if strings.Count(out, "\n") != 3 {
		t.Errorf("expected 3 lines (group header, column header, one row), got %d newlines:\n%s",
			strings.Count(out, "\n"), out)
	}
	if !strings.Contains(out, "first line second line third") {
		t.Errorf("expected whitespace runs collapsed to single spaces, got:\n%s", out)
	}
}
