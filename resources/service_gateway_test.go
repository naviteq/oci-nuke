package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubServiceGatewayClient implements serviceGatewayClient against in-memory data -- zero
// network access.
type stubServiceGatewayClient struct {
	items   []core.ServiceGateway
	deleted []string
	listErr error
}

func (s *stubServiceGatewayClient) ListServiceGateways(
	_ context.Context,
	_ core.ListServiceGatewaysRequest,
) (core.ListServiceGatewaysResponse, error) {
	if s.listErr != nil {
		return core.ListServiceGatewaysResponse{}, s.listErr
	}
	return core.ListServiceGatewaysResponse{Items: s.items}, nil
}

func (s *stubServiceGatewayClient) DeleteServiceGateway(
	_ context.Context,
	req core.DeleteServiceGatewayRequest,
) (core.DeleteServiceGatewayResponse, error) {
	s.deleted = append(s.deleted, *req.ServiceGatewayId)
	return core.DeleteServiceGatewayResponse{}, nil
}

// TestServiceGatewayLister_List proves serviceGatewayList returns BOTH an AVAILABLE and a
// TERMINATED gateway -- filtering is Filter()'s job, not List()'s.
func TestServiceGatewayLister_List(t *testing.T) {
	availableID := "ocid1.servicegateway.oc1..available"
	terminatedID := "ocid1.servicegateway.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubServiceGatewayClient{
		items: []core.ServiceGateway{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.ServiceGatewayLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.ServiceGatewayLifecycleStateTerminated},
		},
	}

	got, err := serviceGatewayList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("serviceGatewayList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("serviceGatewayList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*ServiceGateway).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*ServiceGateway).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestServiceGateway_Filter is table-driven over every core.ServiceGatewayLifecycleStateEnum
// value -- PROVISIONING/AVAILABLE must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded).
func TestServiceGateway_Filter(t *testing.T) {
	tests := []struct {
		state   core.ServiceGatewayLifecycleStateEnum
		present bool
	}{
		{core.ServiceGatewayLifecycleStateProvisioning, true},
		{core.ServiceGatewayLifecycleStateAvailable, true},
		{core.ServiceGatewayLifecycleStateTerminating, false},
		{core.ServiceGatewayLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &ServiceGateway{}
		r.sg.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestServiceGateway_Remove proves the SDK delete call fires with the right parameter
// (ServiceGatewayId), against a client that never touches the network.
func TestServiceGateway_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubServiceGatewayClient{}
	r := &ServiceGateway{client: stub}
	r.sg.Id = &id
	r.sg.CompartmentId = &compartmentID
	r.sg.LifecycleState = core.ServiceGatewayLifecycleStateAvailable
	r.sg.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
