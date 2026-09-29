package ocinuke_test

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// This file is the direct regression test for the 04-13 plan/apply divergence fix: protect-by-tag
// and min-age must be visible in the PLAN artifact a dry run produces, not only enforced once a
// destructive run actually reaches Remove() -- see pkg/ocinuke/scoped_lister.go's SafetyEvaluated
// doc comment for the libnuke@v1.3.0 source citation this fix is built on
// (Nuke.Run() returns immediately after Scan() whenever !NoDryRun, so Remove() is never reached
// at all during a dry run).

const (
	safetyScanRegion        = "us-ashburn-1"
	safetyScanCompartmentID = "ocid1.compartment.oc1..safety-scan-test"
	safetyScanOwner         = safetyScanRegion + "/" + safetyScanCompartmentID
	safetyScanResourceType  = "FixtureSafetyScan"
)

// safetyScanResource is a minimal ocinuke.SafetyEvaluated fixture: its own freeformTags/createdAt
// feed SafetyTags directly (no derived/computed values), so each test below can construct exactly
// the tag/age shape it needs to prove protected vs. unprotected.
type safetyScanResource struct {
	uid           string
	compartmentID string
	freeformTags  map[string]string
	createdAt     time.Time
	removed       *bool
}

// Remove flips removed to true -- every test below asserts this NEVER happens for the protected
// instance, in addition to asserting the plan artifact's own State/Reason, so a regression that
// somehow re-wired protection to a Remove()-time-only check (rather than genuinely restoring the
// pre-04-13 divergence) would still be caught even if the plan assertions were somehow wrong.
func (r *safetyScanResource) Remove(_ context.Context) error {
	if r.removed != nil {
		*r.removed = true
	}
	return nil
}
func (r *safetyScanResource) UniqueKey() string        { return r.uid }
func (r *safetyScanResource) GetCompartmentID() string { return r.compartmentID }
func (r *safetyScanResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return r.freeformTags, nil, r.createdAt
}

type safetyScanLister struct{ instances []*safetyScanResource }

func (l *safetyScanLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		out = append(out, inst)
	}
	return out, nil
}

