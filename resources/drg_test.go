package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubDrgClient implements drgClient against in-memory data -- zero network access.
type stubDrgClient struct {
	items   []core.Drg
	deleted []string
	listErr error
}

func (s *stubDrgClient) ListDrgs(_ context.Context, _ core.ListDrgsRequest) (core.ListDrgsResponse, error) {
	if s.listErr != nil {
		return core.ListDrgsResponse{}, s.listErr
	}
	return core.ListDrgsResponse{Items: s.items}, nil
}

func (s *stubDrgClient) DeleteDrg(_ context.Context, req core.DeleteDrgRequest) (core.DeleteDrgResponse, error) {
	s.deleted = append(s.deleted, *req.DrgId)
	return core.DeleteDrgResponse{}, nil
}

// TestDrgLister_List proves drgList returns BOTH an AVAILABLE and a TERMINATED drg -- filtering
// is Filter()'s job, not List()'s.
func TestDrgLister_List(t *testing.T) {
	availableID := "ocid1.drg.oc1..available"
	terminatedID := "ocid1.drg.oc1..terminated"
	compartmentID := testCompartmentOCID

	stub := &stubDrgClient{
		items: []core.Drg{
			{Id: &availableID, CompartmentId: &compartmentID, LifecycleState: core.DrgLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.DrgLifecycleStateTerminated},
		},
	}

	got, err := drgList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("drgList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("drgList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*Drg).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*Drg).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestDrg_Filter is table-driven over every core.DrgLifecycleStateEnum value --
// PROVISIONING/AVAILABLE must return nil (present), TERMINATING/TERMINATED must both return
// non-nil (excluded). This IS the regression test for the hang trap.
func TestDrg_Filter(t *testing.T) {
	tests := []struct {
		state   core.DrgLifecycleStateEnum
		present bool
	}{
		{core.DrgLifecycleStateProvisioning, true},
		{core.DrgLifecycleStateAvailable, true},
		{core.DrgLifecycleStateTerminating, false},
		{core.DrgLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &Drg{}
		r.drg.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestDrg_Remove proves the SDK delete call fires with the right parameter (DrgId), against a
// client that never touches the network.
func TestDrg_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubDrgClient{}
	r := &Drg{client: stub}
	r.drg.Id = &id
	r.drg.CompartmentId = &compartmentID
	r.drg.LifecycleState = core.DrgLifecycleStateAvailable
	r.drg.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestDrg_Remove_NilTimeCreated proves Remove() does not panic when TimeCreated is nil --
// core.Drg.TimeCreated is `mandatory:"false"` on the pinned SDK, unlike every earlier resource
// type in this wave.
func TestDrg_Remove_NilTimeCreated(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubDrgClient{}
	r := &Drg{client: stub}
	r.drg.Id = &id
	r.drg.CompartmentId = &compartmentID
	r.drg.LifecycleState = core.DrgLifecycleStateAvailable
	r.drg.TimeCreated = nil

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestDrg_DependsOn_DrgAttachment asserts Drg's registered DependsOn is exactly
// []string{DrgAttachmentResourceType} -- proving the CITED docs.oracle.com/.../drg-delete.htm
// ordering constraint is encoded as data, not merely narrated in a comment.
func TestDrg_DependsOn_DrgAttachment(t *testing.T) {
	reg := registry.GetRegistration(DrgResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", DrgResourceType)
	}
	want := []string{DrgAttachmentResourceType}
	if len(reg.DependsOn) != 1 || reg.DependsOn[0] != want[0] {
		t.Fatalf("Drg DependsOn = %v, want %v", reg.DependsOn, want)
	}
}
