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

// volumeAttachmentClient is the narrow slice of core.ComputeClient this lister needs -- a
// hand-written interface so VolumeAttachment is stub-testable with zero network access. Verified
// this session: ListVolumeAttachments/DetachVolume live on core.ComputeClient, NOT
// core.BlockstorageClient, despite this being a "storage" type.
type volumeAttachmentClient interface {
	ListVolumeAttachments(ctx context.Context, req core.ListVolumeAttachmentsRequest) (core.ListVolumeAttachmentsResponse, error)
	DetachVolume(ctx context.Context, req core.DetachVolumeRequest) (core.DetachVolumeResponse, error)
}

// VolumeAttachmentResourceType is the registry.Registration.Name for VolumeAttachment.
const VolumeAttachmentResourceType = "VolumeAttachment"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VolumeAttachmentResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &VolumeAttachment{},
		Lister:   &volumeAttachmentLister{},
		// DependsOn is declared HERE, on the dependent's own registration, per the
		// "DependsOn is always declared by the type that has the dependency" convention
		// (04-RESEARCH.md Q1, resources/instance.go). A bare string literal -- Plan 04-04's
		// Instance type registers under exactly this name; no Go import between the two files.
		DependsOn: []string{InstanceResourceType},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type volumeAttachmentLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// volumeAttachmentList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Compute client.
func (l *volumeAttachmentLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("volumeAttachmentLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Compute(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Compute client for %s: %w", o.Region, err)
	}

	return volumeAttachmentList(ctx, client, o.CompartmentID)
}

// volumeAttachmentList paginates core.ListVolumeAttachments and wraps every returned item as a
// VolumeAttachment. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func volumeAttachmentList(
	ctx context.Context,
	client volumeAttachmentClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListVolumeAttachmentsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListVolumeAttachments(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing VolumeAttachment in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &VolumeAttachment{client: client, attachment: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// VolumeAttachment wraps one core.VolumeAttachment -- a Go INTERFACE (GetId(), GetCompartmentId(),
// GetInstanceId(), GetVolumeId(), GetLifecycleState(), GetTimeCreated(), GetDisplayName(), ...),
// not a struct: the OCI SDK models several concrete attachment kinds (iSCSI, paravirtualized,
// ...) behind one interface. Every access below goes through these getter methods, never direct
// field access -- a real, verified deviation from every other struct-based type in this wave.
type VolumeAttachment struct {
	client     volumeAttachmentClient
	attachment core.VolumeAttachment
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *VolumeAttachment) GetCompartmentID() string { return *r.attachment.GetCompartmentId() }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *VolumeAttachment) UniqueKey() string { return *r.attachment.GetId() }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// core.VolumeAttachmentLifecycleStateEnum (verified this session, core/volume_attachment.go) has
// exactly four values: ATTACHING, ATTACHED, DETACHING, DETACHED. ATTACHING is present-but-not-yet-
// actionable (mirrors Instance's STARTING/STOPPING transitional-but-present states); DETACHING/
// DETACHED are excluded (going/gone, the hang-trap defense).
func (r *VolumeAttachment) Filter() error {
	switch r.attachment.GetLifecycleState() {
	case core.VolumeAttachmentLifecycleStateAttaching,
		core.VolumeAttachmentLifecycleStateAttached:
		return nil
	default:
		return fmt.Errorf("VolumeAttachment is %s, not available", r.attachment.GetLifecycleState())
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
// nil, nil is passed for freeform/defined tags: core.VolumeAttachment's interface exposes no
// GetFreeformTags()/GetDefinedTags() methods at all (verified this session, core/
// volume_attachment.go) -- min-age protection via GetTimeCreated() still applies, but
// protect-by-tag cannot match this type from list-time data alone.
func (r *VolumeAttachment) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.attachment
	return nil, nil, x.GetTimeCreated().Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *VolumeAttachment) Remove(ctx context.Context) error {
	x := r.attachment
	_, err := r.client.DetachVolume(ctx, core.DetachVolumeRequest{VolumeAttachmentId: x.GetId()})
	return holdOn409(err)
}

// Properties is built directly (not via baseProperties, which expects the uniform *string
// DisplayName/tag-map shape every struct-based type in this wave shares -- core.VolumeAttachment
// has no tag fields at all, see Remove() above) using the shared propInstanceID/propVolumeID
// constants from resources/support.go.
func (r *VolumeAttachment) Properties() types.Properties {
	x := r.attachment
	return types.NewProperties().
		Set(propID, x.GetId()).
		Set(propCompartmentID, x.GetCompartmentId()).
		Set(propInstanceID, x.GetInstanceId()).
		Set(propVolumeID, x.GetVolumeId()).
		Set(propLifecycleState, string(x.GetLifecycleState()))
}
