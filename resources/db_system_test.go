package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/database"
)

// stubDbSystemClient implements dbSystemClient against in-memory data -- zero network access,
// mirroring resources/instance_test.go's stubInstanceClient shape.
type stubDbSystemClient struct {
	items   []database.DbSystemSummary
	deleted []string
	listErr error
}

func (s *stubDbSystemClient) ListDbSystems(
	_ context.Context,
	_ database.ListDbSystemsRequest,
) (database.ListDbSystemsResponse, error) {
	if s.listErr != nil {
		return database.ListDbSystemsResponse{}, s.listErr
	}
	return database.ListDbSystemsResponse{Items: s.items}, nil
}

func (s *stubDbSystemClient) TerminateDbSystem(
	_ context.Context,
	req database.TerminateDbSystemRequest,
) (database.TerminateDbSystemResponse, error) {
	s.deleted = append(s.deleted, *req.DbSystemId)
	return database.TerminateDbSystemResponse{}, nil
}

// TestDbSystemLister_List proves dbSystemList returns BOTH an AVAILABLE and a TERMINATED DB
// System -- filtering is Filter()'s job, not List()'s -- without ever constructing a real
// Database client.
func TestDbSystemLister_List(t *testing.T) {
	availableID := "ocid1.test.oc1..available"
	terminatedID := testTerminatedOCID
	compartmentID := testCompartmentOCID

	stub := &stubDbSystemClient{
		items: []database.DbSystemSummary{
			{
				Id: &availableID, CompartmentId: &compartmentID,
				LifecycleState: database.DbSystemSummaryLifecycleStateAvailable,
			},
			{
				Id: &terminatedID, CompartmentId: &compartmentID,
				LifecycleState: database.DbSystemSummaryLifecycleStateTerminated,
			},
		},
	}

	got, err := dbSystemList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("dbSystemList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("dbSystemList() returned %d resources, want 2", len(got))
	}
	if got[0].(*DbSystem).GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", got[0].(*DbSystem).GetCompartmentID(), compartmentID)
	}
}

// TestDbSystem_Filter is table-driven over database.DbSystemSummaryLifecycleStateEnum's full,
// real const block (verified this session -- see db_system.go's Filter() doc comment for the
// deviation from 05-02-PLAN.md's literal wording this table reflects).
func TestDbSystem_Filter(t *testing.T) {
	tests := []struct {
		state   database.DbSystemSummaryLifecycleStateEnum
		present bool
	}{
		{database.DbSystemSummaryLifecycleStateProvisioning, true},
		{database.DbSystemSummaryLifecycleStateAvailable, true},
		{database.DbSystemSummaryLifecycleStateUpdating, true},
		{database.DbSystemSummaryLifecycleStateMaintenanceInProgress, true},
		{database.DbSystemSummaryLifecycleStateNeedsAttention, true},
		{database.DbSystemSummaryLifecycleStateUpgrading, true},
		{database.DbSystemSummaryLifecycleStateTerminating, false},
		{database.DbSystemSummaryLifecycleStateTerminated, false},
		{database.DbSystemSummaryLifecycleStateFailed, false},
		{database.DbSystemSummaryLifecycleStateMigrated, false},
	}

	for _, tc := range tests {
		r := &DbSystem{}
		r.dbSystem.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestDbSystem_SafetyTags_NilTimeCreated proves SafetyTags() does not panic and returns the zero
// time.Time when TimeCreated is nil -- DbSystemSummary.TimeCreated is mandatory:"false" (verified
// this session).
func TestDbSystem_SafetyTags_NilTimeCreated(t *testing.T) {
	r := &DbSystem{dbSystem: database.DbSystemSummary{}}

	_, _, createdAt := r.SafetyTags()
	if !createdAt.IsZero() {
		t.Errorf("SafetyTags() createdAt = %v, want zero time.Time for nil TimeCreated", createdAt)
	}
}

// TestDbSystem_Remove proves the SDK delete call fires with the right parameter, against a client
// that never touches the network.
func TestDbSystem_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubDbSystemClient{}
	r := &DbSystem{client: stub}
	r.dbSystem.Id = &id
	r.dbSystem.CompartmentId = &compartmentID
	r.dbSystem.LifecycleState = database.DbSystemSummaryLifecycleStateAvailable
	r.dbSystem.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
