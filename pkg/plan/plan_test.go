package plan

import (
	"bytes"
	"testing"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	fixtureRegionFrankfurt = "eu-frankfurt-1"
	fixtureTypeVCN         = "vcn"
	fixtureTypeSubnet      = "Subnet"
	fixtureCompartmentA    = "comp-a"
	fixtureCompartmentB    = "comp-b"
)

// sharedFixtureEntries is the same logical set of entries used by every determinism test below.
// e1/e2 deliberately share every sort-key field except ResourceID, so a shuffle of their input
// order would genuinely surface a dropped ResourceID tiebreak in Sort.
func sharedFixtureEntries() (e1, e2, e3 Entry) {
	e1 = Entry{
		ResourceType:  fixtureTypeVCN,
		ResourceID:    "res-b",
		CompartmentID: fixtureCompartmentA,
		Region:        fixtureRegionFrankfurt,
		State:         StateWouldRemove,
	}
	e2 = Entry{
		ResourceType:  fixtureTypeVCN,
		ResourceID:    "res-a",
		CompartmentID: fixtureCompartmentA,
		Region:        fixtureRegionFrankfurt,
		State:         StateWouldRemove,
	}
	e3 = Entry{
		ResourceType:  "bucket",
		ResourceID:    "res-c",
		CompartmentID: fixtureCompartmentB,
		Region:        diffTestRegion,
		State:         StateFiltered,
	}
	return e1, e2, e3
}

func TestHash_ShuffleInputsProduceSameHash(t *testing.T) {
	e1, e2, e3 := sharedFixtureEntries()

	forward := Body{SchemaVersion: SchemaVersion, Entries: []Entry{e1, e2, e3}}
	reversed := Body{SchemaVersion: SchemaVersion, Entries: []Entry{e3, e2, e1}}

	hashForward, err := Hash(forward)
	if err != nil {
		t.Fatalf("Hash(forward) returned error: %v", err)
	}
	hashReversed, err := Hash(reversed)
	if err != nil {
		t.Fatalf("Hash(reversed) returned error: %v", err)
	}

	if hashForward != hashReversed {
		t.Fatalf("hash differs by input order: forward=%s reversed=%s", hashForward, hashReversed)
	}

	// Also exercise the "drained from two separately ranged maps" shape explicitly.
	byResourceID := map[string]Entry{e1.ResourceID: e1, e2.ResourceID: e2, e3.ResourceID: e3}
	byRegion := map[string]Entry{e1.Region + e1.ResourceID: e1, e2.Region + e2.ResourceID: e2, e3.Region + e3.ResourceID: e3}

	var fromMapA, fromMapB []Entry
	for _, e := range byResourceID {
		fromMapA = append(fromMapA, e)
	}
	for _, e := range byRegion {
		fromMapB = append(fromMapB, e)
	}

	hashMapA, err := Hash(Body{SchemaVersion: SchemaVersion, Entries: fromMapA})
	if err != nil {
		t.Fatalf("Hash(fromMapA) returned error: %v", err)
	}
	hashMapB, err := Hash(Body{SchemaVersion: SchemaVersion, Entries: fromMapB})
	if err != nil {
		t.Fatalf("Hash(fromMapB) returned error: %v", err)
	}
	if hashMapA != hashMapB || hashMapA != hashForward {
		t.Fatalf("hash differs across map-ranged construction: mapA=%s mapB=%s literal=%s", hashMapA, hashMapB, hashForward)
	}
}

func TestHash_GoldenFile(t *testing.T) {
	fixture := Body{
		SchemaVersion: 1,
		Entries: []Entry{
			{
				ResourceType:  fixtureTypeVCN,
				ResourceID:    "ocid1.vcn.oc1..aaaa",
				CompartmentID: "ocid1.compartment.oc1..bbbb",
				Region:        fixtureRegionFrankfurt,
				State:         StateWouldRemove,
			},
			{
				ResourceType:  "bucket",
				ResourceID:    "my-bucket",
				CompartmentID: "ocid1.compartment.oc1..bbbb",
				Region:        diffTestRegion,
				State:         StateLeftover,
				Reason:        scope.ReasonAPIError,
				Detail:        "transient 500 from object storage",
			},
		},
	}

	// Hash covers DecisionBody, which drops Detail (ADR-0004): the leftover's free-text detail is
	// not part of these bytes, its reason is.
	const expectedHash = "62cee8836f589ab33b1b1d086d32c0b7c3f31c5a73167bca182b9dec53eda4b4"

	got, err := Hash(fixture)
	if err != nil {
		t.Fatalf("Hash(fixture) returned error: %v", err)
	}
	if got != expectedHash {
		t.Fatalf("golden hash mismatch (field ordering, escaping, or struct tags changed): got %s, want %s", got, expectedHash)
	}
}

