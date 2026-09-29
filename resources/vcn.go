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

// vcnClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a hand-written
// interface so Vcn is stub-testable with zero network access.
type vcnClient interface {
	ListVcns(ctx context.Context, req core.ListVcnsRequest) (core.ListVcnsResponse, error)
	DeleteVcn(ctx context.Context, req core.DeleteVcnRequest) (core.DeleteVcnResponse, error)
}

// VcnResourceType is the registry.Registration.Name for Vcn.
const VcnResourceType = "Vcn"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VcnResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Vcn{},
		Lister:   &vcnLister{},
		// DependsOn aggregates every VCN periphery type this wave registers -- both this plan's
		// own six (Subnet, RouteTable, SecurityList, InternetGateway, NatGateway, ServiceGateway)
		// and Plan 04-07's four "sometimes present" types (NetworkSecurityGroup, DhcpOptions,
		// LocalPeeringGateway, DrgAttachment), landing in the SAME wave with zero file overlap.
		// Bare string literals -- neither plan imports the other's ResourceType constants, per
		// the "DependsOn is declared by the dependent, using bare strings across plan boundaries"
		// convention (04-RESEARCH.md Q1/Q5). A VCN cannot be deleted while any of its periphery
		// exists.
		DependsOn: []string{
			"Subnet", "RouteTable", "SecurityList", "InternetGateway", "NatGateway", "ServiceGateway",
			"NetworkSecurityGroup", "DhcpOptions", "LocalPeeringGateway", "DrgAttachment",
		},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type vcnLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in vcnList, kept
// separate so it is unit-testable against a stub client without ever constructing a real
// VirtualNetwork client.
func (l *vcnLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("vcnLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return vcnList(ctx, client, o.CompartmentID)
}

// vcnList paginates core.ListVcns and wraps every returned item as a Vcn. Isolated from
// ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below exercises
// against a stub, with zero network access.
func vcnList(
	ctx context.Context,
	client vcnClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListVcnsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListVcns(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Vcn in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Vcn{client: client, vcn: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Vcn wraps one core.Vcn.
type Vcn struct {
	client vcnClient
	vcn    core.Vcn
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Vcn) GetCompartmentID() string { return *r.vcn.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Vcn) UniqueKey() string { return *r.vcn.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.VcnLifecycleStateEnum (verified this session, core/vcn.go) has exactly five values:
// PROVISIONING, AVAILABLE, TERMINATING, TERMINATED, UPDATING. UPDATING is present-but-not-yet-
// actionable (mirrors Instance's STARTING/STOPPING transitional-but-present states) -- a VCN
// mid-update is still fully there, not going or gone.
func (r *Vcn) Filter() error {
	switch r.vcn.LifecycleState {
	case core.VcnLifecycleStateProvisioning, core.VcnLifecycleStateAvailable, core.VcnLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Vcn is %s, not available", r.vcn.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *Vcn) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.vcn
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// By the time this call fires, WaitOnDependencies has already ensured every periphery type
// this Vcn's own DependsOn names has finished or permanently failed (04-RESEARCH.md Q3) --
// ordering comes from that round-convergence mechanism, never from this Remove()
// implementation itself.
func (r *Vcn) Remove(ctx context.Context) error {
	x := r.vcn
	_, err := r.client.DeleteVcn(ctx, core.DeleteVcnRequest{VcnId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal. The default
// route table/security list IDs are exposed here too, for filter/debug visibility, independent
// of resources/route_table.go and resources/security_list.go's own internal GetVcn fetch.
func (r *Vcn) Properties() types.Properties {
	x := r.vcn
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set("cidr_block", x.CidrBlock).
		Set("default_route_table_id", x.DefaultRouteTableId).
		Set("default_security_list_id", x.DefaultSecurityListId)
}
