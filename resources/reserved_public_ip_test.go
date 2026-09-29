package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubReservedPublicIpClient implements reservedPublicIpClient against in-memory data -- zero
// network access. lastReq captures the most recent ListPublicIps request so the test below can
// assert Lifetime: ListPublicIpsLifetimeReserved was actually set, not merely present in source.
type stubReservedPublicIpClient struct {
	items   []core.PublicIp
	deleted []string
	listErr error
	lastReq core.ListPublicIpsRequest
}

func (s *stubReservedPublicIpClient) ListPublicIps(
	_ context.Context,
	req core.ListPublicIpsRequest,
) (core.ListPublicIpsResponse, error) {
	s.lastReq = req
	if s.listErr != nil {
		return core.ListPublicIpsResponse{}, s.listErr
	}
	return core.ListPublicIpsResponse{Items: s.items}, nil
}

func (s *stubReservedPublicIpClient) DeletePublicIp(
	_ context.Context,
	req core.DeletePublicIpRequest,
) (core.DeletePublicIpResponse, error) {
	s.deleted = append(s.deleted, *req.PublicIpId)
	return core.DeletePublicIpResponse{}, nil
}

// TestReservedPublicIpLister_List proves reservedPublicIpList returns BOTH an ASSIGNED and a
// TERMINATED public IP -- filtering is Filter()'s job, not List()'s -- AND that the list call
// sets Lifetime: ListPublicIpsLifetimeReserved and Scope: ListPublicIpsScopeRegion (T-04-20): an
// omitted Lifetime filter would list live, VNIC-owned ephemeral public IPs.
func TestReservedPublicIpLister_List(t *testing.T) {
	assignedID := "ocid1.publicip.oc1..assigned"
	terminatedID := "ocid1.publicip.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubReservedPublicIpClient{
		items: []core.PublicIp{
			{Id: &assignedID, CompartmentId: &compartmentID, LifecycleState: core.PublicIpLifecycleStateAssigned},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.PublicIpLifecycleStateTerminated},
		},
	}

	got, err := reservedPublicIpList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("reservedPublicIpList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("reservedPublicIpList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*ReservedPublicIp).Properties(); props.Get("id") != assignedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), assignedID)
	}
	if props := got[1].(*ReservedPublicIp).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}

	if stub.lastReq.Lifetime != core.ListPublicIpsLifetimeReserved {
		t.Fatalf("ListPublicIpsRequest.Lifetime = %q, want %q (T-04-20)", stub.lastReq.Lifetime, core.ListPublicIpsLifetimeReserved)
	}
	if stub.lastReq.Scope != core.ListPublicIpsScopeRegion {
		t.Fatalf("ListPublicIpsRequest.Scope = %q, want %q", stub.lastReq.Scope, core.ListPublicIpsScopeRegion)
	}
}

// TestReservedPublicIp_Filter is table-driven over every core.PublicIpLifecycleStateEnum value --
// everything except TERMINATING/TERMINATED must return nil (present).
func TestReservedPublicIp_Filter(t *testing.T) {
	tests := []struct {
		state   core.PublicIpLifecycleStateEnum
		present bool
	}{
		{core.PublicIpLifecycleStateProvisioning, true},
		{core.PublicIpLifecycleStateAvailable, true},
		{core.PublicIpLifecycleStateAssigning, true},
		{core.PublicIpLifecycleStateAssigned, true},
		{core.PublicIpLifecycleStateUnassigning, true},
		{core.PublicIpLifecycleStateUnassigned, true},
		{core.PublicIpLifecycleStateTerminating, false},
		{core.PublicIpLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &ReservedPublicIp{}
		r.ip.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestReservedPublicIp_GetCompartmentID_NilSafe proves GetCompartmentID returns "" rather than
// panicking when CompartmentId is nil -- core.PublicIp.CompartmentId is `mandatory:"false"` on
// the pinned SDK, mirroring PrivateIp's own nil-CompartmentId handling.
func TestReservedPublicIp_GetCompartmentID_NilSafe(t *testing.T) {
	r := &ReservedPublicIp{}
	r.ip.CompartmentId = nil

	if got := r.GetCompartmentID(); got != "" {
		t.Fatalf("GetCompartmentID() = %q, want \"\" (nil-safe)", got)
	}
}

// TestReservedPublicIp_Remove proves the SDK delete call fires with the right parameter
// (PublicIpId), against a client that never touches the network.
func TestReservedPublicIp_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubReservedPublicIpClient{}
	r := &ReservedPublicIp{client: stub}
	r.ip.Id = &id
	r.ip.CompartmentId = &compartmentID
	r.ip.LifecycleState = core.PublicIpLifecycleStateAssigned
	r.ip.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestReservedPublicIp_Remove_NilTimeCreated proves Remove() does not panic when TimeCreated is
// nil -- core.PublicIp.TimeCreated is `mandatory:"false"` on the pinned SDK.
func TestReservedPublicIp_Remove_NilTimeCreated(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubReservedPublicIpClient{}
	r := &ReservedPublicIp{client: stub}
	r.ip.Id = &id
	r.ip.CompartmentId = &compartmentID
	r.ip.LifecycleState = core.PublicIpLifecycleStateAssigned
	r.ip.TimeCreated = nil

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