// registerSafetyScanFixtures registers instances against a fresh registry, mirroring
// resources_test/equivalence_test.go's registerEquivalenceFixtures shape -- ocinuke.Register (not
// raw registry.Register) so the real, production scopedLister wrapping is exercised end to end.
func registerSafetyScanFixtures(t *testing.T, rec *eventRecorder, instances ...*safetyScanResource) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	restore := ocinuke.SetRunContext(func(string) bool { return true }, rec.report)
	t.Cleanup(restore)

	ocinuke.Register(&registry.Registration{
		Name:     safetyScanResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: instances[0],
		Lister:   &safetyScanLister{instances: instances},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

// newSafetyScanNuke builds a dry-run *libnuke.Nuke over safetyScanResourceType with safetyFilter
// installed on the shared ListerOpts every scanner (and therefore every scopedLister.List call)
// receives -- the exact production wiring path (pkg/commands/run's newCompartmentScanner threads
// pr.safetyFilter the same way).
func newSafetyScanNuke(t *testing.T, safetyFilter ocinuke.SafetyFilterConfig) *libnuke.Nuke {
	t.Helper()

	n := libnuke.New(&libnuke.Parameters{NoDryRun: false, Force: true, ForceSleep: 3}, filter.Filters{}, nil)

	s, err := scanner.New(&scanner.Config{
		Owner:         safetyScanOwner,
		ResourceTypes: []string{safetyScanResourceType},
		Opts: &ocinuke.ListerOpts{
			Region:        safetyScanRegion,
			CompartmentID: safetyScanCompartmentID,
			SafetyFilter:  safetyFilter,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}
	return n
}

// buildSafetyScanPlan runs n (a dry run, per newSafetyScanNuke) and returns the fully-merged plan
// entries -- plan.BuildFromQueue's own StateWouldRemove/StateFiltered output plus rec's
// scope.SkipEvent stream folded in via plan.MergeSkipEvents, exactly as
// pkg/commands/run.buildAndReportArtifact does for a real run.
func buildSafetyScanPlan(t *testing.T, n *libnuke.Nuke, rec *eventRecorder) []plan.Entry {
	t.Helper()
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) (dry run) returned error: %v", err)
	}
	entries := plan.BuildFromQueue(n.Queue.GetItems(), false, nil)
	return plan.MergeSkipEvents(entries, rec.snapshot())
}

// findSafetyScanEntry returns the plan.Entry for uid, or nil.
func findSafetyScanEntry(entries []plan.Entry, uid string) *plan.Entry {
	for i := range entries {
		if entries[i].ResourceID == uid {
			return &entries[i]
		}
	}
	return nil
}

// TestDryRunPlan_ProtectedByTagResourceNeverWouldRemove is the task's first required proof: a dry
// run with a protect-by-tag rule matching a fixture resource must NOT report that resource as
// would-remove in the built plan, and MUST report it with the protected-by-tag reason instead.
// unprotectedUID is this test's non-vacuous control -- it does NOT carry the matching tag, so it
// must appear as would-remove, proving the harness does not drop everything unconditionally.
func TestDryRunPlan_ProtectedByTagResourceNeverWouldRemove(t *testing.T) {
	const protectedUID = "ProtectedVolume-1"
	const unprotectedUID = "UnprotectedVolume-1"

	protectedRemoved := false
	unprotectedRemoved := false
	protected := &safetyScanResource{
		uid: protectedUID, compartmentID: safetyScanCompartmentID,
		freeformTags: map[string]string{testTagKeyRole: testTagValuePersistentVolume},
		removed:      &protectedRemoved,
	}
	unprotected := &safetyScanResource{
		uid: unprotectedUID, compartmentID: safetyScanCompartmentID,
		freeformTags: map[string]string{testTagKeyRole: "scratch"},
		removed:      &unprotectedRemoved,
	}

	rec := &eventRecorder{}
	registerSafetyScanFixtures(t, rec, protected, unprotected)

	safetyFilter := ocinuke.SafetyFilterConfig{
		ProtectTags: []ocinuke.TagMatch{{Key: testTagKeyRole, Value: testTagValuePersistentVolume}},
	}
	n := newSafetyScanNuke(t, safetyFilter)
	entries := buildSafetyScanPlan(t, n, rec)

	protectedEntry := findSafetyScanEntry(entries, protectedUID)
	if protectedEntry == nil {
		t.Fatalf("no plan entry found for %q -- non-vacuous check failed (entries: %+v)", protectedUID, entries)
	}
	if protectedEntry.State == plan.StateWouldRemove {
		t.Errorf("protected resource's plan entry State = %s, want anything but %s -- a protected "+
			"resource must never appear as would-remove in a dry-run plan", protectedEntry.State, plan.StateWouldRemove)
	}
	if protectedEntry.State != plan.StateSkipped {
		t.Errorf("protected resource's plan entry State = %s, want %s", protectedEntry.State, plan.StateSkipped)
	}
	if protectedEntry.Reason != scope.ReasonProtectedByTag {
		t.Errorf("protected resource's plan entry Reason = %q, want %q", protectedEntry.Reason, scope.ReasonProtectedByTag)
	}
	if protectedRemoved {
		t.Error("Remove() was called for the protected resource during a dry run -- must never happen")
	}

	unprotectedEntry := findSafetyScanEntry(entries, unprotectedUID)
	if unprotectedEntry == nil {
		t.Fatalf("no plan entry found for %q -- non-vacuous control check failed (entries: %+v)", unprotectedUID, entries)
	}
	if unprotectedEntry.State != plan.StateWouldRemove {
		t.Errorf("unprotected control's plan entry State = %s, want %s -- the harness must not drop "+
			"every resource unconditionally", unprotectedEntry.State, plan.StateWouldRemove)
	}
}

// TestDryRunPlan_MinAgeProtectsYoungerResource is the task's third required proof: the min-age
// equivalent of the protect-by-tag test above, deliberately exercising the inverted
// dateOlderThan polarity ocinuke.Evaluate documents -- dateOlderThan: "24h" protects resources
// YOUNGER than 24h, not older. youngKey was created 1 hour ago (younger than the 24h MinAge:
// protected). oldKey was created 30 days ago (much older than 24h: not protected, the
// non-vacuous would-remove control).
func TestDryRunPlan_MinAgeProtectsYoungerResource(t *testing.T) {
	const youngKey = "YoungVolume-1"
	const oldKey = "OldVolume-1"

	youngRemoved := false
	oldRemoved := false
	young := &safetyScanResource{
		uid: youngKey, compartmentID: safetyScanCompartmentID,
		createdAt: time.Now().Add(-1 * time.Hour),
		removed:   &youngRemoved,
	}
	old := &safetyScanResource{
		uid: oldKey, compartmentID: safetyScanCompartmentID,
		createdAt: time.Now().Add(-30 * 24 * time.Hour),
		removed:   &oldRemoved,
	}

	rec := &eventRecorder{}
	registerSafetyScanFixtures(t, rec, young, old)

	safetyFilter := ocinuke.SafetyFilterConfig{MinAge: 24 * time.Hour}
	n := newSafetyScanNuke(t, safetyFilter)
	entries := buildSafetyScanPlan(t, n, rec)

	youngEntry := findSafetyScanEntry(entries, youngKey)
	if youngEntry == nil {
		t.Fatalf("no plan entry found for %q -- non-vacuous check failed (entries: %+v)", youngKey, entries)
	}
	if youngEntry.State == plan.StateWouldRemove {
		t.Errorf("too-young resource's plan entry State = %s, want anything but %s -- a resource "+
			"younger than MinAge must never appear as would-remove", youngEntry.State, plan.StateWouldRemove)
	}
	if youngEntry.State != plan.StateSkipped {
		t.Errorf("too-young resource's plan entry State = %s, want %s", youngEntry.State, plan.StateSkipped)
	}
	if youngEntry.Reason != scope.ReasonTooYoung {
		t.Errorf("too-young resource's plan entry Reason = %q, want %q", youngEntry.Reason, scope.ReasonTooYoung)
	}
	if youngRemoved {
		t.Error("Remove() was called for the too-young resource during a dry run -- must never happen")
	}

	oldEntry := findSafetyScanEntry(entries, oldKey)
	if oldEntry == nil {
		t.Fatalf("no plan entry found for %q -- non-vacuous control check failed (entries: %+v)", oldKey, entries)
	}
	if oldEntry.State != plan.StateWouldRemove {
		t.Errorf("old resource control's plan entry State = %s, want %s -- MinAge must not protect a "+
			"resource far older than the configured duration", oldEntry.State, plan.StateWouldRemove)
	}
}
