package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// TestExportLister_List_EnumeratesAcrossADsAndFileSystems proves exportList enumerates
// AD-then-FileSystem-then-Export: two ADs, two file systems (one per AD), each with its own
// export, and List() must return the union across both -- correctly threading each file system's
// CompartmentId onto its Exports (ExportSummary itself has no CompartmentId field at all).
func TestExportLister_List_EnumeratesAcrossADsAndFileSystems(t *testing.T) {
	ad1, ad2 := testAD1, testAD2
	compartmentID := testCompartmentOCID
	fs1, fs2 := testFileSystemID1, testFileSystemID2
	ex1, ex2 := "ocid1.export.oc1..one", "ocid1.export.oc1..two"

	stub := &fakeFileStorageClient{
		ads: []identity.AvailabilityDomain{{Name: &ad1}, {Name: &ad2}},
		fileSystemsByAD: map[string][]filestorage.FileSystemSummary{
			ad1: {{Id: &fs1, CompartmentId: &compartmentID}},
			ad2: {{Id: &fs2, CompartmentId: &compartmentID}},
		},
		exportsByFileSystem: map[string][]filestorage.ExportSummary{
			fs1: {{Id: &ex1, FileSystemId: &fs1, LifecycleState: filestorage.ExportSummaryLifecycleStateActive}},
			fs2: {{Id: &ex2, FileSystemId: &fs2, LifecycleState: filestorage.ExportSummaryLifecycleStateActive}},
		},
	}

	got, err := exportList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("exportList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("exportList() returned %d resources, want 2", len(got))
	}
	if stub.listFileSystemsCalls != 2 {
		t.Errorf("ListFileSystems called %d times, want 2 (once per AD)", stub.listFileSystemsCalls)
	}
	if stub.listExportsCalls != 2 {
		t.Errorf("ListExports called %d times, want 2 (once per FileSystem)", stub.listExportsCalls)
	}

	for _, r := range got {
		exp := r.(*Export)
		if exp.GetCompartmentID() != compartmentID {
			t.Errorf("Export.GetCompartmentID() = %q, want %q (inherited from owning FileSystem)", exp.GetCompartmentID(), compartmentID)
		}
	}
	gotIDs := map[string]bool{}
	for _, r := range got {
		gotIDs[r.(*Export).UniqueKey()] = true
	}
	if !gotIDs[ex1] || !gotIDs[ex2] {
		t.Errorf("exportList() returned IDs %v, want both %q and %q", gotIDs, ex1, ex2)
	}
}

// TestExport_Filter proves the four-value lifecycle switch: CREATING/ACTIVE present,
// DELETING/DELETED excluded -- no FAILED value exists for this type at all.
func TestExport_Filter(t *testing.T) {
	tests := []struct {
		state   filestorage.ExportSummaryLifecycleStateEnum
		present bool
	}{
		{filestorage.ExportSummaryLifecycleStateCreating, true},
		{filestorage.ExportSummaryLifecycleStateActive, true},
		{filestorage.ExportSummaryLifecycleStateDeleting, false},
		{filestorage.ExportSummaryLifecycleStateDeleted, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			id := testResourceOCID
			r := &Export{}
			r.export.Id = &id
			r.export.LifecycleState = tc.state

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

// TestExport_Remove proves the SDK delete call fires with the right ExportId, against a client
// that never touches the network.
func TestExport_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeFileStorageClient{}
	r := &Export{client: stub, compartmentID: compartmentID}
	r.export.Id = &id
	r.export.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedExports) != 1 || stub.deletedExports[0] != id {
		t.Fatalf("stub.deletedExports = %v, want [%s]", stub.deletedExports, id)
	}
}

// TestExport_Properties proves file_system_id/export_set_id/path are all surfaced --
// export_set_id is the SAME property key resources/mount_target.go's Properties() sets, the
// ExportSet visibility exposure this plan's objective documents.
func TestExport_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	fsID := testFileSystemID1
	exportSetID := "ocid1.exportset.oc1..one"
	path := "/mediafiles"

	r := &Export{compartmentID: compartmentID}
	r.export.Id = &id
	r.export.FileSystemId = &fsID
	r.export.ExportSetId = &exportSetID
	r.export.Path = &path

	props := r.Properties()
	if got := props.Get(propFileSystemID); got != fsID {
		t.Errorf("Properties()[%q] = %q, want %q", propFileSystemID, got, fsID)
	}
	if got := props.Get(propExportSetID); got != exportSetID {
		t.Errorf("Properties()[%q] = %q, want %q", propExportSetID, got, exportSetID)
	}
	if got := props.Get("path"); got != path {
		t.Errorf("Properties()[%q] = %q, want %q", "path", got, path)
	}
}
