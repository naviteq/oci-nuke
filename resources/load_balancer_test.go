package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubLoadBalancerClient implements loadBalancerClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubLoadBalancerClient struct {
	items   []loadbalancer.LoadBalancer
	deleted []string
	listErr error

	backendSets       []loadbalancer.BackendSet
	certificates      []loadbalancer.Certificate
	backendSetsCalls  int
	certificatesCalls int
}

// ListLoadBalancers/DeleteLoadBalancer/ListBackendSets/ListCertificates signatures below must
// match the real loadbalancer.LoadBalancerClient methods exactly (request struct by value, not a
// pointer) to satisfy loadBalancerClient.
func (s *stubLoadBalancerClient) ListLoadBalancers(
	_ context.Context,
	_ loadbalancer.ListLoadBalancersRequest,
) (loadbalancer.ListLoadBalancersResponse, error) {
	if s.listErr != nil {
		return loadbalancer.ListLoadBalancersResponse{}, s.listErr
	}
	return loadbalancer.ListLoadBalancersResponse{Items: s.items}, nil
}

func (s *stubLoadBalancerClient) DeleteLoadBalancer(
	_ context.Context,
	req loadbalancer.DeleteLoadBalancerRequest,
) (loadbalancer.DeleteLoadBalancerResponse, error) {
	s.deleted = append(s.deleted, *req.LoadBalancerId)
	return loadbalancer.DeleteLoadBalancerResponse{}, nil
}

func (s *stubLoadBalancerClient) ListBackendSets(
	_ context.Context,
	_ loadbalancer.ListBackendSetsRequest,
) (loadbalancer.ListBackendSetsResponse, error) {
	s.backendSetsCalls++
	return loadbalancer.ListBackendSetsResponse{Items: s.backendSets}, nil
}

func (s *stubLoadBalancerClient) ListCertificates(
	_ context.Context,
	_ loadbalancer.ListCertificatesRequest,
) (loadbalancer.ListCertificatesResponse, error) {
	s.certificatesCalls++
	return loadbalancer.ListCertificatesResponse{Items: s.certificates}, nil
}

// TestLoadBalancerLister_List proves loadBalancerList returns exactly one resource, wrapping the
// stub's single item, without ever constructing a real LoadBalancer client.
func TestLoadBalancerLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubLoadBalancerClient{
		items: []loadbalancer.LoadBalancer{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: loadbalancer.LoadBalancerLifecycleStateActive},
		},
	}

	got, err := loadBalancerList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("loadBalancerList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("loadBalancerList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*LoadBalancer).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
}

