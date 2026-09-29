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

// securityListClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so SecurityList is stub-testable with zero network access. Identical
// shape to routeTableClient (resources/route_table.go): GetVcn is needed to fetch the owning
// VCN's DefaultSecurityListId for the pre-emptive exclusion check below (04-RESEARCH.md Q5).
type securityListClient interface {
	ListSecurityLists(ctx context.Context, req core.ListSecurityListsRequest) (core.ListSecurityListsResponse, error)
	DeleteSecurityList(ctx context.Context, req core.DeleteSecurityListRequest) (core.DeleteSecurityListResponse, error)
	GetVcn(ctx context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error)
}

// SecurityListResourceType is the registry.Registration.Name for SecurityList.
const SecurityListResourceType = "SecurityList"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     SecurityListResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &SecurityList{},
		Lister:   &securityListLister{},
		// DependsOn is intentionally empty -- SecurityList is a leaf in this wave's dependency
		// graph. Vcn declares SecurityListResourceType in ITS OWN DependsOn instead. The VCN's
		// own default security list is excluded via Filter() below, never via DependsOn -- it is
		// a permanent OCI constraint, not an ordering concern.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type securityListLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// securityListList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *securityListLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("securityListLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return securityListList(ctx, client, o.CompartmentID)
}

// securityListList paginates core.ListSecurityLists and wraps every returned item as a
// SecurityList. Each item's owning Vcn is fetched via GetVcn, cached per-VcnId for the duration
// of this single List() call via the same resolveOwningVcn helper resources/route_table.go
// establishes (T-04-16's per-call cache, shared here rather than duplicated).
func securityListList(
	ctx context.Context,
	client securityListClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	vcnCache := make(map[string]core.Vcn)
	vcnErrCache := make(map[string]error)

	req := core.ListSecurityListsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListSecurityLists(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing SecurityList in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			sl := resp.Items[i]
			vcn, vcnErr := resolveOwningVcn(ctx, client, sl.VcnId, vcnCache, vcnErrCache)
			out = append(out, &SecurityList{client: client, sl: sl, vcn: vcn, vcnErr: vcnErr})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// SecurityList wraps one core.SecurityList, plus its owning Vcn (or the error encountered
// fetching it), resolved once at list time -- see securityListList/resolveOwningVcn above.
type SecurityList struct {
	client securityListClient
	sl     core.SecurityList
	vcn    core.Vcn
	vcnErr error
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *SecurityList) GetCompartmentID() string { return *r.sl.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *SecurityList) UniqueKey() string { return *r.sl.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Beyond the lifecycle-state check every type
// in this wave has, SecurityList's Filter() ALSO excludes the VCN's own default security list
// (Vcn.DefaultSecurityListId), a permanent OCI constraint that can never be deleted
// independently of its VCN (`[CITED]` the security-list equivalent of
// docs.oracle.com/en-us/iaas/Content/Network/Tasks/delete-routetable.htm) -- excluded
// pre-emptively here, never attempted and left to fail into a generic api-error (T-04-15).
//
// core.SecurityListLifecycleStateEnum (verified this session, core/security_list.go) has exactly
// four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No FAULTY-equivalent state
// exists.
func (r *SecurityList) Filter() error {
	switch r.sl.LifecycleState {
	case core.SecurityListLifecycleStateProvisioning, core.SecurityListLifecycleStateAvailable:
		// present; fall through to the default-security-list check below.
	default:
		return fmt.Errorf("SecurityList is %s, not available", r.sl.LifecycleState)
	}

	// Fail CLOSED (exclude) if the owning Vcn could not be determined -- never fall through to
	// "not the default" on a fetch failure (T-04-15's trust-boundary requirement).
	if r.vcnErr != nil {
		return fmt.Errorf("SecurityList %s: could not verify owning VCN's default security list: %w", r.UniqueKey(), r.vcnErr)
	}

	if r.sl.Id != nil && r.vcn.DefaultSecurityListId != nil && *r.sl.Id == *r.vcn.DefaultSecurityListId {
		return fmt.Errorf(
			"SecurityList %s is the default security list for VCN %s, cannot be deleted independently of its VCN",
			r.UniqueKey(), safeDeref(r.sl.VcnId),
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
func (r *SecurityList) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.sl
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// The delete request field is SecurityListId (regular naming, verified this session).
func (r *SecurityList) Remove(ctx context.Context) error {
	x := r.sl
	_, err := r.client.DeleteSecurityList(ctx, core.DeleteSecurityListRequest{SecurityListId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *SecurityList) Properties() types.Properties {
	x := r.sl
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
