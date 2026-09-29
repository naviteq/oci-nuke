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

// stubBootVolumeClient implements bootVolumeClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubBootVolumeClient struct {
	items   []core.BootVolume
	deleted []string
	listErr error
}

// ListBootVolumes/DeleteBootVolume signatures below must match the real core.BlockstorageClient
// methods exactly (request struct by value, not a pointer) to satisfy bootVolumeClient -- see
// resources_test's fakeIdentityClient for this project's established precedent.
func (s *stubBootVolumeClient) ListBootVolumes(
	_ context.Context,
	_ core.ListBootVolumesRequest,
) (core.ListBootVolumesResponse, error) {
	if s.listErr != nil {
		return core.ListBootVolumesResponse{}, s.listErr
	}
	return core.ListBootVolumesResponse{Items: s.items}, nil
}

func (s *stubBootVolumeClient) DeleteBootVolume(
	_ context.Context,
	req core.DeleteBootVolumeRequest,
) (core.DeleteBootVolumeResponse, error) {
	s.deleted = append(s.deleted, *req.BootVolumeId)
	return core.DeleteBootVolumeResponse{}, nil
}

// TestBootVolumeLister_List proves bootVolumeList returns exactly one resource, wrapping the
// stub's single item, without ever constructing a real Blockstorage client.
func TestBootVolumeLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubBootVolumeClient{
		items: []core.BootVolume{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.BootVolumeLifecycleStateAvailable},
		},
	}

	got, err := bootVolumeList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("bootVolumeList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("bootVolumeList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*BootVolume).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestBootVolume_Filter is table-driven over every core.BootVolumeLifecycleStateEnum value --
// PROVISIONING/RESTORING/AVAILABLE must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded, the hang-trap regression case), and FAULTY must also return non-nil
// (excluded, and additionally reported -- see TestBootVolume_Filter_FaultyReportsLeftover below).
func TestBootVolume_Filter(t *testing.T) {
	tests := []struct {
		state   core.BootVolumeLifecycleStateEnum
		present bool
	}{
		{core.BootVolumeLifecycleStateProvisioning, true},
		{core.BootVolumeLifecycleStateRestoring, true},
		{core.BootVolumeLifecycleStateAvailable, true},
		{core.BootVolumeLifecycleStateTerminating, false},
		{core.BootVolumeLifecycleStateTerminated, false},
		{core.BootVolumeLifecycleStateFaulty, false},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &BootVolume{}
		r.bootVolume.Id = &id
		r.bootVolume.CompartmentId = &compartmentID
		r.bootVolume.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestBootVolume_Filter_FaultyReportsLeftover proves a FAULTY boot volume is excluded via
// ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its exclusion error --
// FAULTY surfaces as a labeled leftover, never a silent scan-time drop.
func TestBootVolume_Filter_FaultyReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &BootVolume{}
	r.bootVolume.Id = &id
	r.bootVolume.CompartmentId = &compartmentID
	r.bootVolume.LifecycleState = core.BootVolumeLifecycleStateFaulty

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAULTY state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != BootVolumeResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, BootVolumeResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestBootVolume_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestBootVolume_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubBootVolumeClient{}
	r := &BootVolume{client: stub}
	r.bootVolume.Id = &id
	r.bootVolume.CompartmentId = &compartmentID
	r.bootVolume.LifecycleState = core.BootVolumeLifecycleStateAvailable
	r.bootVolume.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
