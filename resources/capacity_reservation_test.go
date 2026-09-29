package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubCapacityReservationClient implements capacityReservationClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubCapacityReservationClient struct {
	items   []core.ComputeCapacityReservationSummary
	deleted []string
	listErr error
}

// ListComputeCapacityReservations/DeleteComputeCapacityReservation signatures below must match the real
// core.ComputeCapacityReservationSummary client's methods exactly (request struct by value, not a
// pointer) to satisfy capacityReservationClient -- see resources_test's fakeIdentityClient for
// this project's established precedent.
func (s *stubCapacityReservationClient) ListComputeCapacityReservations(
	_ context.Context,
	_ core.ListComputeCapacityReservationsRequest,
) (core.ListComputeCapacityReservationsResponse, error) {
	if s.listErr != nil {
		return core.ListComputeCapacityReservationsResponse{}, s.listErr
	}
	return core.ListComputeCapacityReservationsResponse{Items: s.items}, nil
}

func (s *stubCapacityReservationClient) DeleteComputeCapacityReservation(
	_ context.Context,
	req core.DeleteComputeCapacityReservationRequest,
) (core.DeleteComputeCapacityReservationResponse, error) {
	s.deleted = append(s.deleted, *req.CapacityReservationId)
	return core.DeleteComputeCapacityReservationResponse{}, nil
}

// TestCapacityReservationLister_List proves capacityReservationList returns exactly one resource, wrapping
// the stub's single item, without ever constructing a real Compute client.
func TestCapacityReservationLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubCapacityReservationClient{
		items: []core.ComputeCapacityReservationSummary{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.ComputeCapacityReservationLifecycleStateActive},
		},
	}

	got, err := capacityReservationList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("capacityReservationList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("capacityReservationList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*CapacityReservation).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestCapacityReservation_Filter is table-driven over every
// core.ComputeCapacityReservationLifecycleStateEnum value -- ACTIVE/CREATING/UPDATING/MOVING must
// return nil (present), and DELETED/DELETING must both return non-nil (excluded) -- the hang-trap
// regression for a reservation mid-teardown or already gone but still briefly listed.
func TestCapacityReservation_Filter(t *testing.T) {
	tests := []struct {
		state   core.ComputeCapacityReservationLifecycleStateEnum
		present bool
	}{
		{core.ComputeCapacityReservationLifecycleStateActive, true},
		{core.ComputeCapacityReservationLifecycleStateCreating, true},
		{core.ComputeCapacityReservationLifecycleStateUpdating, true},
		{core.ComputeCapacityReservationLifecycleStateMoving, true},
		{core.ComputeCapacityReservationLifecycleStateDeleted, false},
		{core.ComputeCapacityReservationLifecycleStateDeleting, false},
	}

	for _, tc := range tests {
		r := &CapacityReservation{}
		r.capacityReservation.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestCapacityReservation_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestCapacityReservation_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubCapacityReservationClient{}
	r := &CapacityReservation{client: stub}
	r.capacityReservation.Id = &id
	r.capacityReservation.CompartmentId = &compartmentID
	r.capacityReservation.LifecycleState = core.ComputeCapacityReservationLifecycleStateActive
	r.capacityReservation.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
