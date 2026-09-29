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
	"github.com/oracle/oci-go-sdk/v65/filestorage"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// mountTargetClient is the narrow slice this lister needs: availabilityDomainClient's
// ListAvailabilityDomains (resources/file_storage_support.go) plus
// ListMountTargets/DeleteMountTarget, both mandatory on filestorage.FileStorageClient. In
// production this is satisfied by combinedFileStorageClient (identity.IdentityClient +
// filestorage.FileStorageClient); in tests, a single hand-written stub.
type mountTargetClient interface {
	availabilityDomainClient
	ListMountTargets(ctx context.Context, req filestorage.ListMountTargetsRequest) (filestorage.ListMountTargetsResponse, error)
	DeleteMountTarget(ctx context.Context, req filestorage.DeleteMountTargetRequest) (filestorage.DeleteMountTargetResponse, error)
}

// MountTargetResourceType is the registry.Registration.Name for MountTarget.
const MountTargetResourceType = "MountTarget"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     MountTargetResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &MountTarget{},
		Lister:   &mountTargetLister{},
		// DependsOn is intentionally empty -- ExportSet (the one File Storage concept that would
		// have depended on MountTarget) is not an independently registered type at all (this
		// plan's objective: no DeleteExportSet operation exists anywhere in the SDK). Nothing else
		// this wave registers must be removed before a mount target.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type mountTargetLister struct{}

// List satisfies registry.Lister. The enumeration/wrapping logic itself lives in
// mountTargetList, kept separate so it is unit-testable against a stub client without ever
// constructing real Identity/FileStorage clients.
func (l *mountTargetLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("mountTargetLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	identityClient, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing IdentityClient for %s: %w", o.Region, err)
	}
	fsClient, err := o.Clients.FileStorage(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing FileStorageClient for %s: %w", o.Region, err)
	}

	client := combinedFileStorageClient{IdentityClient: identityClient, FileStorageClient: fsClient}
	return mountTargetList(ctx, client, o.CompartmentID)
}

// mountTargetList first enumerates every Availability Domain in compartmentID
// (listAvailabilityDomainNames, resources/file_storage_support.go), then -- for EVERY AD -- fully
// paginates filestorage.ListMountTargets scoped by that AD, wrapping every returned item as a
// MountTarget. ListMountTargetsRequest.AvailabilityDomain is mandatory:"true" (verified this
// session), so there is no compartment-wide call to make; the union across every AD IS the
// complete list (T-04-28) -- a lister that only checked one AD would silently under-list.
// Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test
// below exercises against a stub, with zero network access.
func mountTargetList(
	ctx context.Context,
	client mountTargetClient,
	compartmentID string,
) ([]resource.Resource, error) {
	ads, err := listAvailabilityDomainNames(ctx, client, compartmentID)
	if err != nil {
		return nil, err
	}

	var out []resource.Resource
	for _, ad := range ads {
		req := filestorage.ListMountTargetsRequest{CompartmentId: &compartmentID, AvailabilityDomain: &ad}
		for {
			resp, err := client.ListMountTargets(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing MountTarget in %s AD %s: %w", compartmentID, ad, err)
			}
			for i := range resp.Items {
				out = append(out, &MountTarget{client: client, target: resp.Items[i]})
			}
			if resp.OpcNextPage == nil {
				break
			}
			req.Page = resp.OpcNextPage
		}
	}
	return out, nil
}

// MountTarget wraps one filestorage.MountTargetSummary.
type MountTarget struct {
	client mountTargetClient
	target filestorage.MountTargetSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// MountTargetSummary.CompartmentId is mandatory:"true" (verified this session).
func (r *MountTarget) GetCompartmentID() string { return *r.target.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *MountTarget) UniqueKey() string { return *r.target.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// filestorage.MountTargetSummaryLifecycleStateEnum (verified this session,
// filestorage/mount_target_summary.go) has exactly six values: CREATING, ACTIVE, DELETING,
// DELETED, FAILED, UPDATING. CREATING/ACTIVE/UPDATING are present; DELETING/DELETED are excluded
// (going/gone, the hang-trap defense). FAILED is a genuine error state OCI itself cannot resolve
// -- excluded like DELETING/DELETED, but additionally reported via
// ocinuke.ReportLeftover(ReasonAPIError) before returning the exclusion error, mirroring
// resources/block_volume.go's FAULTY handling, so a FAILED mount target surfaces as a labeled
// leftover rather than a silent scan-time drop.
func (r *MountTarget) Filter() error {
	switch r.target.LifecycleState {
	case filestorage.MountTargetSummaryLifecycleStateCreating,
		filestorage.MountTargetSummaryLifecycleStateActive,
		filestorage.MountTargetSummaryLifecycleStateUpdating:
		return nil
	case filestorage.MountTargetSummaryLifecycleStateFailed:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  MountTargetResourceType,
			ResourceID:    *r.target.Id,
			CompartmentID: *r.target.CompartmentId,
			Detail:        "mount target is FAILED",
		})
		return fmt.Errorf("MountTarget is %s, not available", r.target.LifecycleState)
	default:
		return fmt.Errorf("MountTarget is %s, not available", r.target.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *MountTarget) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.target
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *MountTarget) Remove(ctx context.Context) error {
	x := r.target
	_, err := r.client.DeleteMountTarget(ctx, filestorage.DeleteMountTargetRequest{MountTargetId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper, plus export_set_id -- the ExportSet visibility exposure this
// plan's objective documents: ExportSet is never registered as its own resource type (no
// DeleteExportSet operation exists in the SDK at all), so its identity is surfaced here instead,
// through the mount target that owns it.
func (r *MountTarget) Properties() types.Properties {
	x := r.target
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propAvailabilityDomain, x.AvailabilityDomain).
		Set(propExportSetID, x.ExportSetId)
}
