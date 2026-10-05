package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/keymanagement"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubVaultClient implements vaultClient against in-memory data -- zero network access.
type stubVaultClient struct {
	items       []keymanagement.VaultSummary
	scheduled   []keymanagement.ScheduleVaultDeletionRequest
	listErr     error
	scheduleErr error
}

func (s *stubVaultClient) ListVaults(
	_ context.Context, _ keymanagement.ListVaultsRequest,
) (keymanagement.ListVaultsResponse, error) {
	if s.listErr != nil {
		return keymanagement.ListVaultsResponse{}, s.listErr
	}
	return keymanagement.ListVaultsResponse{Items: s.items}, nil
}

func (s *stubVaultClient) ScheduleVaultDeletion(
	_ context.Context, req keymanagement.ScheduleVaultDeletionRequest,
) (keymanagement.ScheduleVaultDeletionResponse, error) {
	s.scheduled = append(s.scheduled, req)
	if s.scheduleErr != nil {
		return keymanagement.ScheduleVaultDeletionResponse{}, s.scheduleErr
	}
	return keymanagement.ScheduleVaultDeletionResponse{}, nil
}

// TestVaultLister_List proves vaultList wraps every returned item as a Vault, threading the run's
// deletionWindowDays value down.
func TestVaultLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	endpoint := testVaultManagementEndpointA
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubVaultClient{
		items: []keymanagement.VaultSummary{
			{
				Id: &id, CompartmentId: &compartmentID, ManagementEndpoint: &endpoint,
				LifecycleState: keymanagement.VaultSummaryLifecycleStateActive,
				TimeCreated:    &timeCreated,
			},
		},
	}

	got, err := vaultList(context.Background(), stub, compartmentID, 14)
	if err != nil {
		t.Fatalf("vaultList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("vaultList() returned %d resources, want 1", len(got))
	}
	v, ok := got[0].(*Vault)
	if !ok {
		t.Fatalf("vaultList()[0] is %T, want *Vault", got[0])
	}
	if v.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", v.GetCompartmentID(), compartmentID)
	}
	if v.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", v.UniqueKey(), id)
	}
	if v.deletionWindowDays != 14 {
		t.Errorf("deletionWindowDays = %d, want 14", v.deletionWindowDays)
	}
}

// TestVault_Filter is table-driven over every keymanagement.VaultSummaryLifecycleStateEnum
// value -- ACTIVE/UPDATING/BACKUP_IN_PROGRESS/RESTORING are present; every other state (including
// PENDING_DELETION/SCHEDULING_DELETION) is excluded.
func TestVault_Filter(t *testing.T) {
	tests := []struct {
		state   keymanagement.VaultSummaryLifecycleStateEnum
		present bool
	}{
		{keymanagement.VaultSummaryLifecycleStateActive, true},
		{keymanagement.VaultSummaryLifecycleStateUpdating, true},
		{keymanagement.VaultSummaryLifecycleStateBackupInProgress, true},
		{keymanagement.VaultSummaryLifecycleStateRestoring, true},
		{keymanagement.VaultSummaryLifecycleStateCreating, false},
		{keymanagement.VaultSummaryLifecycleStateDeleting, false},
		{keymanagement.VaultSummaryLifecycleStateDeleted, false},
		{keymanagement.VaultSummaryLifecycleStateCancellingDeletion, false},
		{keymanagement.VaultSummaryLifecycleStatePendingDeletion, false},
		{keymanagement.VaultSummaryLifecycleStateSchedulingDeletion, false},
		{keymanagement.VaultSummaryLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	for _, tc := range tests {
		id := testResourceOCID
		compartmentID := testCompartmentOCID
		r := &Vault{vault: keymanagement.VaultSummary{Id: &id, CompartmentId: &compartmentID}}
		r.vault.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil", tc.state)
		}
	}
}

// TestVault_Filter_PendingDeletion_ReportsScheduledDeletion is the named test T-05-06-03/04
// require: PENDING_DELETION and SCHEDULING_DELETION vaults are excluded AND reported via
// ocinuke.ReportLeftover with scope.ReasonScheduledDeletion, never colored as a plain api-error.
func TestVault_Filter_PendingDeletion_ReportsScheduledDeletion(t *testing.T) {
	for _, state := range []keymanagement.VaultSummaryLifecycleStateEnum{
		keymanagement.VaultSummaryLifecycleStatePendingDeletion,
		keymanagement.VaultSummaryLifecycleStateSchedulingDeletion,
	} {
		var got []*scope.SkipEvent
		restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
			got = append(got, evt)
		})

		id := testResourceOCID
		compartmentID := testCompartmentOCID
		r := &Vault{vault: keymanagement.VaultSummary{
			Id: &id, CompartmentId: &compartmentID, LifecycleState: state,
		}}

		if err := r.Filter(); err == nil {
			t.Fatalf("Filter() with state %s = nil, want non-nil (must be excluded)", state)
		}
		if len(got) != 1 {
			t.Fatalf("ReportLeftover called %d times for state %s, want 1", len(got), state)
		}
		if got[0].Reason != scope.ReasonScheduledDeletion {
			t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonScheduledDeletion)
		}
		if got[0].ResourceType != VaultResourceType {
			t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, VaultResourceType)
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

