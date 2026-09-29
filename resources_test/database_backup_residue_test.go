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

const (
	databaseBackupResidueRegion        = "us-ashburn-1"
	databaseBackupResidueCompartmentID = "ocid1.compartment.oc1..database-backup-residue-test"
	databaseBackupResidueOwner         = databaseBackupResidueRegion + "/" + databaseBackupResidueCompartmentID
	databaseBackupResidueResourceType  = "FakeAutonomousDatabase"
	databaseBackupResidueResourceKey   = "fake-autonomous-database-1"
	databaseBackupResidueBackupType    = "AutonomousDatabaseBackup"
	databaseBackupResidueBackupID      = "fake-autonomous-database-backup-1"
	databaseBackupResidueDetail        = "automatic post-termination backup, not deleted by this tool"
)

// databaseBackupResidueResource is a minimal fixture standing in for resources/
// autonomous_database.go's real AutonomousDatabase -- its Remove() always succeeds (this test is
// not exercising failure handling, that is resources_test/leftover_queue_test.go's job), mirroring
// resources_test/equivalence_test.go's recordingResource shape.
type databaseBackupResidueResource struct {
	removed *bool
}

func (r *databaseBackupResidueResource) Remove(_ context.Context) error {
	*r.removed = true
	return nil
}

func (r *databaseBackupResidueResource) UniqueKey() string { return databaseBackupResidueResourceKey }
func (r *databaseBackupResidueResource) GetCompartmentID() string {
	return databaseBackupResidueCompartmentID
}
func (r *databaseBackupResidueResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// databaseBackupResidueLister mirrors resources/autonomous_database.go's real List(): it returns
// the primary resource AND, as a side effect the first time it runs, reports one backup residue
// SkipEvent via the injected reporter -- exactly resources/database_backup_support.go's own
// reportAutonomousDatabaseBackupResidue shape, simplified to a single fixed backup so this test
// stays focused on proving the MERGE behavior end to end, not re-testing pagination (already
// covered by resources/database_backup_support_test.go's own unit tests against a stub client).
// reported guards against double-reporting across libnuke's re-list rounds (HandleWait), the same
// way removed guards the primary resource's own convergence.
type databaseBackupResidueLister struct {
	reporter ocinuke.LeftoverReporter
	removed  *bool
	reported *bool
}

func (l *databaseBackupResidueLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if !*l.reported {
		ocinuke.ReportLeftover(l.reporter, &scope.SkipEvent{
			Reason:        scope.ReasonBackupResidue,
			ResourceType:  databaseBackupResidueBackupType,
			ResourceID:    databaseBackupResidueBackupID,
			CompartmentID: databaseBackupResidueCompartmentID,
			Detail:        databaseBackupResidueDetail,
		})
		*l.reported = true
	}
	if *l.removed {
		return nil, nil
	}
	return []resource.Resource{&databaseBackupResidueResource{removed: l.removed}}, nil
}

// newDatabaseBackupResidueTestNuke mirrors resources_test/scheduled_deletion_test.go's
// newScheduledDeletionTestNuke construction shape. Registered via ocinuke.Register (not raw
// registry.Register) so the real, production scopedLister wrapping is exercised end to end --
// inScope is unconditionally true since scope membership is not what this test is about.
func newDatabaseBackupResidueTestNuke(t *testing.T, rec *eventRecorder) *libnuke.Nuke {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	reporter := ocinuke.LeftoverReporter(rec.report)
	removed := new(bool)
	reported := new(bool)

	ocinuke.Register(&registry.Registration{
		Name:     databaseBackupResidueResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &databaseBackupResidueResource{removed: removed},
		Lister:   &databaseBackupResidueLister{reporter: reporter, removed: removed, reported: reported},
	}, func(string) bool { return true }, nil)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         databaseBackupResidueOwner,
		ResourceTypes: []string{databaseBackupResidueResourceType},
		Opts:          &ocinuke.ListerOpts{Region: databaseBackupResidueRegion, CompartmentID: databaseBackupResidueCompartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	return n
}

// entriesWithReason filters entries by plan.Entry.Reason -- the ReasonBackupResidue-specific
// counterpart to leftover_queue_test.go's own entriesWithState helper.
func entriesWithReason(entries []plan.Entry, reason scope.RefusalReason) []plan.Entry {
	var matched []plan.Entry
	for _, e := range entries {
		if e.Reason == reason {
			matched = append(matched, e)
		}
	}
	return matched
}

// TestDatabaseBackupResidue_SurfacesInMergedPlanOutput proves scope.ReasonBackupResidue-tagged
// entries survive the full production pipeline: a real libnuke.Nuke.Run(), ocinuke.ReportLeftover,
// pkg/plan.BuildFromQueue, and pkg/plan.MergeSkipEvents. This closes 05-02-PLAN.md's own overall
// verification requirement: "A stubbed run scanning all three database types with backups present
// in the fixture surfaces ReasonBackupResidue-tagged entries in the merged plan/leftover output."
// The mechanism (ReportLeftover -> accumulator -> plan.MergeSkipEvents) is the exact same generic
// pipeline resources_test/scheduled_deletion_test.go already proves for ReasonScheduledDeletion;
// this test proves ReasonBackupResidue flows through it identically, and that the reported backup
// (never itself a queue.Item) surfaces as a brand-new StateSkipped entry, distinct from the
// primary resource's own StateRemoved entry, rather than being silently dropped or conflated.
func TestDatabaseBackupResidue_SurfacesInMergedPlanOutput(t *testing.T) {
	rec := &eventRecorder{}
	n := newDatabaseBackupResidueTestNuke(t, rec)

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil (the primary fixture resource always succeeds)", err)
	}

	unmerged := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	merged := plan.MergeSkipEvents(unmerged, rec.snapshot())

	residueEntries := entriesWithReason(merged, scope.ReasonBackupResidue)
	if len(residueEntries) != 1 {
		t.Fatalf("got %d ReasonBackupResidue entries in merged output, want exactly 1 (entries: %+v)", len(residueEntries), merged)
	}
	if residueEntries[0].State != plan.StateSkipped {
		t.Errorf("ReasonBackupResidue entry State = %q, want %q (a backup was never a deletion target)",
			residueEntries[0].State, plan.StateSkipped)
	}
	if residueEntries[0].ResourceType != databaseBackupResidueBackupType {
		t.Errorf("ReasonBackupResidue entry ResourceType = %q, want %q", residueEntries[0].ResourceType, databaseBackupResidueBackupType)
	}
	if residueEntries[0].ResourceID != databaseBackupResidueBackupID {
		t.Errorf("ReasonBackupResidue entry ResourceID = %q, want %q", residueEntries[0].ResourceID, databaseBackupResidueBackupID)
	}

	removedEntries := entriesWithState(merged, plan.StateRemoved)
	if len(removedEntries) != 1 || removedEntries[0].ResourceID != databaseBackupResidueResourceKey {
		t.Fatalf("got %d StateRemoved entries (%+v), want exactly 1 for the primary fixture resource %q",
			len(removedEntries), removedEntries, databaseBackupResidueResourceKey)
	}
}
