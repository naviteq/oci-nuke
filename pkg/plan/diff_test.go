package plan

import (
	"strings"
	"testing"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// diffTestRegion/diffTestCompartmentID are the fixed fixture region/compartment every test in
// this file uses -- named constants rather than repeated literals (goconst), and diffFixtureEntry
// below takes no region/compartment parameter since every call site wants the same value
// (unparam).
const (
	diffTestRegion        = "us-ashburn-1"
	diffTestCompartmentID = "compartment-1"
)

// diffFixtureEntry builds a fully-populated Entry for diff tests, fixed to diffTestRegion/
// diffTestCompartmentID -- both are part of Diff's composite key (matching Body.Sort's own
// tiebreak fields), so every fixture sets them explicitly rather than leaving them at the zero
// value.
func diffFixtureEntry(resourceType, resourceID string, state EntryState) Entry {
	return Entry{
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Region:        diffTestRegion,
		CompartmentID: diffTestCompartmentID,
		State:         state,
	}
}

// TestDiff_IdenticalBodiesInDifferentOrderProduceEmptyDiff proves Diff sorts its own copies
// before comparing, so caller-supplied slice order never affects the result.
func TestDiff_IdenticalBodiesInDifferentOrderProduceEmptyDiff(t *testing.T) {
	a := diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove)
	b := diffFixtureEntry("Bucket", "bucket-1", StateWouldRemove)

	first := Body{SchemaVersion: SchemaVersion, Entries: []Entry{a, b}}
	second := Body{SchemaVersion: SchemaVersion, Entries: []Entry{b, a}}

	got := Diff(first, second)
	if len(got) != 0 {
		t.Fatalf("expected empty diff for identical bodies in different order, got: %v", got)
	}
}

// TestDiff_AddedEntryProducesPlusLine proves a resource present only in new produces exactly one
// "+"-prefixed line naming resource type/ID/region/compartment.
func TestDiff_AddedEntryProducesPlusLine(t *testing.T) {
	oldBody := Body{SchemaVersion: SchemaVersion}
	newEntry := diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove)
	newBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{newEntry}}

	got := Diff(oldBody, newBody)
	if len(got) != 1 {
		t.Fatalf("expected exactly one diff line, got %d: %v", len(got), got)
	}
	if got[0][0] != '+' {
		t.Errorf("expected line to be prefixed '+', got: %q", got[0])
	}
	for _, want := range []string{"Vcn", "vcn-1", diffTestRegion, diffTestCompartmentID} {
		if !strings.Contains(got[0], want) {
			t.Errorf("expected diff line %q to contain %q", got[0], want)
		}
	}
}

// TestDiff_RemovedEntryProducesMinusLine proves a resource present only in old produces exactly
// one "-"-prefixed line.
func TestDiff_RemovedEntryProducesMinusLine(t *testing.T) {
	oldEntry := diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove)
	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{oldEntry}}
	newBody := Body{SchemaVersion: SchemaVersion}

	got := Diff(oldBody, newBody)
	if len(got) != 1 {
		t.Fatalf("expected exactly one diff line, got %d: %v", len(got), got)
	}
	if got[0][0] != '-' {
		t.Errorf("expected line to be prefixed '-', got: %q", got[0])
	}
}

// TestDiff_StateChangeProducesTildeLineWithBothStates proves the same resource (identical
// type/ID/region/compartment key) with a different State in each body produces exactly one
// "~"-prefixed line naming both states.
func TestDiff_StateChangeProducesTildeLineWithBothStates(t *testing.T) {
	oldEntry := diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove)
	newEntry := diffFixtureEntry("Vcn", "vcn-1", StateLeftover)
	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{oldEntry}}
	newBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{newEntry}}

	got := Diff(oldBody, newBody)
	if len(got) != 1 {
		t.Fatalf("expected exactly one diff line, got %d: %v", len(got), got)
	}
	if got[0][0] != '~' {
		t.Errorf("expected line to be prefixed '~', got: %q", got[0])
	}
	if !strings.Contains(got[0], string(StateWouldRemove)) || !strings.Contains(got[0], string(StateLeftover)) {
		t.Errorf("expected diff line %q to name both old and new states", got[0])
	}
}

