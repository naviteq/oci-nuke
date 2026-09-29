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

// exportClient is the narrow slice this lister needs: availabilityDomainClient's
// ListAvailabilityDomains plus ListFileSystems (both resources/file_storage_support.go /
// resources/file_system.go's own shape, reused here since Export must enumerate FileSystems
// first -- ListExportsRequest itself has no AvailabilityDomain parameter at all, see below) and
// ListExports/DeleteExport. In production this is satisfied by combinedFileStorageClient; in
// tests, a single hand-written stub.
type exportClient interface {
	availabilityDomainClient
	ListFileSystems(ctx context.Context, req filestorage.ListFileSystemsRequest) (filestorage.ListFileSystemsResponse, error)
	ListExports(ctx context.Context, req filestorage.ListExportsRequest) (filestorage.ListExportsResponse, error)
	DeleteExport(ctx context.Context, req filestorage.DeleteExportRequest) (filestorage.DeleteExportResponse, error)
}

// ExportResourceType is the registry.Registration.Name for Export.
const ExportResourceType = "Export"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ExportResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Export{},
		Lister:   &exportLister{},
		// DependsOn is intentionally empty -- Export is a leaf in this wave's dependency graph.
		// FileSystem (this plan's own Task 2 type) declares ExportResourceType in ITS OWN
		// DependsOn instead, ensuring exports are removed before the file system they reference.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type exportLister struct{}

// List satisfies registry.Lister. The enumeration/wrapping logic itself lives in exportList, kept
// separate so it is unit-testable against a stub client without ever constructing real
// Identity/FileStorage clients.
func (l *exportLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("exportLister.List: unexpected opts type %T", opts)
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
	return exportList(ctx, client, o.CompartmentID)
}

// exportList enumerates Availability Domains, then FileSystems within each AD (the same two
// levels FileSystem's own lister uses), then -- for EVERY discovered file system -- fully
// paginates filestorage.ListExports scoped by FileSystemId, wrapping every returned item as an
// Export. ListExportsRequest has no AvailabilityDomain parameter at all and both CompartmentId
// and FileSystemId are optional (verified this session,
// filestorage/list_exports_request_response.go) -- enumerating per discovered FileSystem, rather
// than relying on an unfiltered ListExports(CompartmentId: ...) call, keeps the enumeration
// structurally tied to the same AD-correct FileSystem set FileSystem's own lister proves complete
// (T-04-28), and lets every constructed Export inherit its owning file system's CompartmentId --
// ExportSummary itself carries no CompartmentId field at all (verified this session), the same
// "thread the parent's CompartmentId down" shape Plan 04-09 uses for Object Storage's dependent
// types. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list
// test below exercises against a stub, with zero network access.
func exportList(
	ctx context.Context,
	client exportClient,
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
				return nil, fmt.Errorf("listing FileSystem (for Export enumeration) in %s AD %s: %w", compartmentID, ad, err)
			}

			for i := range fsResp.Items {
				exports, err := exportsForFileSystem(ctx, client, &fsResp.Items[i])
				if err != nil {
					return nil, err
				}
				out = append(out, exports...)
			}

			if fsResp.OpcNextPage == nil {
				break
			}
			fsReq.Page = fsResp.OpcNextPage
		}
	}
	return out, nil
}

// exportsForFileSystem fully paginates filestorage.ListExports scoped by fs.Id, wrapping every
// returned item as an Export that inherits fs's CompartmentId. fs is passed by pointer -- it is a
// 200+ byte struct (gocritic hugeParam) and this function never mutates it.
func exportsForFileSystem(
	ctx context.Context,
	client exportClient,
	fs *filestorage.FileSystemSummary,
) ([]resource.Resource, error) {
	var out []resource.Resource
	exReq := filestorage.ListExportsRequest{FileSystemId: fs.Id}
	for {
		exResp, err := client.ListExports(ctx, exReq)
		if err != nil {
			return nil, fmt.Errorf("listing Export for FileSystem %s: %w", safeDeref(fs.Id), err)
		}
		for j := range exResp.Items {
			out = append(out, &Export{
				client:        client,
				export:        exResp.Items[j],
				compartmentID: *fs.CompartmentId,
			})
		}
		if exResp.OpcNextPage == nil {
			break
		}
		exReq.Page = exResp.OpcNextPage
	}
	return out, nil
}

// Export wraps one filestorage.ExportSummary, plus the owning file system's CompartmentId
// (ExportSummary itself has no CompartmentId field -- see exportList's doc comment above).
type Export struct {
	client        exportClient
	export        filestorage.ExportSummary
	compartmentID string
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Export) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Export) UniqueKey() string { return *r.export.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// filestorage.ExportSummaryLifecycleStateEnum (verified this session,
// filestorage/export_summary.go) has exactly FOUR values: CREATING, ACTIVE, DELETING, DELETED --
// no FAILED value exists for this type at all, unlike MountTarget/FileSystem, so there is no
// ReportLeftover branch here. CREATING/ACTIVE are present; DELETING/DELETED are excluded
// (going/gone, the hang-trap defense).
func (r *Export) Filter() error {
	switch r.export.LifecycleState {
	case filestorage.ExportSummaryLifecycleStateCreating,
		filestorage.ExportSummaryLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("Export is %s, not available", r.export.LifecycleState)
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
// nil, nil is passed for freeform/defined tags: filestorage.ExportSummary declares no
// FreeformTags/DefinedTags fields at all (verified this session, filestorage/export_summary.go)
// -- min-age protection via TimeCreated still applies, but protect-by-tag cannot match this type
// from list-time data alone, the same documented shape resources/volume_attachment.go already
// established for core.VolumeAttachment.
func (r *Export) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.export
	return nil, nil, x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Export) Remove(ctx context.Context) error {
	x := r.export
	_, err := r.client.DeleteExport(ctx, filestorage.DeleteExportRequest{ExportId: x.Id})
	return holdOn409(err)
}

// Properties is built via resources/support.go's baseProperties (name nil, tags nil -- Export has
// neither a DisplayName nor any tag fields), plus file_system_id/export_set_id/path --
// export_set_id is the SAME property key resources/mount_target.go's Properties() sets, the
// ExportSet visibility exposure this plan's objective documents.
func (r *Export) Properties() types.Properties {
	x := r.export
	return baseProperties(x.Id, nil, &r.compartmentID, string(x.LifecycleState), x.TimeCreated, nil, nil).
		Set(propFileSystemID, x.FileSystemId).
		Set(propExportSetID, x.ExportSetId).
		Set("path", x.Path)
}
