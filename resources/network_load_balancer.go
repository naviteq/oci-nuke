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
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/sirupsen/logrus"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// networkLoadBalancerClient is the narrow slice of
// networkloadbalancer.NetworkLoadBalancerClient this lister needs -- a hand-written interface so
// NetworkLoadBalancer is stub-testable with zero network access. Every resource type gets its OWN
// narrow interface, scoped to exactly the calls it makes.
//
// networkloadbalancer.ListBackendSetsRequest/Response is a DISTINCT type from Task 1's
// loadbalancer.ListBackendSetsRequest/Response (different SDK package, verified this session
// against networkloadbalancer/list_backend_sets_request_response.go) -- the two packages are
// never conflated here despite the shared method name.
type networkLoadBalancerClient interface {
	ListNetworkLoadBalancers(
		ctx context.Context, req networkloadbalancer.ListNetworkLoadBalancersRequest,
	) (networkloadbalancer.ListNetworkLoadBalancersResponse, error)
	DeleteNetworkLoadBalancer(
		ctx context.Context, req networkloadbalancer.DeleteNetworkLoadBalancerRequest,
	) (networkloadbalancer.DeleteNetworkLoadBalancerResponse, error)
	ListBackendSets(
		ctx context.Context, req networkloadbalancer.ListBackendSetsRequest,
	) (networkloadbalancer.ListBackendSetsResponse, error)
}

// NetworkLoadBalancerResourceType is the registry.Registration.Name for NetworkLoadBalancer.
const NetworkLoadBalancerResourceType = "NetworkLoadBalancer"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     NetworkLoadBalancerResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &NetworkLoadBalancer{},
		Lister:   &networkLoadBalancerLister{},
		// DependsOn is intentionally empty -- the NLB's own backend sets are lifecycle-bound
		// sub-resources exposed only via Properties(), never registered as their own resource
		// type, mirroring Task 1's LoadBalancer/BackendSet/Certificate design.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type networkLoadBalancerLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// networkLoadBalancerList, kept separate so it is unit-testable against a stub client without
// ever constructing a real NetworkLoadBalancer client.
func (l *networkLoadBalancerLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("networkLoadBalancerLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.NetworkLoadBalancer(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing NetworkLoadBalancerClient for %s: %w", o.Region, err)
	}

	return networkLoadBalancerList(ctx, client, o.CompartmentID)
}

