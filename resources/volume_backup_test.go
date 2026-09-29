package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubVolumeBackupClient implements volumeBackupClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubVolumeBackupClient struct {
	items   []core.VolumeBackup
	deleted []string
	listErr error
}

// ListVolumeBackups/DeleteVolumeBackup signatures below must match the real
// core.BlockstorageClient methods exactly (request struct by value, not a pointer) to satisfy
// volumeBackupClient -- see resources_test's fakeIdentityClient for this project's established
// precedent.
func (s *stubVolumeBackupClient) ListVolumeBackups(
	_ context.Context,
	_ core.ListVolumeBackupsRequest,
) (core.ListVolumeBackupsResponse, error) {
	if s.listErr != nil {
		return core.ListVolumeBackupsResponse{}, s.listErr
	}
	return core.ListVolumeBackupsResponse{Items: s.items}, nil
}

func (s *stubVolumeBackupClient) DeleteVolumeBackup(
	_ context.Context,
	req core.DeleteVolumeBackupRequest,
) (core.DeleteVolumeBackupResponse, error) {
	s.deleted = append(s.deleted, *req.VolumeBackupId)
	return core.DeleteVolumeBackupResponse{}, nil
}

// TestVolumeBackupLister_List proves volumeBackupList returns exactly one resource, wrapping the
// stub's single item, without ever constructing a real Blockstorage client.
func TestVolumeBackupLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubVolumeBackupClient{
		items: []core.VolumeBackup{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.VolumeBackupLifecycleStateAvailable},
		},
	}

	got, err := volumeBackupList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("volumeBackupList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("volumeBackupList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*VolumeBackup).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestVolumeBackup_Filter is table-driven over every core.VolumeBackupLifecycleStateEnum value
// -- CREATING/AVAILABLE/REQUEST_RECEIVED must return nil (present), TERMINATING/TERMINATED must
// both return non-nil (excluded, the hang-trap regression case), and FAULTY must also return
// non-nil (excluded, and additionally reported -- see
// TestVolumeBackup_Filter_FaultyReportsLeftover below).
func TestVolumeBackup_Filter(t *testing.T) {
	tests := []struct {
		state   core.VolumeBackupLifecycleStateEnum
		present bool
	}{
		{core.VolumeBackupLifecycleStateCreating, true},
		{core.VolumeBackupLifecycleStateAvailable, true},
		{core.VolumeBackupLifecycleStateRequestReceived, true},
		{core.VolumeBackupLifecycleStateTerminating, false},
		{core.VolumeBackupLifecycleStateTerminated, false},
		{core.VolumeBackupLifecycleStateFaulty, false},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &VolumeBackup{}
		r.backup.Id = &id
		r.backup.CompartmentId = &compartmentID
		r.backup.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestVolumeBackup_Filter_FaultyReportsLeftover proves a FAULTY volume backup is excluded via
// ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its exclusion error --
// FAULTY surfaces as a labeled leftover, never a silent scan-time drop.
func TestVolumeBackup_Filter_FaultyReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &VolumeBackup{}
	r.backup.Id = &id
	r.backup.CompartmentId = &compartmentID
	r.backup.LifecycleState = core.VolumeBackupLifecycleStateFaulty

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAULTY state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != VolumeBackupResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, VolumeBackupResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestVolumeBackup_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestVolumeBackup_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubVolumeBackupClient{}
	r := &VolumeBackup{client: stub}
	r.backup.Id = &id
	r.backup.CompartmentId = &compartmentID
	r.backup.LifecycleState = core.VolumeBackupLifecycleStateAvailable
	r.backup.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
