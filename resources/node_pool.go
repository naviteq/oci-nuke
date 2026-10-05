// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. NodePool is hand-written for TWO independent reasons,
// distinct from Cluster's single reason (resources/cluster.go) -- do not conflate them:
//  1. containerengine.NodePoolSummary has NO TimeCreated field at all (verified against
//     oci-go-sdk/v65/containerengine/node_pool_summary.go); its creation time comes from the
//     NODEPOOL_CREATE work request instead, see nodePoolCreationTimes.
//  2. UNLIKE Cluster, NodePoolSummary has NO Summary-suffixed lifecycle-enum alias at all --
//     only the plain NodePoolLifecycleStateEnum exists, so NodePool fails both
//     generator-friendliness checks Cluster only fails one of.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/containerengine"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// nodePoolClient is the narrow slice of containerengine's client this lister needs -- a
// hand-written interface so NodePool is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type nodePoolClient interface {
	ListNodePools(ctx context.Context, req containerengine.ListNodePoolsRequest) (containerengine.ListNodePoolsResponse, error)
	DeleteNodePool(ctx context.Context, req containerengine.DeleteNodePoolRequest) (containerengine.DeleteNodePoolResponse, error)
	ListWorkRequests(ctx context.Context, req containerengine.ListWorkRequestsRequest) (containerengine.ListWorkRequestsResponse, error)
}

// NodePoolResourceType is the registry.Registration.Name for NodePool.
const NodePoolResourceType = "NodePool"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     NodePoolResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &NodePool{},
		Lister:   &nodePoolLister{},
		// DependsOn is intentionally empty -- declare it on whichever type actually has the
		// dependency, never on the type depended upon (04-RESEARCH.md Q1). Cluster
		// (resources/cluster.go) is the one that declares DependsOn: []string{"NodePool"}.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type nodePoolLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in nodePoolList,
// kept separate so it is unit-testable against a stub client without ever constructing a real
// ContainerEngine client.
func (l *nodePoolLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("nodePoolLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.ContainerEngine(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing ContainerEngine client for %s: %w", o.Region, err)
	}

	return nodePoolList(ctx, client, o.CompartmentID, o.SafetyFilter.MinAge > 0)
}

// nodePoolList paginates containerengine.ListNodePools and wraps every returned item as a
// NodePool. Deliberately does NOT set ListNodePoolsRequest.ClusterId (an optional filter) --
// the union across every cluster in the compartment IS the complete, unconditional per-
// compartment enumeration RES-11 depends on (05-CONTEXT.md: node-pool-backing instances are
// covered by Phase 4's unconditional Instance enumeration, with no OKE-specific type). Isolated
// from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below
// exercises against a stub, with zero network access.
//
// With withCreationTime set -- only when settings.protect.min-age is on -- each node pool also
// gets its creation time, read from the NODEPOOL_CREATE work requests of the compartment.
func nodePoolList(
	ctx context.Context,
	client nodePoolClient,
	compartmentID string,
	withCreationTime bool,
) ([]resource.Resource, error) {
	var created map[string]time.Time
	if withCreationTime {
		var err error
		if created, err = nodePoolCreationTimes(ctx, client, compartmentID); err != nil {
			return nil, err
		}
	}

	var out []resource.Resource
	req := containerengine.ListNodePoolsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListNodePools(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing NodePool in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			np := &NodePool{client: client, nodePool: resp.Items[i]}
			if np.nodePool.Id != nil {
				np.createdAt = created[*np.nodePool.Id]
			}
			out = append(out, np)
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// nodePoolCreationTimes maps each node pool OCID to the time its NODEPOOL_CREATE work request was
// accepted. NodePoolSummary carries no creation time of its own; the work request is the only
// record of one. A failure here fails the listing: min-age cannot be applied without it.
func nodePoolCreationTimes(ctx context.Context, client nodePoolClient, compartmentID string) (map[string]time.Time, error) {
	created := map[string]time.Time{}
	req := containerengine.ListWorkRequestsRequest{
		CompartmentId: &compartmentID,
		ResourceType:  containerengine.ListWorkRequestsResourceTypeNodepool,
	}
	for {
		resp, err := client.ListWorkRequests(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing NodePool work requests in %s (needed for min-age): %w", compartmentID, err)
		}
		for _, wr := range resp.Items {
			if wr.OperationType != containerengine.WorkRequestOperationTypeNodepoolCreate || wr.TimeAccepted == nil {
				continue
			}
			for _, res := range wr.Resources {
				if res.ActionType == containerengine.WorkRequestResourceActionTypeCreated && res.Identifier != nil {
					created[*res.Identifier] = wr.TimeAccepted.Time
				}
			}
		}
		if resp.OpcNextPage == nil {
			return created, nil
		}
		req.Page = resp.OpcNextPage
	}
}

// NodePool wraps one containerengine.NodePoolSummary, plus its creation time when the lister
// looked it up.
type NodePool struct {
	client    nodePoolClient
	nodePool  containerengine.NodePoolSummary
	createdAt time.Time
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// NodePoolSummary.CompartmentId is mandatory:"false" (verified this session) -- nil-checked here
// rather than dereferenced directly, so a record missing it is dropped fail-closed by
// scopedLister rather than panicking mid-scan.
func (r *NodePool) GetCompartmentID() string {
	if r.nodePool.CompartmentId == nil {
		return ""
	}
	return *r.nodePool.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// NodePoolSummary.Id is mandatory:"false" (verified this session) -- nil-checked here, matching
// GetCompartmentID's fail-closed shape, rather than dereferenced directly.
func (r *NodePool) UniqueKey() string {
	if r.nodePool.Id == nil {
		return ""
	}
	return *r.nodePool.Id
}

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment. Present = CREATING, ACTIVE, UPDATING, INACTIVE,
// FAILED, NEEDS_ATTENTION -- every non-terminal state a node pool can be deleted from. DELETING/
// DELETED are excluded (going/gone, the hang-trap defense), as is any future SDK value this
// switch does not recognize (fail-safe exclusion).
func (r *NodePool) Filter() error {
	switch r.nodePool.LifecycleState {
	case containerengine.NodePoolLifecycleStateCreating,
		containerengine.NodePoolLifecycleStateActive,
		containerengine.NodePoolLifecycleStateUpdating,
		containerengine.NodePoolLifecycleStateInactive,
		containerengine.NodePoolLifecycleStateFailed,
		containerengine.NodePoolLifecycleStateNeedsAttention:
		return nil
	default:
		return fmt.Errorf("NodePool is %s, not available", r.nodePool.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated. The creation time is the one nodePoolList found
// in the pool's NODEPOOL_CREATE work request. It stays zero -- old, so not protected -- when
// min-age is off and nothing was looked up, or when OCI no longer keeps that work request, which
// it only stops doing long after any min-age a sandbox would set.
func (r *NodePool) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.nodePool
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), r.createdAt
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *NodePool) Remove(ctx context.Context) error {
	x := r.nodePool
	_, err := r.client.DeleteNodePool(ctx, containerengine.DeleteNodePoolRequest{NodePoolId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper -- timeCreated is passed as nil since NodePoolSummary has no such
// field at all; baseProperties' own timeCreated != nil guard already omits the time_created key
// correctly for that case, so no hand-built Properties() is needed here.
func (r *NodePool) Properties() types.Properties {
	x := r.nodePool
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), nil, x.FreeformTags, x.DefinedTags)
}