// TestLoadBalance_Filter is table-driven over every loadbalancer.LoadBalancerLifecycleStateEnum
// value -- CREATING/ACTIVE must return nil (present), DELETING/DELETED must both return non-nil
// (excluded, the hang-trap regression case), and FAILED must also return non-nil (excluded, and
// additionally reported -- see TestLoadBalancer_Filter_FailedReportsLeftover below).
func TestLoadBalancer_Filter(t *testing.T) {
	tests := []struct {
		state   loadbalancer.LoadBalancerLifecycleStateEnum
		present bool
	}{
		{loadbalancer.LoadBalancerLifecycleStateCreating, true},
		{loadbalancer.LoadBalancerLifecycleStateActive, true},
		{loadbalancer.LoadBalancerLifecycleStateDeleting, false},
		{loadbalancer.LoadBalancerLifecycleStateDeleted, false},
		{loadbalancer.LoadBalancerLifecycleStateFailed, false},
	}

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	for _, tc := range tests {
		r := &LoadBalancer{}
		r.lb.Id = &id
		r.lb.CompartmentId = &compartmentID
		r.lb.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestLoadBalancer_Filter_FailedReportsLeftover proves a FAILED load balancer is excluded via
// ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its exclusion error --
// FAILED surfaces as a labeled leftover, never a silent scan-time drop.
func TestLoadBalancer_Filter_FailedReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &LoadBalancer{}
	r.lb.Id = &id
	r.lb.CompartmentId = &compartmentID
	r.lb.LifecycleState = loadbalancer.LoadBalancerLifecycleStateFailed

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAILED state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != LoadBalancerResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, LoadBalancerResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestLoadBalancer_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestLoadBalancer_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubLoadBalancerClient{}
	r := &LoadBalancer{client: stub}
	r.lb.Id = &id
	r.lb.CompartmentId = &compartmentID
	r.lb.LifecycleState = loadbalancer.LoadBalancerLifecycleStateActive
	r.lb.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestLoadBalancer_Remove_NeverCallsBackendSetOrCertificateLister proves the "not independently
// deleted" half of this plan's objective directly: Remove() never issues ListBackendSets or
// ListCertificates, only DeleteLoadBalancer.
func TestLoadBalancer_Remove_NeverCallsBackendSetOrCertificateLister(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubLoadBalancerClient{}
	r := &LoadBalancer{client: stub}
	r.lb.Id = &id
	r.lb.CompartmentId = &compartmentID
	r.lb.LifecycleState = loadbalancer.LoadBalancerLifecycleStateActive
	r.lb.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if stub.backendSetsCalls != 0 {
		t.Errorf("ListBackendSets called %d times from Remove(), want 0", stub.backendSetsCalls)
	}
	if stub.certificatesCalls != 0 {
		t.Errorf("ListCertificates called %d times from Remove(), want 0", stub.certificatesCalls)
	}
}

// TestLoadBalancer_Properties_BackendSetsAndCertificates proves Properties() surfaces both
// backend_sets and certificates, comma-joined from the stub's named entries, sourced from
// ListBackendSets/ListCertificates -- calls issued ONLY from Properties(), never from Remove().
func TestLoadBalancer_Properties_BackendSetsAndCertificates(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	bsName := "example_backend_set"
	certName := "example_certificate_bundle"

	stub := &stubLoadBalancerClient{
		backendSets:  []loadbalancer.BackendSet{{Name: &bsName}},
		certificates: []loadbalancer.Certificate{{CertificateName: &certName}},
	}
	r := &LoadBalancer{client: stub}
	r.lb.Id = &id
	r.lb.CompartmentId = &compartmentID
	r.lb.LifecycleState = loadbalancer.LoadBalancerLifecycleStateActive

	props := r.Properties()
	if got := props.Get("backend_sets"); got != bsName {
		t.Errorf("Properties()[%q] = %q, want %q", "backend_sets", got, bsName)
	}
	if got := props.Get("certificates"); got != certName {
		t.Errorf("Properties()[%q] = %q, want %q", "certificates", got, certName)
	}
	if stub.backendSetsCalls != 1 {
		t.Errorf("ListBackendSets called %d times, want 1", stub.backendSetsCalls)
	}
	if stub.certificatesCalls != 1 {
		t.Errorf("ListCertificates called %d times, want 1", stub.certificatesCalls)
	}
}

// TestLoadBalancer_Properties_ListErrorOmitsKeyWithoutPanic proves a ListBackendSets/
// ListCertificates failure is logged and swallowed -- Properties() never panics or fails the
// scan over what is deliberately a best-effort, read-only visibility call.
func TestLoadBalancer_Properties_ListErrorOmitsKeyWithoutPanic(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubLoadBalancerErrClient{err: errors.New("boom")}
	r := &LoadBalancer{client: stub}
	r.lb.Id = &id
	r.lb.CompartmentId = &compartmentID
	r.lb.LifecycleState = loadbalancer.LoadBalancerLifecycleStateActive

	props := r.Properties()
	if got := props.Get("backend_sets"); got != "" {
		t.Errorf("Properties()[%q] = %q, want empty on ListBackendSets error", "backend_sets", got)
	}
	if got := props.Get("certificates"); got != "" {
		t.Errorf("Properties()[%q] = %q, want empty on ListCertificates error", "certificates", got)
	}
}

// stubLoadBalancerErrClient always returns err from ListBackendSets/ListCertificates, embedding
// stubLoadBalancerClient for the other two methods so it still satisfies loadBalancerClient.
type stubLoadBalancerErrClient struct {
	stubLoadBalancerClient
	err error
}

func (s *stubLoadBalancerErrClient) ListBackendSets(
	_ context.Context,
	_ loadbalancer.ListBackendSetsRequest,
) (loadbalancer.ListBackendSetsResponse, error) {
	return loadbalancer.ListBackendSetsResponse{}, s.err
}

func (s *stubLoadBalancerErrClient) ListCertificates(
	_ context.Context,
	_ loadbalancer.ListCertificatesRequest,
) (loadbalancer.ListCertificatesResponse, error) {
	return loadbalancer.ListCertificatesResponse{}, s.err
}
