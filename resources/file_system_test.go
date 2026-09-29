package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// TestFileSystemLister_List_EnumeratesEveryAD proves fileSystemList reuses the same
// listAvailabilityDomainNames per-AD enumeration shape as MountTarget (T-04-28): three ADs, a
// stub ListFileSystems returning DIFFERENT results per AD, and List() must return the union
// across all three.
func TestFileSystemLister_List_EnumeratesEveryAD(t *testing.T) {
	ad1, ad2, ad3 := testAD1, testAD2, testAD3
	compartmentID := testCompartmentOCID
	fs1, fs2, fs3 := testFileSystemID1, testFileSystemID2, testFileSystemID3

	stub := &fakeFileStorageClient{
		ads: []identity.AvailabilityDomain{{Name: &ad1}, {Name: &ad2}, {Name: &ad3}},
		fileSystemsByAD: map[string][]filestorage.FileSystemSummary{
			ad1: {{Id: &fs1, CompartmentId: &compartmentID, LifecycleState: filestorage.FileSystemSummaryLifecycleStateActive}},
			ad2: {{Id: &fs2, CompartmentId: &compartmentID, LifecycleState: filestorage.FileSystemSummaryLifecycleStateActive}},
			ad3: {{Id: &fs3, CompartmentId: &compartmentID, LifecycleState: filestorage.FileSystemSummaryLifecycleStateActive}},
		},
	}

	got, err := fileSystemList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("fileSystemList() error = %v, want nil", err)
	}
	if len(got) != 3 {
		t.Fatalf("fileSystemList() returned %d resources, want 3 (one per AD, union across all three)", len(got))
	}
	if stub.listFileSystemsCalls != 3 {
		t.Fatalf("ListFileSystems called %d times, want 3 (once per AD)", stub.listFileSystemsCalls)
	}

	gotIDs := map[string]bool{}
	for _, r := range got {
		gotIDs[r.(*FileSystem).UniqueKey()] = true
	}
	for _, want := range []string{fs1, fs2, fs3} {
		if !gotIDs[want] {
			t.Errorf("fileSystemList() missing %q -- an AD was silently dropped", want)
		}
	}
}

// TestFileSystem_Filter proves the six-value lifecycle switch: CREATING/ACTIVE/UPDATING present,
// DELETING/DELETED excluded, FAILED excluded AND reported via ReportLeftover(ReasonAPIError).
func TestFileSystem_Filter(t *testing.T) {
	tests := []struct {
		state   filestorage.FileSystemSummaryLifecycleStateEnum
		present bool
	}{
		{filestorage.FileSystemSummaryLifecycleStateCreating, true},
		{filestorage.FileSystemSummaryLifecycleStateActive, true},
		{filestorage.FileSystemSummaryLifecycleStateUpdating, true},
		{filestorage.FileSystemSummaryLifecycleStateDeleting, false},
		{filestorage.FileSystemSummaryLifecycleStateDeleted, false},
		{filestorage.FileSystemSummaryLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			id := testResourceOCID
			compartmentID := testCompartmentOCID
			r := &FileSystem{}
			r.fs.Id = &id
			r.fs.CompartmentId = &compartmentID
			r.fs.LifecycleState = tc.state

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

// TestFileSystem_Remove proves the SDK delete call fires with the right FileSystemId, against a
// client that never touches the network.
func TestFileSystem_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeFileStorageClient{}
	r := &FileSystem{client: stub}
	r.fs.Id = &id
	r.fs.CompartmentId = &compartmentID
	r.fs.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedFileSystems) != 1 || stub.deletedFileSystems[0] != id {
		t.Fatalf("stub.deletedFileSystems = %v, want [%s]", stub.deletedFileSystems, id)
	}
}

// TestFileSystem_Properties proves availability_domain is surfaced.
func TestFileSystem_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	ad := testAD1

	r := &FileSystem{}
	r.fs.Id = &id
	r.fs.CompartmentId = &compartmentID
	r.fs.AvailabilityDomain = &ad

	props := r.Properties()
	if got := props.Get(propAvailabilityDomain); got != ad {
		t.Errorf("Properties()[%q] = %q, want %q", propAvailabilityDomain, got, ad)
	}
}

// TestFileSystemRegistration_DependsOnSnapshotAndExport asserts FileSystem's registered
// DependsOn is exactly ["Snapshot", "Export"] -- a snapshot or export referencing a file system
// must be gone before the file system delete is attempted.
func TestFileSystemRegistration_DependsOnSnapshotAndExport(t *testing.T) {
	reg := registry.GetRegistration(FileSystemResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", FileSystemResourceType)
	}

	want := []string{"Snapshot", "Export"}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("FileSystem DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("FileSystem DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}
