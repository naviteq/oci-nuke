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
)

// snapshotClient is the narrow slice this lister needs: availabilityDomainClient's
// ListAvailabilityDomains plus ListFileSystems (the same two-level enumeration Export's own
// lister uses, since ListSnapshotsRequest likewise carries no AvailabilityDomain parameter -- see
// below) and ListSnapshots/DeleteSnapshot. In production this is satisfied by
// combinedFileStorageClient; in tests, a single hand-written stub.
type snapshotClient interface {
	availabilityDomainClient
	ListFileSystems(ctx context.Context, req filestorage.ListFileSystemsRequest) (filestorage.ListFileSystemsResponse, error)
	ListSnapshots(ctx context.Context, req filestorage.ListSnapshotsRequest) (filestorage.ListSnapshotsResponse, error)
	DeleteSnapshot(ctx context.Context, req filestorage.DeleteSnapshotRequest) (filestorage.DeleteSnapshotResponse, error)
}

// SnapshotResourceType is the registry.Registration.Name for Snapshot.
const SnapshotResourceType = "Snapshot"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     SnapshotResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Snapshot{},
		Lister:   &snapshotLister{},
		// DependsOn is intentionally empty -- Snapshot is a leaf in this wave's dependency graph.
		// FileSystem (this plan's own Task 2 type) declares SnapshotResourceType in ITS OWN
		// DependsOn instead, ensuring snapshots are removed before the file system they were taken
		// from.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type snapshotLister struct{}

// List satisfies registry.Lister. The enumeration/wrapping logic itself lives in snapshotList,
// kept separate so it is unit-testable against a stub client without ever constructing real
// Identity/FileStorage clients.
func (l *snapshotLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("snapshotLister.List: unexpected opts type %T", opts)
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
	return snapshotList(ctx, client, o.CompartmentID)
}

// snapshotList enumerates Availability Domains, then FileSystems within each AD, then -- for
// EVERY discovered file system -- fully paginates filestorage.ListSnapshots scoped by
// FileSystemId, wrapping every returned item as a Snapshot. ListSnapshotsRequest has no
// AvailabilityDomain parameter and both CompartmentId and FileSystemId are optional (verified
// this session, filestorage/list_snapshots_request_response.go) -- enumerating per discovered
// FileSystem, the same shape Export's own lister uses, keeps this tied to the same AD-correct
// FileSystem set and lets every constructed Snapshot inherit its owning file system's
// CompartmentId (SnapshotSummary itself carries no CompartmentId field at all, verified this
// session). Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func snapshotList(
	ctx context.Context,
	client snapshotClient,
	compartmentID string,
) ([]resource.Resource, error) {
	ads, err := listAvailabilityDomainNames(ctx, client, compartmentID)
	if err != nil {
		return nil, err
	}

	var out []resource.Resource
	for _, ad := range ads {
		fsReq := filestorage.ListFileSystemsRequest{CompartmentId: &compartmentID, AvailabilityDomain: &ad}
		for {
			fsResp, err := client.ListFileSystems(ctx, fsReq)
			if err != nil {
				return nil, fmt.Errorf("listing FileSystem (for Snapshot enumeration) in %s AD %s: %w", compartmentID, ad, err)
			}

			for i := range fsResp.Items {
				snaps, err := snapshotsForFileSystem(ctx, client, &fsResp.Items[i])
				if err != nil {
					return nil, err
				}
				out = append(out, snaps...)
			}

			if fsResp.OpcNextPage == nil {
				break
			}
			fsReq.Page = fsResp.OpcNextPage
		}
	}
	return out, nil
}

// snapshotsForFileSystem fully paginates filestorage.ListSnapshots scoped by fs.Id, wrapping
// every returned item as a Snapshot that inherits fs's CompartmentId. fs is passed by pointer --
// it is a 200+ byte struct (gocritic hugeParam) and this function never mutates it.
func snapshotsForFileSystem(
	ctx context.Context,
	client snapshotClient,
	fs *filestorage.FileSystemSummary,
) ([]resource.Resource, error) {
	var out []resource.Resource
	snapReq := filestorage.ListSnapshotsRequest{FileSystemId: fs.Id}
	for {
		snapResp, err := client.ListSnapshots(ctx, snapReq)
		if err != nil {
			return nil, fmt.Errorf("listing Snapshot for FileSystem %s: %w", safeDeref(fs.Id), err)
		}
		for j := range snapResp.Items {
			out = append(out, &Snapshot{
				client:        client,
				snapshot:      snapResp.Items[j],
				compartmentID: *fs.CompartmentId,
			})
		}
		if snapResp.OpcNextPage == nil {
			break
		}
		snapReq.Page = snapResp.OpcNextPage
	}
	return out, nil
}

// Snapshot wraps one filestorage.SnapshotSummary, plus the owning file system's CompartmentId
// (SnapshotSummary itself has no CompartmentId field -- see snapshotList's doc comment above).
type Snapshot struct {
	client        snapshotClient
	snapshot      filestorage.SnapshotSummary
	compartmentID string
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Snapshot) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Snapshot) UniqueKey() string { return *r.snapshot.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// filestorage.SnapshotSummaryLifecycleStateEnum (verified this session,
// filestorage/snapshot_summary.go) has exactly FOUR values: CREATING, ACTIVE, DELETING, DELETED
// -- the identical value set to Export's own enum, no FAILED value exists for this type either.
// CREATING/ACTIVE are present; DELETING/DELETED are excluded (going/gone, the hang-trap defense).
func (r *Snapshot) Filter() error {
	switch r.snapshot.LifecycleState {
	case filestorage.SnapshotSummaryLifecycleStateCreating,
		filestorage.SnapshotSummaryLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("Snapshot is %s, not available", r.snapshot.LifecycleState)
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
// filestorage.SnapshotSummary DOES declare FreeformTags/DefinedTags fields (unlike
// ExportSummary -- verified this session), so those are threaded through here for real.
func (r *Snapshot) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.snapshot
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Snapshot) Remove(ctx context.Context) error {
	x := r.snapshot
	_, err := r.client.DeleteSnapshot(ctx, filestorage.DeleteSnapshotRequest{SnapshotId: x.Id})
	return holdOn409(err)
}

// Properties is built via resources/support.go's baseProperties, plus file_system_id.
func (r *Snapshot) Properties() types.Properties {
	x := r.snapshot
	return baseProperties(x.Id, x.Name, &r.compartmentID, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propFileSystemID, x.FileSystemId)
}
