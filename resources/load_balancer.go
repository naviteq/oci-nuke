// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/sirupsen/logrus"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// loadBalancerClient is the narrow slice of loadbalancer.LoadBalancerClient this lister needs --
// a hand-written interface so LoadBalancer is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
//
// ListBackendSets/ListCertificates are included here even though neither is ever called from
// Remove() -- both are read-only calls Properties() issues for filter/debug visibility only. See
// this type's Properties() doc comment for the "not independently deleted" evidence this plan's
// objective is built on.
type loadBalancerClient interface {
	ListLoadBalancers(ctx context.Context, req loadbalancer.ListLoadBalancersRequest) (loadbalancer.ListLoadBalancersResponse, error)
	DeleteLoadBalancer(ctx context.Context, req loadbalancer.DeleteLoadBalancerRequest) (loadbalancer.DeleteLoadBalancerResponse, error)
	GetLoadBalancer(ctx context.Context, req loadbalancer.GetLoadBalancerRequest) (loadbalancer.GetLoadBalancerResponse, error)
	ListBackendSets(ctx context.Context, req loadbalancer.ListBackendSetsRequest) (loadbalancer.ListBackendSetsResponse, error)
	ListCertificates(ctx context.Context, req loadbalancer.ListCertificatesRequest) (loadbalancer.ListCertificatesResponse, error)
}

// LoadBalancerResourceType is the registry.Registration.Name for LoadBalancer.
const LoadBalancerResourceType = "LoadBalancer"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     LoadBalancerResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &LoadBalancer{},
		Lister:   &loadBalancerLister{},
		// DependsOn is intentionally empty -- backend sets and certificates are lifecycle-bound
		// sub-resources exposed only via Properties(), never registered as their own resource
		// type, so there is no sibling type for LoadBalancer to depend on.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type loadBalancerLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// loadBalancerList, kept separate so it is unit-testable against a stub client without ever
// constructing a real LoadBalancer client.
func (l *loadBalancerLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("loadBalancerLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.LoadBalancer(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing LoadBalancerClient for %s: %w", o.Region, err)
	}

	return loadBalancerList(ctx, client, o.CompartmentID)
}

