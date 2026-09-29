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

// reservedPublicIpClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so ReservedPublicIp is stub-testable with zero network access.
type reservedPublicIpClient interface {
	ListPublicIps(ctx context.Context, req core.ListPublicIpsRequest) (core.ListPublicIpsResponse, error)
	DeletePublicIp(ctx context.Context, req core.DeletePublicIpRequest) (core.DeletePublicIpResponse, error)
}

// ReservedPublicIpResourceType is the registry.Registration.Name for ReservedPublicIp.
const ReservedPublicIpResourceType = "ReservedPublicIp"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ReservedPublicIpResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &ReservedPublicIp{},
		Lister:   &reservedPublicIpLister{},
		// DependsOn is intentionally empty -- ReservedPublicIp is a leaf in this wave's
		// dependency graph. Not a distinct SDK type: this is core.PublicIp filtered to
		// Lifetime=RESERVED at list time, distinguishing it from an ephemeral, VNIC-owned public
		// IP (which is deleted with its owning entity and must never be independently targeted).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type reservedPublicIpLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// reservedPublicIpList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *reservedPublicIpLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("reservedPublicIpLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return reservedPublicIpList(ctx, client, o.CompartmentID)
}

// reservedPublicIpList paginates core.ListPublicIps -- with Scope: ListPublicIpsScopeRegion and
// Lifetime: ListPublicIpsLifetimeReserved, the filter that distinguishes a reserved public IP
// from an ephemeral, VNIC-owned one -- and wraps every returned item as a ReservedPublicIp.
// Never omit Lifetime: without it, this call would list (and this type's Remove() would later
// attempt to delete) live, VNIC-owned ephemeral public IPs (T-04-20).
func reservedPublicIpList(
	ctx context.Context,
	client reservedPublicIpClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListPublicIpsRequest{
		CompartmentId: &compartmentID,
		Scope:         core.ListPublicIpsScopeRegion,
		Lifetime:      core.ListPublicIpsLifetimeReserved,
	}
	for {
		resp, err := client.ListPublicIps(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing ReservedPublicIp in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &ReservedPublicIp{client: client, ip: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// ReservedPublicIp wraps one core.PublicIp already known (by the Lifetime=RESERVED list filter
// above) to be a reserved, not ephemeral, public IP.
type ReservedPublicIp struct {
	client reservedPublicIpClient
	ip     core.PublicIp
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
//
// core.PublicIp.CompartmentId is `mandatory:"false"` on the pinned SDK (verified this session,
// core/public_ip.go) -- unlike every earlier compartment-scoped type in this wave, this must be
// nil-safe, mirroring PrivateIp's own nil-CompartmentId handling below in resources/private_ip.go
// (both types share the same OCI SDK quirk: a compartment-scoped resource whose CompartmentId
// field is not itself mandatory).
func (r *ReservedPublicIp) GetCompartmentID() string {
	if r.ip.CompartmentId == nil {
		return ""
	}
	return *r.ip.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *ReservedPublicIp) UniqueKey() string { return *r.ip.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.PublicIpLifecycleStateEnum (verified this session, core/public_ip.go) has exactly eight
// values: PROVISIONING, AVAILABLE, ASSIGNING, ASSIGNED, UNASSIGNING, UNASSIGNED, TERMINATING,
// TERMINATED. Present: everything except TERMINATING/TERMINATED -- a reserved public IP that is
// merely unassigned from its private IP is still a live, billable resource this project's scan
// must report, not a transitional "going" state to skip.
func (r *ReservedPublicIp) Filter() error {
	switch r.ip.LifecycleState {
	case core.PublicIpLifecycleStateProvisioning, core.PublicIpLifecycleStateAvailable,
		core.PublicIpLifecycleStateAssigning, core.PublicIpLifecycleStateAssigned,
		core.PublicIpLifecycleStateUnassigning, core.PublicIpLifecycleStateUnassigned:
		return nil
	default:
		return fmt.Errorf("ReservedPublicIp is %s, not available", r.ip.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// x.TimeCreated is `mandatory:"false"` on core.PublicIp (verified this session) -- unlike
// most earlier resource types in this wave, a direct x.TimeCreated.Time dereference here
// would panic on a real record with no creation timestamp, so this uses support.go's
// timeCreatedOrZero instead.
func (r *ReservedPublicIp) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.ip
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *ReservedPublicIp) Remove(ctx context.Context) error {
	x := r.ip
	_, err := r.client.DeletePublicIp(ctx, core.DeletePublicIpRequest{PublicIpId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *ReservedPublicIp) Properties() types.Properties {
	x := r.ip
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
