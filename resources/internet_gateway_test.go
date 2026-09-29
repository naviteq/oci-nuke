package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubInternetGatewayClient implements internetGatewayClient against in-memory data -- zero
// network access.
type stubInternetGatewayClient struct {
	items   []core.InternetGateway
	deleted []string
	listErr error
}

func (s *stubInternetGatewayClient) ListInternetGateways(
	_ context.Context,
	_ core.ListInternetGatewaysRequest,
) (core.ListInternetGatewaysResponse, error) {
	if s.listErr != nil {
		return core.ListInternetGatewaysResponse{}, s.listErr
	}
	return core.ListInternetGatewaysResponse{Items: s.items}, nil
}

func (s *stubInternetGatewayClient) DeleteInternetGateway(
	_ context.Context,
	req core.DeleteInternetGatewayRequest,
) (core.DeleteInternetGatewayResponse, error) {
	s.deleted = append(s.deleted, *req.IgId)
	return core.DeleteInternetGatewayResponse{}, nil
}

// TestInternetGatewayLister_List proves internetGatewayList returns BOTH an AVAILABLE and a
// TERMINATED gateway -- filtering is Filter()'s job, not List()'s.
func TestInternetGatewayLister_List(t *testing.T) {
	availableID := "ocid1.internetgateway.oc1..available"
	terminatedID := "ocid1.internetgateway.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubInternetGatewayClient{
		items: []core.InternetGateway{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.InternetGatewayLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.InternetGatewayLifecycleStateTerminated},
		},
	}

	got, err := internetGatewayList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("internetGatewayList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("internetGatewayList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*InternetGateway).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*InternetGateway).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestInternetGateway_Filter is table-driven over every core.InternetGatewayLifecycleStateEnum
// value -- PROVISIONING/AVAILABLE must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded).
func TestInternetGateway_Filter(t *testing.T) {
	tests := []struct {
		state   core.InternetGatewayLifecycleStateEnum
		present bool
	}{
		{core.InternetGatewayLifecycleStateProvisioning, true},
		{core.InternetGatewayLifecycleStateAvailable, true},
		{core.InternetGatewayLifecycleStateTerminating, false},
		{core.InternetGatewayLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &InternetGateway{}
		r.ig.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestInternetGateway_Remove proves the SDK delete call fires with the exact request field IgId
// (NOT InternetGatewayId) -- the stub asserts the exact field, not merely that some delete call
// fired, catching a copy-paste of the "regular" naming pattern from a sibling type.
func TestInternetGateway_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubInternetGatewayClient{}
	r := &InternetGateway{client: stub}
	r.ig.Id = &id
	r.ig.CompartmentId = &compartmentID
	r.ig.LifecycleState = core.InternetGatewayLifecycleStateAvailable
	r.ig.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s] (IgId field must carry the id)", stub.deleted, id)
	}
}
