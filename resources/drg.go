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

// drgClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a hand-written
// interface so Drg is stub-testable with zero network access.
type drgClient interface {
	ListDrgs(ctx context.Context, req core.ListDrgsRequest) (core.ListDrgsResponse, error)
	DeleteDrg(ctx context.Context, req core.DeleteDrgRequest) (core.DeleteDrgResponse, error)
}

// DrgResourceType is the registry.Registration.Name for Drg.
const DrgResourceType = "Drg"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     DrgResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Drg{},
		Lister:   &drgLister{},
		// DependsOn: ["DrgAttachment"] -- before deleting a DRG, you must detach it from any VCN
		// by deleting the DrgAttachment resource first ([CITED]
		// docs.oracle.com/en-us/iaas/Content/Network/Tasks/drg-delete.htm: "Before deleting a DRG,
		// the DRG can't be attached to a VCN... you must detach a DRG from a VCN by deleting the
		// DrgAttachment resource before you can delete the DRG itself"). Uses
		// DrgAttachmentResourceType directly (not a bare string literal) -- unlike Vcn's
		// cross-plan DependsOn entries, Drg and DrgAttachment are siblings within this same plan
		// and package, so there is no cross-plan-boundary reason to avoid the constant.
		DependsOn: []string{DrgAttachmentResourceType},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type drgLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in drgList, kept
// separate so it is unit-testable against a stub client without ever constructing a real
// VirtualNetwork client.
func (l *drgLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("drgLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return drgList(ctx, client, o.CompartmentID)
}

// drgList paginates core.ListDrgs and wraps every returned item as a Drg. Isolated from
// ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below exercises
// against a stub, with zero network access.
func drgList(
	ctx context.Context,
	client drgClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListDrgsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDrgs(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Drg in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Drg{client: client, drg: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Drg wraps one core.Drg.
type Drg struct {
	client drgClient
	drg    core.Drg
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Drg) GetCompartmentID() string { return *r.drg.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Drg) UniqueKey() string { return *r.drg.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.DrgLifecycleStateEnum (verified this session, core/drg.go) has exactly four values:
// PROVISIONING, AVAILABLE, TERMINATING, TERMINATED. No FAULTY-equivalent state exists.
func (r *Drg) Filter() error {
	switch r.drg.LifecycleState {
	case core.DrgLifecycleStateProvisioning, core.DrgLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("Drg is %s, not available", r.drg.LifecycleState)
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
// x.TimeCreated is `mandatory:"false"` on core.Drg (verified this session) -- unlike every
// earlier resource type in this wave, a direct x.TimeCreated.Time dereference here would
// panic on a real record with no creation timestamp, so this uses support.go's
// timeCreatedOrZero instead.
func (r *Drg) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.drg
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// By the time this call fires, WaitOnDependencies has already ensured every DrgAttachment
// this Drg's own DependsOn names has finished or permanently failed -- ordering comes from
// that round-convergence mechanism, never from this Remove() implementation itself.
func (r *Drg) Remove(ctx context.Context) error {
	x := r.drg
	_, err := r.client.DeleteDrg(ctx, core.DeleteDrgRequest{DrgId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Drg) Properties() types.Properties {
	x := r.drg
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
