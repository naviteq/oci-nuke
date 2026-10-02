package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// testAD1/testAD2/testAD3 are the shared three-AD fixture names every per-AD enumeration test in
// this plan (MountTarget, FileSystem, and transitively Export/Snapshot's own per-FileSystem tests)
// reuses -- declared once here so the package stays goconst-clean (min-occurrences: 3), mirroring
// resources/object_storage_support_test.go's testNamespace/testBucketName precedent.
const (
	testAD1 = "AD-1"
	testAD2 = "AD-2"
	testAD3 = "AD-3"
)

// testFileSystemID1/2/3 are the shared FileSystem OCID fixtures resources/file_system_test.go,
// resources/export_test.go, and resources/snapshot_test.go all reuse (Export/Snapshot enumerate
// per discovered FileSystem, so their own list tests need FileSystem fixtures too) -- declared
// once here for the same goconst reason as testAD1/testAD2/testAD3 above.
const (
	testFileSystemID1 = "ocid1.filesystem.oc1..one"
	testFileSystemID2 = "ocid1.filesystem.oc1..two"
	testFileSystemID3 = "ocid1.filesystem.oc1..three"
)

// fakeFileStorageClient implements availabilityDomainClient, mountTargetClient, fileSystemClient,
// exportClient, and snapshotClient against in-memory data -- zero network access. Shared by every
// File Storage resource type's tests in this plan (MountTarget, FileSystem, Export, Snapshot),
// mirroring resources/object_storage_support_test.go's fakeObjectStorageClient precedent.
//
// mountTargetsByAD/fileSystemsByAD are keyed by AvailabilityDomain name, proving the per-AD
// enumeration is real (T-04-28) -- a stub keyed by anything else could not distinguish "listed
// once for the whole compartment" from "listed once per AD". exportsByFileSystem/
// snapshotsByFileSystem are keyed by FileSystemId, matching the per-FileSystem enumeration Export
// and Snapshot's own listers use.
type fakeFileStorageClient struct {
	ads    []identity.AvailabilityDomain
	adsErr error

	mountTargetsByAD      map[string][]filestorage.MountTargetSummary
	listMountTargetsCalls int

	fileSystemsByAD      map[string][]filestorage.FileSystemSummary
	listFileSystemsCalls int

	exportsByFileSystem map[string][]filestorage.ExportSummary
	listExportsCalls    int

	snapshotsByFileSystem map[string][]filestorage.SnapshotSummary
	listSnapshotsCalls    int

	deletedMountTargets []string
	deletedFileSystems  []string

	getMountTargetResp filestorage.GetMountTargetResponse
	getFileSystemResp  filestorage.GetFileSystemResponse
	getErr             error
	deletedExports     []string
	deletedSnapshots   []string
}

func (s *fakeFileStorageClient) ListAvailabilityDomains(
	_ context.Context, _ identity.ListAvailabilityDomainsRequest,
) (identity.ListAvailabilityDomainsResponse, error) {
	if s.adsErr != nil {
		return identity.ListAvailabilityDomainsResponse{}, s.adsErr
	}
	return identity.ListAvailabilityDomainsResponse{Items: s.ads}, nil
}

func (s *fakeFileStorageClient) ListMountTargets(
	_ context.Context, req filestorage.ListMountTargetsRequest,
) (filestorage.ListMountTargetsResponse, error) {
	s.listMountTargetsCalls++
	return filestorage.ListMountTargetsResponse{Items: s.mountTargetsByAD[safeDeref(req.AvailabilityDomain)]}, nil
}

func (s *fakeFileStorageClient) DeleteMountTarget(
	_ context.Context, req filestorage.DeleteMountTargetRequest,
) (filestorage.DeleteMountTargetResponse, error) {
	s.deletedMountTargets = append(s.deletedMountTargets, safeDeref(req.MountTargetId))
	return filestorage.DeleteMountTargetResponse{}, nil
}

func (s *fakeFileStorageClient) GetMountTarget(
	_ context.Context, _ filestorage.GetMountTargetRequest,
) (filestorage.GetMountTargetResponse, error) {
	return s.getMountTargetResp, s.getErr
}

func (s *fakeFileStorageClient) ListFileSystems(
	_ context.Context, req filestorage.ListFileSystemsRequest,
) (filestorage.ListFileSystemsResponse, error) {
	s.listFileSystemsCalls++
	return filestorage.ListFileSystemsResponse{Items: s.fileSystemsByAD[safeDeref(req.AvailabilityDomain)]}, nil
}

func (s *fakeFileStorageClient) DeleteFileSystem(
	_ context.Context, req filestorage.DeleteFileSystemRequest,
) (filestorage.DeleteFileSystemResponse, error) {
	s.deletedFileSystems = append(s.deletedFileSystems, safeDeref(req.FileSystemId))
	return filestorage.DeleteFileSystemResponse{}, nil
}

