package resources

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// This file is the end-to-end proof this plan's <critical_safety_rules> and COMP-01..04 require:
// the REAL Compartment type (resources/compartment.go, Plan 06-02's output), driven through a
// REAL *libnuke.Nuke.Run() control flow, with ONLY the OCI SDK client (stubCompartmentClient,
// compartment_test.go) stubbed -- mirroring vault_test.go's
// TestVault_PendingDeletion_EndToEnd_ReportedAsScheduledDeletionNotAPIError precedent exactly:
// build the real struct by hand (white-box, unexported fields), register it via ocinuke.Register
// under a DISTINCT fixture type name (never "Compartment" itself -- this package has no
// TestMain-based registry snapshot/restore, so the package's own real init()-installed
// "Compartment" registration must stay untouched by every test below), wrap it in a minimal
// test-only Lister, construct a real *libnuke.Nuke, call n.Run(ctx) for real, then inspect
// n.Queue.GetItems() via plan.BuildFromQueue (and, for the fourth test, this plan's own Task 1
// plan.EnrichCompartmentLeftover).
//
// Live policy (06-CONTEXT.md's environment_note, restated here since this is the phase's own
// end-to-end proof file): READ-ONLY and DRY-RUN ONLY against any real tenancy -- no compartment
// is ever actually deleted by anything in this repository. Every criterion below closes against a
// stubbed SDK client driven through the real libnuke.Nuke.Run() control flow, exactly as
// 06-CONTEXT.md's environment_note requires; this is stated openly, not claimed as a live result.

const compartmentIntegrationTestRegion = "us-ashburn-1"

// testCompartmentTimeCreated satisfies identity.Compartment's mandatory TimeCreated field for
// every fixture in this file -- Compartment.SafetyTags() dereferences it unconditionally
// (mirroring every other resource type's own SafetyTags convention), so a fixture that omits
// it panics inside libnuke's real scan-time SafetyEvaluated call, not merely in an assertion
// this file writes itself.
func testCompartmentTimeCreated() *common.SDKTime {
	return &common.SDKTime{Time: time.Now()}
}

// fixtureCompartmentLister mirrors resources_test/leftover_queue_test.go's leftoverFakeLister
// `removed *bool` shape: it returns the one fixed *Compartment resource until gone flips true,
// then an empty list -- so the post-SUCCEEDED List()/Filter() re-check inside libnuke's own
// HandleWait (the fallback below the HandleWaitHook branch, 06-RESEARCH.md Contradiction 2)
// correctly finds nothing and converges the item to ItemStateFinished. gone may be nil (tests
// that never expect the compartment to disappear, e.g. the blocklisted-descendant and
// budget-exhaustion cases, simply never flip it).
type fixtureCompartmentLister struct {
	compartment *Compartment
	gone        *bool
}

func (l *fixtureCompartmentLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if l.gone != nil && *l.gone {
		return nil, nil
	}
	return []resource.Resource{l.compartment}, nil
}

// TestCompartmentIntegration_DryRun_PlansAsWouldRemove proves COMP-01's dry-run half: an ACTIVE,
// non-blocklisted-descendant Compartment, scanned with NoDryRun: false, plans as an ordinary
// would-remove entry -- "In dry-run the compartment is an ordinary plan entry" (06-CONTEXT.md),
// no special case carved out for this type.
func TestCompartmentIntegration_DryRun_PlansAsWouldRemove(t *testing.T) {
	const fixtureType = "CompartmentIntegrationDryRunFixture"
	id := testCompartmentOwnOCID

	fixture := &Compartment{
		client: &stubCompartmentClient{},
		compartment: identity.Compartment{
			Id: &id, LifecycleState: identity.CompartmentLifecycleStateActive, TimeCreated: testCompartmentTimeCreated(),
		},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     fixtureType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fixtureCompartmentLister{compartment: fixture},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   false,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3, dry-run or not
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + id,
		ResourceTypes: []string{fixtureType},
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil (a dry run never fails)", err)
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), false, nil)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want exactly 1 (entries: %+v)", len(entries), entries)
	}
	if entries[0].State != plan.StateWouldRemove {
		t.Errorf("entries[0].State = %q, want %q", entries[0].State, plan.StateWouldRemove)
	}
	if entries[0].ResourceID != id {
		t.Errorf("entries[0].ResourceID = %q, want %q", entries[0].ResourceID, id)
	}
}

