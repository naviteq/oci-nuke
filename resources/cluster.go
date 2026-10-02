// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. Cluster is hand-written rather than scaffolded by
// cmd/gen-resource: containerengine.ClusterSummary has NO TimeCreated field at all (verified
// against oci-go-sdk/v65/containerengine/cluster_summary.go this session), which breaks the
// generator template's SafetyTags()/Properties() bodies (both unconditionally dereference
// x.TimeCreated.Time) -- see SafetyTags() below. This is the only reason Cluster is
// hand-written; ClusterSummary DOES have a Summary-suffixed lifecycle-enum alias
// (ClusterSummaryLifecycleStateEnum, a genuine Go type alias for ClusterLifecycleStateEnum, with
// its own duplicated constant set) -- lifecycle-enum naming is not the issue here, unlike
// NodePool (resources/node_pool.go), which has neither.
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

// clusterClient is the narrow slice of containerengine's client this lister needs -- a
// hand-written interface so Cluster is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type clusterClient interface {
	ListClusters(ctx context.Context, req containerengine.ListClustersRequest) (containerengine.ListClustersResponse, error)
	DeleteCluster(ctx context.Context, req containerengine.DeleteClusterRequest) (containerengine.DeleteClusterResponse, error)
}

// ClusterResourceType is the registry.Registration.Name for Cluster.
const ClusterResourceType = "Cluster"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ClusterResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Cluster{},
		Lister:   &clusterLister{},
		// DependsOn: NodePool must be scanned and removed before its owning Cluster, so that a
		// still-attached node pool never blocks or races DeleteCluster. Bare string literal --
		// NodePool is registered in this same plan's other file, but per 05-PATTERNS.md's
		// cross-plan-boundary convention every Wave-5 file is treated as its own PR-boundary
		// unit, matching Vcn's own cross-plan DependsOn shape (04-RESEARCH.md Q1).
		DependsOn: []string{"NodePool"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type clusterLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in clusterList,
// kept separate so it is unit-testable against a stub client without ever constructing a real
// ContainerEngine client.
func (l *clusterLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("clusterLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.ContainerEngine(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing ContainerEngine client for %s: %w", o.Region, err)
	}

	return clusterList(ctx, client, o.CompartmentID)
}

// clusterList paginates containerengine.ListClusters and wraps every returned item as a
// Cluster. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func clusterList(
	ctx context.Context,
	client clusterClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := containerengine.ListClustersRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListClusters(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Cluster in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Cluster{client: client, cluster: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Cluster wraps one containerengine.ClusterSummary.
type Cluster struct {
	client  clusterClient
	cluster containerengine.ClusterSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// ClusterSummary.CompartmentId is mandatory:"false" (verified this session) -- nil-checked here
// rather than dereferenced directly, so a record missing it is dropped fail-closed by
// scopedLister rather than panicking mid-scan.
func (r *Cluster) GetCompartmentID() string {
	if r.cluster.CompartmentId == nil {
		return ""
	}
	return *r.cluster.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// ClusterSummary.Id is mandatory:"false" (verified this session) -- nil-checked here, matching
// GetCompartmentID's fail-closed shape, rather than dereferenced directly.
func (r *Cluster) UniqueKey() string {
	if r.cluster.Id == nil {
		return ""
	}
	return *r.cluster.Id
}

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment. Present = CREATING, ACTIVE, UPDATING, FAILED --
// a FAILED cluster still exists and is still a valid DeleteCluster target, so it is deliberately
// NOT excluded here. LoadBalancer, MountTarget and the other resources/failed_delete.go types
// follow the same rule.
// DELETING/DELETED are excluded (going/gone, the hang-trap defense), as is any future SDK value
// this switch does not recognize (fail-safe exclusion).
func (r *Cluster) Filter() error {
	switch r.cluster.LifecycleState {
	case containerengine.ClusterLifecycleStateCreating,
		containerengine.ClusterLifecycleStateActive,
		containerengine.ClusterLifecycleStateUpdating,
		containerengine.ClusterLifecycleStateFailed:
		return nil
	default:
		return fmt.Errorf("Cluster is %s, not available", r.cluster.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and a zero time.Time for createdAt.
// containerengine.ClusterSummary has NO TimeCreated-shaped field at all (verified this session,
// oci-go-sdk/v65/containerengine/cluster_summary.go) -- a third, more extreme case than
// resources/support.go's timeCreatedOrZero (that helper nil-checks a field that EXISTS; here the
// field does not exist at all, so the zero value is hardcoded directly). A zero time.Time is
// never falsely "protected" by a min-age check (see timeCreatedOrZero's own doc comment for the
// exact guarantee this relies on).
func (r *Cluster) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.cluster
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), time.Time{}
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Cluster) Remove(ctx context.Context) error {
	x := r.cluster
	_, err := r.client.DeleteCluster(ctx, containerengine.DeleteClusterRequest{ClusterId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper -- timeCreated is passed as nil since ClusterSummary has no such
// field at all; baseProperties' own timeCreated != nil guard already omits the time_created key
// correctly for that case, so no hand-built Properties() is needed here.
func (r *Cluster) Properties() types.Properties {
	x := r.cluster
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), nil, x.FreeformTags, x.DefinedTags)
}