// networkLoadBalancerList paginates networkloadbalancer.ListNetworkLoadBalancers and wraps every
// returned item as a NetworkLoadBalancer. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on
// purpose -- this is what the list test below exercises against a stub, with zero network access.
//
// ListNetworkLoadBalancersResponse embeds networkloadbalancer.NetworkLoadBalancerCollection
// (Items []NetworkLoadBalancerSummary, promoted onto the response struct), NOT
// []NetworkLoadBalancer directly -- verified this session against
// networkloadbalancer/list_network_load_balancers_request_response.go and
// network_load_balancer_summary.go. NetworkLoadBalancerSummary carries every field this type's
// Filter()/Remove()/Properties() need (Id, CompartmentId, DisplayName, LifecycleState,
// TimeCreated, FreeformTags, DefinedTags, all mandatory:"true"), so no nil-safety wrapper is
// needed here, unlike Plan 04-07's mandatory:"false" types.
func networkLoadBalancerList(
	ctx context.Context,
	client networkLoadBalancerClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := networkloadbalancer.ListNetworkLoadBalancersRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListNetworkLoadBalancers(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing NetworkLoadBalancer in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &NetworkLoadBalancer{client: client, nlb: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// NetworkLoadBalancer wraps one networkloadbalancer.NetworkLoadBalancerSummary (the list-shape
// type ListNetworkLoadBalancers actually returns -- see networkLoadBalancerList's doc comment).
//
// Notes: Network load balancers are enumerated unconditionally over the compartment, with no relationship
// to any OKE cluster -- one created as a side effect of a Kubernetes `LoadBalancer`-type Service is
// listed and deleted exactly like one created any other way (RES-11). This is proven by
// `resources_test/oke_independent_enumeration_test.go`'s `TestOKEIndependentEnumeration`.
type NetworkLoadBalancer struct {
	client networkLoadBalancerClient
	nlb    networkloadbalancer.NetworkLoadBalancerSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *NetworkLoadBalancer) GetCompartmentID() string { return *r.nlb.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *NetworkLoadBalancer) UniqueKey() string { return *r.nlb.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed here
// excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// networkloadbalancer.LifecycleStateEnum (verified this session, networkloadbalancer/
// lifecycle_state.go -- a package-shared type, distinct from Task 1's per-service
// LoadBalancerLifecycleStateEnum) has exactly six values: CREATING, UPDATING, ACTIVE, DELETING,
// DELETED, FAILED. FAILED is a genuine error state neither cleanly "present" nor "gone" --
// excluded like DELETING/DELETED, but additionally reported via
// ocinuke.ReportLeftover(ReasonAPIError) before returning the exclusion error, matching Task 1's
// LoadBalancer and this wave's established FAULTY/FAILED convention.
func (r *NetworkLoadBalancer) Filter() error {
	switch r.nlb.LifecycleState {
	case networkloadbalancer.LifecycleStateCreating,
		networkloadbalancer.LifecycleStateUpdating,
		networkloadbalancer.LifecycleStateActive:
		return nil
	case networkloadbalancer.LifecycleStateFailed:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  NetworkLoadBalancerResourceType,
			ResourceID:    *r.nlb.Id,
			CompartmentID: *r.nlb.CompartmentId,
			Detail:        "network load balancer is FAILED",
		})
		return fmt.Errorf("NetworkLoadBalancer is %s, not available", r.nlb.LifecycleState)
	default:
		return fmt.Errorf("NetworkLoadBalancer is %s, not available", r.nlb.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *NetworkLoadBalancer) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.nlb
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// DeleteNetworkLoadBalancer is the ONLY mutating call this type ever issues; ListBackendSets
// is read-only and is called ONLY from Properties() below, never from here -- mirroring Task
// 1's "not independently deleted" discipline, proven by
// TestNetworkLoadBalancer_Remove_NeverCallsBackendSetLister.
func (r *NetworkLoadBalancer) Remove(ctx context.Context) error {
	x := r.nlb
	_, err := r.client.DeleteNetworkLoadBalancer(
		ctx, networkloadbalancer.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: x.Id},
	)
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal, plus
// backend_sets -- surfaced here, and ONLY here, mirroring Task 1's LoadBalancer design.
// networkloadbalancer.BackendSet (returned as BackendSetSummary by ListBackendSets, see below)
// has NO Id/OCID field, only Name -- verified this session against networkloadbalancer/
// backend_set.go and backend_set_summary.go -- the same no-independent-identity shape Task 1
// verified for the classic LB's BackendSet/Certificate.
//
// libnuke's resource.Resource.Properties() signature carries no context.Context, so
// context.Background() originates one for this out-of-band, read-only visibility call -- a
// failure here is logged and Properties() continues with the key omitted, never panicking or
// failing the scan over what is deliberately a best-effort visibility call, not a required one.
func (r *NetworkLoadBalancer) Properties() types.Properties {
	x := r.nlb
	props := baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)

	bsResp, err := r.client.ListBackendSets(
		context.Background(), networkloadbalancer.ListBackendSetsRequest{NetworkLoadBalancerId: x.Id},
	)
	if err != nil {
		logrus.WithError(err).WithField("network_load_balancer_id", *x.Id).
			Warn("NetworkLoadBalancer.Properties: ListBackendSets failed, omitting backend_sets")
	} else {
		names := make([]string, 0, len(bsResp.Items))
		for _, bs := range bsResp.Items {
			if bs.Name != nil {
				names = append(names, *bs.Name)
			}
		}
		props.Set("backend_sets", strings.Join(names, ","))
	}

	return props
}
