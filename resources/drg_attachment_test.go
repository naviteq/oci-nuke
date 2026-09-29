package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubDrgAttachmentClient implements drgAttachmentClient against in-memory data -- zero network
// access.
type stubDrgAttachmentClient struct {
	items   []core.DrgAttachment
	deleted []string
	listErr error
}

func (s *stubDrgAttachmentClient) ListDrgAttachments(
	_ context.Context,
	_ core.ListDrgAttachmentsRequest,
) (core.ListDrgAttachmentsResponse, error) {
	if s.listErr != nil {
		return core.ListDrgAttachmentsResponse{}, s.listErr
	}
	return core.ListDrgAttachmentsResponse{Items: s.items}, nil
}

func (s *stubDrgAttachmentClient) DeleteDrgAttachment(
	_ context.Context,
	req core.DeleteDrgAttachmentRequest,
) (core.DeleteDrgAttachmentResponse, error) {
	s.deleted = append(s.deleted, *req.DrgAttachmentId)
	return core.DeleteDrgAttachmentResponse{}, nil
}

// TestDrgAttachmentLister_List proves drgAttachmentList returns BOTH an ATTACHED and a DETACHED
// attachment -- filtering is Filter()'s job, not List()'s.
func TestDrgAttachmentLister_List(t *testing.T) {
	attachedID := "ocid1.drgattachment.oc1..attached"
	detachedID := "ocid1.drgattachment.oc1..detached"
	compartmentID := testCompartmentOCID

	stub := &stubDrgAttachmentClient{
		items: []core.DrgAttachment{
			{Id: &attachedID, CompartmentId: &compartmentID, LifecycleState: core.DrgAttachmentLifecycleStateAttached},
			{Id: &detachedID, CompartmentId: &compartmentID, LifecycleState: core.DrgAttachmentLifecycleStateDetached},
		},
	}

	got, err := drgAttachmentList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("drgAttachmentList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("drgAttachmentList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*DrgAttachment).Properties(); props.Get("id") != attachedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), attachedID)
	}
	if props := got[1].(*DrgAttachment).Properties(); props.Get("id") != detachedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), detachedID)
	}
}

// TestDrgAttachment_Filter is table-driven over every core.DrgAttachmentLifecycleStateEnum
// value -- ATTACHING/ATTACHED must return nil (present), DETACHING/DETACHED must both return
// non-nil (excluded). This IS the regression test for the hang trap.
func TestDrgAttachment_Filter(t *testing.T) {
	tests := []struct {
		state   core.DrgAttachmentLifecycleStateEnum
		present bool
	}{
		{core.DrgAttachmentLifecycleStateAttaching, true},
		{core.DrgAttachmentLifecycleStateAttached, true},
		{core.DrgAttachmentLifecycleStateDetaching, false},
		{core.DrgAttachmentLifecycleStateDetached, false},
	}

	for _, tc := range tests {
		r := &DrgAttachment{}
		r.att.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestDrgAttachment_Remove proves the SDK delete call fires with the right parameter
// (DrgAttachmentId), against a client that never touches the network.
func TestDrgAttachment_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubDrgAttachmentClient{}
	r := &DrgAttachment{client: stub}
	r.att.Id = &id
	r.att.CompartmentId = &compartmentID
	r.att.LifecycleState = core.DrgAttachmentLifecycleStateAttached
	r.att.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestDrgAttachment_Remove_NilTimeCreated proves Remove() does not panic when TimeCreated is
// nil -- core.DrgAttachment.TimeCreated is `mandatory:"false"` on the pinned SDK, unlike every
// earlier resource type in this wave.
func TestDrgAttachment_Remove_NilTimeCreated(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubDrgAttachmentClient{}
	r := &DrgAttachment{client: stub}
	r.att.Id = &id
	r.att.CompartmentId = &compartmentID
	r.att.LifecycleState = core.DrgAttachmentLifecycleStateAttached
	r.att.TimeCreated = nil

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
