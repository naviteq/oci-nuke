package ocinuke_test

import (
	"context"
	"fmt"
	"reflect"
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

// This file is Plan 05-07's wave-level integration proof: it registers a small, representative
// cross-section of synthetic fixture types -- never the real Cluster/NodePool/Vault/
// AutonomousDatabase types this wave's six domain plans registered -- exercising every genuinely
// NEW run-control-flow mechanic Wave 5 introduced, end to end through a real libnuke.Nuke.Run(),
// mirroring resources_test/equivalence_test.go's own registerXFixtures/newXNuke/real-Run()
// convention (never a synthetic in-memory list that would prove nothing about the actual queue/
// filter/scan control flow):
//
//  1. Cross-type DependsOn ordering (Cluster->NodePool, Vault->KmsKey, ... in production):
//     FakeOKENodePool/FakeOKECluster, a real DependsOn edge, WaitOnDependencies: true.
//  2. Global-geography exclusion (IAM's BeforeList(ocinuke.Global) in production):
//     FakeGlobalIAMResource, scanned from a non-home-region scanner construction.
//  3. ReasonScheduledDeletion exclusion (Vault/KmsKey's PENDING_DELETION Filter() in production):
//     FakeScheduledDeletionVault, excluded AND reported in the same Filter() branch -- unlike
//     resources_test/scheduled_deletion_test.go's own scheduledDeletionResource (which reports
//     from Remove(), a shape that could never prove absence from the DRY-RUN planned set, since
//     Remove() is never reached during a dry run), this fixture mirrors resources/vault.go's real
//     Filter()-time exclude-and-report shape instead, documented in Deviations below.
//  4. ReasonBackupResidue reporting (the Database plan's List()-time side effect in production):
//     FakeAutonomousDatabaseWave5, whose Lister reports a synthetic backup as a side effect of
//     its own List() call, exactly resources/database_backup_support.go's own mechanism.

const (
	wave5Region        = "eu-frankfurt-1" // deliberately NOT wave5HomeRegion -- mechanic 2's whole point.
	wave5HomeRegion    = "us-ashburn-1"
	wave5CompartmentID = "ocid1.compartment.oc1..wave5-equivalence-test"
	wave5Owner         = wave5Region + "/" + wave5CompartmentID

	fakeOKENodePoolType = "FakeOKENodePool"
	fakeOKENodePoolKey  = "FakeOKENodePool-1"
	fakeOKEClusterType  = "FakeOKECluster"
	fakeOKEClusterKey   = "FakeOKECluster-1"

	fakeGlobalIAMType = "FakeGlobalIAMResource"
	fakeGlobalIAMKey  = "FakeGlobalIAMResource-1"

	fakeScheduledDeletionVaultType = "FakeScheduledDeletionVault"
	fakeScheduledDeletionVaultKey  = "fake-scheduled-deletion-vault-1"

	fakeBackupResiduePrimaryType = "FakeAutonomousDatabaseWave5"
	fakeBackupResiduePrimaryKey  = "fake-autonomous-database-wave5-1"
	fakeBackupResidueBackupType  = "AutonomousDatabaseBackupWave5"
	fakeBackupResidueBackupID    = "fake-autonomous-database-wave5-backup-1"
	fakeBackupResidueDetail      = "automatic post-termination backup, not deleted by this tool"

	// wantWave5ResourceCount is this file's non-vacuousness guard, mirroring equivalence_test.go's
	// own wantResourceCount: FakeOKENodePool-1 + FakeOKECluster-1 + FakeAutonomousDatabaseWave5-1
	// = 3. Neither FakeGlobalIAMResource-1 (excluded by BeforeList(ocinuke.Global), never even
	// queued) nor FakeScheduledDeletionVault (excluded by its own Filter()) ever becomes a
	// would-remove/attempted candidate -- proven explicitly below, not merely implied by this
	// count.
	wantWave5ResourceCount = 3
)

// wave5GlobalResource models mechanic 2: a Global-geography resource type, mirroring
// resources/policy.go's real shape. Remove() is intentionally never expected to be called --
// if it ever is, that is this fixture's own proof failing, since BeforeList(ocinuke.Global)
// should have excluded it at List() time before it could ever become a queue.Item.
type wave5GlobalResource struct {
	uid           string
	compartmentID string
}

func (r *wave5GlobalResource) Remove(_ context.Context) error { return nil }
func (r *wave5GlobalResource) UniqueKey() string              { return r.uid }
func (r *wave5GlobalResource) GetCompartmentID() string       { return r.compartmentID }
func (r *wave5GlobalResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// wave5GlobalLister's List() opens with o.BeforeList(ocinuke.Global), exactly the first statement
// every real Wave-5 IAM Lister's List() calls (resources_test/iam_global_geography_test.go proves
// this structurally for the real files; this fixture proves the RUNTIME BEHAVIOR that guard
// produces, end to end through libnuke's own scanner.list(), which is a different, complementary
// proof).
type wave5GlobalLister struct{ instance *wave5GlobalResource }

func (l *wave5GlobalLister) List(_ context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	return []resource.Resource{l.instance}, nil
}

// wave5ScheduledDeletionResource models mechanic 3: a resource whose Filter() excludes it AND
// reports scope.ReasonScheduledDeletion in the SAME branch, mirroring resources/vault.go's real
// Vault.Filter()/KmsKey.Filter() shape (05-06-SUMMARY.md's own documented deviation from
// scheduled_deletion_test.go's Remove()-time-reporting shape) -- never a redundant Remove()-time
// recheck. This is the mechanic that lets the resource be absent from BOTH the dry-run planned
// set AND the destructive-run attempted set: Filter() runs identically during Scan() in both dry
// and destructive modes (Remove() does not -- a dry run never reaches it), so only a Filter()-time
// exclusion can produce that property. Remove() below is defensive/unreachable in normal
// operation, matching resources/vault.go's own Remove() being unreachable once Filter() has
// already excluded every instance this fixture will ever list.
type wave5ScheduledDeletionResource struct {
	uid           string
	compartmentID string
	removed       *bool
}

func (r *wave5ScheduledDeletionResource) Remove(_ context.Context) error {
	*r.removed = true
	return nil
}
func (r *wave5ScheduledDeletionResource) UniqueKey() string        { return r.uid }
func (r *wave5ScheduledDeletionResource) GetCompartmentID() string { return r.compartmentID }
func (r *wave5ScheduledDeletionResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

func (r *wave5ScheduledDeletionResource) Filter() error {
	ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
		Reason:        scope.ReasonScheduledDeletion,
		ResourceType:  fakeScheduledDeletionVaultType,
		ResourceID:    r.uid,
		CompartmentID: r.compartmentID,
		Detail:        "scheduled for deletion in 7 days",
	})
	return fmt.Errorf("FakeScheduledDeletionVault %s is scheduled for deletion", r.uid)
}

type wave5ScheduledDeletionLister struct {
	instance *wave5ScheduledDeletionResource
}

func (l *wave5ScheduledDeletionLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{l.instance}, nil
}

// wave5BackupResidueLister models mechanic 4: List() returns the primary resource AND, as a side
// effect the first time it runs, reports one synthetic backup residue SkipEvent -- exactly
// resources/database_backup_support.go's own mechanism, wired into
// resources/autonomous_database.go's List(). primary is a *recordingResource (the same fixture
// type equivalence_test.go's mechanics 1 use), so its own Remove() call feeds the SAME shared
// recorder used for the DependsOn-ordering proof below, letting this file prove the planned/
// attempted set equivalence and the backup-residue mechanic together, in one run, rather than two
// disconnected ones.
type wave5BackupResidueLister struct {
	primary  *recordingResource
	reported *bool
}

func (l *wave5BackupResidueLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if !*l.reported {
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonBackupResidue,
			ResourceType:  fakeBackupResidueBackupType,
			ResourceID:    fakeBackupResidueBackupID,
			CompartmentID: wave5CompartmentID,
			Detail:        fakeBackupResidueDetail,
		})
		*l.reported = true
	}
	if l.primary.removed != nil && *l.primary.removed {
		return nil, nil
	}
	return []resource.Resource{l.primary}, nil
}

