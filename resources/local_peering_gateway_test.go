package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubLocalPeeringGatewayClient implements localPeeringGatewayClient against in-memory data --
// zero network access.
type stubLocalPeeringGatewayClient struct {
	items   []core.LocalPeeringGateway
	deleted []string
	listErr error
}

func (s *stubLocalPeeringGatewayClient) ListLocalPeeringGateways(
	_ context.Context,
	_ core.ListLocalPeeringGatewaysRequest,
) (core.ListLocalPeeringGatewaysResponse, error) {
	if s.listErr != nil {
		return core.ListLocalPeeringGatewaysResponse{}, s.listErr
	}
	return core.ListLocalPeeringGatewaysResponse{Items: s.items}, nil
}

func (s *stubLocalPeeringGatewayClient) DeleteLocalPeeringGateway(
	_ context.Context,
	req core.DeleteLocalPeeringGatewayRequest,
) (core.DeleteLocalPeeringGatewayResponse, error) {
	s.deleted = append(s.deleted, *req.LocalPeeringGatewayId)
	return core.DeleteLocalPeeringGatewayResponse{}, nil
}

// TestLocalPeeringGatewayLister_List proves localPeeringGatewayList returns BOTH an AVAILABLE
// and a TERMINATED gateway -- filtering is Filter()'s job, not List()'s.
func TestLocalPeeringGatewayLister_List(t *testing.T) {
	availableID := "ocid1.localpeeringgateway.oc1..available"
	terminatedID := "ocid1.localpeeringgateway.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubLocalPeeringGatewayClient{
		items: []core.LocalPeeringGateway{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.LocalPeeringGatewayLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.LocalPeeringGatewayLifecycleStateTerminated},
		},
	}

	got, err := localPeeringGatewayList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("localPeeringGatewayList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("localPeeringGatewayList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*LocalPeeringGateway).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*LocalPeeringGateway).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestLocalPeeringGateway_Filter is table-driven over every
// core.LocalPeeringGatewayLifecycleStateEnum value -- PROVISIONING/AVAILABLE must return nil
// (present), TERMINATING/TERMINATED must both return non-nil (excluded). This IS the regression
// test for the hang trap.
func TestLocalPeeringGateway_Filter(t *testing.T) {
	tests := []struct {
		state   core.LocalPeeringGatewayLifecycleStateEnum
		present bool
	}{
		{core.LocalPeeringGatewayLifecycleStateProvisioning, true},
		{core.LocalPeeringGatewayLifecycleStateAvailable, true},
		{core.LocalPeeringGatewayLifecycleStateTerminating, false},
		{core.LocalPeeringGatewayLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &LocalPeeringGateway{}
		r.lpg.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestLocalPeeringGateway_Remove proves the SDK delete call fires with the right parameter
// (LocalPeeringGatewayId), against a client that never touches the network.
func TestLocalPeeringGateway_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubLocalPeeringGatewayClient{}
	r := &LocalPeeringGateway{client: stub}
	r.lpg.Id = &id
	r.lpg.CompartmentId = &compartmentID
	r.lpg.LifecycleState = core.LocalPeeringGatewayLifecycleStateAvailable
	r.lpg.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
