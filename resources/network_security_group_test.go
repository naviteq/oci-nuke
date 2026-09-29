package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubNetworkSecurityGroupClient implements networkSecurityGroupClient against in-memory data --
// zero network access.
type stubNetworkSecurityGroupClient struct {
	items   []core.NetworkSecurityGroup
	deleted []string
	listErr error
}

func (s *stubNetworkSecurityGroupClient) ListNetworkSecurityGroups(
	_ context.Context,
	_ core.ListNetworkSecurityGroupsRequest,
) (core.ListNetworkSecurityGroupsResponse, error) {
	if s.listErr != nil {
		return core.ListNetworkSecurityGroupsResponse{}, s.listErr
	}
	return core.ListNetworkSecurityGroupsResponse{Items: s.items}, nil
}

func (s *stubNetworkSecurityGroupClient) DeleteNetworkSecurityGroup(
	_ context.Context,
	req core.DeleteNetworkSecurityGroupRequest,
) (core.DeleteNetworkSecurityGroupResponse, error) {
	s.deleted = append(s.deleted, *req.NetworkSecurityGroupId)
	return core.DeleteNetworkSecurityGroupResponse{}, nil
}

// TestNetworkSecurityGroupLister_List proves networkSecurityGroupList returns BOTH an AVAILABLE
// and a TERMINATED group -- filtering is Filter()'s job, not List()'s.
func TestNetworkSecurityGroupLister_List(t *testing.T) {
	availableID := "ocid1.networksecuritygroup.oc1..available"
	terminatedID := "ocid1.networksecuritygroup.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubNetworkSecurityGroupClient{
		items: []core.NetworkSecurityGroup{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.NetworkSecurityGroupLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.NetworkSecurityGroupLifecycleStateTerminated},
		},
	}

	got, err := networkSecurityGroupList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("networkSecurityGroupList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("networkSecurityGroupList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*NetworkSecurityGroup).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*NetworkSecurityGroup).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestNetworkSecurityGroup_Filter is table-driven over every
// core.NetworkSecurityGroupLifecycleStateEnum value -- PROVISIONING/AVAILABLE must return nil
// (present), TERMINATING/TERMINATED must both return non-nil (excluded). This IS the regression
// test for the hang trap.
func TestNetworkSecurityGroup_Filter(t *testing.T) {
	tests := []struct {
		state   core.NetworkSecurityGroupLifecycleStateEnum
		present bool
	}{
		{core.NetworkSecurityGroupLifecycleStateProvisioning, true},
		{core.NetworkSecurityGroupLifecycleStateAvailable, true},
		{core.NetworkSecurityGroupLifecycleStateTerminating, false},
		{core.NetworkSecurityGroupLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &NetworkSecurityGroup{}
		r.nsg.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestNetworkSecurityGroup_Remove proves the SDK delete call fires with the right parameter
// (NetworkSecurityGroupId), against a client that never touches the network.
func TestNetworkSecurityGroup_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubNetworkSecurityGroupClient{}
	r := &NetworkSecurityGroup{client: stub}
	r.nsg.Id = &id
	r.nsg.CompartmentId = &compartmentID
	r.nsg.LifecycleState = core.NetworkSecurityGroupLifecycleStateAvailable
	r.nsg.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