// registerWave5Fixtures registers all four fixtures against a fresh registry, using
// ocinuke.SetRunContext + ocinuke.CurrentScope/ocinuke.CurrentReporter -- the SAME
// function-value-indirection wiring every real resources/*.go init() uses (CONTRIBUTING.md
// section 4), not a locally-constructed closure -- so this file proves this wave's mechanics
// through the exact run-context plumbing production uses, mirroring
// resources_test/wave_convergence_test.go's own registerWaveConvergenceFixtures precedent.
// inScope is unconditionally true; scope membership is not what this file is about.
func registerWave5Fixtures(t *testing.T, rec *recorder, evtRec *eventRecorder) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	restore := ocinuke.SetRunContext(func(string) bool { return true }, evtRec.report)
	t.Cleanup(restore)

	// Mechanic 1: DependsOn ordering. FakeOKECluster declares DependsOn: [FakeOKENodePool],
	// mirroring resources/cluster.go's real DependsOn: ["NodePool"] -- declared by the dependent,
	// never the dependency, per CONTRIBUTING.md section 4.
	nodePool := &recordingResource{uid: fakeOKENodePoolKey, compartmentID: wave5CompartmentID, recorder: rec, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:     fakeOKENodePoolType,
		Scope:    ocinuke.CompartmentScope,
		Resource: nodePool,
		Lister:   &recordingLister{instances: []*recordingResource{nodePool}},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	cluster := &recordingResource{uid: fakeOKEClusterKey, compartmentID: wave5CompartmentID, recorder: rec, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:      fakeOKEClusterType,
		Scope:     ocinuke.CompartmentScope,
		Resource:  cluster,
		Lister:    &recordingLister{instances: []*recordingResource{cluster}},
		DependsOn: []string{fakeOKENodePoolType},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	// Mechanic 2: Global geography exclusion.
	global := &wave5GlobalResource{uid: fakeGlobalIAMKey, compartmentID: wave5CompartmentID}
	ocinuke.Register(&registry.Registration{
		Name:     fakeGlobalIAMType,
		Scope:    ocinuke.CompartmentScope,
		Resource: global,
		Lister:   &wave5GlobalLister{instance: global},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	// Mechanic 3: ReasonScheduledDeletion exclusion.
	scheduled := &wave5ScheduledDeletionResource{
		uid: fakeScheduledDeletionVaultKey, compartmentID: wave5CompartmentID, removed: new(bool),
	}
	ocinuke.Register(&registry.Registration{
		Name:     fakeScheduledDeletionVaultType,
		Scope:    ocinuke.CompartmentScope,
		Resource: scheduled,
		Lister:   &wave5ScheduledDeletionLister{instance: scheduled},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	// Mechanic 4: ReasonBackupResidue reporting.
	primary := &recordingResource{uid: fakeBackupResiduePrimaryKey, compartmentID: wave5CompartmentID, recorder: rec, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:     fakeBackupResiduePrimaryType,
		Scope:    ocinuke.CompartmentScope,
		Resource: primary,
		Lister:   &wave5BackupResidueLister{primary: primary, reported: new(bool)},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

// newWave5Nuke mirrors equivalence_test.go's newEquivalenceNuke shape, adapted for four
// registered fixture types with WaitOnDependencies: true (mechanic 1's real DependsOn edge needs
// it, per CONTRIBUTING.md section 4) and a Region deliberately different from HomeRegion
// (mechanic 2's whole point) set on every scanner's shared ListerOpts.
func newWave5Nuke(t *testing.T, params *libnuke.Parameters) *libnuke.Nuke {
	t.Helper()

	n := libnuke.New(params, filter.Filters{}, nil)

	s, err := scanner.New(&scanner.Config{
		Owner: wave5Owner,
		ResourceTypes: []string{
			fakeOKENodePoolType, fakeOKEClusterType, fakeGlobalIAMType,
			fakeScheduledDeletionVaultType, fakeBackupResiduePrimaryType,
		},
		Opts: &ocinuke.ListerOpts{
			Region:        wave5Region,
			CompartmentID: wave5CompartmentID,
			HomeRegion:    wave5HomeRegion,
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

// TestWave5Equivalence_PlannedMatchesAttempted is this file's headline proof: a dry run's planned
// set of UniqueKey()s and a destructive run's attempted set are equal, each derived from its own
// independent, real libnuke.Nuke.Run() call, exactly TestPlanApplyEquivalence's own two-run shape
// -- extended here to prove all four Wave-5 mechanics compose correctly in ONE run, not four
// disconnected ones. Mechanics 2 and 3 (Global exclusion, scheduled-deletion exclusion) are proven
// by their fixtures' ABSENCE from both sets; mechanic 1 (DependsOn ordering) is proven by the
// shared recorder's own call-order snapshot from Run B; mechanic 4 (backup residue) is proven
// separately below by merging Run B's own accumulated scope.SkipEvent stream, reusing Run B's
// already-executed n.Queue.GetItems() rather than a fifth libnuke.Nuke.Run() call.
func TestWave5Equivalence_PlannedMatchesAttempted(t *testing.T) {
	ctx := context.Background()

	// Run A: dry run.
	recA := &recorder{}
	evtRecA := &eventRecorder{}
	registerWave5Fixtures(t, recA, evtRecA)
	nA := newWave5Nuke(t, &libnuke.Parameters{NoDryRun: false, Force: true, ForceSleep: 3, WaitOnDependencies: true})
	if err := nA.Run(ctx); err != nil {
		t.Fatalf("Run A (dry run) returned error: %v", err)
	}
	plannedSet := wave5PlannedSet(nA)

	// Run B: destructive run, fresh registry + fresh recorders + fresh *libnuke.Nuke.
	recB := &recorder{}
	evtRecB := &eventRecorder{}
	registerWave5Fixtures(t, recB, evtRecB)
	nB := newWave5Nuke(t, &libnuke.Parameters{NoDryRun: true, Force: true, ForceSleep: 3, WaitOnDependencies: true})
	nB.SetRunSleep(1 * time.Millisecond)
	if err := nB.Run(ctx); err != nil {
		t.Logf("Run B (destructive run) returned error: %v (not asserted on -- recorder is the source of truth)", err)
	}
	attemptedSet := wave5AttemptedSet(recB)

	assertWave5PlannedMatchesAttempted(t, plannedSet, attemptedSet)
	assertWave5GlobalGeographyExcluded(t, plannedSet, attemptedSet)                 // mechanic 2
	assertWave5ScheduledDeletionExcluded(t, plannedSet, attemptedSet)               // mechanic 3
	assertWave5DependsOnOrdering(t, recB)                                           // mechanic 1
	assertWave5BackupResidueSurvivesMerge(t, nB, evtRecB, plannedSet, attemptedSet) // mechanic 4
}

// wave5PlannedSet derives the dry-run planned set (every StateWouldRemove entry's ResourceID)
// from n's own already-executed queue state, mirroring equivalence_test.go's own inline shape,
// factored out here purely to keep TestWave5Equivalence_PlannedMatchesAttempted's own cyclomatic
// complexity low (gocyclo).
func wave5PlannedSet(n *libnuke.Nuke) map[string]struct{} {
	entries := plan.BuildFromQueue(n.Queue.GetItems(), false, nil)
	set := map[string]struct{}{}
	for _, e := range entries {
		if e.State != plan.StateWouldRemove {
			continue
		}
		set[e.ResourceID] = struct{}{}
	}
	return set
}

// wave5AttemptedSet derives the destructive-run attempted set from rec's own recorded Remove()
// calls, mirroring equivalence_test.go's own inline shape.
func wave5AttemptedSet(rec *recorder) map[string]struct{} {
	set := map[string]struct{}{}
	for _, key := range rec.snapshot() {
		set[key] = struct{}{}
	}
	return set
}

// assertWave5PlannedMatchesAttempted is the non-vacuousness guard plus the headline
// planned-equals-attempted equality assertion, exactly TestPlanApplyEquivalence's own shape.
func assertWave5PlannedMatchesAttempted(t *testing.T, plannedSet, attemptedSet map[string]struct{}) {
	t.Helper()
	if len(plannedSet) != wantWave5ResourceCount {
		t.Fatalf("plannedSet has %d elements, want %d (non-vacuousness guard): %v", len(plannedSet), wantWave5ResourceCount, plannedSet)
	}
	if len(attemptedSet) != wantWave5ResourceCount {
		t.Fatalf("attemptedSet has %d elements, want %d (non-vacuousness guard): %v", len(attemptedSet), wantWave5ResourceCount, attemptedSet)
	}
	if !reflect.DeepEqual(plannedSet, attemptedSet) {
		t.Fatalf("plannedSet %v != attemptedSet %v -- the dry-run plan does not match what the "+
			"destructive run actually attempted", plannedSet, attemptedSet)
	}
}

// assertWave5GlobalGeographyExcluded proves mechanic 2: FakeGlobalIAMResource-1 must never appear
// in either set -- BeforeList(ocinuke.Global) returns ErrSkipRequest for every List() call this
// run makes, since wave5Region != wave5HomeRegion, so the resource never even becomes a
// queue.Item.
func assertWave5GlobalGeographyExcluded(t *testing.T, plannedSet, attemptedSet map[string]struct{}) {
	t.Helper()
	if _, ok := plannedSet[fakeGlobalIAMKey]; ok {
		t.Errorf("plannedSet contains %q -- a Global-geography resource scanned from a non-home-region "+
			"scanner must never be planned", fakeGlobalIAMKey)
	}
	if _, ok := attemptedSet[fakeGlobalIAMKey]; ok {
		t.Errorf("attemptedSet contains %q -- a Global-geography resource scanned from a non-home-region "+
			"scanner must never be attempted", fakeGlobalIAMKey)
	}
}

// assertWave5ScheduledDeletionExcluded proves mechanic 3: FakeScheduledDeletionVault must never
// appear in either set -- its own Filter() excludes it (and reports
// scope.ReasonScheduledDeletion) before it can ever be would-remove or attempted.
func assertWave5ScheduledDeletionExcluded(t *testing.T, plannedSet, attemptedSet map[string]struct{}) {
	t.Helper()
	if _, ok := plannedSet[fakeScheduledDeletionVaultKey]; ok {
		t.Errorf("plannedSet contains %q -- a Filter()-excluded, scheduled-for-deletion resource "+
			"must never be planned", fakeScheduledDeletionVaultKey)
	}
	if _, ok := attemptedSet[fakeScheduledDeletionVaultKey]; ok {
		t.Errorf("attemptedSet contains %q -- a Filter()-excluded, scheduled-for-deletion resource "+
			"must never be attempted", fakeScheduledDeletionVaultKey)
	}
}

// assertWave5DependsOnOrdering proves mechanic 1: Run B's shared recorder must show
// FakeOKENodePool-1's Remove() called strictly before FakeOKECluster-1's -- WaitOnDependencies:
// true is what makes this true; without it, both would be queued as ItemStateNew immediately and
// ordering would be undefined.
func assertWave5DependsOnOrdering(t *testing.T, recB *recorder) {
	t.Helper()
	order := recB.snapshot()
	nodePoolIdx, clusterIdx := -1, -1
	for i, key := range order {
		switch key {
		case fakeOKENodePoolKey:
			nodePoolIdx = i
		case fakeOKEClusterKey:
			clusterIdx = i
		}
	}
	if nodePoolIdx == -1 || clusterIdx == -1 {
		t.Fatalf("recorder is missing an expected Remove() call (order: %v)", order)
	}
	if nodePoolIdx > clusterIdx {
		t.Fatalf("FakeOKENodePool-1's Remove() (call #%d) happened AFTER FakeOKECluster-1's (call #%d) -- "+
			"WaitOnDependencies must ensure the dependency is removed first (call order: %v)",
			nodePoolIdx, clusterIdx, order)
	}
}

// assertWave5BackupResidueSurvivesMerge proves mechanic 4: ReasonBackupResidue survives
// plan.MergeSkipEvents as a distinct StateSkipped entry, tagged with the backup's own
// ResourceType/ResourceID -- never present in either the planned or attempted set, since it was
// never a registered, independently deletable resource type at all. Reuses Run B's own
// already-executed queue state (BuildFromQueue with plan.ClassifyLeftover, the real production
// leftover-report shape) rather than a fifth libnuke.Nuke.Run() call. Also asserts the companion,
// non-vacuous check for mechanic 3: the scheduled-deletion reason must likewise survive the merge
// (as a SEPARATE StateSkipped entry alongside the resource's own StateFiltered entry, since
// MergeSkipEvents only overwrites a StateLeftover entry -- a Filter()-time exclusion is
// StateFiltered, so the merge unconditionally appends a new entry instead; this is a deliberate,
// documented shape difference from ReasonScheduledDeletion's OTHER production consumer pattern in
// resources_test/scheduled_deletion_test.go, see this file's own doc comment above).
func assertWave5BackupResidueSurvivesMerge(
	t *testing.T, nB *libnuke.Nuke, evtRecB *eventRecorder, plannedSet, attemptedSet map[string]struct{},
) {
	t.Helper()
	mergedB := plan.MergeSkipEvents(plan.BuildFromQueue(nB.Queue.GetItems(), true, plan.ClassifyLeftover), evtRecB.snapshot())

	residueEntries := entriesWithReason(mergedB, scope.ReasonBackupResidue)
	if len(residueEntries) != 1 {
		t.Fatalf("got %d ReasonBackupResidue entries in merged output, want exactly 1 (entries: %+v)", len(residueEntries), mergedB)
	}
	if residueEntries[0].State != plan.StateSkipped {
		t.Errorf("ReasonBackupResidue entry State = %q, want %q (a backup was never a deletion target)",
			residueEntries[0].State, plan.StateSkipped)
	}
	if residueEntries[0].ResourceType != fakeBackupResidueBackupType {
		t.Errorf("ReasonBackupResidue entry ResourceType = %q, want %q", residueEntries[0].ResourceType, fakeBackupResidueBackupType)
	}
	if residueEntries[0].ResourceID != fakeBackupResidueBackupID {
		t.Errorf("ReasonBackupResidue entry ResourceID = %q, want %q", residueEntries[0].ResourceID, fakeBackupResidueBackupID)
	}
	if _, ok := plannedSet[fakeBackupResidueBackupID]; ok {
		t.Errorf("plannedSet contains %q -- a reported backup residue must never itself be a plan candidate", fakeBackupResidueBackupID)
	}
	if _, ok := attemptedSet[fakeBackupResidueBackupID]; ok {
		t.Errorf("attemptedSet contains %q -- a reported backup residue must never itself be attempted", fakeBackupResidueBackupID)
	}

	scheduledEntries := entriesWithReason(mergedB, scope.ReasonScheduledDeletion)
	if len(scheduledEntries) != 1 {
		t.Fatalf("got %d ReasonScheduledDeletion entries in merged output, want exactly 1 (entries: %+v)", len(scheduledEntries), mergedB)
	}
	if scheduledEntries[0].State != plan.StateSkipped {
		t.Errorf("ReasonScheduledDeletion entry State = %q, want %q", scheduledEntries[0].State, plan.StateSkipped)
	}
}
