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

// bootVolumeClient is the narrow slice of core.BlockstorageClient this lister needs -- a
// hand-written interface so BootVolume is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type bootVolumeClient interface {
	ListBootVolumes(ctx context.Context, req core.ListBootVolumesRequest) (core.ListBootVolumesResponse, error)
	DeleteBootVolume(ctx context.Context, req core.DeleteBootVolumeRequest) (core.DeleteBootVolumeResponse, error)
}

// BootVolumeResourceType is the registry.Registration.Name for BootVolume.
const BootVolumeResourceType = "BootVolume"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     BootVolumeResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &BootVolume{},
		Lister:   &bootVolumeLister{},
		// DependsOn is intentionally empty -- nothing in this wave must be removed before a
		// boot volume; the instance it is attached to is torn down via VolumeAttachment/
		// Instance, not via a DependsOn edge on this type.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type bootVolumeLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// bootVolumeList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Blockstorage client.
func (l *bootVolumeLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("bootVolumeLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Blockstorage(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Blockstorage client for %s: %w", o.Region, err)
	}

	return bootVolumeList(ctx, client, o.CompartmentID)
}

// bootVolumeList paginates core.ListBootVolumes and wraps every returned item as a BootVolume.
// Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test
// below exercises against a stub, with zero network access.
//
// Only CompartmentId is set on the request -- ListBootVolumesRequest.AvailabilityDomain is
// mandatory:"false" (verified against oci-go-sdk/v65/core/list_boot_volumes_request_response.go
// this session), so a compartment-scoped list without an AD filter returns every AD's boot
// volumes in that compartment/region, consistent with this project's existing live-verified
// real block-volume listing (04-RESEARCH.md RES-02 row).
func bootVolumeList(
	ctx context.Context,
	client bootVolumeClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListBootVolumesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListBootVolumes(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing BootVolume in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &BootVolume{client: client, bootVolume: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// BootVolume wraps one core.BootVolume.
type BootVolume struct {
	client     bootVolumeClient
	bootVolume core.BootVolume
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *BootVolume) GetCompartmentID() string { return *r.bootVolume.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *BootVolume) UniqueKey() string { return *r.bootVolume.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// FAULTY is a genuine error state OCI itself cannot resolve -- neither cleanly "present" nor
// "gone" (04-RESEARCH.md Q2's FAULTY/FAILED note). It is excluded like TERMINATING/TERMINATED,
// but additionally reported via ocinuke.ReportLeftover(ReasonAPIError) before returning the
// exclusion error, so a FAULTY boot volume surfaces as a labeled leftover rather than a silent
// scan-time drop.
func (r *BootVolume) Filter() error {
	switch r.bootVolume.LifecycleState {
	case core.BootVolumeLifecycleStateProvisioning,
		core.BootVolumeLifecycleStateRestoring,
		core.BootVolumeLifecycleStateAvailable:
		return nil
	case core.BootVolumeLifecycleStateFaulty:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  BootVolumeResourceType,
			ResourceID:    *r.bootVolume.Id,
			CompartmentID: *r.bootVolume.CompartmentId,
			Detail:        "boot volume is FAULTY",
		})
		return fmt.Errorf("BootVolume is %s, not available", r.bootVolume.LifecycleState)
	default:
		return fmt.Errorf("BootVolume is %s, not available", r.bootVolume.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *BootVolume) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.bootVolume
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *BootVolume) Remove(ctx context.Context) error {
	x := r.bootVolume
	_, err := r.client.DeleteBootVolume(ctx, core.DeleteBootVolumeRequest{BootVolumeId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *BootVolume) Properties() types.Properties {
	x := r.bootVolume
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propSizeInGBs, x.SizeInGBs)
}