// TestDiff_MultipleDifferencesAreDeterministicRegardlessOfInputOrder proves that calling Diff
// twice with the SAME logical inputs, built in different slice order, produces byte-identical
// output both times -- Diff's own sorted-key walk order, not caller-supplied order or map
// iteration order, decides the result.
func TestDiff_MultipleDifferencesAreDeterministicRegardlessOfInputOrder(t *testing.T) {
	added := diffFixtureEntry("Vcn", "vcn-added", StateWouldRemove)
	removed := diffFixtureEntry("Bucket", "bucket-removed", StateWouldRemove)
	changedOld := diffFixtureEntry("Instance", "instance-1", StateWouldRemove)
	changedNew := diffFixtureEntry("Instance", "instance-1", StateLeftover)
	unchanged := diffFixtureEntry("Subnet", "subnet-1", StateWouldRemove)

	oldBodyA := Body{SchemaVersion: SchemaVersion, Entries: []Entry{removed, changedOld, unchanged}}
	newBodyA := Body{SchemaVersion: SchemaVersion, Entries: []Entry{added, changedNew, unchanged}}
	firstResult := Diff(oldBodyA, newBodyA)

	oldBodyB := Body{SchemaVersion: SchemaVersion, Entries: []Entry{unchanged, changedOld, removed}}
	newBodyB := Body{SchemaVersion: SchemaVersion, Entries: []Entry{unchanged, added, changedNew}}
	secondResult := Diff(oldBodyB, newBodyB)

	if len(firstResult) != 3 {
		t.Fatalf("expected 3 diff lines (added, removed, changed), got %d: %v", len(firstResult), firstResult)
	}
	if len(firstResult) != len(secondResult) {
		t.Fatalf("expected identical-length results, got %d vs %d", len(firstResult), len(secondResult))
	}
	for i := range firstResult {
		if firstResult[i] != secondResult[i] {
			t.Errorf("result line %d differs by input order: %q vs %q", i, firstResult[i], secondResult[i])
		}
	}
}

// TestDiff_SameStateDifferentReasonProducesTildeLine proves WR-01 (07-REVIEW.md): two entries
// sharing the same composite key and the same State, but a different Reason, still produce a
// "~"-prefixed diff line naming the reason change -- not an empty diff. Before the fix, Diff's
// only comparison was oldEntry.State != newEntry.State, so this exact pair (same State, different
// Reason) produced zero lines despite Hash (which covers the whole Entry) reporting a genuine
// mismatch upstream.
func TestDiff_SameStateDifferentReasonProducesTildeLine(t *testing.T) {
	oldEntry := diffFixtureEntry("Vcn", "vcn-1", StateSkipped)
	oldEntry.Reason = scope.ReasonProtectedByTag
	newEntry := diffFixtureEntry("Vcn", "vcn-1", StateSkipped)
	newEntry.Reason = scope.ReasonOutOfScope

	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{oldEntry}}
	newBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{newEntry}}

	got := Diff(oldBody, newBody)
	if len(got) != 1 {
		t.Fatalf("expected exactly one diff line for a same-State, different-Reason pair, got %d: %v", len(got), got)
	}
	if got[0][0] != '~' {
		t.Errorf("expected line to be prefixed '~', got: %q", got[0])
	}
	if !strings.Contains(got[0], string(oldEntry.Reason)) || !strings.Contains(got[0], string(newEntry.Reason)) {
		t.Errorf("expected diff line %q to name both old and new reasons", got[0])
	}
}

// TestDiff_SameStateDifferentDetailProducesTildeLine proves WR-01's Detail half: same key, same
// State, same Reason, different Detail still produces a "~" line.
func TestDiff_SameStateDifferentDetailProducesTildeLine(t *testing.T) {
	oldEntry := diffFixtureEntry("Vcn", "vcn-1", StateLeftover)
	oldEntry.Detail = "old detail"
	newEntry := diffFixtureEntry("Vcn", "vcn-1", StateLeftover)
	newEntry.Detail = "new detail"

	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{oldEntry}}
	newBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{newEntry}}

	got := Diff(oldBody, newBody)
	if len(got) != 1 {
		t.Fatalf("expected exactly one diff line for a same-State, different-Detail pair, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "old detail") || !strings.Contains(got[0], "new detail") {
		t.Errorf("expected diff line %q to name both old and new details", got[0])
	}
}

