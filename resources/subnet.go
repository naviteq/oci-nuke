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

// subnetClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so Subnet is stub-testable with zero network access.
type subnetClient interface {
	ListSubnets(ctx context.Context, req core.ListSubnetsRequest) (core.ListSubnetsResponse, error)
	DeleteSubnet(ctx context.Context, req core.DeleteSubnetRequest) (core.DeleteSubnetResponse, error)
}

// SubnetResourceType is the registry.Registration.Name for Subnet.
const SubnetResourceType = "Subnet"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     SubnetResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Subnet{},
		Lister:   &subnetLister{},
		// DependsOn: ["PrivateIp"] -- a subnet's private IPs (beyond the primary VNIC IP, which
		// is deleted with the VNIC/instance) should be released first. [ASSUMED] per
		// 04-RESEARCH.md Q5/Assumption A1 -- not independently verified live this session; a bare
		// string literal, since PrivateIp is Plan 04-07's type and this plan does not import it.
		DependsOn: []string{"PrivateIp"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type subnetLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in subnetList, kept
// separate so it is unit-testable against a stub client without ever constructing a real
// VirtualNetwork client.
func (l *subnetLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("subnetLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return subnetList(ctx, client, o.CompartmentID)
}

// subnetList paginates core.ListSubnets and wraps every returned item as a Subnet. Isolated from
// ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below exercises
// against a stub, with zero network access.
func subnetList(
	ctx context.Context,
	client subnetClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListSubnetsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListSubnets(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Subnet in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Subnet{client: client, subnet: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Subnet wraps one core.Subnet.
type Subnet struct {
	client subnetClient
	subnet core.Subnet
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Subnet) GetCompartmentID() string { return *r.subnet.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Subnet) UniqueKey() string { return *r.subnet.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.SubnetLifecycleStateEnum (verified this session, core/subnet.go) has exactly five values,
// identical shape to Vcn's: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED, UPDATING. UPDATING
// is present-but-not-yet-actionable.
func (r *Subnet) Filter() error {
	switch r.subnet.LifecycleState {
	case core.SubnetLifecycleStateProvisioning, core.SubnetLifecycleStateAvailable, core.SubnetLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Subnet is %s, not available", r.subnet.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *Subnet) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.subnet
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Subnet) Remove(ctx context.Context) error {
	x := r.subnet
	_, err := r.client.DeleteSubnet(ctx, core.DeleteSubnetRequest{SubnetId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Subnet) Properties() types.Properties {
	x := r.subnet
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId).
		Set("cidr_block", x.CidrBlock)
}
