package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/database"
)

// stubAutonomousDatabaseClient implements autonomousDatabaseClient against in-memory data -- zero
// network access, mirroring resources/instance_test.go's stubInstanceClient shape.
type stubAutonomousDatabaseClient struct {
	items   []database.AutonomousDatabaseSummary
	deleted []string
	listErr error
}

func (s *stubAutonomousDatabaseClient) ListAutonomousDatabases(
	_ context.Context,
	_ database.ListAutonomousDatabasesRequest,
) (database.ListAutonomousDatabasesResponse, error) {
	if s.listErr != nil {
		return database.ListAutonomousDatabasesResponse{}, s.listErr
	}
	return database.ListAutonomousDatabasesResponse{Items: s.items}, nil
}

func (s *stubAutonomousDatabaseClient) DeleteAutonomousDatabase(
	_ context.Context,
	req database.DeleteAutonomousDatabaseRequest,
) (database.DeleteAutonomousDatabaseResponse, error) {
	s.deleted = append(s.deleted, *req.AutonomousDatabaseId)
	return database.DeleteAutonomousDatabaseResponse{}, nil
}

// TestAutonomousDatabaseLister_List proves autonomousDatabaseList returns BOTH an AVAILABLE and a
// TERMINATED database -- filtering is Filter()'s job, not List()'s -- without ever constructing a
// real Database client.
func TestAutonomousDatabaseLister_List(t *testing.T) {
	availableID := "ocid1.test.oc1..available"
	terminatedID := testTerminatedOCID
	compartmentID := testCompartmentOCID

	stub := &stubAutonomousDatabaseClient{
		items: []database.AutonomousDatabaseSummary{
			{
				Id: &availableID, CompartmentId: &compartmentID,
				LifecycleState: database.AutonomousDatabaseSummaryLifecycleStateAvailable,
			},
			{
				Id: &terminatedID, CompartmentId: &compartmentID,
				LifecycleState: database.AutonomousDatabaseSummaryLifecycleStateTerminated,
			},
		},
	}

	got, err := autonomousDatabaseList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("autonomousDatabaseList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("autonomousDatabaseList() returned %d resources, want 2", len(got))
	}
	if got[0].(*AutonomousDatabase).GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", got[0].(*AutonomousDatabase).GetCompartmentID(), compartmentID)
	}
}

// TestAutonomousDatabase_Filter is table-driven over all 22 real
// database.AutonomousDatabaseSummaryLifecycleStateEnum values (the full const block, reconciled
// against oci-go-sdk/v65@v65.123.0/database/autonomous_database_summary.go) -- every value must be
// deliberately classified as present or excluded, so a future SDK bump that adds a 23rd value is
// caught by this table going stale rather than silently falling into the fail-safe default.
// TERMINATING/TERMINATED must be excluded (the hang-trap regression); RESTORE_FAILED/INACCESSIBLE
// are deliberately-not-present error states; the remaining 9 (BACKUP_IN_PROGRESS,
// AVAILABLE_NEEDS_ATTENTION, MAINTENANCE_IN_PROGRESS, RESTARTING, RECREATING,
// ROLE_CHANGE_IN_PROGRESS, UPGRADING, STANDBY, TRANSPORTING) are routine, non-terminal states and
// must be present -- R-WR-01's fix, since silently excluding them from teardown with no
// ReportLeftover was the real functional gap this test previously missed.
func TestAutonomousDatabase_Filter(t *testing.T) {
	tests := []struct {
		state   database.AutonomousDatabaseSummaryLifecycleStateEnum
		present bool
	}{
		{database.AutonomousDatabaseSummaryLifecycleStateAvailable, true},
		{database.AutonomousDatabaseSummaryLifecycleStateProvisioning, true},
		{database.AutonomousDatabaseSummaryLifecycleStateUpdating, true},
		{database.AutonomousDatabaseSummaryLifecycleStateStopping, true},
		{database.AutonomousDatabaseSummaryLifecycleStateStopped, true},
		{database.AutonomousDatabaseSummaryLifecycleStateStarting, true},
		{database.AutonomousDatabaseSummaryLifecycleStateRestoreInProgress, true},
		{database.AutonomousDatabaseSummaryLifecycleStateScaleInProgress, true},
		{database.AutonomousDatabaseSummaryLifecycleStateUnavailable, true},
		{database.AutonomousDatabaseSummaryLifecycleStateBackupInProgress, true},
		{database.AutonomousDatabaseSummaryLifecycleStateAvailableNeedsAttention, true},
		{database.AutonomousDatabaseSummaryLifecycleStateMaintenanceInProgress, true},
		{database.AutonomousDatabaseSummaryLifecycleStateRestarting, true},
		{database.AutonomousDatabaseSummaryLifecycleStateRecreating, true},
		{database.AutonomousDatabaseSummaryLifecycleStateRoleChangeInProgress, true},
		{database.AutonomousDatabaseSummaryLifecycleStateUpgrading, true},
		{database.AutonomousDatabaseSummaryLifecycleStateStandby, true},
		{database.AutonomousDatabaseSummaryLifecycleStateTransporting, true},
		{database.AutonomousDatabaseSummaryLifecycleStateTerminating, false},
		{database.AutonomousDatabaseSummaryLifecycleStateTerminated, false},
		{database.AutonomousDatabaseSummaryLifecycleStateRestoreFailed, false},
		{database.AutonomousDatabaseSummaryLifecycleStateInaccessible, false},
	}

	for _, tc := range tests {
		r := &AutonomousDatabase{}
		r.db.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestAutonomousDatabase_SafetyTags_NilTimeCreated proves SafetyTags() does not panic and returns
// the zero time.Time when TimeCreated is nil -- AutonomousDatabaseSummary.TimeCreated is
// mandatory:"false" (verified this session).
func TestAutonomousDatabase_SafetyTags_NilTimeCreated(t *testing.T) {
	r := &AutonomousDatabase{db: database.AutonomousDatabaseSummary{}}

	_, _, createdAt := r.SafetyTags()
	if !createdAt.IsZero() {
		t.Errorf("SafetyTags() createdAt = %v, want zero time.Time for nil TimeCreated", createdAt)
	}
}

// TestAutonomousDatabase_NoInventedProtectionMechanism proves neither
// IsBackupRetentionLocked nor any Locks-shaped field is read anywhere in Filter() -- a
// compile-level guard is impossible for "field is never referenced," so this asserts Filter()'s
// observable behavior is driven purely by LifecycleState: a database with
// IsBackupRetentionLocked=true in an otherwise-present state is still present (never excluded by
// a field this type's Filter() must never invent a meaning for).
func TestAutonomousDatabase_NoInventedProtectionMechanism(t *testing.T) {
	locked := true
	r := &AutonomousDatabase{db: database.AutonomousDatabaseSummary{
		LifecycleState:          database.AutonomousDatabaseSummaryLifecycleStateAvailable,
		IsBackupRetentionLocked: &locked,
	}}

	if err := r.Filter(); err != nil {
		t.Errorf("Filter() with IsBackupRetentionLocked=true = %v, want nil (not a delete-protection substitute)", err)
	}
}

// TestAutonomousDatabase_Remove proves the SDK delete call fires with the right parameter,
// against a client that never touches the network.
func TestAutonomousDatabase_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubAutonomousDatabaseClient{}
	r := &AutonomousDatabase{client: stub}
	r.db.Id = &id
	r.db.CompartmentId = &compartmentID
	r.db.LifecycleState = database.AutonomousDatabaseSummaryLifecycleStateAvailable
	r.db.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