// TestDiff_IdenticalStateReasonDetailProducesEmptyDiff proves the unchanged half of WR-01's fix:
// a same-key pair identical across State, Reason, AND Detail still produces zero lines -- the
// fix must not turn Diff into a false-positive generator for genuinely unchanged entries.
func TestDiff_IdenticalStateReasonDetailProducesEmptyDiff(t *testing.T) {
	entry := diffFixtureEntry("Vcn", "vcn-1", StateLeftover)
	entry.Reason = scope.ReasonProtectedByTag
	entry.Detail = "identical detail"

	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{entry}}
	newBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{entry}}

	got := Diff(oldBody, newBody)
	if len(got) != 0 {
		t.Fatalf("expected empty diff for an identical State/Reason/Detail pair, got: %v", got)
	}
}

// TestDiff_DuplicateKeyWithinOneBodyReportsDiagnosticLine proves WR-05 (07-REVIEW.md): two
// entries sharing the identical composite key within a SINGLE body no longer silently collapse to
// one, invisible-to-the-operator entry -- Diff now emits an explicit "!"-prefixed diagnostic line
// naming the duplicate count, in addition to its normal (single, last-entry-wins) comparison
// behavior for that key.
func TestDiff_DuplicateKeyWithinOneBodyReportsDiagnosticLine(t *testing.T) {
	dupA := diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove)
	dupB := diffFixtureEntry("Vcn", "vcn-1", StateLeftover) // same key, different State
	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{dupA, dupB}}
	newBody := Body{SchemaVersion: SchemaVersion}

	got := Diff(oldBody, newBody)

	var sawDuplicateLine, sawMinusLine bool
	for _, line := range got {
		if line[0] == '!' && strings.Contains(line, "duplicate key") && strings.Contains(line, "2 entries") {
			sawDuplicateLine = true
		}
		if line[0] == '-' {
			sawMinusLine = true
		}
	}
	if !sawDuplicateLine {
		t.Errorf("expected a '!'-prefixed duplicate-key diagnostic line, got: %v", got)
	}
	if !sawMinusLine {
		t.Errorf("expected the normal '-'-prefixed removed-entry line for the (last-wins) key too, got: %v", got)
	}
}

// TestDiff_NoDuplicatesProducesNoDiagnosticLine proves WR-05's fix does not emit a spurious "!"
// line for the common case -- every existing Diff test in this file already has no duplicate
// keys, but this test names the property explicitly rather than relying on inference from
// unrelated assertions.
func TestDiff_NoDuplicatesProducesNoDiagnosticLine(t *testing.T) {
	a := diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove)
	b := diffFixtureEntry("Bucket", "bucket-1", StateWouldRemove)
	oldBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{a, b}}
	newBody := Body{SchemaVersion: SchemaVersion, Entries: []Entry{a, b}}

	got := Diff(oldBody, newBody)
	for _, line := range got {
		if line[0] == '!' {
			t.Errorf("expected no '!'-prefixed diagnostic line for a duplicate-free pair of bodies, got: %v", got)
		}
	}
}

// TestDiff_NeverMutatesInputs proves Diff builds its own copies -- neither old.Entries nor
// new.Entries is mutated as a side effect of the comparison, checked by len and content equality
// before and after the call.
func TestDiff_NeverMutatesInputs(t *testing.T) {
	oldEntries := []Entry{
		diffFixtureEntry("Bucket", "bucket-2", StateWouldRemove),
		diffFixtureEntry("Vcn", "vcn-1", StateWouldRemove),
	}
	newEntries := []Entry{
		diffFixtureEntry("Vcn", "vcn-1", StateLeftover),
		diffFixtureEntry("Instance", "instance-9", StateWouldRemove),
	}
	oldBody := Body{SchemaVersion: SchemaVersion, Entries: append([]Entry(nil), oldEntries...)}
	newBody := Body{SchemaVersion: SchemaVersion, Entries: append([]Entry(nil), newEntries...)}

	_ = Diff(oldBody, newBody)

	if len(oldBody.Entries) != len(oldEntries) {
		t.Fatalf("old.Entries length changed: got %d, want %d", len(oldBody.Entries), len(oldEntries))
	}
	for i := range oldEntries {
		if oldBody.Entries[i] != oldEntries[i] {
			t.Errorf("old.Entries[%d] mutated: got %+v, want %+v", i, oldBody.Entries[i], oldEntries[i])
		}
	}
	if len(newBody.Entries) != len(newEntries) {
		t.Fatalf("new.Entries length changed: got %d, want %d", len(newBody.Entries), len(newEntries))
	}
	for i := range newEntries {
		if newBody.Entries[i] != newEntries[i] {
			t.Errorf("new.Entries[%d] mutated: got %+v, want %+v", i, newBody.Entries[i], newEntries[i])
		}
	}
}
