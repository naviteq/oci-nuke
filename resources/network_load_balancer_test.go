package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubNetworkLoadBalancerClient implements networkLoadBalancerClient against in-memory data --
// zero network access, mirroring the seam pkg/scope's IdentityClient/RegionClient already
// establish.
type stubNetworkLoadBalancerClient struct {
	items   []networkloadbalancer.NetworkLoadBalancerSummary
	deleted []string
	listErr error

	backendSets      []networkloadbalancer.BackendSetSummary
	backendSetsCalls int
}

// ListNetworkLoadBalancers/DeleteNetworkLoadBalancer/ListBackendSets signatures below must match
// the real networkloadbalancer.NetworkLoadBalancerClient methods exactly (request struct by
// value, not a pointer) to satisfy networkLoadBalancerClient.
func (s *stubNetworkLoadBalancerClient) ListNetworkLoadBalancers(
	_ context.Context,
	_ networkloadbalancer.ListNetworkLoadBalancersRequest,
) (networkloadbalancer.ListNetworkLoadBalancersResponse, error) {
	if s.listErr != nil {
		return networkloadbalancer.ListNetworkLoadBalancersResponse{}, s.listErr
	}
	return networkloadbalancer.ListNetworkLoadBalancersResponse{
		NetworkLoadBalancerCollection: networkloadbalancer.NetworkLoadBalancerCollection{Items: s.items},
	}, nil
}

func (s *stubNetworkLoadBalancerClient) DeleteNetworkLoadBalancer(
	_ context.Context,
	req networkloadbalancer.DeleteNetworkLoadBalancerRequest,
) (networkloadbalancer.DeleteNetworkLoadBalancerResponse, error) {
	s.deleted = append(s.deleted, *req.NetworkLoadBalancerId)
	return networkloadbalancer.DeleteNetworkLoadBalancerResponse{}, nil
}

func (s *stubNetworkLoadBalancerClient) ListBackendSets(
	_ context.Context,
	_ networkloadbalancer.ListBackendSetsRequest,
) (networkloadbalancer.ListBackendSetsResponse, error) {
	s.backendSetsCalls++
	return networkloadbalancer.ListBackendSetsResponse{
		BackendSetCollection: networkloadbalancer.BackendSetCollection{Items: s.backendSets},
	}, nil
}

// TestNetworkLoadBalancerLister_List proves networkLoadBalancerList returns exactly one
// resource, wrapping the stub's single item, without ever constructing a real
// NetworkLoadBalancer client.
func TestNetworkLoadBalancerLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubNetworkLoadBalancerClient{
		items: []networkloadbalancer.NetworkLoadBalancerSummary{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: networkloadbalancer.LifecycleStateActive},
		},
	}

	got, err := networkLoadBalancerList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("networkLoadBalancerList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("networkLoadBalancerList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*NetworkLoadBalancer).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestNetworkLoadBalancer_Filter is table-driven over every
