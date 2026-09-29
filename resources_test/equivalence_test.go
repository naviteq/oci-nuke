package ocinuke_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
)

const (
	equivalenceRegion        = "us-ashburn-1"
	equivalenceCompartmentID = "ocid1.compartment.oc1..equivalence-test"
	equivalenceOwner         = equivalenceRegion + "/" + equivalenceCompartmentID

	equivalenceTypeA = "FakeResourceA"
	equivalenceTypeB = "FakeResourceB"

	// wantResourceCount is the fixture's total resource count across both types (1 from
	// FakeResourceA + 2 from FakeResourceB) -- both plannedSet and attemptedSet must equal this,
	// not merely equal each other, or the test would pass vacuously on two empty sets.
	wantResourceCount = 3
)

// recorder is a shared, mutex-guarded []string accumulator that a call-recording Remove()
// appends its own UniqueKey() into -- this is what lets TestPlanApplyEquivalence's Run B know
// WHICH resources were attempted, not merely how many, mirroring 03-RESEARCH.md Q5's explicit
// requirement that "attempted" be derived from resource identity, not a bare counter.
type recorder struct {
	mu    sync.Mutex
	items []string
}

func (r *recorder) record(uid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, uid)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.items...)
}

// recordingResource implements ocinuke.CompartmentScoped (resource.Resource + GetCompartmentID)
// and resource.UniqueKeyGetter. Remove() records its own UniqueKey() into the shared recorder
// and flips removed to true before returning nil -- it always succeeds, since this test is not
// exercising failure handling (that is resources_test/leftover_queue_test.go's job). removed is
// a *bool, checked by recordingLister on every re-list (mirroring dryrun_test.go's fakeLister),
// so libnuke's HandleWait re-list converges to ItemStateFinished instead of seeing the same
// "still present" resource forever.
type recordingResource struct {
	uid           string
	compartmentID string
	recorder      *recorder
	removed       *bool
	// freeformTags/createdAt feed SafetyTags below -- both zero-valued (nil / time.Time{}) unless
	// a test sets them, so a recordingResource is never protected by default, preserving every
	// pre-existing test's behavior. TestPlanApplyEquivalence_WithProtections is the one test that
	// sets freeformTags to actually exercise a protect-by-tag match.
	freeformTags map[string]string
	createdAt    time.Time
}

func (r *recordingResource) Remove(_ context.Context) error {
	r.recorder.record(r.uid)
	*r.removed = true
	return nil
}

func (r *recordingResource) UniqueKey() string        { return r.uid }
func (r *recordingResource) GetCompartmentID() string { return r.compartmentID }
func (r *recordingResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return r.freeformTags, nil, r.createdAt
}

// recordingLister returns every instance whose removed flag is not yet set -- one lister per
// registry.Registration.Name, mirroring dryrun_test.go's fakeLister shape.
type recordingLister struct {
	instances []*recordingResource
}

func (l *recordingLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// registerEquivalenceFixtures registers the SAME two fake resource type definitions (same Names,
// same UniqueKey()s) but against a FRESH registry.ClearRegistry() and a FRESH recorder -- called
// once per run (Run A, Run B) so neither run's queue/recorder state leaks into the other.
// Registered via ocinuke.Register (not raw registry.Register) so the real, production
// scopedLister wrapping is exercised end to end -- inScope is unconditionally true since
// scope-membership is not what this test is about.
func registerEquivalenceFixtures(t *testing.T, rec *recorder) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	inScope := func(string) bool { return true }

	fakeA1 := &recordingResource{uid: "FakeResourceA-1", compartmentID: equivalenceCompartmentID, recorder: rec, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:     equivalenceTypeA,
		Scope:    ocinuke.CompartmentScope,
		Resource: fakeA1,
		Lister:   &recordingLister{instances: []*recordingResource{fakeA1}},
	}, inScope, nil)

	fakeB1 := &recordingResource{uid: "FakeResourceB-1", compartmentID: equivalenceCompartmentID, recorder: rec, removed: new(bool)}
	fakeB2 := &recordingResource{uid: "FakeResourceB-2", compartmentID: equivalenceCompartmentID, recorder: rec, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:     equivalenceTypeB,
		Scope:    ocinuke.CompartmentScope,
		Resource: fakeB1,
		Lister:   &recordingLister{instances: []*recordingResource{fakeB1, fakeB2}},
	}, inScope, nil)
}