// TestVault_Remove_ExplicitTimeOfDeletion proves Remove() always computes an explicit
// TimeOfDeletion at least vaultDeletionWindowFloorDays (168h) in the future when deletionWindowDays
// is 0 (the floor clamp fires), and exactly N days when explicitly set to a valid N in [7,30] --
// T-05-06-02's Remove()-level enforcement point.
func TestVault_Remove_ExplicitTimeOfDeletion(t *testing.T) {
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
			stub := &stubVaultClient{}
			r := &Vault{client: stub, vault: keymanagement.VaultSummary{Id: &id}, deletionWindowDays: tc.deletionWindowDays}

			before := time.Now()
			if err := r.Remove(context.Background()); err != nil {
				t.Fatalf("Remove() error = %v, want nil", err)
			}
			after := time.Now()

			if len(stub.scheduled) != 1 {
				t.Fatalf("ScheduleVaultDeletion called %d times, want 1", len(stub.scheduled))
			}
			req := stub.scheduled[0]
			if req.VaultId == nil || *req.VaultId != id {
				t.Errorf("ScheduleVaultDeletionRequest.VaultId = %v, want %q", req.VaultId, id)
			}
			if req.TimeOfDeletion == nil {
				t.Fatal("ScheduleVaultDeletionDetails.TimeOfDeletion is nil, want an explicit value")
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

// TestVault_Properties proves Properties() surfaces management_endpoint alongside the standard
// baseProperties fields.
func TestVault_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	endpoint := testVaultManagementEndpointA
	displayName := "test-vault"

	r := &Vault{vault: keymanagement.VaultSummary{
		Id: &id, CompartmentId: &compartmentID, DisplayName: &displayName,
		ManagementEndpoint: &endpoint, LifecycleState: keymanagement.VaultSummaryLifecycleStateActive,
	}}

	props := r.Properties()
	if got := props.Get(propID); got != id {
		t.Errorf("Properties()[%q] = %q, want %q", propID, got, id)
	}
	if got := props.Get("management_endpoint"); got != endpoint {
		t.Errorf("Properties()[%q] = %q, want %q", "management_endpoint", got, endpoint)
	}
}

// TestVault_DependsOn_KmsKey proves Vault's registration declares DependsOn: ["KmsKey",
// "VaultSecret"] -- 05-CONTEXT.md's locked no-cascade decision, extended to secrets, verified
// against the real, package-init-installed registration.
func TestVault_DependsOn_KmsKey(t *testing.T) {
	reg := registry.GetRegistration(VaultResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", VaultResourceType)
	}
	want := []string{"KmsKey", VaultSecretResourceType}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("Vault DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("Vault DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}

const (
	vaultScheduledDeletionTestType   = "VaultScheduledDeletionTestFixture"
	vaultScheduledDeletionTestRegion = "us-ashburn-1"
)

// fixtureVaultLister always returns the one fixed *Vault resource it is constructed with --
// mirroring resources_test/scheduled_deletion_test.go's scheduledDeletionLister shape.
type fixtureVaultLister struct {
	vault *Vault
}

func (l *fixtureVaultLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{l.vault}, nil
}

// TestVault_PendingDeletion_EndToEnd_ReportedAsScheduledDeletionNotAPIError is the end-to-end
// proof Task 3's acceptance criteria require, mirroring resources_test/
// scheduled_deletion_test.go's runScheduledDeletionFixture shape but exercising the REAL,
// production Vault.Filter() implementation (not a synthetic fixture type) through a real
// *libnuke.Nuke.Run() pass. Filter()-time exclusion means the item never leaves ItemStateNew for
// the round-based removal loop -- it is marked ItemStateFiltered directly during Scan(), so
// BuildFromQueue's own leftoverReason classifier (ClassifyLeftover) is never even consulted for
// this item; the ONLY source of scope.ReasonScheduledDeletion is the ReportLeftover side channel
// installed via ocinuke.SetRunContext, merged in afterward via plan.MergeSkipEvents as a new
// StateSkipped entry (pkg/plan/build.go's mergeSkipEvent: a SkipEvent never upgrades a
// StateFiltered entry, only StateLeftover -- it appends a fresh StateSkipped entry instead). This
// is registered under a distinct fixture type name, never "Vault" itself, so the package's own
// real init()-installed registration is untouched (resources/dynamic_group_test.go's own
// established precedent for why: this package has no TestMain-based registry snapshot/restore).
func TestVault_PendingDeletion_EndToEnd_ReportedAsScheduledDeletionNotAPIError(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	fixture := &Vault{
		client: &stubVaultClient{},
		vault: keymanagement.VaultSummary{
			Id: &id, CompartmentId: &compartmentID,
			LifecycleState: keymanagement.VaultSummaryLifecycleStatePendingDeletion,
			TimeCreated:    &timeCreated,
		},
	}

	var events []scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		events = append(events, *evt)
	})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     vaultScheduledDeletionTestType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fixtureVaultLister{vault: fixture},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         vaultScheduledDeletionTestRegion + "/" + compartmentID,
		ResourceTypes: []string{vaultScheduledDeletionTestType},
		Opts:          &ocinuke.ListerOpts{Region: vaultScheduledDeletionTestRegion, CompartmentID: compartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil (a Filter()-excluded item is not a run failure)", err)
	}

	unmerged := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	merged := plan.MergeSkipEvents(unmerged, events)

	skipped := entriesWithState(merged, plan.StateSkipped)
	if len(skipped) != 1 {
		t.Fatalf("got %d StateSkipped entries after merge, want exactly 1 (entries: %+v)", len(skipped), merged)
	}
	if skipped[0].Reason != scope.ReasonScheduledDeletion {
		t.Errorf("merged StateSkipped entry Reason = %q, want %q (not %q)",
			skipped[0].Reason, scope.ReasonScheduledDeletion, scope.ReasonAPIError)
	}
	if skipped[0].ResourceID != id {
		t.Errorf("merged StateSkipped entry ResourceID = %q, want %q", skipped[0].ResourceID, id)
	}
}

// entriesWithState mirrors resources_test/leftover_queue_test.go's own helper of the same name --
// duplicated here (not imported) since resources_test is a separate, external test-only package
// this package cannot import from.
func entriesWithState(entries []plan.Entry, state plan.EntryState) []plan.Entry {
	var out []plan.Entry
	for _, e := range entries {
		if e.State == state {
			out = append(out, e)
		}
	}
	return out
}