// TestCompartmentIntegration_DestructiveRun_ConvergesThroughAcceptedFailedAcceptedSucceeded
// proves COMP-02/COMP-03's central convergence guarantee at the integration level: a real
// libnuke.Nuke.Run() drives Compartment.Remove()/HandleWait() through a work-request status
// sequence embedding one FAILED round (ACCEPTED, ACCEPTED, FAILED, ACCEPTED, SUCCEEDED) and still
// converges to StateRemoved -- Plan 06-01's fault-injection proof exercised the same mechanism
// against a SYNTHETIC resource; this proves it against the REAL Compartment type.
func TestCompartmentIntegration_DestructiveRun_ConvergesThroughAcceptedFailedAcceptedSucceeded(t *testing.T) {
	const fixtureType = "CompartmentIntegrationConvergenceFixture"
	id := testCompartmentOwnOCID

	statuses := []identity.WorkRequestStatusEnum{
		identity.WorkRequestStatusAccepted,
		identity.WorkRequestStatusAccepted,
		identity.WorkRequestStatusFailed,
		identity.WorkRequestStatusAccepted,
		identity.WorkRequestStatusSucceeded,
	}
	idx := new(int)
	gone := new(bool)

	stub := &stubCompartmentClient{
		getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
			status := statuses[*idx]
			if *idx < len(statuses)-1 {
				*idx++
			}
			if status == identity.WorkRequestStatusSucceeded {
				*gone = true
			}
			return identity.GetWorkRequestResponse{WorkRequest: identity.WorkRequest{Status: status}}, nil
		},
	}

	fixture := &Compartment{
		client: stub,
		compartment: identity.Compartment{
			Id: &id, LifecycleState: identity.CompartmentLifecycleStateActive, TimeCreated: testCompartmentTimeCreated(),
		},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     fixtureType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fixtureCompartmentLister{compartment: fixture, gone: gone},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + id,
		ResourceTypes: []string{fixtureType},
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil -- a single FAILED round embedded among in-flight "+
			"rounds must never abort a real destructive run", err)
	}

	if *idx < len(statuses)-1 {
		t.Fatalf("HandleWait consumed only %d of %d statuses, want the full sequence to have been "+
			"reached (the FAILED entry must have genuinely fired, not been skipped)", *idx+1, len(statuses))
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want exactly 1 (entries: %+v)", len(entries), entries)
	}
	if entries[0].State != plan.StateRemoved {
		t.Errorf("entries[0].State = %q, want %q (entries: %+v)", entries[0].State, plan.StateRemoved, entries)
	}
}

// TestCompartmentIntegration_BlocklistedDescendant_NeverCallsDeleteCompartment proves COMP-01's
// blocklisted-descendant guarantee at the integration level, stronger than Plan 06-02's own unit
// test: Filter()'s scan-time exclusion genuinely prevents Remove() (and therefore
// DeleteCompartment) from ever running inside the real queue lifecycle, not merely that Filter()
// returns non-nil in isolation. Also asserts the accompanying scope.SkipEvent, mirroring
// vault_test.go's exact ocinuke.SetRunContext capture pattern.
func TestCompartmentIntegration_BlocklistedDescendant_NeverCallsDeleteCompartment(t *testing.T) {
	const fixtureType = "CompartmentIntegrationBlocklistedFixture"
	id := testCompartmentOwnOCID

	deleteCalls := new(int)
	stub := &stubCompartmentClient{
		deleteCompartmentFn: func(_ context.Context, _ identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error) {
			*deleteCalls++
			wr := testWorkRequestOCID
			return identity.DeleteCompartmentResponse{OpcWorkRequestId: &wr}, nil
		},
	}

	fixture := &Compartment{
		client: stub,
		compartment: identity.Compartment{
			Id: &id, LifecycleState: identity.CompartmentLifecycleStateActive, TimeCreated: testCompartmentTimeCreated(),
		},
		hasBlocklistedDescendant: true,
	}

	var events []scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		events = append(events, *evt)
	})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     fixtureType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fixtureCompartmentLister{compartment: fixture},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + id,
		ResourceTypes: []string{fixtureType},
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil (a Filter()-excluded item is not a run failure)", err)
	}

	if *deleteCalls != 0 {
		t.Errorf("DeleteCompartment called %d times, want 0 -- Filter()'s scan-time exclusion must "+
			"prevent Remove() from ever being reached inside the real queue lifecycle", *deleteCalls)
	}

	var matches []scope.SkipEvent
	for _, e := range events {
		if e.Reason == scope.ReasonCompartmentNotEmpty {
			matches = append(matches, e)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("got %d scope.SkipEvent(s) with Reason %q, want exactly 1 (events: %+v)",
			len(matches), scope.ReasonCompartmentNotEmpty, events)
	}
}