// newEquivalenceNuke mirrors dryrun_test.go's newTestNuke construction shape exactly, adapted for
// two registered resource types instead of one.
func newEquivalenceNuke(t *testing.T, params *libnuke.Parameters) *libnuke.Nuke {
	t.Helper()

	n := libnuke.New(params, filter.Filters{}, nil)

	s, err := scanner.New(&scanner.Config{
		Owner:         equivalenceOwner,
		ResourceTypes: []string{equivalenceTypeA, equivalenceTypeB},
		Opts:          &ocinuke.ListerOpts{Region: equivalenceRegion, CompartmentID: equivalenceCompartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPlanApplyEquivalence proves 03-RESEARCH.md Q5's headline property: a dry run's planned set
// of UniqueKey()s and a destructive run's attempted-Remove() set of UniqueKey()s are equal, each
// derived from its own independent, real libnuke.Nuke.Run() call over the same fake resource
// registrations -- never from one shared in-memory list, which would prove nothing.
//
// Genuine-failure check (not merely a doc claim): if Run A's registerEquivalenceFixtures call
// were replaced by one that registers only equivalenceTypeA (omitting equivalenceTypeB),
// plannedSet would contain 1 element ("FakeResourceA-1") while attemptedSet would still contain
// all 3 ("FakeResourceA-1", "FakeResourceB-1", "FakeResourceB-2") -- the cardinality guard below
// and the set-equality assertion would both fail. This is not a tautology.
func TestPlanApplyEquivalence(t *testing.T) {
	ctx := context.Background()

	// Run A: dry run. The "planned set" is the set of UniqueKey()s from the REAL production
	// pkg/plan writer (plan.BuildFromQueue), filtered to State == StateWouldRemove.
	recA := &recorder{}
	registerEquivalenceFixtures(t, recA)
	nA := newEquivalenceNuke(t, &libnuke.Parameters{NoDryRun: false, Force: true, ForceSleep: 3})
	if err := nA.Run(ctx); err != nil {
		t.Fatalf("Run A (dry run) returned error: %v", err)
	}

	plannedEntries := plan.BuildFromQueue(nA.Queue.GetItems(), false, nil)
	plannedSet := map[string]struct{}{}
	for _, e := range plannedEntries {
		if e.State != plan.StateWouldRemove {
			continue
		}
		plannedSet[e.ResourceID] = struct{}{}
	}

	// Run B: a completely fresh registry + fresh recorder + fresh *libnuke.Nuke, NoDryRun: true.
	// The "attempted set" is every UniqueKey() the call-recording Remove() actually recorded --
	// not merely every item that ended ItemStateFinished (see this test's doc comment on why that
	// distinction matters). n.Run(ctx)'s own error is not the point of this test -- the fixture
	// resources all succeed, so it should be nil, but the assertion below is on the recorder, not
	// on Run's return value.
	recB := &recorder{}
	registerEquivalenceFixtures(t, recB)
	nB := newEquivalenceNuke(t, &libnuke.Parameters{NoDryRun: true, Force: true, ForceSleep: 3})
	nB.SetRunSleep(1 * time.Millisecond) // libnuke's runSleep defaults to 5s; keep the test fast
	if err := nB.Run(ctx); err != nil {
		t.Logf("Run B (destructive run) returned error: %v (not asserted on -- recorder is the source of truth)", err)
	}

	attemptedSet := map[string]struct{}{}
	for _, key := range recB.snapshot() {
		attemptedSet[key] = struct{}{}
	}

	// Non-vacuousness guard: both sets must have the fixture's full resource count, not merely
	// equal each other -- two empty sets would trivially satisfy reflect.DeepEqual below.
	if len(plannedSet) != wantResourceCount {
		t.Fatalf("plannedSet has %d elements, want %d (non-vacuousness guard): %v", len(plannedSet), wantResourceCount, plannedSet)
	}
	if len(attemptedSet) != wantResourceCount {
		t.Fatalf("attemptedSet has %d elements, want %d (non-vacuousness guard): %v", len(attemptedSet), wantResourceCount, attemptedSet)
	}

	if !reflect.DeepEqual(plannedSet, attemptedSet) {
		t.Fatalf("plannedSet %v != attemptedSet %v -- the dry-run plan does not match what the "+
			"destructive run actually attempted", plannedSet, attemptedSet)
	}
}

// protectionEquivalenceType names the third fixture type TestPlanApplyEquivalence_WithProtections
// registers, alongside a fresh copy of both equivalenceTypeA/equivalenceTypeB fixtures --
// deliberately keeping the original two types' own contribution to plannedSet/attemptedSet
// unchanged, so this test only ever adds new assertions, never removes coverage the original
// TestPlanApplyEquivalence already had.
const protectionEquivalenceType = "FakeResourceProtected"

// testTagKeyRole / testTagValuePersistentVolume are the protect-by-tag key/value 04-13's live
// dry-run verification uses against the real `demo` compartment's OKE BlockVolumes
// (role=persistent_volume) -- shared across this file and safety_scan_test.go so neither literal
// is ever repeated (goconst, min-occurrences: 3).
const testTagKeyRole = "role"
const testTagValuePersistentVolume = "persistent_volume"

// protectionEquivalenceSafetyFilter is the run-wide SafetyFilterConfig both Run A and Run B below
// install identically -- protects role=persistent_volume, the exact shape this project's live
// dry-run verification (04-13) uses against the real `demo` compartment's OKE BlockVolumes.
var protectionEquivalenceSafetyFilter = ocinuke.SafetyFilterConfig{
	ProtectTags: []ocinuke.TagMatch{{Key: testTagKeyRole, Value: testTagValuePersistentVolume}},
}

// registerProtectionEquivalenceFixtures registers equivalenceTypeA/equivalenceTypeB exactly as
// registerEquivalenceFixtures does (so this test's non-vacuousness guard below can still require
// wantResourceCount from those two types alone), PLUS a third type with one protected instance
// (role=persistent_volume, matching protectionEquivalenceSafetyFilter) and one unprotected
// instance (role=scratch) -- the protected/unprotected pair this test's assertions are about.
func registerProtectionEquivalenceFixtures(t *testing.T, rec *recorder) (protectedUID, unprotectedUID string) {
	t.Helper()
	registerEquivalenceFixtures(t, rec)

	inScope := func(string) bool { return true }

	protected := &recordingResource{
		uid: "FakeResourceProtected-persistent", compartmentID: equivalenceCompartmentID,
		recorder: rec, removed: new(bool),
		freeformTags: map[string]string{testTagKeyRole: testTagValuePersistentVolume},
	}
	unprotected := &recordingResource{
		uid: "FakeResourceProtected-scratch", compartmentID: equivalenceCompartmentID,
		recorder: rec, removed: new(bool),
		freeformTags: map[string]string{testTagKeyRole: "scratch"},
	}
	ocinuke.Register(&registry.Registration{
		Name:     protectionEquivalenceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: protected,
		Lister:   &recordingLister{instances: []*recordingResource{protected, unprotected}},
	}, inScope, nil)

	return protected.uid, unprotected.uid
}

// newProtectionEquivalenceNuke mirrors newEquivalenceNuke, adding protectionEquivalenceType to
// the scanned resource types and protectionEquivalenceSafetyFilter to the shared ListerOpts every
// scanner (hence every registered type, per scopedLister.List's own contract) receives.
func newProtectionEquivalenceNuke(t *testing.T, params *libnuke.Parameters) *libnuke.Nuke {
	t.Helper()

	n := libnuke.New(params, filter.Filters{}, nil)

	s, err := scanner.New(&scanner.Config{
		Owner:         equivalenceOwner,
		ResourceTypes: []string{equivalenceTypeA, equivalenceTypeB, protectionEquivalenceType},
		Opts: &ocinuke.ListerOpts{
			Region: equivalenceRegion, CompartmentID: equivalenceCompartmentID,
			SafetyFilter: protectionEquivalenceSafetyFilter,
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

// TestPlanApplyEquivalence_WithProtections is TestPlanApplyEquivalence's sibling with protections
// active (04-13 plan/apply divergence fix) -- the assertion whose absence let the original bug
// through: a protected resource must be absent from BOTH the planned set (dry run) AND the
// attempted set (destructive run), not merely absent from one or the other. Reverting the 04-13
// fix (moving protect-by-tag back to a Remove()-time-only check) would make this test fail on
// Run A alone -- FakeResourceProtected-persistent would appear in plannedSet as would-remove,
// even though Run B would still correctly never attempt it -- which is exactly the divergence
// TestPlanApplyEquivalence's original, no-protections form could never catch.
func TestPlanApplyEquivalence_WithProtections(t *testing.T) {
	ctx := context.Background()

	// Run A: dry run.
	recA := &recorder{}
	protectedUID, unprotectedUID := registerProtectionEquivalenceFixtures(t, recA)
	nA := newProtectionEquivalenceNuke(t, &libnuke.Parameters{NoDryRun: false, Force: true, ForceSleep: 3})
	if err := nA.Run(ctx); err != nil {
		t.Fatalf("Run A (dry run) returned error: %v", err)
	}

	plannedEntries := plan.BuildFromQueue(nA.Queue.GetItems(), false, nil)
	plannedSet := map[string]struct{}{}
	for _, e := range plannedEntries {
		if e.State != plan.StateWouldRemove {
			continue
		}
		plannedSet[e.ResourceID] = struct{}{}
	}

	// Run B: destructive run, fresh registry + fresh recorder + fresh *libnuke.Nuke.
	recB := &recorder{}
	registerProtectionEquivalenceFixtures(t, recB)
	nB := newProtectionEquivalenceNuke(t, &libnuke.Parameters{NoDryRun: true, Force: true, ForceSleep: 3})
	nB.SetRunSleep(1 * time.Millisecond)
	if err := nB.Run(ctx); err != nil {
		t.Logf("Run B (destructive run) returned error: %v (not asserted on -- recorder is the source of truth)", err)
	}

	attemptedSet := map[string]struct{}{}
	for _, key := range recB.snapshot() {
		attemptedSet[key] = struct{}{}
	}

	// Non-vacuousness guard: both sets must contain the ORIGINAL wantResourceCount fixtures
	// (equivalenceTypeA/B) plus exactly the ONE unprotected FakeResourceProtected instance --
	// never the protected one.
	wantCount := wantResourceCount + 1
	if len(plannedSet) != wantCount {
		t.Fatalf("plannedSet has %d elements, want %d (non-vacuousness guard): %v", len(plannedSet), wantCount, plannedSet)
	}
	if len(attemptedSet) != wantCount {
		t.Fatalf("attemptedSet has %d elements, want %d (non-vacuousness guard): %v", len(attemptedSet), wantCount, attemptedSet)
	}

	if !reflect.DeepEqual(plannedSet, attemptedSet) {
		t.Fatalf("plannedSet %v != attemptedSet %v -- a protected resource must be equally absent "+
			"from both, never present in one but not the other", plannedSet, attemptedSet)
	}

	if _, ok := plannedSet[protectedUID]; ok {
		t.Errorf("plannedSet contains %q -- a protected resource must never appear as would-remove in the plan", protectedUID)
	}
	if _, ok := attemptedSet[protectedUID]; ok {
		t.Errorf("attemptedSet contains %q -- a protected resource must never be attempted", protectedUID)
	}
	if _, ok := plannedSet[unprotectedUID]; !ok {
		t.Errorf("plannedSet does not contain %q -- the non-vacuous unprotected control must still be planned", unprotectedUID)
	}
	if _, ok := attemptedSet[unprotectedUID]; !ok {
		t.Errorf("attemptedSet does not contain %q -- the non-vacuous unprotected control must still be attempted", unprotectedUID)
	}
}
