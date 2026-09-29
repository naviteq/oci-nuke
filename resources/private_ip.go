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

// privateIpClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so PrivateIp is stub-testable with zero network access. ListSubnets is
// needed alongside ListPrivateIps/DeletePrivateIp because core.ListPrivateIpsRequest has NO
// CompartmentId parameter at all (verified this session against core/list_private_ips_request_
// response.go) -- private IPs are listed by SubnetId or VnicId, never by compartment, so this
// lister must first enumerate every Subnet in the compartment (reusing the same
// VirtualNetworkClient Plan 04-06's Subnet type already uses) and call ListPrivateIps once per
// subnet.
type privateIpClient interface {
	ListSubnets(ctx context.Context, req core.ListSubnetsRequest) (core.ListSubnetsResponse, error)
	ListPrivateIps(ctx context.Context, req core.ListPrivateIpsRequest) (core.ListPrivateIpsResponse, error)
	DeletePrivateIp(ctx context.Context, req core.DeletePrivateIpRequest) (core.DeletePrivateIpResponse, error)
}

// PrivateIpResourceType is the registry.Registration.Name for PrivateIp.
const PrivateIpResourceType = "PrivateIp"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     PrivateIpResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &PrivateIp{},
		Lister:   &privateIpLister{},
		// DependsOn is intentionally empty -- PrivateIp is a leaf in this wave's dependency
		// graph. Subnet (Plan 04-06) declares PrivateIpResourceType in ITS OWN DependsOn instead
		// -- a subnet's non-primary private IPs should be released before the subnet itself.
		// Registering PrivateIp here also closes Plan 04-06's own observed gap: Subnet's
		// DependsOn: ["PrivateIp"] named an unregistered type until this init() runs, which made
		// Subnet invisible to registry.GetNames() (verified fixed below by
		// TestPrivateIpRegistration_MakesSubnetVisible).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type privateIpLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in privateIpList,
// kept separate so it is unit-testable against a stub client without ever constructing a real
// VirtualNetwork client.
func (l *privateIpLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("privateIpLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return privateIpList(ctx, client, o.CompartmentID)
}

// privateIpList first paginates core.ListSubnets for compartmentID, then -- for every subnet
// returned -- paginates core.ListPrivateIps scoped by that subnet's SubnetId, wrapping every
// returned item as a PrivateIp. There is no compartment-scoped ListPrivateIps call to make; this
// two-level enumeration IS the only way to reach every private IP in a compartment (04-RESEARCH.md
// Q5). Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list
// test below exercises against a stub, with zero network access.
func privateIpList(
	ctx context.Context,
	client privateIpClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource

	subnetReq := core.ListSubnetsRequest{CompartmentId: &compartmentID}
	for {
		subnetResp, err := client.ListSubnets(ctx, subnetReq)
		if err != nil {
			return nil, fmt.Errorf("listing Subnet (for PrivateIp enumeration) in %s: %w", compartmentID, err)
		}

		for i := range subnetResp.Items {
			subnetID := subnetResp.Items[i].Id
			piReq := core.ListPrivateIpsRequest{SubnetId: subnetID}
			for {
				piResp, err := client.ListPrivateIps(ctx, piReq)
				if err != nil {
					return nil, fmt.Errorf("listing PrivateIp for subnet %s: %w", safeDeref(subnetID), err)
				}
				for j := range piResp.Items {
					out = append(out, &PrivateIp{client: client, pi: piResp.Items[j]})
				}
				if piResp.OpcNextPage == nil {
					break
				}
				piReq.Page = piResp.OpcNextPage
			}
		}

		if subnetResp.OpcNextPage == nil {
			break
		}
		subnetReq.Page = subnetResp.OpcNextPage
	}

	return out, nil
}

// PrivateIp wraps one core.PrivateIp.
type PrivateIp struct {
	client privateIpClient
	pi     core.PrivateIp
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
//
// core.PrivateIp.CompartmentId is `mandatory:"false"` on the pinned SDK (verified this session,
// core/private_ip.go) and can be nil on some records -- this returns "" rather than panicking,
// which correctly fails closed through scopedLister's existing out-of-scope drop (a PrivateIp
// whose compartment cannot be determined is dropped, never kept).
func (r *PrivateIp) GetCompartmentID() string {
	if r.pi.CompartmentId == nil {
		return ""
	}
	return *r.pi.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *PrivateIp) UniqueKey() string { return *r.pi.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Unlike every other type in this wave,
// core.PrivateIp has NO LifecycleState field at all (verified this session, core/private_ip.go)
// -- "gone" is entirely "absent from List()" for this type, so there is no lifecycle switch
// here. Filter() instead unconditionally checks IsPrimary: a primary private IP is unassigned
// and deleted automatically when its owning VNIC/instance is terminated, and attempting to
// delete it independently risks disrupting a live instance's networking (T-04-18) -- excluded
// regardless of any other field. A non-primary (secondary) private IP is present, proceed.
func (r *PrivateIp) Filter() error {
	if r.pi.IsPrimary != nil && *r.pi.IsPrimary {
		return fmt.Errorf(
			"PrivateIp %s is the primary private IP, deleted with its owning VNIC/instance, never independently targeted",
			r.UniqueKey(),
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
//
// x.TimeCreated is `mandatory:"false"` on core.PrivateIp (verified this session) -- unlike
// most earlier resource types in this wave, a direct x.TimeCreated.Time dereference here
// would panic on a real record with no creation timestamp, so this uses support.go's
// timeCreatedOrZero instead.
func (r *PrivateIp) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.pi
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *PrivateIp) Remove(ctx context.Context) error {
	x := r.pi
	_, err := r.client.DeletePrivateIp(ctx, core.DeletePrivateIpRequest{PrivateIpId: x.Id})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties -- unlike
// every other type in this wave, PrivateIp has no LifecycleState to pass (there is no
// lifecycle_state property key set at all) and a possibly-nil CompartmentId (types.Properties.
// Set is nil-safe on a nil *string, matching baseProperties' own contract, so this is safe).
func (r *PrivateIp) Properties() types.Properties {
	x := r.pi
	return types.NewProperties().
		Set(propID, x.Id).
		Set(propName, x.DisplayName).
		Set(propCompartmentID, x.CompartmentId).
		Set(propSubnetID, x.SubnetId).
		Set("is_primary", x.IsPrimary)
}
