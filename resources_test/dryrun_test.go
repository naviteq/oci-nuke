package ocinuke_test

import (
	"context"
	"testing"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

type fakeResource struct{ removed *bool }

func (r *fakeResource) Remove(_ context.Context) error {
	*r.removed = true
	return nil
}
func (r *fakeResource) UniqueKey() string { return "fake-1" }

// fakeLister returns one resource on the first call and an empty list thereafter, so a
// real (non-dry-run) removal loop converges instead of polling forever against a resource
// that "never disappears".
type fakeLister struct{ removed *bool }

func (l *fakeLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if *l.removed {
		return nil, nil
	}
	return []resource.Resource{&fakeResource{removed: l.removed}}, nil
}

func newTestNuke(t *testing.T, noDryRun bool, removed *bool) *libnuke.Nuke {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	registry.Register(&registry.Registration{
		Name:   "FakeResource",
		Scope:  ocinuke.CompartmentScope,
		Lister: &fakeLister{removed: removed},
	})

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   noDryRun,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)

	s, err := scanner.New(&scanner.Config{
		Owner:         "us-ashburn-1/ocid1.compartment.oc1..test",
		ResourceTypes: []string{"FakeResource"},
		Opts:          &ocinuke.ListerOpts{Region: "us-ashburn-1", CompartmentID: "ocid1.compartment.oc1..test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDryRunNeverCallsRemove(t *testing.T) {
	var wasRemoved bool
	n := newTestNuke(t, false, &wasRemoved)

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if wasRemoved {
		t.Fatal("Remove() must never be called when NoDryRun is false")
	}
}

// Control case: proves the harness actually exercises Remove() when NoDryRun is true, so a
// future regression that breaks the dry-run gate (e.g. an accidental early return) would be
// caught -- a harness that never calls Remove() under any configuration proves nothing.
func TestNoDryRunCallsRemove(t *testing.T) {
	var wasRemoved bool
	n := newTestNuke(t, true, &wasRemoved)

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if !wasRemoved {
		t.Fatal("Remove() should have been called when NoDryRun is true (control case)")
	}
}
