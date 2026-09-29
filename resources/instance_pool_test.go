package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubInstancePoolClient implements instancePoolClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubInstancePoolClient struct {
	items   []core.InstancePoolSummary
	deleted []string
	listErr error
}

// ListInstancePools/TerminateInstancePool signatures below must match the real
// core.InstancePoolSummary client's methods exactly (request struct by value, not a
// pointer) to satisfy instancePoolClient -- see resources_test's fakeIdentityClient for
// this project's established precedent.
func (s *stubInstancePoolClient) ListInstancePools(
	_ context.Context,
	_ core.ListInstancePoolsRequest,
) (core.ListInstancePoolsResponse, error) {
	if s.listErr != nil {
		return core.ListInstancePoolsResponse{}, s.listErr
	}
	return core.ListInstancePoolsResponse{Items: s.items}, nil
}

func (s *stubInstancePoolClient) TerminateInstancePool(
	_ context.Context,
	req core.TerminateInstancePoolRequest,
) (core.TerminateInstancePoolResponse, error) {
	s.deleted = append(s.deleted, *req.InstancePoolId)
	return core.TerminateInstancePoolResponse{}, nil
}

// TestInstancePoolLister_List proves instancePoolList returns exactly one resource, wrapping
// the stub's single item, without ever constructing a real ComputeManagement client.
func TestInstancePoolLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubInstancePoolClient{
		items: []core.InstancePoolSummary{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: core.InstancePoolSummaryLifecycleStateProvisioning},
		},
	}

	got, err := instancePoolList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("instancePoolList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("instancePoolList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*InstancePool).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestInstancePool_Filter is table-driven over every core.InstancePoolSummaryLifecycleStateEnum
// value -- PROVISIONING/SCALING/STARTING/STOPPING/STOPPED/RUNNING must return nil (present), and
// TERMINATING/TERMINATED must both return non-nil (excluded) -- the hang-trap regression: a
// Filter() that forgets the TERMINATING branch never converges HandleWait for a still-listed,
// mid-teardown pool.
func TestInstancePool_Filter(t *testing.T) {
	tests := []struct {
		state   core.InstancePoolSummaryLifecycleStateEnum
		present bool
	}{
		{core.InstancePoolSummaryLifecycleStateProvisioning, true},
		{core.InstancePoolSummaryLifecycleStateScaling, true},
		{core.InstancePoolSummaryLifecycleStateStarting, true},
		{core.InstancePoolSummaryLifecycleStateStopping, true},
		{core.InstancePoolSummaryLifecycleStateStopped, true},
		{core.InstancePoolSummaryLifecycleStateRunning, true},
		{core.InstancePoolSummaryLifecycleStateTerminating, false},
		{core.InstancePoolSummaryLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &InstancePool{}
		r.instancePool.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestInstancePool_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestInstancePool_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubInstancePoolClient{}
	r := &InstancePool{client: stub}
	r.instancePool.Id = &id
	r.instancePool.CompartmentId = &compartmentID
	r.instancePool.LifecycleState = core.InstancePoolSummaryLifecycleStateProvisioning
	r.instancePool.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