// TestCompartmentIntegration_BlocklistedDescendant_MergesToSingleSkippedEntry proves 06-REVIEW.md
// WR-01's fix at the full-pipeline layer: unlike
// TestCompartmentIntegration_BlocklistedDescendant_NeverCallsDeleteCompartment above (which only
// asserts deleteCalls==0 and the raw scope.SkipEvent count), this test builds the FINAL merged
// artifact production code actually writes -- plan.BuildFromQueue followed by
// plan.MergeSkipEvents, mirroring runNukes' own call order (pkg/commands/run/command.go) -- and
// asserts exactly ONE entry results for the blocklisted-descendant compartment, in StateSkipped
// with Reason ReasonCompartmentNotEmpty, not two competing entries (one generic "filtered", one
// "skipped"). README.md documents this reason as a single reported entry; before WR-01's fix,
// Filter()'s non-nil error return produced a StateFiltered entry from BuildFromQueue AND its
// accompanying ocinuke.ReportLeftover SkipEvent produced a second, separate StateSkipped entry
// from MergeSkipEvents, since mergeSkipEvent only matched StateLeftover entries.
func TestCompartmentIntegration_BlocklistedDescendant_MergesToSingleSkippedEntry(t *testing.T) {
	const fixtureType = "CompartmentIntegrationMergeFixture"
	id := testCompartmentOwnOCID

	deleteCalls := new(int)
	stub := &stubCompartmentClient{
		deleteCompartmentFn: func(_ context.Context, _ identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error) {
			*deleteCalls++
			wr := testWorkRequestOCID
			return identity.DeleteCompartmentResponse{OpcWorkRequestId: &wr}, nil
		},
	}

	fixture := &Compartment{
		client: stub,
		compartment: identity.Compartment{
			Id: &id, LifecycleState: identity.CompartmentLifecycleStateActive, TimeCreated: testCompartmentTimeCreated(),
		},
		hasBlocklistedDescendant: true,
	}

	var events []scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		events = append(events, *evt)
	})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     fixtureType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fixtureCompartmentLister{compartment: fixture},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + id,
		ResourceTypes: []string{fixtureType},
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil (a Filter()-excluded item is not a run failure)", err)
	}
	if *deleteCalls != 0 {
		t.Fatalf("DeleteCompartment called %d times, want 0", *deleteCalls)
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	merged := plan.MergeSkipEvents(entries, events)

	var forCompartment []plan.Entry
	for _, e := range merged {
		if e.ResourceID == id {
			forCompartment = append(forCompartment, e)
		}
	}
	if len(forCompartment) != 1 {
		t.Fatalf("got %d entries for compartment %s, want exactly 1 (entries: %+v)", len(forCompartment), id, forCompartment)
	}
	if forCompartment[0].State != plan.StateSkipped {
		t.Errorf("entries[0].State = %q, want %q", forCompartment[0].State, plan.StateSkipped)
	}
	if forCompartment[0].Reason != scope.ReasonCompartmentNotEmpty {
		t.Errorf("entries[0].Reason = %q, want %q", forCompartment[0].Reason, scope.ReasonCompartmentNotEmpty)
	}
}

// compartmentIntegrationBudgetSiblingOCID/Type are the fixture blocker resource this test's
// EnrichCompartmentLeftover assertion looks for in the Compartment entry's Detail.
const (
	compartmentIntegrationBudgetSiblingType = "CompartmentIntegrationBudgetSiblingFixture"
	compartmentIntegrationBudgetSiblingOCID = "ocid1.test.oc1..compartment-integration-sibling"
)

// compartmentIntegrationSiblingResource mirrors resources_test/leftover_queue_test.go's
// leftoverFakeResource shape (alwaysFail=true case): Remove() always fails, so this resource
// never reaches StateRemoved -- the sibling this test's Detail assertion names.
type compartmentIntegrationSiblingResource struct{}

func (r *compartmentIntegrationSiblingResource) Remove(_ context.Context) error {
	return fmt.Errorf("sibling remove always fails (budget-exhaustion fixture)")
}

func (r *compartmentIntegrationSiblingResource) UniqueKey() string {
	return compartmentIntegrationBudgetSiblingOCID
}

type compartmentIntegrationSiblingLister struct{}

func (l *compartmentIntegrationSiblingLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{&compartmentIntegrationSiblingResource{}}, nil
}