func (s *fakeFileStorageClient) GetFileSystem(
	_ context.Context, _ filestorage.GetFileSystemRequest,
) (filestorage.GetFileSystemResponse, error) {
	return s.getFileSystemResp, s.getErr
}

func (s *fakeFileStorageClient) ListExports(
	_ context.Context, req filestorage.ListExportsRequest,
) (filestorage.ListExportsResponse, error) {
	s.listExportsCalls++
	return filestorage.ListExportsResponse{Items: s.exportsByFileSystem[safeDeref(req.FileSystemId)]}, nil
}

func (s *fakeFileStorageClient) DeleteExport(
	_ context.Context, req filestorage.DeleteExportRequest,
) (filestorage.DeleteExportResponse, error) {
	s.deletedExports = append(s.deletedExports, safeDeref(req.ExportId))
	return filestorage.DeleteExportResponse{}, nil
}

func (s *fakeFileStorageClient) ListSnapshots(
	_ context.Context, req filestorage.ListSnapshotsRequest,
) (filestorage.ListSnapshotsResponse, error) {
	s.listSnapshotsCalls++
	return filestorage.ListSnapshotsResponse{Items: s.snapshotsByFileSystem[safeDeref(req.FileSystemId)]}, nil
}

func (s *fakeFileStorageClient) DeleteSnapshot(
	_ context.Context, req filestorage.DeleteSnapshotRequest,
) (filestorage.DeleteSnapshotResponse, error) {
	s.deletedSnapshots = append(s.deletedSnapshots, safeDeref(req.SnapshotId))
	return filestorage.DeleteSnapshotResponse{}, nil
}