// networkloadbalancer.LifecycleStateEnum value -- CREATING/UPDATING/ACTIVE must return nil
// (present), DELETING/DELETED must both return non-nil (excluded, the hang-trap regression
// case), and FAILED must also return non-nil (excluded, and additionally reported -- see
// TestNetworkLoadBalancer_Filter_FailedReportsLeftover below).
func TestNetworkLoadBalancer_Filter(t *testing.T) {
	tests := []struct {
		state   networkloadbalancer.LifecycleStateEnum
		present bool
	}{
		{networkloadbalancer.LifecycleStateCreating, true},
		{networkloadbalancer.LifecycleStateUpdating, true},
		{networkloadbalancer.LifecycleStateActive, true},
		{networkloadbalancer.LifecycleStateDeleting, false},
		{networkloadbalancer.LifecycleStateDeleted, false},
		{networkloadbalancer.LifecycleStateFailed, false},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &NetworkLoadBalancer{}
		r.nlb.Id = &id
		r.nlb.CompartmentId = &compartmentID
		r.nlb.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestNetworkLoadBalancer_Filter_FailedReportsLeftover proves a FAILED network load balancer is
// excluded via ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its
// exclusion error -- FAILED surfaces as a labeled leftover, never a silent scan-time drop.
func TestNetworkLoadBalancer_Filter_FailedReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &NetworkLoadBalancer{}
	r.nlb.Id = &id
	r.nlb.CompartmentId = &compartmentID
	r.nlb.LifecycleState = networkloadbalancer.LifecycleStateFailed

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAILED state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != NetworkLoadBalancerResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, NetworkLoadBalancerResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestNetworkLoadBalancer_Remove proves the SDK delete call fires with the right parameter,
// against a client that never touches the network.
func TestNetworkLoadBalancer_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubNetworkLoadBalancerClient{}
	r := &NetworkLoadBalancer{client: stub}
	r.nlb.Id = &id
	r.nlb.CompartmentId = &compartmentID
	r.nlb.LifecycleState = networkloadbalancer.LifecycleStateActive
	r.nlb.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestNetworkLoadBalancer_Remove_NeverCallsBackendSetLister proves the "not independently
// deleted" half of this plan's objective directly: Remove() never issues ListBackendSets, only
// DeleteNetworkLoadBalancer.
func TestNetworkLoadBalancer_Remove_NeverCallsBackendSetLister(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubNetworkLoadBalancerClient{}
	r := &NetworkLoadBalancer{client: stub}
	r.nlb.Id = &id
	r.nlb.CompartmentId = &compartmentID
	r.nlb.LifecycleState = networkloadbalancer.LifecycleStateActive
	r.nlb.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if stub.backendSetsCalls != 0 {
		t.Errorf("ListBackendSets called %d times from Remove(), want 0", stub.backendSetsCalls)
	}
}

// TestNetworkLoadBalancer_Properties_BackendSets proves Properties() surfaces backend_sets,
// comma-joined from the stub's named entries, sourced from ListBackendSets -- called ONLY from
// Properties(), never from Remove().
func TestNetworkLoadBalancer_Properties_BackendSets(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	bsName := "example_backend_set"

	stub := &stubNetworkLoadBalancerClient{
		backendSets: []networkloadbalancer.BackendSetSummary{{Name: &bsName}},
	}
	r := &NetworkLoadBalancer{client: stub}
	r.nlb.Id = &id
	r.nlb.CompartmentId = &compartmentID
	r.nlb.LifecycleState = networkloadbalancer.LifecycleStateActive

	props := r.Properties()
	if got := props.Get("backend_sets"); got != bsName {
		t.Errorf("Properties()[%q] = %q, want %q", "backend_sets", got, bsName)
	}
	if stub.backendSetsCalls != 1 {
		t.Errorf("ListBackendSets called %d times, want 1", stub.backendSetsCalls)
	}
}

// TestNetworkLoadBalancer_Properties_ListErrorOmitsKeyWithoutPanic proves a ListBackendSets
// failure is logged and swallowed -- Properties() never panics or fails the scan over what is
// deliberately a best-effort, read-only visibility call.
func TestNetworkLoadBalancer_Properties_ListErrorOmitsKeyWithoutPanic(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubNetworkLoadBalancerErrClient{err: errors.New("boom")}
	r := &NetworkLoadBalancer{client: stub}
	r.nlb.Id = &id
	r.nlb.CompartmentId = &compartmentID
	r.nlb.LifecycleState = networkloadbalancer.LifecycleStateActive

	props := r.Properties()
	if got := props.Get("backend_sets"); got != "" {
		t.Errorf("Properties()[%q] = %q, want empty on ListBackendSets error", "backend_sets", got)
	}
}

// stubNetworkLoadBalancerErrClient always returns err from ListBackendSets, embedding
// stubNetworkLoadBalancerClient for the other two methods so it still satisfies
// networkLoadBalancerClient.
type stubNetworkLoadBalancerErrClient struct {
	stubNetworkLoadBalancerClient
	err error
}

func (s *stubNetworkLoadBalancerErrClient) ListBackendSets(
	_ context.Context,
	_ networkloadbalancer.ListBackendSetsRequest,
) (networkloadbalancer.ListBackendSetsResponse, error) {
	return networkloadbalancer.ListBackendSetsResponse{}, s.err
}
