package ocinuke_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// This file proves, against a REAL libnuke.Nuke.Run() (never a mock of Run() itself), that the
// design Plan 06-02/06-03 depend on -- Remove() always accepts synchronously, HandleWaitHook
// polls a work-request-shaped status and reports liberrors.ErrWaitResource while genuinely
// in-flight -- never trips libnuke's hardcoded `n.failedCount >= 2` hard-abort in handleFailure
// (pkg/nuke/nuke.go, ekristen/libnuke@v1.3.0, verified directly from
// $GOMODCACHE/github.com/ekristen/libnuke@v1.3.0/pkg/nuke/nuke.go lines ~241-273). That check is
// independent of Parameters.MaxWaitRetries and fires whenever nothing else in the queue is
// active and at least one item is ItemStateFailed for three consecutive rounds -- exactly the
// end-state of a compartment delete (06-RESEARCH.md Contradiction 3). See
// TestCompartmentFaultInjection_FailedRoundNeverAbortsRun below for the mechanism that avoids it:
// HandleQueue's `case queue.ItemStateFailed:` branch (nuke.go's HandleQueue) calls HandleRemove
// and then HandleWait again in the SAME pass, so a recovering item never sits in ItemStateFailed
// across two consecutive full rounds, and n.failedCount keeps resetting to 0 instead of
// accumulating toward the abort threshold.

// Work-request-shaped status literals HandleWait reads through, mirroring
// identity.WorkRequestStatusEnum's ACCEPTED/IN_PROGRESS/FAILED/SUCCEEDED values
// (06-RESEARCH.md's Code Examples section) without importing the SDK -- this file is a synthetic
// proof of libnuke's own state-machine behavior and deliberately has no OCI SDK dependency.
const (
	statusAccepted   = "ACCEPTED"
	statusInProgress = "IN_PROGRESS"
	statusFailed     = "FAILED"
	statusSucceeded  = "SUCCEEDED"
)

const (
	compartmentFaultOwner         = "us-ashburn-1/ocid1.compartment.oc1..fault-test"
	compartmentFaultCompartmentID = "ocid1.compartment.oc1..fault-test"
)

// compartmentFaultResource models production DeleteCompartment as always accepting
// synchronously (06-RESEARCH.md Pitfall A: Remove()'s success never means "compartment is
// empty") and drives the async-poll outcome entirely through HandleWait, reading one entry of
// statuses per invocation -- mirroring a work request cycling through
// ACCEPTED/IN_PROGRESS/FAILED/SUCCEEDED.
type compartmentFaultResource struct {
	calls    *int
	statuses []string
	idx      *int
	done     *bool
}

func (r *compartmentFaultResource) Remove(_ context.Context) error {
	*r.calls++
	return nil
}

func (r *compartmentFaultResource) UniqueKey() string { return "compartment-fault-1" }

// HandleWait implements resource.HandleWaitHook. It is called on THIS struct instance -- the
// same one Remove() populated -- never on a fresh List()-produced copy (06-RESEARCH.md
// Contradiction 2), which is exactly why HandleWaitHook, not Filter(), is the correct seam here.
func (r *compartmentFaultResource) HandleWait(_ context.Context) error {
	if *r.idx >= len(r.statuses) {
		return liberrors.ErrWaitResource("no more statuses")
	}
	status := r.statuses[*r.idx]
	*r.idx++
	switch status {
	case statusAccepted, statusInProgress:
		return liberrors.ErrWaitResource("work request in progress")
	case statusFailed:
		return fmt.Errorf("work request failed (round %d)", *r.idx)
	case statusSucceeded:
		*r.done = true
		return nil
	default:
		return liberrors.ErrWaitResource("no more statuses")
	}
}

// compartmentFaultLister mirrors leftoverFakeLister's `removed *bool` shape (resources_test/
// leftover_queue_test.go): the fake resource is returned by List() until it is genuinely done,
// then List() returns nothing -- so the post-SUCCEEDED List()/Filter() fallback inside libnuke's
// own HandleWait (below the HandleWaitHook branch) correctly finds nothing and finishes the item.
type compartmentFaultLister struct {
	resource *compartmentFaultResource
	done     *bool
}

func (l *compartmentFaultLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if *l.done {
		return nil, nil
	}
	return []resource.Resource{l.resource}, nil
}

