package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// TestSnapshotLister_List_EnumeratesAcrossADsAndFileSystems proves snapshotList enumerates
// AD-then-FileSystem-then-Snapshot, the same shape Export's own lister uses -- two ADs, two file
// systems, each with its own snapshot, and List() must return the union across both, correctly
// threading each file system's CompartmentId onto its Snapshots (SnapshotSummary itself has no
// CompartmentId field at all).
func TestSnapshotLister_List_EnumeratesAcrossADsAndFileSystems(t *testing.T) {
	ad1, ad2 := testAD1, testAD2
	compartmentID := testCompartmentOCID
	fs1, fs2 := testFileSystemID1, testFileSystemID2
	snap1, snap2 := "ocid1.snapshot.oc1..one", "ocid1.snapshot.oc1..two"

	stub := &fakeFileStorageClient{
		ads: []identity.AvailabilityDomain{{Name: &ad1}, {Name: &ad2}},
		fileSystemsByAD: map[string][]filestorage.FileSystemSummary{
			ad1: {{Id: &fs1, CompartmentId: &compartmentID}},
			ad2: {{Id: &fs2, CompartmentId: &compartmentID}},
		},
		snapshotsByFileSystem: map[string][]filestorage.SnapshotSummary{
			fs1: {{Id: &snap1, FileSystemId: &fs1, LifecycleState: filestorage.SnapshotSummaryLifecycleStateActive}},
			fs2: {{Id: &snap2, FileSystemId: &fs2, LifecycleState: filestorage.SnapshotSummaryLifecycleStateActive}},
		},
	}

	got, err := snapshotList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("snapshotList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("snapshotList() returned %d resources, want 2", len(got))
	}
	if stub.listFileSystemsCalls != 2 {
		t.Errorf("ListFileSystems called %d times, want 2 (once per AD)", stub.listFileSystemsCalls)
	}
	if stub.listSnapshotsCalls != 2 {
		t.Errorf("ListSnapshots called %d times, want 2 (once per FileSystem)", stub.listSnapshotsCalls)
	}

	for _, r := range got {
		s := r.(*Snapshot)
		if s.GetCompartmentID() != compartmentID {
			t.Errorf("Snapshot.GetCompartmentID() = %q, want %q (inherited from owning FileSystem)", s.GetCompartmentID(), compartmentID)
		}
	}
	gotIDs := map[string]bool{}
	for _, r := range got {
		gotIDs[r.(*Snapshot).UniqueKey()] = true
	}
	if !gotIDs[snap1] || !gotIDs[snap2] {
		t.Errorf("snapshotList() returned IDs %v, want both %q and %q", gotIDs, snap1, snap2)
	}
}

// TestSnapshot_Filter proves the four-value lifecycle switch: CREATING/ACTIVE present,
// DELETING/DELETED excluded -- the identical value set to Export's own enum, no FAILED value
// exists for this type either.
func TestSnapshot_Filter(t *testing.T) {
	tests := []struct {
		state   filestorage.SnapshotSummaryLifecycleStateEnum
		present bool
	}{
		{filestorage.SnapshotSummaryLifecycleStateCreating, true},
		{filestorage.SnapshotSummaryLifecycleStateActive, true},
		{filestorage.SnapshotSummaryLifecycleStateDeleting, false},
		{filestorage.SnapshotSummaryLifecycleStateDeleted, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			id := testResourceOCID
			r := &Snapshot{}
			r.snapshot.Id = &id
			r.snapshot.LifecycleState = tc.state

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

// TestSnapshot_Remove proves the SDK delete call fires with the right SnapshotId, against a
// client that never touches the network.
func TestSnapshot_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeFileStorageClient{}
	r := &Snapshot{client: stub, compartmentID: compartmentID}
	r.snapshot.Id = &id
	r.snapshot.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedSnapshots) != 1 || stub.deletedSnapshots[0] != id {
		t.Fatalf("stub.deletedSnapshots = %v, want [%s]", stub.deletedSnapshots, id)
	}
}

// TestSnapshot_Properties proves file_system_id is surfaced.
func TestSnapshot_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	fsID := testFileSystemID1

	r := &Snapshot{compartmentID: compartmentID}
	r.snapshot.Id = &id
	r.snapshot.FileSystemId = &fsID

	props := r.Properties()
	if got := props.Get(propFileSystemID); got != fsID {
		t.Errorf("Properties()[%q] = %q, want %q", propFileSystemID, got, fsID)
	}
}
