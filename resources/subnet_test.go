package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubSubnetClient implements subnetClient against in-memory data -- zero network access.
type stubSubnetClient struct {
	items   []core.Subnet
	deleted []string
	listErr error
}

func (s *stubSubnetClient) ListSubnets(_ context.Context, _ core.ListSubnetsRequest) (core.ListSubnetsResponse, error) {
	if s.listErr != nil {
		return core.ListSubnetsResponse{}, s.listErr
	}
	return core.ListSubnetsResponse{Items: s.items}, nil
}

func (s *stubSubnetClient) DeleteSubnet(_ context.Context, req core.DeleteSubnetRequest) (core.DeleteSubnetResponse, error) {
	s.deleted = append(s.deleted, *req.SubnetId)
	return core.DeleteSubnetResponse{}, nil
}

// TestSubnetLister_List proves subnetList returns BOTH an AVAILABLE and a TERMINATED subnet --
// filtering is Filter()'s job, not List()'s.
func TestSubnetLister_List(t *testing.T) {
	availableID := "ocid1.subnet.oc1..available"
	terminatedID := "ocid1.subnet.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubSubnetClient{
		items: []core.Subnet{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.SubnetLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.SubnetLifecycleStateTerminated},
		},
	}

	got, err := subnetList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("subnetList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("subnetList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*Subnet).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*Subnet).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestSubnet_Filter is table-driven over every core.SubnetLifecycleStateEnum value --
// PROVISIONING/AVAILABLE/UPDATING must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded). This IS the regression test for the hang trap.
func TestSubnet_Filter(t *testing.T) {
	tests := []struct {
		state   core.SubnetLifecycleStateEnum
		present bool
	}{
		{core.SubnetLifecycleStateProvisioning, true},
		{core.SubnetLifecycleStateAvailable, true},
		{core.SubnetLifecycleStateUpdating, true},
		{core.SubnetLifecycleStateTerminating, false},
		{core.SubnetLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &Subnet{}
		r.subnet.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestSubnet_Remove proves the SDK delete call fires with the right parameter, against a client
// that never touches the network.
func TestSubnet_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubSubnetClient{}
	r := &Subnet{client: stub}
	r.subnet.Id = &id
	r.subnet.CompartmentId = &compartmentID
	r.subnet.LifecycleState = core.SubnetLifecycleStateAvailable
	r.subnet.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestSubnet_DependsOn_PrivateIp asserts Subnet's registered DependsOn is exactly
// []string{"PrivateIp"} -- Plan 04-07's type, per 04-RESEARCH.md Q5/Assumption A1.
func TestSubnet_DependsOn_PrivateIp(t *testing.T) {
	reg := registry.GetRegistration(SubnetResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", SubnetResourceType)
	}
	want := []string{"PrivateIp"}
	if len(reg.DependsOn) != 1 || reg.DependsOn[0] != want[0] {
		t.Fatalf("Subnet DependsOn = %v, want %v", reg.DependsOn, want)
	}
}
