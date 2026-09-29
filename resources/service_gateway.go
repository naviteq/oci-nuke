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

// serviceGatewayClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so ServiceGateway is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type serviceGatewayClient interface {
	ListServiceGateways(ctx context.Context, req core.ListServiceGatewaysRequest) (core.ListServiceGatewaysResponse, error)
	DeleteServiceGateway(ctx context.Context, req core.DeleteServiceGatewayRequest) (core.DeleteServiceGatewayResponse, error)
}

// ServiceGatewayResourceType is the registry.Registration.Name for ServiceGateway.
const ServiceGatewayResourceType = "ServiceGateway"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ServiceGatewayResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &ServiceGateway{},
		Lister:   &serviceGatewayLister{},
		// DependsOn is intentionally empty -- ServiceGateway is a leaf in this wave's dependency
		// graph. Vcn declares ServiceGatewayResourceType in ITS OWN DependsOn instead.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type serviceGatewayLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// serviceGatewayList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *serviceGatewayLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("serviceGatewayLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return serviceGatewayList(ctx, client, o.CompartmentID)
}

// serviceGatewayList paginates core.ListServiceGateways and wraps every returned item as a
// ServiceGateway. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func serviceGatewayList(
	ctx context.Context,
	client serviceGatewayClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListServiceGatewaysRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListServiceGateways(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing ServiceGateway in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &ServiceGateway{client: client, sg: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// ServiceGateway wraps one core.ServiceGateway.
type ServiceGateway struct {
	client serviceGatewayClient
	sg     core.ServiceGateway
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *ServiceGateway) GetCompartmentID() string { return *r.sg.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *ServiceGateway) UniqueKey() string { return *r.sg.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment for the full explanation, identical here.
//
// core.ServiceGatewayLifecycleStateEnum (verified this session, core/service_gateway.go) has
// exactly four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No FAULTY-equivalent
// state exists, so no ReportLeftover branch is needed here.
func (r *ServiceGateway) Filter() error {
	switch r.sg.LifecycleState {
	case core.ServiceGatewayLifecycleStateProvisioning, core.ServiceGatewayLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("ServiceGateway is %s, not available", r.sg.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *ServiceGateway) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.sg
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// The delete request field is ServiceGatewayId (regular naming, verified this session).
func (r *ServiceGateway) Remove(ctx context.Context) error {
	x := r.sg
	_, err := r.client.DeleteServiceGateway(ctx, core.DeleteServiceGatewayRequest{ServiceGatewayId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *ServiceGateway) Properties() types.Properties {
	x := r.sg
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
