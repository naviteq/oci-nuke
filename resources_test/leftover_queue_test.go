package ocinuke_test

import (
	"context"
	"fmt"
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

// leftoverFakeResource's Remove() always fails when alwaysFail is true, or fails on exactly its
// first two calls and succeeds thereafter otherwise -- the exact two shapes 03-RESEARCH.md's
// Q5/Q7 sections recommend to prove libnuke's ~3-round give-up threshold both trips (always-fail)
// and tolerates transient failure (fail-twice-then-succeed).
type leftoverFakeResource struct {
	calls      *int
	alwaysFail bool
	removed    *bool
}

func (r *leftoverFakeResource) Remove(_ context.Context) error {
	*r.calls++
	if r.alwaysFail || *r.calls <= 2 {
		return fmt.Errorf("remove failed (call %d)", *r.calls)
	}
	*r.removed = true
	return nil
}

func (r *leftoverFakeResource) UniqueKey() string { return "leftover-fake-1" }

// leftoverFakeLister returns the one fake resource until it has actually been removed --
// mirroring dryrun_test.go's fakeLister -- so both the initial Scan() and every subsequent
// HandleWait re-list observe the same live state: present until Remove() actually succeeds, gone
// immediately after.
type leftoverFakeLister struct {
	calls      *int
	alwaysFail bool
	removed    *bool
}

func (l *leftoverFakeLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if *l.removed {
		return nil, nil
	}
	return []resource.Resource{
		&leftoverFakeResource{calls: l.calls, alwaysFail: l.alwaysFail, removed: l.removed},
	}, nil
}

// newLeftoverTestNuke mirrors dryrun_test.go's newTestNuke construction shape exactly
// (registry.ClearRegistry, libnuke.New(&libnuke.Parameters{...}), scanner.New, RegisterScanner),
// wired with alwaysFail's fake resource instead. n.SetRunSleep is lowered from libnuke's 5s
// default so exercising the real ~3-round give-up threshold does not make this test slow --
// SetRunSleep(0) means "unset, revert to 5s" (03-CONTEXT.md's carried-forward note), so a small
// non-zero duration is used instead.
func newLeftoverTestNuke(t *testing.T, alwaysFail bool) (n *libnuke.Nuke, calls *int, removed *bool) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	calls = new(int)
	removed = new(bool)

	registry.Register(&registry.Registration{
		Name:   "LeftoverFakeResource",
		Scope:  ocinuke.CompartmentScope,
		Lister: &leftoverFakeLister{calls: calls, alwaysFail: alwaysFail, removed: removed},
	})

	n = libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         "us-ashburn-1/ocid1.compartment.oc1..test",
		ResourceTypes: []string{"LeftoverFakeResource"},
		Opts:          &ocinuke.ListerOpts{Region: "us-ashburn-1", CompartmentID: "ocid1.compartment.oc1..test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	return n, calls, removed
}

// TestLeftoverQueue_AlwaysFailingResourceReportsAPIErrorDespiteRunError is the load-bearing
// proof (T-03-07) that leftover reporting is never gated on n.Run(ctx)'s own error return: the
// always-failing resource makes n.Run(ctx) return a non-nil error after libnuke's real,
// unexported ~3-round give-up threshold, yet the leftover walk -- run unconditionally afterward
// -- still correctly classifies the item as api-error.
func TestLeftoverQueue_AlwaysFailingResourceReportsAPIErrorDespiteRunError(t *testing.T) {
	n, calls, removed := newLeftoverTestNuke(t, true)

	err := n.Run(context.Background())
	if err == nil {
		t.Fatal("n.Run(ctx) returned nil, want a non-nil error after the ~3-round give-up threshold")
	}
	if *calls < 3 {
		t.Fatalf("Remove() called %d times, want at least 3 (the ~3-round give-up threshold)", *calls)
	}
	if *removed {
		t.Fatal("resource should never have been marked removed for the always-failing case")
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)

	leftovers := entriesWithState(entries, plan.StateLeftover)
	if len(leftovers) != 1 {
		t.Fatalf("got %d leftover entries, want exactly 1 (entries: %+v)", len(leftovers), entries)
	}
	if leftovers[0].Reason != scope.ReasonAPIError {
		t.Errorf("leftover entry Reason = %q, want %q", leftovers[0].Reason, scope.ReasonAPIError)
	}
}

// TestLeftoverQueue_TransientFailureThenSuccessReportsRemoved is the non-vacuous control case:
// a harness that always reported StateLeftover regardless of outcome would pass the
// always-failing test above but fail this one. Remove() fails exactly twice then succeeds;
// n.Run(ctx) must converge to nil, and the leftover walk must report StateRemoved, not
// StateLeftover, proving the ~3-round budget tolerates transient, eventually-resolving failure.
func TestLeftoverQueue_TransientFailureThenSuccessReportsRemoved(t *testing.T) {
	n, calls, removed := newLeftoverTestNuke(t, false)

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) returned error %v, want nil (transient failure should resolve within the give-up threshold)", err)
	}
	if *calls != 3 {
		t.Fatalf("Remove() called %d times, want exactly 3 (fail, fail, succeed)", *calls)
	}
	if !*removed {
		t.Fatal("resource should have been marked removed once Remove() finally succeeded")
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)

	removedEntries := entriesWithState(entries, plan.StateRemoved)
	if len(removedEntries) != 1 {
		t.Fatalf("got %d removed entries, want exactly 1 (entries: %+v)", len(removedEntries), entries)
	}

	leftovers := entriesWithState(entries, plan.StateLeftover)
	if len(leftovers) != 0 {
		t.Fatalf("got %d leftover entries, want 0 (entries: %+v)", len(leftovers), entries)
	}
}

func entriesWithState(entries []plan.Entry, state plan.EntryState) []plan.Entry {
	var matched []plan.Entry
	for _, e := range entries {
		if e.State == state {
			matched = append(matched, e)
		}
	}
	return matched
}
