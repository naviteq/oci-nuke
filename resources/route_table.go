// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// routeTableClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so RouteTable is stub-testable with zero network access. Unlike every
// leaf gateway type in this plan, RouteTable's own addition beyond the plain list/delete pair is
// GetVcn, needed to fetch the owning VCN's DefaultRouteTableId for the pre-emptive exclusion
// check below (04-RESEARCH.md Q5). core.VirtualNetworkClient has all three methods, so this
// stays a single narrow interface, just with one more method than NatGateway's.
type routeTableClient interface {
	ListRouteTables(ctx context.Context, req core.ListRouteTablesRequest) (core.ListRouteTablesResponse, error)
	DeleteRouteTable(ctx context.Context, req core.DeleteRouteTableRequest) (core.DeleteRouteTableResponse, error)
	GetVcn(ctx context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error)
}

// RouteTableResourceType is the registry.Registration.Name for RouteTable.
const RouteTableResourceType = "RouteTable"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     RouteTableResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &RouteTable{},
		Lister:   &routeTableLister{},
		// DependsOn is intentionally empty -- RouteTable is a leaf in this wave's dependency
		// graph. Vcn declares RouteTableResourceType in ITS OWN DependsOn instead. The VCN's own
		// default route table is excluded via Filter() below, never via DependsOn -- it is a
		// permanent OCI constraint (cannot be deleted independently of its VCN), not an ordering
		// concern.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type routeTableLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// routeTableList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *routeTableLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("routeTableLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return routeTableList(ctx, client, o.CompartmentID)
}

// routeTableList paginates core.ListRouteTables and wraps every returned item as a RouteTable.
// Each item's owning Vcn is fetched via GetVcn, cached per-VcnId for the duration of this single
// List() call (a map[string]core.Vcn local to this call, not a package-level cache) so N route
// tables sharing one VCN cost one GetVcn call, not N (T-04-16). A GetVcn failure for a given
// VcnId is ALSO cached (as an error), so a transient failure on one route table's owning VCN
// does not trigger a repeat fetch for a sibling route table sharing the same VcnId within this
// call -- and, per T-04-15, is threaded onto the constructed RouteTable so its own Filter() can
// fail closed (exclude) rather than silently treating an unknown default-route-table status as
// "not the default."
func routeTableList(
	ctx context.Context,
	client routeTableClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	vcnCache := make(map[string]core.Vcn)
	vcnErrCache := make(map[string]error)

	req := core.ListRouteTablesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListRouteTables(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing RouteTable in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			rt := resp.Items[i]
			vcn, vcnErr := resolveOwningVcn(ctx, client, rt.VcnId, vcnCache, vcnErrCache)
			out = append(out, &RouteTable{client: client, rt: rt, vcn: vcn, vcnErr: vcnErr})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// resolveOwningVcn fetches (or returns the already-cached) core.Vcn for vcnID via GetVcn, caching
// both successes and failures in the caller-owned maps so repeated lookups for the same VcnId
// within one List() call never re-issue the GetVcn request. A nil vcnID (should never happen --
// VcnId is a mandatory field on core.RouteTable/core.SecurityList -- but defended against
// anyway) returns a zero-value core.Vcn with a non-nil error, which callers' Filter()
// implementations must treat as fail-closed (excluded), never as "not the default."
func resolveOwningVcn(
	ctx context.Context,
	client interface {
		GetVcn(ctx context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error)
	},
	vcnID *string,
	vcnCache map[string]core.Vcn,
	vcnErrCache map[string]error,
) (core.Vcn, error) {
	if vcnID == nil {
		return core.Vcn{}, fmt.Errorf("resolveOwningVcn: no VcnId on this resource")
	}
	id := *vcnID
	if vcn, ok := vcnCache[id]; ok {
		return vcn, nil
	}
	if err, ok := vcnErrCache[id]; ok {
		return core.Vcn{}, err
	}
	resp, err := client.GetVcn(ctx, core.GetVcnRequest{VcnId: vcnID})
	if err != nil {
		wrapped := fmt.Errorf("fetching owning VCN %s: %w", id, err)
		vcnErrCache[id] = wrapped
		return core.Vcn{}, wrapped
	}
	vcnCache[id] = resp.Vcn
	return resp.Vcn, nil
}

// RouteTable wraps one core.RouteTable, plus its owning Vcn (or the error encountered fetching
// it), resolved once at list time -- see routeTableList/resolveOwningVcn above.
type RouteTable struct {
	client routeTableClient
	rt     core.RouteTable
	vcn    core.Vcn
	vcnErr error
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *RouteTable) GetCompartmentID() string { return *r.rt.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *RouteTable) UniqueKey() string { return *r.rt.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Beyond the lifecycle-state check every type
// in this wave has, RouteTable's Filter() ALSO excludes the VCN's own default route table
// (Vcn.DefaultRouteTableId), a permanent OCI constraint that can never be deleted independently
// of its VCN (`[CITED]` docs.oracle.com/en-us/iaas/Content/Network/Tasks/delete-routetable.htm) --
// excluded pre-emptively here, never attempted and left to fail into a generic api-error
// (T-04-15).
//
// core.RouteTableLifecycleStateEnum (verified this session, core/route_table.go) has exactly
// four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No FAULTY-equivalent state
// exists.
func (r *RouteTable) Filter() error {
	switch r.rt.LifecycleState {
	case core.RouteTableLifecycleStateProvisioning, core.RouteTableLifecycleStateAvailable:
		// present; fall through to the default-route-table check below.
	default:
		return fmt.Errorf("RouteTable is %s, not available", r.rt.LifecycleState)
	}

	// Fail CLOSED (exclude) if the owning Vcn could not be determined -- never fall through to
	// "not the default" on a fetch failure (T-04-15's trust-boundary requirement).
	if r.vcnErr != nil {
		return fmt.Errorf("RouteTable %s: could not verify owning VCN's default route table: %w", r.UniqueKey(), r.vcnErr)
	}

	if r.rt.Id != nil && r.vcn.DefaultRouteTableId != nil && *r.rt.Id == *r.vcn.DefaultRouteTableId {
		return fmt.Errorf(
			"RouteTable %s is the default route table for VCN %s, cannot be deleted independently of its VCN",
			r.UniqueKey(), safeDeref(r.rt.VcnId),
		)
	}

	return nil
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *RouteTable) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.rt
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// The delete request field is RtId, NOT RouteTableId -- a real SDK naming irregularity
// verified this session against core/delete_route_table_request_response.go.
func (r *RouteTable) Remove(ctx context.Context) error {
	x := r.rt
	_, err := r.client.DeleteRouteTable(ctx, core.DeleteRouteTableRequest{RtId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *RouteTable) Properties() types.Properties {
	x := r.rt
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}

// safeDeref returns "" for a nil *string, the pointed-to value otherwise -- used only in error
// message formatting, where a nil VcnId (should never happen, but defended against) must never
// panic.
func safeDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
