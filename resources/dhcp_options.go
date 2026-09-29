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

// dhcpOptionsClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so DhcpOptions is stub-testable with zero network access. GetVcn is
// here for the same reason routeTableClient has it: the owning VCN's DefaultDhcpOptionsId is
// what identifies the one set in the compartment that must never be attempted. See Filter().
type dhcpOptionsClient interface {
	ListDhcpOptions(ctx context.Context, req core.ListDhcpOptionsRequest) (core.ListDhcpOptionsResponse, error)
	DeleteDhcpOptions(ctx context.Context, req core.DeleteDhcpOptionsRequest) (core.DeleteDhcpOptionsResponse, error)
	GetVcn(ctx context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error)
}

// DhcpOptionsResourceType is the registry.Registration.Name for DhcpOptions.
const DhcpOptionsResourceType = "DhcpOptions"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     DhcpOptionsResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &DhcpOptions{},
		Lister:   &dhcpOptionsLister{},
		// DependsOn is intentionally empty -- DhcpOptions is a leaf in this wave's dependency
		// graph. Vcn (Plan 04-06) declares DhcpOptionsResourceType in ITS OWN DependsOn instead.
		// The VCN's own default set is excluded via Filter() below, never via DependsOn -- it is
		// a permanent OCI constraint, not an ordering concern, exactly as for RouteTable and
		// SecurityList.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type dhcpOptionsLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// dhcpOptionsList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *dhcpOptionsLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("dhcpOptionsLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return dhcpOptionsList(ctx, client, o.CompartmentID)
}

// dhcpOptionsList paginates core.ListDhcpOptions and wraps every returned item as a
// DhcpOptions. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
//
// Each item's owning VCN is resolved through the same resolveOwningVcn helper routeTableList and
// securityListList use, with the same per-call success-and-failure caches, so N option sets
// sharing one VCN cost one GetVcn call rather than N and a transient failure is not retried per
// sibling. The resolved VCN (or the error) is threaded onto the constructed DhcpOptions so its
// Filter() can fail closed.
func dhcpOptionsList(
	ctx context.Context,
	client dhcpOptionsClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	vcnCache := make(map[string]core.Vcn)
	vcnErrCache := make(map[string]error)

	req := core.ListDhcpOptionsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDhcpOptions(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing DhcpOptions in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			opts := resp.Items[i]
			vcn, vcnErr := resolveOwningVcn(ctx, client, opts.VcnId, vcnCache, vcnErrCache)
			out = append(out, &DhcpOptions{client: client, opts: opts, vcn: vcn, vcnErr: vcnErr})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// DhcpOptions wraps one core.DhcpOptions, plus its owning Vcn (or the error encountered fetching
// it), resolved once at list time -- see dhcpOptionsList above.
type DhcpOptions struct {
	client dhcpOptionsClient
	opts   core.DhcpOptions
	vcn    core.Vcn
	vcnErr error
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *DhcpOptions) GetCompartmentID() string { return *r.opts.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *DhcpOptions) UniqueKey() string { return *r.opts.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Beyond the lifecycle-state check, it excludes
// the owning VCN's own default set of DHCP options (Vcn.DefaultDhcpOptionsId), which can never be
// deleted independently of its VCN.
//
// This was missing while RouteTable and SecurityList both had it, and the omission was not
// harmless: ListDhcpOptions in a compartment DOES return the default set, so every destructive
// run over any compartment holding a VCN attempted to delete it, was refused every round until
// the retry budget ran out, and reported a leftover. With --fail-on-leftover that is a non-zero
// exit on a run that actually emptied the compartment -- and the leftover is a resource that
// disappears with its VCN moments later.
//
// Verified live against us-ashburn-1 rather than inferred from the docs (probe: create a VCN,
// list its compartment's DHCP options, attempt the default set, delete the VCN):
//
//	ListDhcpOptions returns the default set, DisplayName "Default DHCP Options for <vcn>"
//	DeleteDhcpOptions -> 409 IncorrectState:
//	  "<ocid> is the default for VCN that is in use"
//
// core.DhcpOptionsLifecycleStateEnum (verified this session, core/dhcp_options.go) has exactly
// four values: PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No FAULTY-equivalent state
// exists.
func (r *DhcpOptions) Filter() error {
	switch r.opts.LifecycleState {
	case core.DhcpOptionsLifecycleStateProvisioning, core.DhcpOptionsLifecycleStateAvailable:
		// present; fall through to the default-set check below.
	default:
		return fmt.Errorf("DhcpOptions is %s, not available", r.opts.LifecycleState)
	}

	// Fail CLOSED (exclude) if the owning Vcn could not be determined -- never fall through to
	// "not the default" on a fetch failure, matching RouteTable's own trust boundary (T-04-15).
	if r.vcnErr != nil {
		return fmt.Errorf("DhcpOptions %s: could not verify owning VCN's default set: %w", r.UniqueKey(), r.vcnErr)
	}

	if r.opts.Id != nil && r.vcn.DefaultDhcpOptionsId != nil && *r.opts.Id == *r.vcn.DefaultDhcpOptionsId {
		return fmt.Errorf(
			"DhcpOptions %s is the default set for VCN %s, cannot be deleted independently of its VCN",
			r.UniqueKey(), safeDeref(r.opts.VcnId),
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
func (r *DhcpOptions) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.opts
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// The delete request field is DhcpId, NOT DhcpOptionsId -- a real SDK naming irregularity
// verified this session against core/delete_dhcp_options_request_response.go.
func (r *DhcpOptions) Remove(ctx context.Context) error {
	x := r.opts
	_, err := r.client.DeleteDhcpOptions(ctx, core.DeleteDhcpOptionsRequest{DhcpId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *DhcpOptions) Properties() types.Properties {
	x := r.opts
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propVcnID, x.VcnId)
}
