package ocinuke_test

import (
	"context"
	"fmt"
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
	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	scheduledDeletionRegion        = "us-ashburn-1"
	scheduledDeletionCompartmentID = "ocid1.compartment.oc1..scheduled-deletion-test"
	scheduledDeletionOwner         = scheduledDeletionRegion + "/" + scheduledDeletionCompartmentID
	scheduledDeletionResourceType  = "FakeKMSVault"
	scheduledDeletionResourceKey   = "fake-kms-vault-1"
)

// eventRecorder is a shared, mutex-guarded []scope.SkipEvent accumulator -- the same shape
// 03-RESEARCH.md Q4 says production must add ("Phase 3 must add a per-pipeline-run accumulator
// ([]scope.SkipEvent, injected the same way InScope already is)"). This test is the first place
// in the codebase that constructs a non-nil ocinuke.LeftoverReporter and actually calls it from
// within a Remove() implementation.
type eventRecorder struct {
	mu     sync.Mutex
	events []scope.SkipEvent
}

// report's signature must match ocinuke.LeftoverReporter (by pointer, the project-wide
// convention for scope.SkipEvent). It stores a dereferenced copy in r.events rather than
// retaining evt itself, since the whole point of this accumulator is to keep every reported
// event independently addressable after the run, not aliased to whatever the caller does with
// evt next.
func (r *eventRecorder) report(evt *scope.SkipEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *evt)
}

func (r *eventRecorder) snapshot() []scope.SkipEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]scope.SkipEvent(nil), r.events...)
}

// scheduledDeletionResource is precisely 03-CONTEXT.md's environment-note DoD stub: "a
// compartment containing a scheduled-for-deletion KMS vault produces a leftover with reason
// scheduled-deletion -- stubbed resource returning that state. No KMS vault is created." Its
// Remove() calls ocinuke.ReportLeftover with scope.ReasonScheduledDeletion immediately before
// returning a plain, non-nil error -- never liberrors.ErrHoldResource, per
// pkg/ocinuke/leftover.go's documented hazard (ErrHoldResource has no bounded retry budget of its
// own and would hang the run indefinitely instead of converging to a reported leftover).
type scheduledDeletionResource struct {
	reporter ocinuke.LeftoverReporter
}

func (r *scheduledDeletionResource) Remove(_ context.Context) error {
	ocinuke.ReportLeftover(r.reporter, &scope.SkipEvent{
		Reason:        scope.ReasonScheduledDeletion,
		ResourceType:  scheduledDeletionResourceType,
		ResourceID:    r.UniqueKey(),
		CompartmentID: r.GetCompartmentID(),
		Detail:        "scheduled for deletion in 7 days",
	})
	return fmt.Errorf("cannot remove FakeKMSVault %s: scheduled for deletion", r.UniqueKey())
}

func (r *scheduledDeletionResource) UniqueKey() string        { return scheduledDeletionResourceKey }
func (r *scheduledDeletionResource) GetCompartmentID() string { return scheduledDeletionCompartmentID }
func (r *scheduledDeletionResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// scheduledDeletionLister always returns the one fixed resource -- it never disappears, mirroring
// leftover_queue_test.go's alwaysFail=true shape, since this fixture's whole point is that the
// resource is never actually removable this run.
type scheduledDeletionLister struct {
	reporter ocinuke.LeftoverReporter
}

func (l *scheduledDeletionLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{&scheduledDeletionResource{reporter: l.reporter}}, nil
}

// newScheduledDeletionTestNuke mirrors resources_test/leftover_queue_test.go's
// newLeftoverTestNuke construction shape (registry.ClearRegistry, libnuke.New(&libnuke.Parameters
// {...}), scanner.New, RegisterScanner, a lowered SetRunSleep so the ~3-round give-up threshold
// does not make the test slow), adapted for a fake resource whose Remove() takes a reporter
// reference. Registered via ocinuke.Register (not raw registry.Register) so the real, production
// scopedLister wrapping is exercised -- inScope is unconditionally true since scope membership is
// not what this test is about.
func newScheduledDeletionTestNuke(t *testing.T, rec *eventRecorder) *libnuke.Nuke {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	reporter := ocinuke.LeftoverReporter(rec.report)

	ocinuke.Register(&registry.Registration{
		Name:     scheduledDeletionResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &scheduledDeletionResource{reporter: reporter},
		Lister:   &scheduledDeletionLister{reporter: reporter},
	}, func(string) bool { return true }, nil)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         scheduledDeletionOwner,
		ResourceTypes: []string{scheduledDeletionResourceType},
		Opts:          &ocinuke.ListerOpts{Region: scheduledDeletionRegion, CompartmentID: scheduledDeletionCompartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	return n
}

// runScheduledDeletionFixture runs a real n.Run(ctx) end to end (Remove() always errors, so
// n.Run(ctx) returns non-nil after libnuke's ~3-round give-up threshold -- ignored here exactly
// as leftover_queue_test.go's own always-fail case already established: leftover reporting must
// never be gated on n.Run()'s own error return) and returns both the pre-merge and post-merge
// plan.Entry slices for the same underlying queue state, so each test below can assert on
// whichever half proves its own point.
func runScheduledDeletionFixture(t *testing.T) (unmerged, merged []plan.Entry) {
	t.Helper()

	rec := &eventRecorder{}
	n := newScheduledDeletionTestNuke(t, rec)

	_ = n.Run(context.Background())

	unmerged = plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	merged = plan.MergeSkipEvents(unmerged, rec.snapshot())
	return unmerged, merged
}

// TestScheduledDeletionLeftover_ReportedReasonSurvivesMerge proves the merged plan.Entry carries
// scope.ReasonScheduledDeletion -- not the generic api-error fallback -- end to end through a
// real libnuke.Nuke.Run(), ocinuke.ReportLeftover, and plan.MergeSkipEvents. This is the exact
// production upgrade path Plan 03-06 wires into the real run.
func TestScheduledDeletionLeftover_ReportedReasonSurvivesMerge(t *testing.T) {
	_, merged := runScheduledDeletionFixture(t)

	matches := entriesWithState(merged, plan.StateLeftover)
	if len(matches) != 1 {
		t.Fatalf("got %d leftover entries after merge, want exactly 1 (entries: %+v)", len(matches), merged)
	}
	if matches[0].Reason != scope.ReasonScheduledDeletion {
		t.Errorf("merged leftover entry Reason = %q, want %q", matches[0].Reason, scope.ReasonScheduledDeletion)
	}
}

// TestScheduledDeletionLeftover_UnmergedEntryIsGenericAPIError is the companion, non-vacuous
// assertion: the SAME fixture's plan.BuildFromQueue output, taken alone before MergeSkipEvents
// runs, has Reason == scope.ReasonAPIError. This proves the merge step is what upgrades the
// reason -- not that plan.ClassifyLeftover accidentally already knows about scheduled-deletion
// (it deliberately does not; see pkg/plan/leftover.go's doc comment).
func TestScheduledDeletionLeftover_UnmergedEntryIsGenericAPIError(t *testing.T) {
	unmerged, _ := runScheduledDeletionFixture(t)

	matches := entriesWithState(unmerged, plan.StateLeftover)
	if len(matches) != 1 {
		t.Fatalf("got %d leftover entries before merge, want exactly 1 (entries: %+v)", len(matches), unmerged)
	}
	if matches[0].Reason != scope.ReasonAPIError {
		t.Errorf("unmerged leftover entry Reason = %q, want %q (proving the merge step is load-bearing)", matches[0].Reason, scope.ReasonAPIError)
	}
}
