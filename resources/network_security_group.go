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

// networkSecurityGroupClient is the narrow slice of core.VirtualNetworkClient this lister
// needs -- a hand-written interface so NetworkSecurityGroup is stub-testable with zero network
// access, mirroring resources/nat_gateway.go's canonical shape.
type networkSecurityGroupClient interface {
	ListNetworkSecurityGroups(
		ctx context.Context, req core.ListNetworkSecurityGroupsRequest,
	) (core.ListNetworkSecurityGroupsResponse, error)
	DeleteNetworkSecurityGroup(
		ctx context.Context, req core.DeleteNetworkSecurityGroupRequest,
	) (core.DeleteNetworkSecurityGroupResponse, error)
}

// NetworkSecurityGroupResourceType is the registry.Registration.Name for NetworkSecurityGroup.
const NetworkSecurityGroupResourceType = "NetworkSecurityGroup"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     NetworkSecurityGroupResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &NetworkSecurityGroup{},
		Lister:   &networkSecurityGroupLister{},
		// DependsOn is intentionally empty -- NetworkSecurityGroup is a leaf in this wave's
		// dependency graph. Vcn (Plan 04-06) declares NetworkSecurityGroupResourceType in ITS OWN
		// DependsOn instead.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type networkSecurityGroupLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// networkSecurityGroupList, kept separate so it is unit-testable against a stub client without
// ever constructing a real VirtualNetwork client.
func (l *networkSecurityGroupLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("networkSecurityGroupLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return networkSecurityGroupList(ctx, client, o.CompartmentID)
}

// networkSecurityGroupList paginates core.ListNetworkSecurityGroups and wraps every returned
// item as a NetworkSecurityGroup. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose --
// this is what the list test below exercises against a stub, with zero network access.
func networkSecurityGroupList(
	ctx context.Context,
	client networkSecurityGroupClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListNetworkSecurityGroupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListNetworkSecurityGroups(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing NetworkSecurityGroup in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &NetworkSecurityGroup{client: client, nsg: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// NetworkSecurityGroup wraps one core.NetworkSecurityGroup.
type NetworkSecurityGroup struct {
	client networkSecurityGroupClient
	nsg    core.NetworkSecurityGroup
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *NetworkSecurityGroup) GetCompartmentID() string { return *r.nsg.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *NetworkSecurityGroup) UniqueKey() string { return *r.nsg.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.NetworkSecurityGroupLifecycleStateEnum (verified this session, core/network_security_
// group.go) has exactly four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No
// FAULTY-equivalent state exists.
func (r *NetworkSecurityGroup) Filter() error {
	switch r.nsg.LifecycleState {
	case core.NetworkSecurityGroupLifecycleStateProvisioning, core.NetworkSecurityGroupLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("NetworkSecurityGroup is %s, not available", r.nsg.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *NetworkSecurityGroup) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.nsg
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *NetworkSecurityGroup) Remove(ctx context.Context) error {
	x := r.nsg
	_, err := r.client.DeleteNetworkSecurityGroup(ctx, core.DeleteNetworkSecurityGroupRequest{NetworkSecurityGroupId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *NetworkSecurityGroup) Properties() types.Properties {
	x := r.nsg
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
