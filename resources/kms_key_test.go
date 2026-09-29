package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/keymanagement"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// TestKmsKey_Filter is table-driven over every keymanagement.KeySummaryLifecycleStateEnum value
// (all thirteen -- see kms_key.go's Filter() doc comment for the plan's own read_first
// undercount). Present = Creating/Enabling/Enabled/Disabling/Disabled/CancellingDeletion; every
// other state (including PendingDeletion/SchedulingDeletion, and the two states this plan's
// read_first note omitted -- Updating/BackupInProgress/Restoring) is excluded.
func TestKmsKey_Filter(t *testing.T) {
	tests := []struct {
		state   keymanagement.KeySummaryLifecycleStateEnum
		present bool
	}{
		{keymanagement.KeySummaryLifecycleStateCreating, true},
		{keymanagement.KeySummaryLifecycleStateEnabling, true},
		{keymanagement.KeySummaryLifecycleStateEnabled, true},
		{keymanagement.KeySummaryLifecycleStateDisabling, true},
		{keymanagement.KeySummaryLifecycleStateDisabled, true},
		{keymanagement.KeySummaryLifecycleStateCancellingDeletion, true},
		{keymanagement.KeySummaryLifecycleStateDeleting, false},
		{keymanagement.KeySummaryLifecycleStateDeleted, false},
		{keymanagement.KeySummaryLifecycleStatePendingDeletion, false},
		{keymanagement.KeySummaryLifecycleStateSchedulingDeletion, false},
		{keymanagement.KeySummaryLifecycleStateUpdating, false},
		{keymanagement.KeySummaryLifecycleStateBackupInProgress, false},
		{keymanagement.KeySummaryLifecycleStateRestoring, false},
		{keymanagement.KeySummaryLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	for _, tc := range tests {
		id := testResourceOCID
		r := &KmsKey{key: keymanagement.KeySummary{Id: &id}, compartmentID: testCompartmentOCID}
		r.key.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil", tc.state)
		}
	}
}

// TestKmsKey_Filter_PendingDeletion_ReportsScheduledDeletion proves PENDING_DELETION and
// SCHEDULING_DELETION keys are excluded AND reported via ocinuke.ReportLeftover with
// scope.ReasonScheduledDeletion, identical to Vault.Filter()'s own shape.
func TestKmsKey_Filter_PendingDeletion_ReportsScheduledDeletion(t *testing.T) {
	for _, state := range []keymanagement.KeySummaryLifecycleStateEnum{
		keymanagement.KeySummaryLifecycleStatePendingDeletion,
		keymanagement.KeySummaryLifecycleStateSchedulingDeletion,
	} {
		var got []*scope.SkipEvent
		restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
			got = append(got, evt)
		})

		id := testResourceOCID
		compartmentID := testCompartmentOCID
		r := &KmsKey{
			key:           keymanagement.KeySummary{Id: &id, LifecycleState: state},
			compartmentID: compartmentID,
		}

		if err := r.Filter(); err == nil {
			t.Fatalf("Filter() with state %s = nil, want non-nil (must be excluded)", state)
		}
		if len(got) != 1 {
			t.Fatalf("ReportLeftover called %d times for state %s, want 1", len(got), state)
		}
		if got[0].Reason != scope.ReasonScheduledDeletion {
			t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonScheduledDeletion)
		}
		if got[0].ResourceType != KmsKeyResourceType {
			t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, KmsKeyResourceType)
		}
		if got[0].ResourceID != id {
			t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
		}
		if got[0].CompartmentID != compartmentID {
			t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
		}

		restore()
	}
}

// TestKmsKey_Remove_ExplicitTimeOfDeletion proves Remove() always computes an explicit
// TimeOfDeletion at least vaultDeletionWindowFloorDays (168h) in the future when
// deletionWindowDays is 0 (the floor clamp fires), and exactly N days when explicitly set to a
// valid N in [7,30] -- mirroring TestVault_Remove_ExplicitTimeOfDeletion's own shape exactly.
func TestKmsKey_Remove_ExplicitTimeOfDeletion(t *testing.T) {
	tests := []struct {
		name               string
		deletionWindowDays int
		wantDays           int
	}{
		{name: "zero clamps to floor", deletionWindowDays: 0, wantDays: vaultDeletionWindowFloorDays},
		{name: "negative clamps to floor", deletionWindowDays: -3, wantDays: vaultDeletionWindowFloorDays},
		{name: "below-floor 3 days clamps to floor", deletionWindowDays: 3, wantDays: vaultDeletionWindowFloorDays},
		{name: "below-floor 6 days clamps to floor", deletionWindowDays: 6, wantDays: vaultDeletionWindowFloorDays},
		{name: "explicit 14 days honored exactly", deletionWindowDays: 14, wantDays: 14},
		{name: "explicit 30 days honored exactly", deletionWindowDays: 30, wantDays: 30},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := testResourceOCID
			stub := &stubKmsManagementClient{}
			r := &KmsKey{client: stub, key: keymanagement.KeySummary{Id: &id}, deletionWindowDays: tc.deletionWindowDays}

			before := time.Now()
			if err := r.Remove(context.Background()); err != nil {
				t.Fatalf("Remove() error = %v, want nil", err)
			}
			after := time.Now()

			if len(stub.scheduled) != 1 {
				t.Fatalf("ScheduleKeyDeletion called %d times, want 1", len(stub.scheduled))
			}
			req := stub.scheduled[0]
			if req.KeyId == nil || *req.KeyId != id {
				t.Errorf("ScheduleKeyDeletionRequest.KeyId = %v, want %q", req.KeyId, id)
			}
			if req.TimeOfDeletion == nil {
				t.Fatal("ScheduleKeyDeletionDetails.TimeOfDeletion is nil, want an explicit value")
			}

			wantWindow := time.Duration(tc.wantDays) * 24 * time.Hour
			minWant := before.Add(wantWindow).Add(-time.Second)
			maxWant := after.Add(wantWindow).Add(time.Second)
			got := req.TimeOfDeletion.Time
			if got.Before(minWant) || got.After(maxWant) {
				t.Errorf("TimeOfDeletion = %v, want within [%v, %v] (now + %d days)", got, minWant, maxWant, tc.wantDays)
			}
		})
	}
}

// TestKmsKey_Properties proves Properties() surfaces vault_id alongside the standard
// baseProperties fields, sourced from the inherited compartmentID field.
func TestKmsKey_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	vaultID := "ocid1.vault.oc1..a"
	displayName := "test-key"
	timeCreated := common.SDKTime{Time: time.Now()}

	r := &KmsKey{
		compartmentID: compartmentID,
		key: keymanagement.KeySummary{
			Id: &id, DisplayName: &displayName, VaultId: &vaultID,
			LifecycleState: keymanagement.KeySummaryLifecycleStateEnabled, TimeCreated: &timeCreated,
		},
	}

	props := r.Properties()
	if got := props.Get(propID); got != id {
		t.Errorf("Properties()[%q] = %q, want %q", propID, got, id)
	}
	if got := props.Get(propCompartmentID); got != compartmentID {
		t.Errorf("Properties()[%q] = %q, want %q", propCompartmentID, got, compartmentID)
	}
	if got := props.Get("vault_id"); got != vaultID {
		t.Errorf("Properties()[%q] = %q, want %q", "vault_id", got, vaultID)
	}
}
