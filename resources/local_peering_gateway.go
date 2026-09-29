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

// localPeeringGatewayClient is the narrow slice of core.VirtualNetworkClient this lister needs --
// a hand-written interface so LocalPeeringGateway is stub-testable with zero network access,
// mirroring resources/nat_gateway.go's canonical shape.
type localPeeringGatewayClient interface {
	ListLocalPeeringGateways(ctx context.Context, req core.ListLocalPeeringGatewaysRequest) (core.ListLocalPeeringGatewaysResponse, error)
	DeleteLocalPeeringGateway(ctx context.Context, req core.DeleteLocalPeeringGatewayRequest) (core.DeleteLocalPeeringGatewayResponse, error)
}

// LocalPeeringGatewayResourceType is the registry.Registration.Name for LocalPeeringGateway.
const LocalPeeringGatewayResourceType = "LocalPeeringGateway"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     LocalPeeringGatewayResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &LocalPeeringGateway{},
		Lister:   &localPeeringGatewayLister{},
		// DependsOn is intentionally empty -- LocalPeeringGateway is a leaf in this wave's
		// dependency graph. Vcn (Plan 04-06) declares LocalPeeringGatewayResourceType in ITS OWN
		// DependsOn instead.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type localPeeringGatewayLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// localPeeringGatewayList, kept separate so it is unit-testable against a stub client without
// ever constructing a real VirtualNetwork client.
func (l *localPeeringGatewayLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("localPeeringGatewayLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return localPeeringGatewayList(ctx, client, o.CompartmentID)
}

// localPeeringGatewayList paginates core.ListLocalPeeringGateways and wraps every returned item
// as a LocalPeeringGateway. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this
// is what the list test below exercises against a stub, with zero network access.
func localPeeringGatewayList(
	ctx context.Context,
	client localPeeringGatewayClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListLocalPeeringGatewaysRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListLocalPeeringGateways(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing LocalPeeringGateway in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &LocalPeeringGateway{client: client, lpg: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// LocalPeeringGateway wraps one core.LocalPeeringGateway.
type LocalPeeringGateway struct {
	client localPeeringGatewayClient
	lpg    core.LocalPeeringGateway
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *LocalPeeringGateway) GetCompartmentID() string { return *r.lpg.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *LocalPeeringGateway) UniqueKey() string { return *r.lpg.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.LocalPeeringGatewayLifecycleStateEnum (verified this session, core/local_peering_
// gateway.go) has exactly four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No
// FAULTY-equivalent state exists.
func (r *LocalPeeringGateway) Filter() error {
	switch r.lpg.LifecycleState {
	case core.LocalPeeringGatewayLifecycleStateProvisioning, core.LocalPeeringGatewayLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("LocalPeeringGateway is %s, not available", r.lpg.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *LocalPeeringGateway) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.lpg
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *LocalPeeringGateway) Remove(ctx context.Context) error {
	x := r.lpg
	_, err := r.client.DeleteLocalPeeringGateway(ctx, core.DeleteLocalPeeringGatewayRequest{LocalPeeringGatewayId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *LocalPeeringGateway) Properties() types.Properties {
	x := r.lpg
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
