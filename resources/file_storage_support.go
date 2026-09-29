// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// availabilityDomainClient is the narrow slice of identity.IdentityClient this file's helper
// needs -- matching the one real method identity.IdentityClient exposes that this plan depends
// on, reached in production via pkg/clients.Cache.Identity (already shipped Phase 1, already used
// elsewhere in this project for region resolution).
type availabilityDomainClient interface {
	ListAvailabilityDomains(
		ctx context.Context, req identity.ListAvailabilityDomainsRequest,
	) (identity.ListAvailabilityDomainsResponse, error)
}

// listAvailabilityDomainNames returns every Availability Domain's Name for compartmentID.
// Shared by MountTarget and FileSystem (and, transitively through their own per-FileSystem
// enumeration, Export and Snapshot), whose ListMountTargets/ListFileSystems SDK calls both mark
// AvailabilityDomain mandatory:"true" (verified this session against
// oci-go-sdk/v65/filestorage/list_mount_targets_request_response.go and
// list_file_systems_request_response.go) -- unlike every other lister in this wave, which lists
// once per compartment, these two types must enumerate every AD first and call their list
// operation once per AD, never silently dropping one (T-04-28).
//
// identity.ListAvailabilityDomainsRequest carries no Page field on the request side at all
// (verified this session, identity/list_availability_domains_request_response.go), even though
// the response embeds OpcNextPage like every other paginated response in this SDK -- this
// operation returns every AD for a compartment/region in a single call, so this helper makes
// exactly one request, never a pagination loop.
func listAvailabilityDomainNames(
	ctx context.Context, client availabilityDomainClient, compartmentID string,
) ([]string, error) {
	resp, err := client.ListAvailabilityDomains(ctx, identity.ListAvailabilityDomainsRequest{CompartmentId: &compartmentID})
	if err != nil {
		return nil, fmt.Errorf("listing availability domains in %s: %w", compartmentID, err)
	}

	names := make([]string, 0, len(resp.Items))
	for i := range resp.Items {
		if resp.Items[i].Name == nil {
			continue
		}
		names = append(names, *resp.Items[i].Name)
	}
	return names, nil
}

// combinedFileStorageClient satisfies every one of this plan's four narrow interfaces
// (mountTargetClient, fileSystemClient, exportClient, snapshotClient) by embedding BOTH concrete
// SDK clients each of them needs: identity.IdentityClient (for the shared
// listAvailabilityDomainNames helper above) and filestorage.FileStorageClient (for every
// List*/Delete* call in this plan -- ListMountTargets/DeleteMountTarget,
// ListFileSystems/DeleteFileSystem, ListExports/DeleteExport, ListSnapshots/DeleteSnapshot all
// live on this ONE client). Two different pkg/clients.Cache accessors (Identity, FileStorage)
// combined into a single Go interface value at each Lister.List call site -- this wave's third
// real deviation from "one client, one interface" (after Plan 04-05's VolumeAttachment reaching
// across core.ComputeClient/core.BlockstorageClient conceptually, and Plan 04-09's shared
// six-type Object Storage interface). Declared once here, shared by all four of this plan's
// listers, rather than duplicated per file, since the combination is identical in every case.
type combinedFileStorageClient struct {
	identity.IdentityClient
	filestorage.FileStorageClient
}
