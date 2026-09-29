package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/streaming"
)

// stubStreamPoolClient implements streamPoolClient against in-memory data -- zero network
// access, mirroring stubInstanceClient's own established seam shape.
type stubStreamPoolClient struct {
	items   []streaming.StreamPoolSummary
	deleted []string
	listErr error
}

func (s *stubStreamPoolClient) ListStreamPools(
	_ context.Context,
	_ streaming.ListStreamPoolsRequest,
) (streaming.ListStreamPoolsResponse, error) {
	if s.listErr != nil {
		return streaming.ListStreamPoolsResponse{}, s.listErr
	}
	return streaming.ListStreamPoolsResponse{Items: s.items}, nil
}

func (s *stubStreamPoolClient) DeleteStreamPool(
	_ context.Context,
	req streaming.DeleteStreamPoolRequest,
) (streaming.DeleteStreamPoolResponse, error) {
	s.deleted = append(s.deleted, *req.StreamPoolId)
	return streaming.DeleteStreamPoolResponse{}, nil
}

// TestStreamPoolList_List proves streamPoolList wraps every streaming.StreamPoolSummary returned
// by ListStreamPools as a StreamPool, threading compartmentID through the request.
func TestStreamPoolList_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	name := "default-pool"
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubStreamPoolClient{
		items: []streaming.StreamPoolSummary{{
			Id:             &id,
			Name:           &name,
			CompartmentId:  &compartmentID,
			LifecycleState: streaming.StreamPoolSummaryLifecycleStateActive,
			TimeCreated:    &timeCreated,
		}},
	}

	got, err := streamPoolList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("streamPoolList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("streamPoolList() returned %d resources, want 1", len(got))
	}
	p, ok := got[0].(*StreamPool)
	if !ok {
		t.Fatalf("streamPoolList()[0] is %T, want *StreamPool", got[0])
	}
	if p.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", p.GetCompartmentID(), compartmentID)
	}
	if p.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", p.UniqueKey(), id)
	}
}

// TestStreamPool_Filter is table-driven over every streaming.StreamPoolSummaryLifecycleStateEnum
// value.
func TestStreamPool_Filter(t *testing.T) {
	tests := []struct {
		state   streaming.StreamPoolSummaryLifecycleStateEnum
		present bool
	}{
		{streaming.StreamPoolSummaryLifecycleStateCreating, true},
		{streaming.StreamPoolSummaryLifecycleStateActive, true},
		{streaming.StreamPoolSummaryLifecycleStateUpdating, true},
		{streaming.StreamPoolSummaryLifecycleStateDeleting, false},
		{streaming.StreamPoolSummaryLifecycleStateDeleted, false},
		{streaming.StreamPoolSummaryLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &StreamPool{}
			r.pool.LifecycleState = tc.state
			err := r.Filter()
			if tc.present && err != nil {
				t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
			}
			if !tc.present && err == nil {
				t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
			}
		})
	}
}

// TestStreamPool_Remove proves the SDK delete call fires with the right StreamPoolId, against a
// client that never touches the network.
func TestStreamPool_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubStreamPoolClient{}
	r := &StreamPool{client: stub}
	r.pool.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestStreamPoolRegistration_DependsOnStream proves StreamPool declares DependsOn: ["Stream"]
// exactly as 05-CONTEXT.md locks -- Streams must be scanned/removed before the pool that hosts
// them.
func TestStreamPoolRegistration_DependsOnStream(t *testing.T) {
	reg := registry.GetRegistration(StreamPoolResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", StreamPoolResourceType)
	}
	want := []string{"Stream"}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("StreamPool DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("StreamPool DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}
