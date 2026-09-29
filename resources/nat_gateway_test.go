package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubNatGatewayClient implements natGatewayClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubNatGatewayClient struct {
	items   []core.NatGateway
	deleted []string
	listErr error
}

func (s *stubNatGatewayClient) ListNatGateways(
	_ context.Context,
	_ core.ListNatGatewaysRequest,
) (core.ListNatGatewaysResponse, error) {
	if s.listErr != nil {
		return core.ListNatGatewaysResponse{}, s.listErr
	}
	return core.ListNatGatewaysResponse{Items: s.items}, nil
}

func (s *stubNatGatewayClient) DeleteNatGateway(
	_ context.Context,
	req core.DeleteNatGatewayRequest,
) (core.DeleteNatGatewayResponse, error) {
	s.deleted = append(s.deleted, *req.NatGatewayId)
	return core.DeleteNatGatewayResponse{}, nil
}

// TestNatGatewayLister_List proves natGatewayList returns BOTH an AVAILABLE and a TERMINATED
// gateway -- filtering is Filter()'s job, not List()'s -- without ever constructing a real
// VirtualNetwork client.
func TestNatGatewayLister_List(t *testing.T) {
	availableID := "ocid1.natgateway.oc1..available"
	terminatedID := "ocid1.natgateway.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubNatGatewayClient{
		items: []core.NatGateway{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.NatGatewayLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.NatGatewayLifecycleStateTerminated},
		},
	}

	got, err := natGatewayList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("natGatewayList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("natGatewayList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*NatGateway).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*NatGateway).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestNatGateway_Filter is table-driven over every core.NatGatewayLifecycleStateEnum value --
// PROVISIONING/AVAILABLE must return nil (present), and TERMINATING/TERMINATED must both return
// non-nil (excluded). This IS the regression test for the hang trap.
func TestNatGateway_Filter(t *testing.T) {
	tests := []struct {
		state   core.NatGatewayLifecycleStateEnum
		present bool
	}{
		{core.NatGatewayLifecycleStateProvisioning, true},
		{core.NatGatewayLifecycleStateAvailable, true},
		{core.NatGatewayLifecycleStateTerminating, false},
		{core.NatGatewayLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &NatGateway{}
		r.natGateway.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestNatGateway_Remove proves the SDK delete call fires with the right parameter (NatGatewayId),
// against a client that never touches the network.
func TestNatGateway_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubNatGatewayClient{}
	r := &NatGateway{client: stub}
	r.natGateway.Id = &id
	r.natGateway.CompartmentId = &compartmentID
	r.natGateway.LifecycleState = core.NatGatewayLifecycleStateAvailable
	r.natGateway.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
