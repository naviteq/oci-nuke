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

// stubVolumeGroupClient implements volumeGroupClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubVolumeGroupClient struct {
	items   []core.VolumeGroup
	deleted []string
	listErr error
}

// ListVolumeGroups/DeleteVolumeGroup signatures below must match the real
// core.BlockstorageClient methods exactly (request struct by value, not a pointer) to satisfy
// volumeGroupClient -- see resources_test's fakeIdentityClient for this project's established
// precedent.
func (s *stubVolumeGroupClient) ListVolumeGroups(
	_ context.Context,
	_ core.ListVolumeGroupsRequest,
) (core.ListVolumeGroupsResponse, error) {
	if s.listErr != nil {
		return core.ListVolumeGroupsResponse{}, s.listErr
	}
	return core.ListVolumeGroupsResponse{Items: s.items}, nil
}

func (s *stubVolumeGroupClient) DeleteVolumeGroup(
	_ context.Context,
	req core.DeleteVolumeGroupRequest,
) (core.DeleteVolumeGroupResponse, error) {
	s.deleted = append(s.deleted, *req.VolumeGroupId)
	return core.DeleteVolumeGroupResponse{}, nil
}

// TestVolumeGroupLister_List proves volumeGroupList returns exactly one resource, wrapping the
// stub's single item, without ever constructing a real Blockstorage client.
func TestVolumeGroupLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubVolumeGroupClient{
		items: []core.VolumeGroup{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.VolumeGroupLifecycleStateAvailable},
		},
	}

	got, err := volumeGroupList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("volumeGroupList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("volumeGroupList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*VolumeGroup).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestVolumeGroup_Filter is table-driven over every core.VolumeGroupLifecycleStateEnum value --
// PROVISIONING/AVAILABLE/UPDATE_PENDING must return nil (present), TERMINATING/TERMINATED must
// both return non-nil (excluded, the hang-trap regression case), and FAULTY must also return
// non-nil (excluded, and additionally reported -- see
// TestVolumeGroup_Filter_FaultyReportsLeftover below).
func TestVolumeGroup_Filter(t *testing.T) {
	tests := []struct {
		state   core.VolumeGroupLifecycleStateEnum
		present bool
	}{
		{core.VolumeGroupLifecycleStateProvisioning, true},
		{core.VolumeGroupLifecycleStateAvailable, true},
		{core.VolumeGroupLifecycleStateUpdatePending, true},
		{core.VolumeGroupLifecycleStateTerminating, false},
		{core.VolumeGroupLifecycleStateTerminated, false},
		{core.VolumeGroupLifecycleStateFaulty, false},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &VolumeGroup{}
		r.group.Id = &id
		r.group.CompartmentId = &compartmentID
		r.group.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestVolumeGroup_Filter_FaultyReportsLeftover proves a FAULTY volume group is excluded via
// ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its exclusion error --
// FAULTY surfaces as a labeled leftover, never a silent scan-time drop.
func TestVolumeGroup_Filter_FaultyReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &VolumeGroup{}
	r.group.Id = &id
	r.group.CompartmentId = &compartmentID
	r.group.LifecycleState = core.VolumeGroupLifecycleStateFaulty

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAULTY state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != VolumeGroupResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, VolumeGroupResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestVolumeGroup_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestVolumeGroup_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubVolumeGroupClient{}
	r := &VolumeGroup{client: stub}
	r.group.Id = &id
	r.group.CompartmentId = &compartmentID
	r.group.LifecycleState = core.VolumeGroupLifecycleStateAvailable
	r.group.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