func TestCanonicalJSON_DeterministicAcrossRuns(t *testing.T) {
	e1, e2, e3 := sharedFixtureEntries()

	first := Body{SchemaVersion: SchemaVersion, Entries: []Entry{e1, e2, e3}}
	second := Body{SchemaVersion: SchemaVersion, Entries: []Entry{e3, e1, e2}}

	bytesFirst, err := CanonicalJSON(first)
	if err != nil {
		t.Fatalf("CanonicalJSON(first) returned error: %v", err)
	}
	bytesSecond, err := CanonicalJSON(second)
	if err != nil {
		t.Fatalf("CanonicalJSON(second) returned error: %v", err)
	}

	if !bytes.Equal(bytesFirst, bytesSecond) {
		t.Fatalf("canonical JSON bytes differ by input order:\nfirst:  %s\nsecond: %s", bytesFirst, bytesSecond)
	}
}

func TestCanonicalJSON_DoesNotMutateCallerOrder(t *testing.T) {
	e1, e2, e3 := sharedFixtureEntries()

	body := Body{SchemaVersion: SchemaVersion, Entries: []Entry{e3, e1, e2}}
	original := append([]Entry(nil), body.Entries...)

	if _, err := CanonicalJSON(body); err != nil {
		t.Fatalf("CanonicalJSON returned error: %v", err)
	}

	if len(body.Entries) != len(original) {
		t.Fatalf("caller's entries length changed: got %d, want %d", len(body.Entries), len(original))
	}
	for i := range original {
		if body.Entries[i] != original[i] {
			t.Fatalf("caller's entry order mutated at index %d: got %+v, want %+v", i, body.Entries[i], original[i])
		}
	}
}

// sandboxDriftBodies reproduces a refused apply in miniature: the same decision set, scanned
// four minutes apart, with only non-decision entries differing -- a deferred compartment's
// blocker prose and a stream pool that aged out of too-young into filtered.
func sandboxDriftBodies() (approved, rescanned Body) {
	vcn := Entry{
		ResourceType: "Vcn", ResourceID: "vcn-1", CompartmentID: fixtureCompartmentA, Region: fixtureRegionFrankfurt,
		State: StateWouldRemove,
	}
	deferred := Entry{
		ResourceType: compartmentEntryResourceType, ResourceID: fixtureCompartmentB, CompartmentID: fixtureCompartmentB,
		Region: diffTestRegion,
		State:  StateSkipped, Reason: scope.ReasonCompartmentNotEmpty, Detail: "will still hold Vault v; VaultSecret s",
	}
	pool := Entry{
		ResourceType: "StreamPool", ResourceID: "pool-1", CompartmentID: fixtureCompartmentA,
		State: StateSkipped, Reason: scope.ReasonTooYoung,
	}

	approved = Body{SchemaVersion: SchemaVersion, Entries: []Entry{vcn, deferred, pool}}

	deferred.Detail = "will still hold VaultSecret s; Vault v"
	pool.Region, pool.State, pool.Reason, pool.Detail = fixtureRegionFrankfurt, StateFiltered, "", "StreamPool is DELETING"
	rescanned = Body{SchemaVersion: SchemaVersion, Entries: []Entry{pool, deferred, vcn}}
	return approved, rescanned
}

func mustHash(t *testing.T, body Body) string {
	t.Helper()
	h, err := Hash(body)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}

func TestHash_IgnoresNonDecisionDrift(t *testing.T) {
	approved, rescanned := sandboxDriftBodies()
	if a, r := mustHash(t, approved), mustHash(t, rescanned); a != r {
		t.Fatalf("non-decision drift changed the hash: approved=%s rescanned=%s\n%v", a, r,
			Diff(DecisionBody(approved), DecisionBody(rescanned)))
	}
}

// TestHash_ChangesWhenTheDecisionSetChanges is the half that keeps the gate a gate: anything that
// changes what the run would delete -- or which compartments it would call Remove() on -- must
// still change the hash.
func TestHash_ChangesWhenTheDecisionSetChanges(t *testing.T) {
	approved, _ := sandboxDriftBodies()
	base := mustHash(t, approved)

	mutations := map[string]func(b *Body){
		"skipped resource becomes would-remove": func(b *Body) {
			b.Entries[2].State, b.Entries[2].Reason = StateWouldRemove, ""
		},
		"would-remove resource disappears": func(b *Body) { b.Entries = b.Entries[1:] },
		"would-remove resource becomes filtered": func(b *Body) {
			b.Entries[0].State = StateFiltered
		},
		"new would-remove resource appears": func(b *Body) {
			b.Entries = append(b.Entries, Entry{
				ResourceType: fixtureTypeSubnet, ResourceID: "sn-1", CompartmentID: fixtureCompartmentA, State: StateWouldRemove,
			})
		},
		"deferred compartment becomes would-remove": func(b *Body) {
			b.Entries[1].State, b.Entries[1].Reason, b.Entries[1].Detail = StateWouldRemove, "", ""
		},
		"would-remove resource moves region": func(b *Body) { b.Entries[0].Region = diffTestRegion },
		"unclassified future state is hashed": func(b *Body) {
			b.Entries = append(b.Entries, Entry{
				ResourceType: fixtureTypeSubnet, ResourceID: "sn-2", CompartmentID: fixtureCompartmentA, State: "failed",
			})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := Body{SchemaVersion: approved.SchemaVersion, Entries: append([]Entry(nil), approved.Entries...)}
			mutate(&b)
			if mustHash(t, b) == base {
				t.Fatalf("hash unchanged after %q", name)
			}
		})
	}
}
