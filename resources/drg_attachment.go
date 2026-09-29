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

// drgAttachmentClient is the narrow slice of core.VirtualNetworkClient this lister needs -- a
// hand-written interface so DrgAttachment is stub-testable with zero network access.
type drgAttachmentClient interface {
	ListDrgAttachments(ctx context.Context, req core.ListDrgAttachmentsRequest) (core.ListDrgAttachmentsResponse, error)
	DeleteDrgAttachment(ctx context.Context, req core.DeleteDrgAttachmentRequest) (core.DeleteDrgAttachmentResponse, error)
}

// DrgAttachmentResourceType is the registry.Registration.Name for DrgAttachment.
const DrgAttachmentResourceType = "DrgAttachment"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     DrgAttachmentResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &DrgAttachment{},
		Lister:   &drgAttachmentLister{},
		// DependsOn is intentionally empty -- DrgAttachment is a leaf in this wave's dependency
		// graph. Drg (this plan) declares DrgAttachmentResourceType in ITS OWN DependsOn instead --
		// a DRG attachment must be removed before the DRG itself
		// ([CITED] docs.oracle.com/en-us/iaas/Content/Network/Tasks/drg-delete.htm). Vcn (Plan
		// 04-06) also names DrgAttachmentResourceType in its own DependsOn.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type drgAttachmentLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// drgAttachmentList, kept separate so it is unit-testable against a stub client without ever
// constructing a real VirtualNetwork client.
func (l *drgAttachmentLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("drgAttachmentLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.VirtualNetwork(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VirtualNetworkClient for %s: %w", o.Region, err)
	}

	return drgAttachmentList(ctx, client, o.CompartmentID)
}

// drgAttachmentList paginates core.ListDrgAttachments and wraps every returned item as a
// DrgAttachment. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func drgAttachmentList(
	ctx context.Context,
	client drgAttachmentClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListDrgAttachmentsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDrgAttachments(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing DrgAttachment in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &DrgAttachment{client: client, att: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// DrgAttachment wraps one core.DrgAttachment.
type DrgAttachment struct {
	client drgAttachmentClient
	att    core.DrgAttachment
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *DrgAttachment) GetCompartmentID() string { return *r.att.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *DrgAttachment) UniqueKey() string { return *r.att.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// core.DrgAttachmentLifecycleStateEnum (verified this session, core/drg_attachment.go) has
// exactly four values: ATTACHING, ATTACHED, DETACHING, DETACHED. Present: ATTACHING/ATTACHED.
// Excluded: DETACHING/DETACHED.
func (r *DrgAttachment) Filter() error {
	switch r.att.LifecycleState {
	case core.DrgAttachmentLifecycleStateAttaching, core.DrgAttachmentLifecycleStateAttached:
		return nil
	default:
		return fmt.Errorf("DrgAttachment is %s, not attached", r.att.LifecycleState)
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
// x.TimeCreated is `mandatory:"false"` on core.DrgAttachment (verified this session) --
// unlike every earlier resource type in this wave, a direct x.TimeCreated.Time dereference
// here would panic on a real record with no creation timestamp, so this uses support.go's
// timeCreatedOrZero instead.
func (r *DrgAttachment) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.att
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *DrgAttachment) Remove(ctx context.Context) error {
	x := r.att
	_, err := r.client.DeleteDrgAttachment(ctx, core.DeleteDrgAttachmentRequest{DrgAttachmentId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *DrgAttachment) Properties() types.Properties {
	x := r.att
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
