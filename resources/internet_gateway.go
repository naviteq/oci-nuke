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

// internetGatewayClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so InternetGateway is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type internetGatewayClient interface {
	ListInternetGateways(ctx context.Context, req core.ListInternetGatewaysRequest) (core.ListInternetGatewaysResponse, error)
	DeleteInternetGateway(ctx context.Context, req core.DeleteInternetGatewayRequest) (core.DeleteInternetGatewayResponse, error)
}

// InternetGatewayResourceType is the registry.Registration.Name for InternetGateway.
const InternetGatewayResourceType = "InternetGateway"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     InternetGatewayResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &InternetGateway{},
		Lister:   &internetGatewayLister{},
		// DependsOn is intentionally empty -- InternetGateway is a leaf in this wave's dependency
		// graph. Vcn declares InternetGatewayResourceType in ITS OWN DependsOn instead.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type internetGatewayLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// internetGatewayList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *internetGatewayLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("internetGatewayLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return internetGatewayList(ctx, client, o.CompartmentID)
}

// internetGatewayList paginates core.ListInternetGateways and wraps every returned item as an
// InternetGateway. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func internetGatewayList(
	ctx context.Context,
	client internetGatewayClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListInternetGatewaysRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListInternetGateways(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing InternetGateway in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &InternetGateway{client: client, ig: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// InternetGateway wraps one core.InternetGateway.
type InternetGateway struct {
	client internetGatewayClient
	ig     core.InternetGateway
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *InternetGateway) GetCompartmentID() string { return *r.ig.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *InternetGateway) UniqueKey() string { return *r.ig.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment for the full explanation, identical here.
//
// core.InternetGatewayLifecycleStateEnum (verified this session, core/internet_gateway.go) has
// exactly four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No FAULTY-equivalent
// state exists, so no ReportLeftover branch is needed here.
func (r *InternetGateway) Filter() error {
	switch r.ig.LifecycleState {
	case core.InternetGatewayLifecycleStateProvisioning, core.InternetGatewayLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("InternetGateway is %s, not available", r.ig.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *InternetGateway) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.ig
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// The delete request field is IgId, NOT InternetGatewayId -- a real SDK naming irregularity
// verified this session against core/delete_internet_gateway_request_response.go.
func (r *InternetGateway) Remove(ctx context.Context) error {
	x := r.ig
	_, err := r.client.DeleteInternetGateway(ctx, core.DeleteInternetGatewayRequest{IgId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *InternetGateway) Properties() types.Properties {
	x := r.ig
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