// loadBalancerList paginates loadbalancer.ListLoadBalancers and wraps every returned item as a
// LoadBalancer. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func loadBalancerList(
	ctx context.Context,
	client loadBalancerClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := loadbalancer.ListLoadBalancersRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListLoadBalancers(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing LoadBalancer in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &LoadBalancer{client: client, lb: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// LoadBalancer wraps one loadbalancer.LoadBalancer.
//
// Notes: Load balancers are enumerated unconditionally over the compartment, with no relationship to any
// OKE cluster -- a load balancer created as a side effect of a Kubernetes `LoadBalancer`-type
// Service is listed and deleted exactly like one created any other way (RES-11). This is proven by
// `resources_test/oke_independent_enumeration_test.go`'s `TestOKEIndependentEnumeration`.
type LoadBalancer struct {
	failedDeletes
	client loadBalancerClient
	lb     loadbalancer.LoadBalancer
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *LoadBalancer) GetCompartmentID() string { return *r.lb.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *LoadBalancer) UniqueKey() string { return *r.lb.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed here
// excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// loadbalancer.LoadBalancerLifecycleStateEnum (verified this session, loadbalancer/load_balancer.go)
// has exactly five values: CREATING, FAILED, ACTIVE, DELETING, DELETED. FAILED is present: a
// FAILED load balancer still exists and DeleteLoadBalancer accepts it. A delete that ends in FAILED
// is caught by HandleWait below, not here -- see resources/failed_delete.go.
func (r *LoadBalancer) Filter() error {
	switch r.lb.LifecycleState {
	case loadbalancer.LoadBalancerLifecycleStateCreating,
		loadbalancer.LoadBalancerLifecycleStateActive,
		loadbalancer.LoadBalancerLifecycleStateFailed:
		return nil
	default:
		return fmt.Errorf("LoadBalancer is %s, not available", r.lb.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *LoadBalancer) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.lb
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// DeleteLoadBalancer is the ONLY mutating call this type ever issues; ListBackendSets/
// ListCertificates are read-only and are called ONLY from Properties() below, never from here
// -- this is the "not independently deleted" half of this plan's objective, proven by
// TestLoadBalancer_Remove_NeverCallsBackendSetOrCertificateLister. It stops calling it, and holds,
// once maxFailedDeletes deletes have ended in FAILED (resources/failed_delete.go).
func (r *LoadBalancer) Remove(ctx context.Context) error {
	if refused := r.refuse(); refused != nil {
		return refused
	}
	x := r.lb
	_, err := r.client.DeleteLoadBalancer(ctx, loadbalancer.DeleteLoadBalancerRequest{LoadBalancerId: x.Id})
	r.issued(err)
	return holdOn409(err)
}

// HandleWait satisfies resource.HandleWaitHook. It reads the load balancer back after the delete
// and reports a delete that ended in FAILED -- see resources/failed_delete.go.
func (r *LoadBalancer) HandleWait(ctx context.Context) error {
	resp, err := r.client.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{LoadBalancerId: r.lb.Id})
	outcome := deleteNotStarted
	switch resp.LifecycleState {
	case loadbalancer.LoadBalancerLifecycleStateDeleting:
		outcome = deleteInFlight
	case loadbalancer.LoadBalancerLifecycleStateDeleted:
		outcome = deleteGone
	case loadbalancer.LoadBalancerLifecycleStateFailed:
		outcome = deleteFailed
	}
	return r.wait("load balancer", outcome, "", err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal, plus
// backend_sets/certificates -- surfaced here, and ONLY here, per this plan's objective (04-08-
// PLAN.md's resolution of 04-RESEARCH.md's Assumption A5): loadbalancer.BackendSet and
// loadbalancer.Certificate have NO Id/OCID field at all (verified this session against
// loadbalancer/backend_set.go and loadbalancer/certificate.go -- BackendSet.Name and
// Certificate.CertificateName are the only identity fields either struct carries), so neither is
// independently addressable as its own resource type. They are lifecycle-bound sub-resources of
// their parent LoadBalancer, deleted only as a side effect of DeleteLoadBalancer.
//
// libnuke's resource.Resource.Properties() signature carries no context.Context, so
// context.Background() originates one for these two out-of-band, read-only visibility calls --
// a failure here is logged and Properties() continues with the key omitted, never panicking or
// failing the scan over what is deliberately a best-effort visibility call, not a required one.
func (r *LoadBalancer) Properties() types.Properties {
	x := r.lb
	props := baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)

	bsResp, err := r.client.ListBackendSets(context.Background(), loadbalancer.ListBackendSetsRequest{LoadBalancerId: x.Id})
	if err != nil {
		logrus.WithError(err).WithField("load_balancer_id", *x.Id).Warn("LoadBalancer.Properties: ListBackendSets failed, omitting backend_sets")
	} else {
		names := make([]string, 0, len(bsResp.Items))
		for _, bs := range bsResp.Items {
			if bs.Name != nil {
				names = append(names, *bs.Name)
			}
		}
		props.Set("backend_sets", strings.Join(names, ","))
	}

	certResp, err := r.client.ListCertificates(context.Background(), loadbalancer.ListCertificatesRequest{LoadBalancerId: x.Id})
	if err != nil {
		logrus.WithError(err).WithField("load_balancer_id", *x.Id).Warn("LoadBalancer.Properties: ListCertificates failed, omitting certificates")
	} else {
		names := make([]string, 0, len(certResp.Items))
		for _, c := range certResp.Items {
			if c.CertificateName != nil {
				names = append(names, *c.CertificateName)
			}
		}
		props.Set("certificates", strings.Join(names, ","))
	}

	return props
}
