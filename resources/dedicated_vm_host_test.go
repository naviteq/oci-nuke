package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubDedicatedVmHostClient implements dedicatedVmHostClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubDedicatedVmHostClient struct {
	items   []core.DedicatedVmHostSummary
	deleted []string
	listErr error
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
// value -- CREATING/ACTIVE/UPDATING must return nil (present), DELETING/DELETED must return
// non-nil (excluded), and FAILED must also return non-nil (excluded, and additionally reported --
// see TestDedicatedVmHost_Filter_FailedReportsLeftover below).
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
		{core.DedicatedVmHostSummaryLifecycleStateFailed, false},
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

// TestDedicatedVmHost_Filter_FailedReportsLeftover proves a FAILED host is excluded via
// ocinuke.ReportLeftover(scope.ReasonAPIError) before Filter() returns its exclusion error --
// FAILED surfaces as a labeled leftover, never a silent scan-time drop (T-04-12).
func TestDedicatedVmHost_Filter_FailedReportsLeftover(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	r := &DedicatedVmHost{}
	r.dedicatedVmHost.Id = &id
	r.dedicatedVmHost.CompartmentId = &compartmentID
	r.dedicatedVmHost.LifecycleState = core.DedicatedVmHostSummaryLifecycleStateFailed

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with FAILED state = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonAPIError {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonAPIError)
	}
	if got[0].ResourceType != DedicatedVmHostResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, DedicatedVmHostResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
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
