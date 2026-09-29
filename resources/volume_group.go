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
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// volumeGroupClient is the narrow slice of core.BlockstorageClient this lister needs -- a
// hand-written interface so VolumeGroup is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type volumeGroupClient interface {
	ListVolumeGroups(ctx context.Context, req core.ListVolumeGroupsRequest) (core.ListVolumeGroupsResponse, error)
	DeleteVolumeGroup(ctx context.Context, req core.DeleteVolumeGroupRequest) (core.DeleteVolumeGroupResponse, error)
}

// VolumeGroupResourceType is the registry.Registration.Name for VolumeGroup.
const VolumeGroupResourceType = "VolumeGroup"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VolumeGroupResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &VolumeGroup{},
		Lister:   &volumeGroupLister{},
		// DependsOn is intentionally empty -- nothing in this wave must be removed before a
		// volume group.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type volumeGroupLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// volumeGroupList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Blockstorage client.
func (l *volumeGroupLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("volumeGroupLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Blockstorage(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Blockstorage client for %s: %w", o.Region, err)
	}

	return volumeGroupList(ctx, client, o.CompartmentID)
}

// volumeGroupList paginates core.ListVolumeGroups and wraps every returned item as a
// VolumeGroup. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func volumeGroupList(
	ctx context.Context,
	client volumeGroupClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListVolumeGroupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListVolumeGroups(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing VolumeGroup in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &VolumeGroup{client: client, group: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// VolumeGroup wraps one core.VolumeGroup.
type VolumeGroup struct {
	client volumeGroupClient
	group  core.VolumeGroup
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *VolumeGroup) GetCompartmentID() string { return *r.group.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *VolumeGroup) UniqueKey() string { return *r.group.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// FAULTY is a genuine error state OCI itself cannot resolve -- neither cleanly "present" nor
// "gone" (04-RESEARCH.md Q2's FAULTY/FAILED note). It is excluded like TERMINATING/TERMINATED,
// but additionally reported via ocinuke.ReportLeftover(ReasonAPIError) before returning the
// exclusion error, so a FAULTY volume group surfaces as a labeled leftover rather than a silent
// scan-time drop.
func (r *VolumeGroup) Filter() error {
	switch r.group.LifecycleState {
	case core.VolumeGroupLifecycleStateProvisioning,
		core.VolumeGroupLifecycleStateAvailable,
		core.VolumeGroupLifecycleStateUpdatePending:
		return nil
	case core.VolumeGroupLifecycleStateFaulty:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  VolumeGroupResourceType,
			ResourceID:    *r.group.Id,
			CompartmentID: *r.group.CompartmentId,
			Detail:        "volume group is FAULTY",
		})
		return fmt.Errorf("VolumeGroup is %s, not available", r.group.LifecycleState)
	default:
		return fmt.Errorf("VolumeGroup is %s, not available", r.group.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *VolumeGroup) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.group
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *VolumeGroup) Remove(ctx context.Context) error {
	x := r.group
	_, err := r.client.DeleteVolumeGroup(ctx, core.DeleteVolumeGroupRequest{VolumeGroupId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *VolumeGroup) Properties() types.Properties {
	x := r.group
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propSizeInGBs, x.SizeInGBs)
}
