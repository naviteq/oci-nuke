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

// stubBlockVolumeClient implements blockVolumeClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubBlockVolumeClient struct {
	items   []core.Volume
	deleted []string
	listErr error
}

// ListVolumes/DeleteVolume signatures below must match the real core.BlockstorageClient methods
// exactly (request struct by value, not a pointer) to satisfy blockVolumeClient -- see
// resources_test's fakeIdentityClient for this project's established precedent.
func (s *stubBlockVolumeClient) ListVolumes(
	_ context.Context,
	_ core.ListVolumesRequest,
) (core.ListVolumesResponse, error) {
	if s.listErr != nil {
		return core.ListVolumesResponse{}, s.listErr
	}
	return core.ListVolumesResponse{Items: s.items}, nil
}

func (s *stubBlockVolumeClient) DeleteVolume(
	_ context.Context,
	req core.DeleteVolumeRequest,
) (core.DeleteVolumeResponse, error) {
	s.deleted = append(s.deleted, *req.VolumeId)
	return core.DeleteVolumeResponse{}, nil
}

// TestBlockVolumeLister_List proves blockVolumeList returns exactly one resource, wrapping the
// stub's single item, without ever constructing a real Blockstorage client.
func TestBlockVolumeLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubBlockVolumeClient{
		items: []core.Volume{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.VolumeLifecycleStateAvailable},
		},
	}

	got, err := blockVolumeList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("blockVolumeList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("blockVolumeList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*BlockVolume).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestBlockVolume_Filter is table-driven over every core.VolumeLifecycleStateEnum value --
// PROVISIONING/RESTORING/AVAILABLE must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded, the hang-trap regression case), and FAULTY must also return non-nil
// (excluded, and additionally reported -- see TestBlockVolume_Filter_FaultyReportsLeftover
// below).
func TestBlockVolume_Filter(t *testing.T) {
	tests := []struct {
		state   core.VolumeLifecycleStateEnum
		present bool
	}{
		{core.VolumeLifecycleStateProvisioning, true},
		{core.VolumeLifecycleStateRestoring, true},
		{core.VolumeLifecycleStateAvailable, true},
		{core.VolumeLifecycleStateTerminating, false},
		{core.VolumeLifecycleStateTerminated, false},
		{core.VolumeLifecycleStateFaulty, false},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &BlockVolume{}
		r.volume.Id = &id
		r.volume.CompartmentId = &compartmentID
		r.volume.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestBlockVolume_Filter_FaultyReportsLeftover proves a FAULTY block volume is excluded via
// ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its exclusion error --
// FAULTY surfaces as a labeled leftover, never a silent scan-time drop.
func TestBlockVolume_Filter_FaultyReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &BlockVolume{}
	r.volume.Id = &id
	r.volume.CompartmentId = &compartmentID
	r.volume.LifecycleState = core.VolumeLifecycleStateFaulty

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAULTY state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != BlockVolumeResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, BlockVolumeResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestBlockVolume_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestBlockVolume_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubBlockVolumeClient{}
	r := &BlockVolume{client: stub}
	r.volume.Id = &id
	r.volume.CompartmentId = &compartmentID
	r.volume.LifecycleState = core.VolumeLifecycleStateAvailable
	r.volume.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
