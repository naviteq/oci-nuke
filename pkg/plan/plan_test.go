package plan

import (
	"bytes"
	"testing"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	fixtureRegionFrankfurt = "eu-frankfurt-1"
	fixtureTypeVCN         = "vcn"
)

// sharedFixtureEntries is the same logical set of entries used by every determinism test below.
// e1/e2 deliberately share every sort-key field except ResourceID, so a shuffle of their input
// order would genuinely surface a dropped ResourceID tiebreak in Sort.
func sharedFixtureEntries() (e1, e2, e3 Entry) {
	e1 = Entry{
		ResourceType:  fixtureTypeVCN,
		ResourceID:    "res-b",
		CompartmentID: "comp-a",
		Region:        fixtureRegionFrankfurt,
		State:         StateWouldRemove,
	}
	e2 = Entry{
		ResourceType:  fixtureTypeVCN,
		ResourceID:    "res-a",
		CompartmentID: "comp-a",
		Region:        fixtureRegionFrankfurt,
		State:         StateWouldRemove,
	}
	e3 = Entry{
		ResourceType:  "bucket",
		ResourceID:    "res-c",
		CompartmentID: "comp-b",
		Region:        "us-ashburn-1",
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
				Region:        "us-ashburn-1",
				State:         StateLeftover,
				Reason:        scope.ReasonAPIError,
				Detail:        "transient 500 from object storage",
			},
		},
	}

	const expectedHash = "10e0eb79515027136489503f084869c24f637adc1ad738803eebe17b976b90ca"

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
