package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubVlanClient implements vlanClient against in-memory data -- zero network access.
type stubVlanClient struct {
	items   []core.Vlan
	deleted []string
	listErr error
}

func (s *stubVlanClient) ListVlans(_ context.Context, _ core.ListVlansRequest) (core.ListVlansResponse, error) {
	if s.listErr != nil {
		return core.ListVlansResponse{}, s.listErr
	}
	return core.ListVlansResponse{Items: s.items}, nil
}

func (s *stubVlanClient) DeleteVlan(_ context.Context, req core.DeleteVlanRequest) (core.DeleteVlanResponse, error) {
	s.deleted = append(s.deleted, *req.VlanId)
	return core.DeleteVlanResponse{}, nil
}

// TestVlanLister_List proves vlanList returns BOTH an AVAILABLE and a TERMINATED vlan --
// filtering is Filter()'s job, not List()'s.
func TestVlanLister_List(t *testing.T) {
	availableID := "ocid1.vlan.oc1..available"
	terminatedID := "ocid1.vlan.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubVlanClient{
		items: []core.Vlan{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.VlanLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.VlanLifecycleStateTerminated},
		},
	}

	got, err := vlanList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("vlanList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("vlanList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*Vlan).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*Vlan).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestVlan_Filter is table-driven over every core.VlanLifecycleStateEnum value --
// PROVISIONING/AVAILABLE/UPDATING must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded). This IS the regression test for the hang trap.
func TestVlan_Filter(t *testing.T) {
	tests := []struct {
		state   core.VlanLifecycleStateEnum
		present bool
	}{
		{core.VlanLifecycleStateProvisioning, true},
		{core.VlanLifecycleStateAvailable, true},
		{core.VlanLifecycleStateUpdating, true},
		{core.VlanLifecycleStateTerminating, false},
		{core.VlanLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &Vlan{}
		r.vlan.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestVlan_Remove proves the SDK delete call fires with the right parameter (VlanId), against a
// client that never touches the network.
func TestVlan_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubVlanClient{}
	r := &Vlan{client: stub}
	r.vlan.Id = &id
	r.vlan.CompartmentId = &compartmentID
	r.vlan.LifecycleState = core.VlanLifecycleStateAvailable
	r.vlan.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestVlan_Remove_NilTimeCreated proves Remove() does not panic when TimeCreated is nil --
// core.Vlan.TimeCreated is `mandatory:"false"` on the pinned SDK.
func TestVlan_Remove_NilTimeCreated(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubVlanClient{}
	r := &Vlan{client: stub}
	r.vlan.Id = &id
	r.vlan.CompartmentId = &compartmentID
	r.vlan.LifecycleState = core.VlanLifecycleStateAvailable
	r.vlan.TimeCreated = nil

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
