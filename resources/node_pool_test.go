package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/containerengine"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubNodePoolClient implements nodePoolClient against in-memory data -- zero network access,
// mirroring stubInstanceClient's established precedent (resources/instance_test.go).
type stubNodePoolClient struct {
	items        []containerengine.NodePoolSummary
	deleted      []string
	listErr      error
	workRequests []containerengine.WorkRequestSummary
	wrErr        error
	wrCalls      int
}

func (s *stubNodePoolClient) ListWorkRequests(
	_ context.Context,
	req containerengine.ListWorkRequestsRequest,
) (containerengine.ListWorkRequestsResponse, error) {
	s.wrCalls++
	if req.ResourceType != containerengine.ListWorkRequestsResourceTypeNodepool {
		return containerengine.ListWorkRequestsResponse{}, errors.New("unexpected resource type " + string(req.ResourceType))
	}
	return containerengine.ListWorkRequestsResponse{Items: s.workRequests}, s.wrErr
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

	got, err := nodePoolList(context.Background(), stub, compartmentID, false)
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

// TestNodePoolList_CreationTimeFromWorkRequest: with min-age on, a node pool's creation time is
// its NODEPOOL_CREATE work request's, so a fresh pool is too young to delete. Other operations
// and RELATED resources are not mistaken for it.
func TestNodePoolList_CreationTimeFromWorkRequest(t *testing.T) {
	poolID, clusterID, compartmentID := "ocid1.nodepool.oc1..fresh", "ocid1.cluster.oc1..c", testCompartmentOCID
	created := common.SDKTime{Time: time.Now().Add(-48 * time.Minute)}
	updated := common.SDKTime{Time: time.Now().Add(-1 * time.Minute)}
	stub := &stubNodePoolClient{
		items: []containerengine.NodePoolSummary{{Id: &poolID, CompartmentId: &compartmentID}},
		workRequests: []containerengine.WorkRequestSummary{
			{
				OperationType: containerengine.WorkRequestOperationTypeNodepoolCreate, TimeAccepted: &created,
				Resources: []containerengine.WorkRequestResource{
					{ActionType: containerengine.WorkRequestResourceActionTypeCreated, Identifier: &poolID},
					{ActionType: containerengine.WorkRequestResourceActionTypeRelated, Identifier: &clusterID},
				},
			},
			{
				OperationType: containerengine.WorkRequestOperationTypeNodepoolUpdate, TimeAccepted: &updated,
				Resources: []containerengine.WorkRequestResource{
					{ActionType: containerengine.WorkRequestResourceActionTypeUpdated, Identifier: &poolID},
				},
			},
		},
	}

	got, err := nodePoolList(context.Background(), stub, compartmentID, true)
	if err != nil {
		t.Fatalf("nodePoolList() error = %v", err)
	}
	freeform, defined, createdAt := got[0].(*NodePool).SafetyTags()
	if !createdAt.Equal(created.Time) {
		t.Fatalf("createdAt = %v, want the NODEPOOL_CREATE time %v", createdAt, created.Time)
	}
	evt := ocinuke.Evaluate(compartmentID, NodePoolResourceType, poolID, freeform, defined, createdAt,
		ocinuke.SafetyFilterConfig{MinAge: 2 * time.Hour})
	if evt == nil || evt.Reason != scope.ReasonTooYoung {
		t.Errorf("Evaluate() = %+v, want too-young", evt)
	}
}

// TestNodePoolList_NoLookupWithoutMinAge: with min-age off the work requests are not read.
func TestNodePoolList_NoLookupWithoutMinAge(t *testing.T) {
	stub := &stubNodePoolClient{}
	if _, err := nodePoolList(context.Background(), stub, testCompartmentOCID, false); err != nil {
		t.Fatalf("nodePoolList() error = %v", err)
	}
	if stub.wrCalls != 0 {
		t.Errorf("ListWorkRequests called %d times, want 0", stub.wrCalls)
	}
}

// TestNodePoolList_WorkRequestFailureFailsTheListing: min-age cannot be applied without the
// creation time, so the listing fails rather than treating every pool as old.
func TestNodePoolList_WorkRequestFailureFailsTheListing(t *testing.T) {
	stub := &stubNodePoolClient{wrErr: errors.New("NotAuthorizedOrNotFound")}
	if _, err := nodePoolList(context.Background(), stub, testCompartmentOCID, true); err == nil {
		t.Fatal("nodePoolList() = nil error, want the work-request failure")
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
