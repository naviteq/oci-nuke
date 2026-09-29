package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// testInstanceOCID/testVolumeOCID are shared, package-level test fixture OCIDs for this file's
// four+ construction sites (goconst, min-occurrences 3).
const (
	testInstanceOCID = "ocid1.instance.oc1..id"
	testVolumeOCID   = "ocid1.volume.oc1..id"
)

// stubVolumeAttachmentClient implements volumeAttachmentClient against in-memory data -- zero
// network access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubVolumeAttachmentClient struct {
	items   []core.VolumeAttachment
	deleted []string
	listErr error
}

// ListVolumeAttachments/DetachVolume signatures below must match the real core.ComputeClient
// methods exactly (request struct by value, not a pointer) to satisfy volumeAttachmentClient --
// see resources_test's fakeIdentityClient for this project's established precedent.
func (s *stubVolumeAttachmentClient) ListVolumeAttachments(
	_ context.Context,
	_ core.ListVolumeAttachmentsRequest,
) (core.ListVolumeAttachmentsResponse, error) {
	if s.listErr != nil {
		return core.ListVolumeAttachmentsResponse{}, s.listErr
	}
	return core.ListVolumeAttachmentsResponse{Items: s.items}, nil
}

func (s *stubVolumeAttachmentClient) DetachVolume(
	_ context.Context,
	req core.DetachVolumeRequest,
) (core.DetachVolumeResponse, error) {
	s.deleted = append(s.deleted, *req.VolumeAttachmentId)
	return core.DetachVolumeResponse{}, nil
}

// newTestVolumeAttachment builds a core.ParavirtualizedVolumeAttachment as the fixture concrete
// type implementing the core.VolumeAttachment interface (verified this session: its Get*()
// methods satisfy every interface method) -- simpler to construct in a test than a hand-rolled
// unexported struct implementing every getter. Every field but LifecycleState is fixed to this
// file's shared test fixture OCIDs -- every call site in this file shares the one fixture
// identity, so only state varies across tests.
func newTestVolumeAttachment(state core.VolumeAttachmentLifecycleStateEnum) core.VolumeAttachment {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	instanceID := testInstanceOCID
	volumeID := testVolumeOCID
	timeCreated := common.SDKTime{Time: time.Now()}
	return core.ParavirtualizedVolumeAttachment{
		Id:                 &id,
		CompartmentId:      &compartmentID,
		InstanceId:         &instanceID,
		VolumeId:           &volumeID,
		LifecycleState:     state,
		TimeCreated:        &timeCreated,
		AvailabilityDomain: common.String("Uocm:PHX-AD-1"),
	}
}

// TestVolumeAttachmentLister_List proves volumeAttachmentList returns exactly one resource,
// wrapping the stub's single interface-typed item, without ever constructing a real Compute
// client.
func TestVolumeAttachmentLister_List(t *testing.T) {
	stub := &stubVolumeAttachmentClient{
		items: []core.VolumeAttachment{
			newTestVolumeAttachment(core.VolumeAttachmentLifecycleStateAttached),
		},
	}

	got, err := volumeAttachmentList(context.Background(), stub, testCompartmentOCID)
	if err != nil {
		t.Fatalf("volumeAttachmentList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("volumeAttachmentList() returned %d resources, want 1", len(got))
	}
	va, ok := got[0].(*VolumeAttachment)
	if !ok {
		t.Fatalf("volumeAttachmentList()[0] = %T, want *VolumeAttachment", got[0])
	}
	if props := va.Properties(); props.Get("id") != testResourceOCID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), testResourceOCID)
	}
	if props := va.Properties(); props.Get(propInstanceID) != testInstanceOCID {
		t.Fatalf("Properties()[%q] = %q, want %q", propInstanceID, props.Get(propInstanceID), testInstanceOCID)
	}
	if props := va.Properties(); props.Get(propVolumeID) != testVolumeOCID {
		t.Fatalf("Properties()[%q] = %q, want %q", propVolumeID, props.Get(propVolumeID), testVolumeOCID)
	}
}

// TestVolumeAttachment_Filter is table-driven over every core.VolumeAttachmentLifecycleStateEnum
// value (ATTACHING, ATTACHED, DETACHING, DETACHED -- the interface's complete real enum, verified
// against core/volume_attachment.go this session) -- ATTACHING/ATTACHED must return nil
// (present), DETACHING/DETACHED must both return non-nil (excluded, the hang-trap regression
// case). Every access goes through GetLifecycleState(), never a direct struct field.
func TestVolumeAttachment_Filter(t *testing.T) {
	tests := []struct {
		state   core.VolumeAttachmentLifecycleStateEnum
		present bool
	}{
		{core.VolumeAttachmentLifecycleStateAttaching, true},
		{core.VolumeAttachmentLifecycleStateAttached, true},
		{core.VolumeAttachmentLifecycleStateDetaching, false},
		{core.VolumeAttachmentLifecycleStateDetached, false},
	}

	for _, tc := range tests {
		r := &VolumeAttachment{attachment: newTestVolumeAttachment(tc.state)}
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestVolumeAttachment_Remove proves the SDK detach call fires with the right parameter --
// derived via x.GetId(), never x.Id, since core.VolumeAttachment is an interface with no exported
// struct fields to access directly -- against a client that never touches the network.
func TestVolumeAttachment_Remove(t *testing.T) {
	stub := &stubVolumeAttachmentClient{}
	r := &VolumeAttachment{
		client:     stub,
		attachment: newTestVolumeAttachment(core.VolumeAttachmentLifecycleStateAttached),
	}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != testResourceOCID {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, testResourceOCID)
	}
}

// TestVolumeAttachment_GetCompartmentID_UniqueKey proves GetCompartmentID()/UniqueKey() both
// route through Get*() accessor calls, never direct field access on the interface value.
func TestVolumeAttachment_GetCompartmentID_UniqueKey(t *testing.T) {
	r := &VolumeAttachment{
		attachment: newTestVolumeAttachment(core.VolumeAttachmentLifecycleStateAttached),
	}

	if got := r.GetCompartmentID(); got != testCompartmentOCID {
		t.Errorf("GetCompartmentID() = %q, want %q", got, testCompartmentOCID)
	}
	if got := r.UniqueKey(); got != testResourceOCID {
		t.Errorf("UniqueKey() = %q, want %q", got, testResourceOCID)
	}
}
