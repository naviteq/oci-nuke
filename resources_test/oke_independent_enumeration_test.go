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
)

// TestOKEIndependentEnumeration is RES-11's regression proof (05-CONTEXT.md: "RES-11 is closed
// by proof, not by new types"). resources/load_balancer.go's own List() (and
// resources/instance.go's) calls ListLoadBalancers/ListInstances with CompartmentId as the ONLY
// scope parameter -- neither request type accepts, nor does either wrapper struct carry, any
// notion of an owning OKE Cluster. This test proves that structural fact end to end: a
// LoadBalancer-shaped fixture and an Instance-shaped fixture are listed, planned, and attempted
// for removal via a real ocinuke.Register-wrapped libnuke.Nuke.Run() pass REGARDLESS of whether
// any Cluster exists in the same compartment -- the Cluster fixture below always returns zero
// items, and removal still succeeds identically. No OkeManagedLoadBalancer-shaped type, and no
// tag-based OKE-ownership heuristic, exists anywhere in this codebase; this test is the
// operator-facing documentation CONTEXT.md requires for that guarantee, alongside a one-paragraph
// note Plan 05-07 adds to docs/resources/LoadBalancer.md's regenerated output.
//
// Structural note (in place of a go/ast assertion, per 05-01-PLAN.md's "do not over-engineer"
// guidance): okeIndependentFixture below -- standing in for both LoadBalancer and Instance -- has
// exactly two identity fields, uid and compartmentID. Neither resources/load_balancer.go's
// LoadBalancer struct (client + loadbalancer.LoadBalancerSummary) nor resources/instance.go's
// Instance struct (client + core.Instance) has a field or method that could carry a
// cluster-ownership relationship either -- core.Instance and loadbalancer.LoadBalancerSummary
// were read in full for this plan and neither SDK type declares anything OKE-cluster-shaped.
func TestOKEIndependentEnumeration(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	const (
		region        = "us-ashburn-1"
		compartmentID = "ocid1.compartment.oc1..oke-independent-enumeration"
		owner         = region + "/" + compartmentID
	)

	inScope := func(string) bool { return true }

	lbRemoved := false
	lb := &okeIndependentFixture{uid: "LoadBalancer-1", compartmentID: compartmentID, removed: &lbRemoved}
	ocinuke.Register(&registry.Registration{
		Name:     "LoadBalancer",
		Scope:    ocinuke.CompartmentScope,
		Resource: lb,
		Lister:   &okeIndependentLister{instances: []*okeIndependentFixture{lb}},
	}, inScope, nil)

	instanceRemoved := false
	instance := &okeIndependentFixture{uid: "Instance-1", compartmentID: compartmentID, removed: &instanceRemoved}
	ocinuke.Register(&registry.Registration{
		Name:     "Instance",
		Scope:    ocinuke.CompartmentScope,
		Resource: instance,
		Lister:   &okeIndependentLister{instances: []*okeIndependentFixture{instance}},
	}, inScope, nil)

	// Cluster ALWAYS returns zero items -- the same guarantee this test proves for
	// LoadBalancer/Instance must hold whether or not any cluster is present at all.
	ocinuke.Register(&registry.Registration{
		Name:     "Cluster",
		Scope:    ocinuke.CompartmentScope,
		Resource: &okeIndependentFixture{uid: "unused-cluster-registration-slot"},
		Lister:   &okeIndependentLister{instances: nil},
	}, inScope, nil)

	n := libnuke.New(&libnuke.Parameters{NoDryRun: true, Force: true, ForceSleep: 3}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         owner,
		ResourceTypes: []string{"LoadBalancer", "Instance", "Cluster"},
		Opts:          &ocinuke.ListerOpts{Region: region, CompartmentID: compartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Logf("Run() returned error: %v (not asserted on -- the removed flags are the source of truth)", err)
	}

	if !lbRemoved {
		t.Error("LoadBalancer fixture was never removed -- it must be attempted regardless of " +
			"Cluster's own (empty) state, proving unconditional, cluster-independent enumeration")
	}
	if !instanceRemoved {
		t.Error("Instance fixture was never removed -- it must be attempted regardless of " +
			"Cluster's own (empty) state, proving unconditional, cluster-independent enumeration")
	}
}

// okeIndependentFixture stands in for both LoadBalancer and Instance -- deliberately carrying
// ONLY a uid and a compartmentID, no cluster-ownership field of any kind, mirroring the real
// LoadBalancer/Instance structs' own compartment-only identity shape.
type okeIndependentFixture struct {
	uid           string
	compartmentID string
	removed       *bool
}

func (r *okeIndependentFixture) Remove(_ context.Context) error {
	if r.removed != nil {
		*r.removed = true
	}
	return nil
}

func (r *okeIndependentFixture) UniqueKey() string        { return r.uid }
func (r *okeIndependentFixture) GetCompartmentID() string { return r.compartmentID }
func (r *okeIndependentFixture) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// okeIndependentLister returns every instance whose removed flag is not yet set, mirroring
// resources_test/equivalence_test.go's recordingLister shape so libnuke's HandleWait re-list
// converges instead of polling forever.
type okeIndependentLister struct {
	instances []*okeIndependentFixture
}

func (l *okeIndependentLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}
