package resources

import (
	"context"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/containerengine"
)

// stubNodePoolClient implements nodePoolClient against in-memory data -- zero network access,
// mirroring stubInstanceClient's established precedent (resources/instance_test.go).
type stubNodePoolClient struct {
	items   []containerengine.NodePoolSummary
	deleted []string
	listErr error
}

func (s *stubNodePoolClient) ListNodePools(
	_ context.Context,
	_ containerengine.ListNodePoolsRequest,
) (containerengine.ListNodePoolsResponse, error) {
	if s.listErr != nil {
		return containerengine.ListNodePoolsResponse{}, s.listErr
	}
	return containerengine.ListNodePoolsResponse{Items: s.items}, nil
}

func (s *stubNodePoolClient) DeleteNodePool(
	_ context.Context,
	req containerengine.DeleteNodePoolRequest,
) (containerengine.DeleteNodePoolResponse, error) {
	s.deleted = append(s.deleted, *req.NodePoolId)
	return containerengine.DeleteNodePoolResponse{}, nil
}

// TestNodePoolLister_List proves nodePoolList returns every item ListNodePools gives back,
// wrapped, without ever constructing a real ContainerEngine client.
func TestNodePoolLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubNodePoolClient{
		items: []containerengine.NodePoolSummary{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: containerengine.NodePoolLifecycleStateActive},
		},
	}

	got, err := nodePoolList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("nodePoolList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("nodePoolList() returned %d resources, want 1", len(got))
	}
	np, ok := got[0].(*NodePool)
	if !ok {
		t.Fatalf("nodePoolList()[0] is %T, want *NodePool", got[0])
	}
	if np.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", np.GetCompartmentID(), compartmentID)
	}
	if np.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", np.UniqueKey(), id)
	}
}

// TestNodePool_GetCompartmentID_NilSafe proves a nil CompartmentId
// (NodePoolSummary.CompartmentId is mandatory:"false") returns "" rather than panicking --
// scopedLister then drops the resource fail-closed (T-05-01-03).
func TestNodePool_GetCompartmentID_NilSafe(t *testing.T) {
	r := &NodePool{}
	if got := r.GetCompartmentID(); got != "" {
		t.Errorf("GetCompartmentID() with nil CompartmentId = %q, want \"\"", got)
	}
}

// TestNodePool_UniqueKey_NilSafe proves a nil Id (NodePoolSummary.Id is mandatory:"false")
// returns "" rather than panicking -- scopedLister then drops the resource fail-closed
// (T-05-01-03).
func TestNodePool_UniqueKey_NilSafe(t *testing.T) {
	r := &NodePool{}
	if got := r.UniqueKey(); got != "" {
		t.Errorf("UniqueKey() with nil Id = %q, want \"\"", got)
	}
}

// TestNodePool_Filter is table-driven over every containerengine.NodePoolLifecycleStateEnum
// value -- CREATING/ACTIVE/UPDATING/INACTIVE/FAILED/NEEDS_ATTENTION must return nil (present),
// and DELETING/DELETED must both return non-nil (excluded). This IS the regression test for the
// hang trap (T-05-01-02): a Filter() that forgets the DELETING branch fails this test
// immediately.
func TestNodePool_Filter(t *testing.T) {
	tests := []struct {
		state   containerengine.NodePoolLifecycleStateEnum
		present bool
	}{
		{containerengine.NodePoolLifecycleStateCreating, true},
		{containerengine.NodePoolLifecycleStateActive, true},
		{containerengine.NodePoolLifecycleStateUpdating, true},
		{containerengine.NodePoolLifecycleStateInactive, true},
		{containerengine.NodePoolLifecycleStateFailed, true},
		{containerengine.NodePoolLifecycleStateNeedsAttention, true},
		{containerengine.NodePoolLifecycleStateDeleting, false},
		{containerengine.NodePoolLifecycleStateDeleted, false},
		{containerengine.NodePoolLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &NodePool{}
		r.nodePool.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestNodePool_SafetyTags_ZeroTimeCreated proves SafetyTags returns a zero time.Time, since
// containerengine.NodePoolSummary has no TimeCreated field at all to read from.
func TestNodePool_SafetyTags_ZeroTimeCreated(t *testing.T) {
	r := &NodePool{}
	_, _, createdAt := r.SafetyTags()
	if !createdAt.IsZero() {
		t.Errorf("SafetyTags() createdAt = %v, want zero time.Time", createdAt)
	}
}

// TestNodePool_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestNodePool_Remove(t *testing.T) {
	id := testResourceOCID

	stub := &stubNodePoolClient{}
	r := &NodePool{client: stub}
	r.nodePool.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
