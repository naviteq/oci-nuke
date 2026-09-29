package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubVcnClient implements vcnClient against in-memory data -- zero network access.
type stubVcnClient struct {
	items   []core.Vcn
	deleted []string
	listErr error
}

func (s *stubVcnClient) ListVcns(_ context.Context, _ core.ListVcnsRequest) (core.ListVcnsResponse, error) {
	if s.listErr != nil {
		return core.ListVcnsResponse{}, s.listErr
	}
	return core.ListVcnsResponse{Items: s.items}, nil
}

func (s *stubVcnClient) DeleteVcn(_ context.Context, req core.DeleteVcnRequest) (core.DeleteVcnResponse, error) {
	s.deleted = append(s.deleted, *req.VcnId)
	return core.DeleteVcnResponse{}, nil
}

// TestVcnLister_List proves vcnList returns BOTH an AVAILABLE and a TERMINATED vcn -- filtering
// is Filter()'s job, not List()'s.
func TestVcnLister_List(t *testing.T) {
	availableID := "ocid1.vcn.oc1..available"
	terminatedID := "ocid1.vcn.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubVcnClient{
		items: []core.Vcn{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.VcnLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.VcnLifecycleStateTerminated},
		},
	}

	got, err := vcnList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("vcnList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("vcnList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*Vcn).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*Vcn).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestVcn_Filter is table-driven over every core.VcnLifecycleStateEnum value --
// PROVISIONING/AVAILABLE/UPDATING must return nil (present), TERMINATING/TERMINATED must both
// return non-nil (excluded). This IS the regression test for the hang trap.
func TestVcn_Filter(t *testing.T) {
	tests := []struct {
		state   core.VcnLifecycleStateEnum
		present bool
	}{
		{core.VcnLifecycleStateProvisioning, true},
		{core.VcnLifecycleStateAvailable, true},
		{core.VcnLifecycleStateUpdating, true},
		{core.VcnLifecycleStateTerminating, false},
		{core.VcnLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &Vcn{}
		r.vcn.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestVcn_Remove proves the SDK delete call fires with the right parameter, against a client
// that never touches the network.
func TestVcn_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubVcnClient{}
	r := &Vcn{client: stub}
	r.vcn.Id = &id
	r.vcn.CompartmentId = &compartmentID
	r.vcn.LifecycleState = core.VcnLifecycleStateAvailable
	r.vcn.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestVcn_DependsOn_AggregatesFullPeriphery asserts Vcn's registered DependsOn is the exact
// ten-element list spanning both this plan (Subnet, RouteTable, SecurityList, InternetGateway,
// NatGateway, ServiceGateway) and Plan 04-07's remaining four (NetworkSecurityGroup, DhcpOptions,
// LocalPeeringGateway, DrgAttachment) -- proving the aggregation is complete even before Plan
// 04-07 lands, since both plans were planned together from the same source list
// (04-RESEARCH.md Q5).
func TestVcn_DependsOn_AggregatesFullPeriphery(t *testing.T) {
	reg := registry.GetRegistration(VcnResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", VcnResourceType)
	}

	want := []string{
		"Subnet", "RouteTable", "SecurityList", "InternetGateway", "NatGateway", "ServiceGateway",
		"NetworkSecurityGroup", "DhcpOptions", "LocalPeeringGateway", "DrgAttachment",
	}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("Vcn DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("Vcn DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}