// TestCompartmentIntegration_BudgetExhaustion_BlockerDetailNamesTheSibling proves COMP-04 at the
// integration level: two resource types share one *libnuke.Nuke and one CompartmentID -- the
// Compartment fixture, whose HandleWait always returns liberrors.ErrWaitResource (a work request
// that never resolves), and a sibling fixture resource whose Remove() always fails. A small
// MaxWaitRetries (2) forces n.Run(ctx) to a terminal, non-nil-error state quickly regardless of
// which internal give-up threshold (handleWaiting's wait-retry budget vs. handleFailure's
// failed-round threshold) fires first -- this test asserts only on the FINAL entries' content,
// never on which round or threshold triggered termination, so it stays robust to either.
//
// EnrichCompartmentLeftover (this plan's Task 1) matches on the literal "Compartment"
// ResourceType (resources.CompartmentResourceType's own value, same package here). This fixture
// cannot be REGISTERED under that literal name directly: registry.Register panics on a duplicate
// name ("a resource with the name Compartment already exists"), since this package's real init()
// already registered the production Compartment type, and there is no unregister primitive short
// of registry.ClearRegistry() -- which this package's tests deliberately never call (this file's
// own top-of-file note: the real "Compartment" registration must stay untouched). The real
// n.Run(ctx) still drives every bit of actual state below (Filter/Remove/HandleWait, item states,
// the sibling's own leftover classification, via the queue.GetItems() this test reads back) --
// only the ResourceType label on the already-built entry is relabeled here, post-hoc, to the
// exact value production code guarantees it always carries in a real run (proven independently by
// resources/compartment.go's own init() and pkg/plan/compartment_leftover_test.go).
func TestCompartmentIntegration_BudgetExhaustion_BlockerDetailNamesTheSibling(t *testing.T) {
	const fixtureType = "CompartmentIntegrationBudgetFixture"
	compartmentID := testCompartmentOwnOCID

	stub := &stubCompartmentClient{
		getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
			return identity.GetWorkRequestResponse{
				WorkRequest: identity.WorkRequest{Status: identity.WorkRequestStatusAccepted},
			}, nil
		},
	}
	compartmentFixture := &Compartment{
		client: stub,
		compartment: identity.Compartment{
			Id: &compartmentID, LifecycleState: identity.CompartmentLifecycleStateActive, TimeCreated: testCompartmentTimeCreated(),
		},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     fixtureType,
		Scope:    ocinuke.CompartmentScope,
		Resource: compartmentFixture,
		Lister:   &fixtureCompartmentLister{compartment: compartmentFixture},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	registry.Register(&registry.Registration{
		Name:   compartmentIntegrationBudgetSiblingType,
		Scope:  ocinuke.CompartmentScope,
		Lister: &compartmentIntegrationSiblingLister{},
	})

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:       true,
		Force:          true,
		ForceSleep:     3,
		MaxWaitRetries: 2,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + compartmentID,
		ResourceTypes: []string{fixtureType, compartmentIntegrationBudgetSiblingType},
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: compartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err == nil {
		t.Fatal("n.Run() error = nil, want non-nil -- the compartment never empties within the low retry budget")
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	for i := range entries {
		if entries[i].ResourceID == compartmentID {
			entries[i].ResourceType = CompartmentResourceType
		}
	}
	entries = plan.EnrichCompartmentLeftover(entries, compartmentID)

	var compartmentEntry *plan.Entry
	for i := range entries {
		if entries[i].ResourceType == CompartmentResourceType {
			compartmentEntry = &entries[i]
		}
	}
	if compartmentEntry == nil {
		t.Fatalf("no Compartment entry found after enrichment (entries: %+v)", entries)
	}
	if compartmentEntry.Reason != scope.ReasonCompartmentNotEmpty {
		t.Errorf("Compartment entry Reason = %q, want %q (entries: %+v)", compartmentEntry.Reason, scope.ReasonCompartmentNotEmpty, entries)
	}
	if !strings.Contains(compartmentEntry.Detail, compartmentIntegrationBudgetSiblingType) {
		t.Errorf("Compartment entry Detail = %q, want it to contain sibling type %q",
			compartmentEntry.Detail, compartmentIntegrationBudgetSiblingType)
	}
	if !strings.Contains(compartmentEntry.Detail, compartmentIntegrationBudgetSiblingOCID) {
		t.Errorf("Compartment entry Detail = %q, want it to contain sibling OCID %q",
			compartmentEntry.Detail, compartmentIntegrationBudgetSiblingOCID)
	}
}
