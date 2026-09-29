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

// fileSystemClient is the narrow slice this lister needs: availabilityDomainClient's
// ListAvailabilityDomains (resources/file_storage_support.go) plus
// ListFileSystems/DeleteFileSystem, both mandatory on filestorage.FileStorageClient. In
// production this is satisfied by combinedFileStorageClient (resources/mount_target.go's
// production wiring uses the same shape); in tests, a single hand-written stub.
type fileSystemClient interface {
	availabilityDomainClient
	ListFileSystems(ctx context.Context, req filestorage.ListFileSystemsRequest) (filestorage.ListFileSystemsResponse, error)
	DeleteFileSystem(ctx context.Context, req filestorage.DeleteFileSystemRequest) (filestorage.DeleteFileSystemResponse, error)
}

// FileSystemResourceType is the registry.Registration.Name for FileSystem.
const FileSystemResourceType = "FileSystem"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     FileSystemResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &FileSystem{},
		Lister:   &fileSystemLister{},
		// DependsOn: a file system cannot be deleted while a snapshot or export referencing it
		// still exists -- bare string literals, matching the cross-plan/within-plan sibling
		// convention already established (resources/bucket.go); Snapshot and Export are this
		// plan's own Task 3 types.
		DependsOn: []string{"Snapshot", "Export"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type fileSystemLister struct{}

// List satisfies registry.Lister. The enumeration/wrapping logic itself lives in
// fileSystemList, kept separate so it is unit-testable against a stub client without ever
// constructing real Identity/FileStorage clients.
func (l *fileSystemLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("fileSystemLister.List: unexpected opts type %T", opts)
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
	return fileSystemList(ctx, client, o.CompartmentID)
}

// fileSystemList first enumerates every Availability Domain in compartmentID
// (listAvailabilityDomainNames, resources/file_storage_support.go), then -- for EVERY AD -- fully
// paginates filestorage.ListFileSystems scoped by that AD, wrapping every returned item as a
// FileSystem. ListFileSystemsRequest.AvailabilityDomain is mandatory:"true" (verified this
// session, matching MountTarget's own per-AD requirement) -- the union across every AD IS the
// complete list (T-04-28). Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this
// is what the list test below exercises against a stub, with zero network access.
func fileSystemList(
	ctx context.Context,
	client fileSystemClient,
	compartmentID string,
) ([]resource.Resource, error) {
	ads, err := listAvailabilityDomainNames(ctx, client, compartmentID)
	if err != nil {
		return nil, err
	}

	var out []resource.Resource
	for _, ad := range ads {
		req := filestorage.ListFileSystemsRequest{CompartmentId: &compartmentID, AvailabilityDomain: &ad}
		for {
			resp, err := client.ListFileSystems(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing FileSystem in %s AD %s: %w", compartmentID, ad, err)
			}
			for i := range resp.Items {
				out = append(out, &FileSystem{client: client, fs: resp.Items[i]})
			}
			if resp.OpcNextPage == nil {
				break
			}
			req.Page = resp.OpcNextPage
		}
	}
	return out, nil
}

// FileSystem wraps one filestorage.FileSystemSummary.
type FileSystem struct {
	client fileSystemClient
	fs     filestorage.FileSystemSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// FileSystemSummary.CompartmentId is mandatory:"true" (verified this session).
func (r *FileSystem) GetCompartmentID() string { return *r.fs.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *FileSystem) UniqueKey() string { return *r.fs.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// filestorage.FileSystemSummaryLifecycleStateEnum (verified this session,
// filestorage/file_system_summary.go) has exactly six values: CREATING, ACTIVE, UPDATING,
// DELETING, DELETED, FAILED -- the identical value set to MountTarget's own enum.
// CREATING/ACTIVE/UPDATING are present; DELETING/DELETED are excluded (going/gone, the hang-trap
// defense). FAILED is excluded like DELETING/DELETED, but additionally reported via
// ocinuke.ReportLeftover(ReasonAPIError) before returning the exclusion error, mirroring
// MountTarget's own FAILED handling.
func (r *FileSystem) Filter() error {
	switch r.fs.LifecycleState {
	case filestorage.FileSystemSummaryLifecycleStateCreating,
		filestorage.FileSystemSummaryLifecycleStateActive,
		filestorage.FileSystemSummaryLifecycleStateUpdating:
		return nil
	case filestorage.FileSystemSummaryLifecycleStateFailed:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  FileSystemResourceType,
			ResourceID:    *r.fs.Id,
			CompartmentID: *r.fs.CompartmentId,
			Detail:        "file system is FAILED",
		})
		return fmt.Errorf("FileSystem is %s, not available", r.fs.LifecycleState)
	default:
		return fmt.Errorf("FileSystem is %s, not available", r.fs.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *FileSystem) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.fs
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// By the time this call fires, WaitOnDependencies has already ensured every Snapshot/Export
// referencing this file system has finished or permanently failed (this type's own DependsOn
// above).
func (r *FileSystem) Remove(ctx context.Context) error {
	x := r.fs
	_, err := r.client.DeleteFileSystem(ctx, filestorage.DeleteFileSystemRequest{FileSystemId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper.
func (r *FileSystem) Properties() types.Properties {
	x := r.fs
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propAvailabilityDomain, x.AvailabilityDomain)
}
