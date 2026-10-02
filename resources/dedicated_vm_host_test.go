package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubDedicatedVmHostClient implements dedicatedVmHostClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubDedicatedVmHostClient struct {
	items   []core.DedicatedVmHostSummary
	deleted []string
	listErr error
	getResp core.GetDedicatedVmHostResponse
	getErr  error
}

// ListDedicatedVmHosts/DeleteDedicatedVmHost signatures below must match the real
// core.DedicatedVmHostSummary client's methods exactly (request struct by value, not a
// pointer) to satisfy dedicatedVmHostClient -- see resources_test's fakeIdentityClient for
// this project's established precedent.
func (s *stubDedicatedVmHostClient) ListDedicatedVmHosts(
	_ context.Context,
	_ core.ListDedicatedVmHostsRequest,
) (core.ListDedicatedVmHostsResponse, error) {
	if s.listErr != nil {
		return core.ListDedicatedVmHostsResponse{}, s.listErr
	}
	return core.ListDedicatedVmHostsResponse{Items: s.items}, nil
}

func (s *stubDedicatedVmHostClient) DeleteDedicatedVmHost(
	_ context.Context,
	req core.DeleteDedicatedVmHostRequest,
) (core.DeleteDedicatedVmHostResponse, error) {
	s.deleted = append(s.deleted, *req.DedicatedVmHostId)
	return core.DeleteDedicatedVmHostResponse{}, nil
}

func (s *stubDedicatedVmHostClient) GetDedicatedVmHost(
	_ context.Context,
	_ core.GetDedicatedVmHostRequest,
) (core.GetDedicatedVmHostResponse, error) {
	return s.getResp, s.getErr
}

// TestDedicatedVmHostLister_List proves dedicatedVmHostList returns exactly one resource, wrapping
// the stub's single item, without ever constructing a real Compute client.
func TestDedicatedVmHostLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubDedicatedVmHostClient{
		items: []core.DedicatedVmHostSummary{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.DedicatedVmHostSummaryLifecycleStateCreating},
		},
	}

	got, err := dedicatedVmHostList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("dedicatedVmHostList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("dedicatedVmHostList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*DedicatedVmHost).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestDedicatedVmHost_Filter is table-driven over every core.DedicatedVmHostSummaryLifecycleStateEnum
// value -- CREATING/ACTIVE/UPDATING/FAILED must return nil (present), DELETING/DELETED must return
// non-nil (excluded). FAILED is present so a FAILED host still gets deleted.
func TestDedicatedVmHost_Filter(t *testing.T) {
	tests := []struct {
		state   core.DedicatedVmHostSummaryLifecycleStateEnum
		present bool
	}{
		{core.DedicatedVmHostSummaryLifecycleStateCreating, true},
		{core.DedicatedVmHostSummaryLifecycleStateActive, true},
		{core.DedicatedVmHostSummaryLifecycleStateUpdating, true},
		{core.DedicatedVmHostSummaryLifecycleStateDeleting, false},
		{core.DedicatedVmHostSummaryLifecycleStateDeleted, false},
		{core.DedicatedVmHostSummaryLifecycleStateFailed, true},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &DedicatedVmHost{}
		r.dedicatedVmHost.Id = &id
		r.dedicatedVmHost.CompartmentId = &compartmentID
		r.dedicatedVmHost.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil", tc.state)
		}
	}
}

// TestDedicatedVmHost_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestDedicatedVmHost_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubDedicatedVmHostClient{}
	r := &DedicatedVmHost{client: stub}
	r.dedicatedVmHost.Id = &id
	r.dedicatedVmHost.CompartmentId = &compartmentID
	r.dedicatedVmHost.LifecycleState = core.DedicatedVmHostSummaryLifecycleStateCreating
	r.dedicatedVmHost.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
