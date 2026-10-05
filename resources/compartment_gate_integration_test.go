package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// slowResource disappears from its listing only some rounds after Remove(), like a subnet whose
// delete takes a while.
type slowResource struct {
	removed   bool
	listsLeft int
}

func (r *slowResource) Remove(context.Context) error { r.removed = true; return nil }

func (r *slowResource) UniqueKey() string { return "ocid1.subnet.oc1..slow" }

type slowLister struct{ r *slowResource }

func (l *slowLister) List(context.Context, interface{}) ([]resource.Resource, error) {
	if l.r.removed {
		if l.r.listsLeft == 0 {
			return nil, nil
		}
		l.r.listsLeft--
	}
	return []resource.Resource{l.r}, nil
}

// runGatedCompartment runs one destructive Nuke over a gated Compartment plus, when slow is set,
// a resource of the same compartment that takes a few rounds to go. skips stand in for what the
// run reported elsewhere. It returns the plan entries, how many DeleteCompartment calls went
// out, whether slow was already gone at the first one, and the run error.
func runGatedCompartment(
	t *testing.T, suffix string, slow *slowResource, skips []scope.SkipEvent,
) (entries []plan.Entry, deletes int, slowGoneAtDelete bool, runErr error) {
	t.Helper()
	id := testCompartmentOwnOCID
	gone := new(bool)

	client := &stubCompartmentClient{
		deleteCompartmentFn: func(context.Context, identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error) {
			deletes++
			if deletes == 1 && slow != nil {
				slowGoneAtDelete = slow.removed && slow.listsLeft == 0
			}
			wrID := testWorkRequestOCID
			return identity.DeleteCompartmentResponse{OpcWorkRequestId: &wrID}, nil
		},
		getWorkRequestFn: func(context.Context, identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
			*gone = true
			return identity.GetWorkRequestResponse{WorkRequest: identity.WorkRequest{Status: identity.WorkRequestStatusSucceeded}}, nil
		},
	}
	fixture := &Compartment{
		client: client,
		compartment: identity.Compartment{
			Id: &id, LifecycleState: identity.CompartmentLifecycleStateActive, TimeCreated: testCompartmentTimeCreated(),
		},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	compartmentType := "CompartmentGateFixture" + suffix
	slowType := "CompartmentGateSlowFixture" + suffix
	types := []string{compartmentType}
	ocinuke.Register(&registry.Registration{
		Name: compartmentType, Scope: ocinuke.CompartmentScope, Resource: fixture,
		Lister: &fixtureCompartmentLister{compartment: fixture, gone: gone},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
	if slow != nil {
		registry.Register(&registry.Registration{
			Name: slowType, Scope: ocinuke.CompartmentScope, Resource: slow, Lister: &slowLister{r: slow},
		})
		types = append(types, slowType)
	}

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun: true, Force: true, ForceSleep: 3,
		// Large on purpose: a gate that waited on residue would sit here for the whole budget.
		MaxWaitRetries: 1000,
	}, filter.Filters{}, nil)
	n.SetRunSleep(time.Millisecond)

	// The fixture types are not "Compartment", so stand in for the real type name the way the
	// pipeline's own queue would carry it.
	fixture.occupancy = func() ocinuke.Occupancy {
		items := n.Queue.GetItems()
		var others []*queue.Item
		for _, it := range items {
			if it.Type != compartmentType {
				others = append(others, it)
			}
		}
		return ocinuke.CompartmentOccupancy(id, others, skips)
	}

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + id,
		ResourceTypes: types,
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	runErr = n.Run(context.Background())
	return plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover), deletes, slowGoneAtDelete, runErr
}

// TestCompartmentGate_WaitsForTheRunThenDeletesOnce: the compartment is not deleted while its
// last resource is still going, and then exactly once.
func TestCompartmentGate_WaitsForTheRunThenDeletesOnce(t *testing.T) {
	slow := &slowResource{listsLeft: 3}

	entries, deletes, slowGone, runErr := runGatedCompartment(t, "Wait", slow, nil)

	if runErr != nil {
		t.Fatalf("n.Run() = %v, want nil", runErr)
	}
	if deletes != 1 || !slowGone {
		t.Fatalf("DeleteCompartment called %d times, first with the resource gone=%v; want once, after it was gone", deletes, slowGone)
	}
	for _, e := range entries {
		if e.State != plan.StateRemoved {
			t.Errorf("entry %s %s = %s, want removed", e.ResourceType, e.ResourceID, e.State)
		}
	}
}

// TestCompartmentGate_StopsOverResidueWithoutBurningTheBudget: residue the run cannot remove ends
// the Nuke within libnuke's failed-item rounds, with no DeleteCompartment and the compartment a
// leftover.
func TestCompartmentGate_StopsOverResidueWithoutBurningTheBudget(t *testing.T) {
	skips := []scope.SkipEvent{{
		Reason: scope.ReasonScheduledDeletion, ResourceType: VaultResourceType, ResourceID: "ocid1.vault.oc1..pending",
		CompartmentID: testCompartmentOwnOCID, Detail: "vault already scheduled for deletion",
	}}

	start := time.Now()
	entries, deletes, _, runErr := runGatedCompartment(t, "Residue", nil, skips)

	if runErr == nil {
		t.Fatal("n.Run() = nil, want the failed-item error a refused compartment ends with")
	}
	if deletes != 0 {
		t.Errorf("DeleteCompartment called %d times, want 0", deletes)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("run took %v; the gate waited instead of refusing", elapsed)
	}
	if len(entries) != 1 || entries[0].State != plan.StateLeftover {
		t.Fatalf("entries = %+v, want one leftover compartment", entries)
	}
}
