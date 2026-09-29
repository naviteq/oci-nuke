package run

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naviteq/oci-nuke/pkg/plan"
)

// writeApprovedPlanFixture marshals artifact as JSON and writes it to a fresh temp file, returning
// the path -- the shared setup every loadApprovedPlan test in this file uses.
func writeApprovedPlanFixture(t *testing.T, artifact plan.Artifact) string {
	t.Helper()
	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshaling fixture artifact: %v", err)
	}
	path := filepath.Join(t.TempDir(), "approved-plan.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing fixture artifact to %s: %v", path, err)
	}
	return path
}

// TestLoadApprovedPlan_WithinMaxAgeReturnsArtifact proves a valid artifact file whose
// GeneratedAt is within --max-plan-age loads successfully.
func TestLoadApprovedPlan_WithinMaxAgeReturnsArtifact(t *testing.T) {
	body := plan.Body{SchemaVersion: plan.SchemaVersion}
	hash, err := plan.Hash(body)
	if err != nil {
		t.Fatalf("computing fixture hash: %v", err)
	}
	fixture := plan.Artifact{Body: body, Hash: hash, GeneratedAt: time.Now().UTC().Add(-1 * time.Hour)}
	path := writeApprovedPlanFixture(t, fixture)

	got, err := loadApprovedPlan(path, 24*time.Hour)
	if err != nil {
		t.Fatalf("expected nil error for a 1-hour-old artifact within a 24h max age, got: %v", err)
	}
	if got.Hash != fixture.Hash {
		t.Errorf("expected loaded artifact Hash %q, got %q", fixture.Hash, got.Hash)
	}
}

// TestLoadApprovedPlan_OlderThanMaxAgeErrors proves an artifact older than --max-plan-age is
// refused, naming the age and the limit, before any OCI API call could be made.
func TestLoadApprovedPlan_OlderThanMaxAgeErrors(t *testing.T) {
	body := plan.Body{SchemaVersion: plan.SchemaVersion}
	hash, err := plan.Hash(body)
	if err != nil {
		t.Fatalf("computing fixture hash: %v", err)
	}
	fixture := plan.Artifact{Body: body, Hash: hash, GeneratedAt: time.Now().UTC().Add(-48 * time.Hour)}
	path := writeApprovedPlanFixture(t, fixture)

	_, err = loadApprovedPlan(path, 24*time.Hour)
	if err == nil {
		t.Fatal("expected a non-nil error for a 48-hour-old artifact against a 24h max age")
	}
}

// TestLoadApprovedPlan_MissingFileErrors proves a missing --approved-plan path errors naming the
// path, wrapped for errors.Is(err, os.ErrNotExist) compatibility.
func TestLoadApprovedPlan_MissingFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")

	_, err := loadApprovedPlan(missing, 24*time.Hour)
	if err == nil {
		t.Fatal("expected a non-nil error for a missing approved-plan file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected err to satisfy errors.Is(err, os.ErrNotExist), got: %v", err)
	}
}

// TestLoadApprovedPlan_MalformedJSONErrors proves a malformed-JSON approved-plan file errors
// rather than panicking or silently returning a zero-value artifact.
func TestLoadApprovedPlan_MalformedJSONErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved-plan.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("writing malformed fixture: %v", err)
	}

	_, err := loadApprovedPlan(path, 24*time.Hour)
	if err == nil {
		t.Fatal("expected a non-nil error for a malformed-JSON approved-plan file")
	}
}

// wr02FixtureResourceType/wr02FixtureResourceID are the ResourceType/ResourceID
// TestLoadApprovedPlan_StaleHashRefused/TestLoadApprovedPlan_ConsistentHashAccepted's fixture
// entries share (goconst: each referenced twice in this file, avoiding a third raw literal
// alongside command_test.go's own unrelated "Vcn"/"vcn-1" fixture).
const (
	wr02FixtureResourceType = "Vcn"
	wr02FixtureResourceID   = "vcn-1"
)

// TestLoadApprovedPlan_StaleHashRefused is the WR-02 (07-REVIEW.md) regression test: an artifact
// whose stored Hash does not match plan.Hash(Body) -- e.g. a hand-edited Body whose Hash was never
// recomputed -- is refused, naming both the stored and the recomputed hash, rather than being
// silently trusted at face value.
func TestLoadApprovedPlan_StaleHashRefused(t *testing.T) {
	body := plan.Body{SchemaVersion: plan.SchemaVersion, Entries: []plan.Entry{{
		ResourceType: wr02FixtureResourceType, ResourceID: wr02FixtureResourceID, State: plan.StateWouldRemove,
	}}}
	correctHash, err := plan.Hash(body)
	if err != nil {
		t.Fatalf("computing fixture hash: %v", err)
	}

	// Hand-edit Body after computing Hash, mirroring an operator who tweaks one field of a local
	// approved-plan file without recomputing Hash -- fixture.Hash is now stale relative to Body.
	body.Entries[0].State = plan.StateLeftover
	staleFixture := plan.Artifact{Body: body, Hash: correctHash, GeneratedAt: time.Now().UTC()}
	path := writeApprovedPlanFixture(t, staleFixture)

	_, err = loadApprovedPlan(path, 24*time.Hour)
	if err == nil {
		t.Fatal("expected a non-nil error for an approved plan whose stored Hash does not match its own Body")
	}
	if !strings.Contains(err.Error(), "internally inconsistent") {
		t.Errorf("expected error to mention the artifact is internally inconsistent, got: %v", err)
	}
}

// TestLoadApprovedPlan_ConsistentHashAccepted proves the WR-02 fix's non-regression half: a
// normally-constructed artifact (Hash genuinely matching Body, the plan.NewArtifact-produced
// shape every real run writes) still loads successfully -- the new check must not reject the
// common case.
func TestLoadApprovedPlan_ConsistentHashAccepted(t *testing.T) {
	body := plan.Body{SchemaVersion: plan.SchemaVersion, Entries: []plan.Entry{{
		ResourceType: wr02FixtureResourceType, ResourceID: wr02FixtureResourceID, State: plan.StateWouldRemove,
	}}}
	fixture, err := plan.NewArtifact(body)
	if err != nil {
		t.Fatalf("building fixture artifact: %v", err)
	}
	path := writeApprovedPlanFixture(t, fixture)

	got, err := loadApprovedPlan(path, 24*time.Hour)
	if err != nil {
		t.Fatalf("expected nil error for an internally-consistent approved plan, got: %v", err)
	}
	if got.Hash != fixture.Hash {
		t.Errorf("expected loaded artifact Hash %q, got %q", fixture.Hash, got.Hash)
	}
}