// compartmentFaultState bundles the counters/flags a test observes after n.Run(ctx) returns --
// grouping the return value avoids a 3-blank-identifier dogsled at call sites that only need one
// of them.
type compartmentFaultState struct {
	idx   *int
	calls *int
	done  *bool
}

// newCompartmentFaultTestNuke mirrors newLeftoverTestNuke's exact construction shape
// (resources_test/leftover_queue_test.go): registry.ClearRegistry + t.Cleanup(restoreRealRegistry),
// registry.Register with Scope: ocinuke.CompartmentScope, libnuke.New with NoDryRun/Force/ForceSleep,
// n.SetRunSleep(1*time.Millisecond) to keep the ~3-round give-up threshold fast in tests, and a
// single scanner registered against the fake lister.
func newCompartmentFaultTestNuke(t *testing.T, statuses []string) (*libnuke.Nuke, *compartmentFaultState) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	st := &compartmentFaultState{calls: new(int), idx: new(int), done: new(bool)}

	res := &compartmentFaultResource{calls: st.calls, statuses: statuses, idx: st.idx, done: st.done}

	registry.Register(&registry.Registration{
		Name:   "CompartmentFaultResource",
		Scope:  ocinuke.CompartmentScope,
		Lister: &compartmentFaultLister{resource: res, done: st.done},
	})

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentFaultOwner,
		ResourceTypes: []string{"CompartmentFaultResource"},
		Opts:          &ocinuke.ListerOpts{Region: safetyScanRegion, CompartmentID: compartmentFaultCompartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	return n, st
}

// TestCompartmentFaultInjection_FailedRoundNeverAbortsRun is the load-bearing proof this entire
// phase depends on (06-RESEARCH.md Contradiction 3, 06-CONTEXT.md's "prove this with a
// fault-injection test against a real libnuke.Nuke.Run() early in the phase"). The status
// sequence embeds exactly one FAILED round among otherwise in-flight rounds, ending in SUCCEEDED
// -- if libnuke's handleFailure hard-abort fired on a single FAILED round the way a naive
// Remove()-returns-plain-error-every-round implementation would, n.Run(ctx) would return a
// non-nil error here. It must not.
func TestCompartmentFaultInjection_FailedRoundNeverAbortsRun(t *testing.T) {
	statuses := []string{statusAccepted, statusAccepted, statusFailed, statusAccepted, statusSucceeded}
	n, st := newCompartmentFaultTestNuke(t, statuses)

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) returned error %v, want nil -- a single FAILED round embedded among "+
			"in-flight rounds must never trip libnuke's hardcoded failedCount>=2 hard-abort", err)
	}

	if *st.idx < len(statuses) {
		t.Fatalf("HandleWait consumed only %d of %d statuses, want at least %d -- the FAILED entry "+
			"must have been genuinely reached and consumed, not skipped", *st.idx, len(statuses), len(statuses))
	}

	items := n.Queue.GetItems()
	if len(items) != 1 {
		t.Fatalf("got %d queue items, want exactly 1", len(items))
	}
	if items[0].GetState() != queue.ItemStateFinished {
		t.Fatalf("queue item ended in state %v, want ItemStateFinished", items[0].GetState())
	}
}

// TestCompartmentFaultInjection_AlwaysNonRetryableFailureAborts is the non-vacuous control
// (06-RESEARCH.md's own instruction: "this must be proven, not just argued"). A HandleWait that
// ALWAYS returns a plain, non-ErrWaitResource error, forever, must still trip libnuke's real
// abort -- proving this harness can actually detect the failure mode the first test claims is
// avoided, rather than being trivially green regardless of HandleWait's behavior.
func TestCompartmentFaultInjection_AlwaysNonRetryableFailureAborts(t *testing.T) {
	// No terminal SUCCEEDED entry -- HandleWait must keep returning a plain error every round.
	statuses := []string{
		statusFailed, statusFailed, statusFailed, statusFailed,
		statusFailed, statusFailed, statusFailed, statusFailed,
	}
	n, _ := newCompartmentFaultTestNuke(t, statuses)

	if err := n.Run(context.Background()); err == nil {
		t.Fatal("n.Run(ctx) returned nil, want a non-nil error -- a resource that genuinely never " +
			"recovers must trip libnuke's real abort, proving this harness is not vacuously green")
	}
}