// TestListAvailabilityDomainNames proves listAvailabilityDomainNames returns every AD's Name,
// against a stub returning three ADs -- the fixture every per-AD enumeration test in this plan
// (MountTarget, FileSystem) builds on top of.
func TestListAvailabilityDomainNames(t *testing.T) {
	ad1, ad2, ad3 := testAD1, testAD2, testAD3
	stub := &fakeFileStorageClient{
		ads: []identity.AvailabilityDomain{{Name: &ad1}, {Name: &ad2}, {Name: &ad3}},
	}

	got, err := listAvailabilityDomainNames(context.Background(), stub, testCompartmentOCID)
	if err != nil {
		t.Fatalf("listAvailabilityDomainNames() error = %v, want nil", err)
	}
	want := []string{ad1, ad2, ad3}
	if len(got) != len(want) {
		t.Fatalf("listAvailabilityDomainNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listAvailabilityDomainNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestListAvailabilityDomainNames_NilNameSkipped proves an AvailabilityDomain with a nil Name is
// skipped rather than causing a nil-pointer panic.
func TestListAvailabilityDomainNames_NilNameSkipped(t *testing.T) {
	ad1 := testAD1
	stub := &fakeFileStorageClient{
		ads: []identity.AvailabilityDomain{{Name: &ad1}, {Name: nil}},
	}

	got, err := listAvailabilityDomainNames(context.Background(), stub, testCompartmentOCID)
	if err != nil {
		t.Fatalf("listAvailabilityDomainNames() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != ad1 {
		t.Fatalf("listAvailabilityDomainNames() = %v, want [%q]", got, ad1)
	}
}

// TestListAvailabilityDomainNames_Error proves a ListAvailabilityDomains error propagates rather
// than being silently swallowed.
func TestListAvailabilityDomainNames_Error(t *testing.T) {
	stub := &fakeFileStorageClient{adsErr: errors.New("boom")}

	_, err := listAvailabilityDomainNames(context.Background(), stub, testCompartmentOCID)
	if err == nil {
		t.Fatal("listAvailabilityDomainNames() error = nil, want non-nil")
	}
}

// TestMountTargetLister_List_EnumeratesEveryAD is this plan's named proof of T-04-28: three ADs,
// a stub ListMountTargets returning DIFFERENT results per AD, and List() must return the union
// across all three -- proving per-AD enumeration is real, not a single call with an AD silently
// dropped (the exact failure mode a region with 3 ADs but only 1 checked would produce).
func TestMountTargetLister_List_EnumeratesEveryAD(t *testing.T) {
	ad1, ad2, ad3 := testAD1, testAD2, testAD3
	compartmentID := testCompartmentOCID
	mt1, mt2, mt3 := "ocid1.mounttarget.oc1..one", "ocid1.mounttarget.oc1..two", "ocid1.mounttarget.oc1..three"

	stub := &fakeFileStorageClient{
		ads: []identity.AvailabilityDomain{{Name: &ad1}, {Name: &ad2}, {Name: &ad3}},
		mountTargetsByAD: map[string][]filestorage.MountTargetSummary{
			ad1: {{Id: &mt1, CompartmentId: &compartmentID, LifecycleState: filestorage.MountTargetSummaryLifecycleStateActive}},
			ad2: {{Id: &mt2, CompartmentId: &compartmentID, LifecycleState: filestorage.MountTargetSummaryLifecycleStateActive}},
			ad3: {{Id: &mt3, CompartmentId: &compartmentID, LifecycleState: filestorage.MountTargetSummaryLifecycleStateActive}},
		},
	}

	got, err := mountTargetList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("mountTargetList() error = %v, want nil", err)
	}
	if len(got) != 3 {
		t.Fatalf("mountTargetList() returned %d resources, want 3 (one per AD, union across all three)", len(got))
	}
	if stub.listMountTargetsCalls != 3 {
		t.Fatalf("ListMountTargets called %d times, want 3 (once per AD)", stub.listMountTargetsCalls)
	}

	gotIDs := map[string]bool{}
	for _, r := range got {
		gotIDs[r.(*MountTarget).UniqueKey()] = true
	}
	for _, want := range []string{mt1, mt2, mt3} {
		if !gotIDs[want] {
			t.Errorf("mountTargetList() missing %q -- an AD was silently dropped", want)
		}
	}
}

// TestMountTarget_Filter proves the six-value lifecycle switch: CREATING/ACTIVE/UPDATING/FAILED
// present, DELETING/DELETED excluded.
func TestMountTarget_Filter(t *testing.T) {
	tests := []struct {
		state   filestorage.MountTargetSummaryLifecycleStateEnum
		present bool
	}{
		{filestorage.MountTargetSummaryLifecycleStateCreating, true},
		{filestorage.MountTargetSummaryLifecycleStateActive, true},
		{filestorage.MountTargetSummaryLifecycleStateUpdating, true},
		{filestorage.MountTargetSummaryLifecycleStateDeleting, false},
		{filestorage.MountTargetSummaryLifecycleStateDeleted, false},
		{filestorage.MountTargetSummaryLifecycleStateFailed, true},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			id := testResourceOCID
			compartmentID := testCompartmentOCID
			r := &MountTarget{}
			r.target.Id = &id
			r.target.CompartmentId = &compartmentID
			r.target.LifecycleState = tc.state

			err := r.Filter()
			if tc.present && err != nil {
				t.Errorf("Filter() = %v, want nil (present)", err)
			}
			if !tc.present && err == nil {
				t.Errorf("Filter() = nil, want non-nil (excluded, hang-trap regression)")
			}
		})
	}
}

// TestMountTarget_Remove proves the SDK delete call fires with the right MountTargetId, against a
// client that never touches the network.
func TestMountTarget_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	timeCreated := common.SDKTime{Time: time.Now()}
	stub := &fakeFileStorageClient{}
	r := &MountTarget{client: stub}
	r.target.Id = &id
	r.target.CompartmentId = &compartmentID
	r.target.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedMountTargets) != 1 || stub.deletedMountTargets[0] != id {
		t.Fatalf("stub.deletedMountTargets = %v, want [%s]", stub.deletedMountTargets, id)
	}
}

// TestMountTarget_Properties_ExportSetIDPresent proves export_set_id is surfaced in Properties()
// -- the ExportSet visibility exposure this plan's objective documents (ExportSet is never
// registered as its own resource type).
func TestMountTarget_Properties_ExportSetIDPresent(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	exportSetID := "ocid1.exportset.oc1..one"
	ad := testAD1

	r := &MountTarget{}
	r.target.Id = &id
	r.target.CompartmentId = &compartmentID
	r.target.ExportSetId = &exportSetID
	r.target.AvailabilityDomain = &ad

	props := r.Properties()
	if got := props.Get(propExportSetID); got != exportSetID {
		t.Errorf("Properties()[%q] = %q, want %q", propExportSetID, got, exportSetID)
	}
	if got := props.Get(propAvailabilityDomain); got != ad {
		t.Errorf("Properties()[%q] = %q, want %q", propAvailabilityDomain, got, ad)
	}
}

// TestMountTargetRegistration_DependsOnEmpty proves MountTarget declares no DependsOn -- ExportSet
// (the one type that would have depended on it) is not independently registered at all.
func TestMountTargetRegistration_DependsOnEmpty(t *testing.T) {
	reg := registry.GetRegistration(MountTargetResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", MountTargetResourceType)
	}
	if len(reg.DependsOn) != 0 {
		t.Errorf("MountTarget DependsOn = %v, want empty", reg.DependsOn)
	}
}
