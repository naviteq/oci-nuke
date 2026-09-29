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

// vlanClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so Vlan is stub-testable with zero network access.
type vlanClient interface {
	ListVlans(ctx context.Context, req core.ListVlansRequest) (core.ListVlansResponse, error)
	DeleteVlan(ctx context.Context, req core.DeleteVlanRequest) (core.DeleteVlanResponse, error)
}

// VlanResourceType is the registry.Registration.Name for Vlan.
const VlanResourceType = "Vlan"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VlanResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Vlan{},
		Lister:   &vlanLister{},
		// DependsOn is intentionally empty -- Vlan is a leaf in this wave's dependency graph.
		// Vlan is only used with the Oracle Cloud VMware Solution; PrivateIp objects assigned to
		// a VLAN (rather than a VNIC) are enumerated per-Subnet, never per-VLAN, so no ordering
		// edge from PrivateIp to Vlan is declared here.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type vlanLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in vlanList, kept
// separate so it is unit-testable against a stub client without ever constructing a real
// VirtualNetwork client.
func (l *vlanLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("vlanLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return vlanList(ctx, client, o.CompartmentID)
}

// vlanList paginates core.ListVlans and wraps every returned item as a Vlan. Isolated from
// ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below exercises
// against a stub, with zero network access.
func vlanList(
	ctx context.Context,
	client vlanClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListVlansRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListVlans(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Vlan in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Vlan{client: client, vlan: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Vlan wraps one core.Vlan.
type Vlan struct {
	client vlanClient
	vlan   core.Vlan
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Vlan) GetCompartmentID() string { return *r.vlan.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Vlan) UniqueKey() string { return *r.vlan.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.VlanLifecycleStateEnum (verified this session, core/vlan.go) has exactly five values:
// PROVISIONING, AVAILABLE, TERMINATING, TERMINATED, UPDATING. UPDATING is
// present-but-not-yet-actionable, mirroring Vcn/Subnet's identical five-value shape.
func (r *Vlan) Filter() error {
	switch r.vlan.LifecycleState {
	case core.VlanLifecycleStateProvisioning, core.VlanLifecycleStateAvailable, core.VlanLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Vlan is %s, not available", r.vlan.LifecycleState)
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
// x.TimeCreated is `mandatory:"false"` on core.Vlan (verified this session) -- unlike most
// earlier resource types in this wave, a direct x.TimeCreated.Time dereference here would
// panic on a real record with no creation timestamp, so this uses support.go's
// timeCreatedOrZero instead.
func (r *Vlan) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.vlan
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Vlan) Remove(ctx context.Context) error {
	x := r.vlan
	_, err := r.client.DeleteVlan(ctx, core.DeleteVlanRequest{VlanId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Vlan) Properties() types.Properties {
	x := r.vlan
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
