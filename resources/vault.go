// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/keymanagement"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// vaultDeletionWindowFloorDays is the minimum window OCI itself enforces for a scheduled vault
// deletion (verified this session, keymanagement/schedule_vault_deletion_details.go's doc
// comment: "The specified time must be between 7 and 30 days from the time when the request is
// received"). Remove() below clamps to this floor defensively, independent of
// ocinuke.VaultDeletionWindowDaysFromSettings already enforcing the same floor upstream --
// T-05-06-02's second, independent enforcement point for the one hard safety requirement in this
// plan.
const vaultDeletionWindowFloorDays = 7

// vaultClient is the narrow slice of keymanagement's KmsVaultClient this lister needs -- a
// hand-written interface so Vault is stub-testable with zero network access, satisfied in
// production by pkg/clients.Cache.KmsVault.
type vaultClient interface {
	ListVaults(ctx context.Context, req keymanagement.ListVaultsRequest) (keymanagement.ListVaultsResponse, error)
	ScheduleVaultDeletion(
		ctx context.Context, req keymanagement.ScheduleVaultDeletionRequest,
	) (keymanagement.ScheduleVaultDeletionResponse, error)
}

// VaultResourceType is the registry.Registration.Name for Vault.
const VaultResourceType = "Vault"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VaultResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Vault{},
		Lister:   &vaultLister{},
		// DependsOn names KmsKey -- 05-CONTEXT.md's locked no-cascade decision: keys are
		// enumerated and scheduled independently of their vault (resources/kms_key.go), so they
		// must be removed BEFORE the vault that hosts them, never assumed to be cleaned up as a
		// side effect of ScheduleVaultDeletion. A bare string literal, per every other
		// cross-plan-boundary DependsOn edge in this wave (resources/vcn.go's own convention).
		// VaultSecret is listed too, not left to arrive through KmsKey: with every key in a
		// vault protected, nothing would hold the vault back for its secrets.
		DependsOn: []string{"KmsKey", VaultSecretResourceType},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type vaultLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in vaultList, kept
// separate so it is unit-testable against a stub client without ever constructing a real
// KmsVaultClient. Vault is Regional -- only IAM's four types are Global in this wave.
func (l *vaultLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("vaultLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.KmsVault(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing KmsVaultClient for %s: %w", o.Region, err)
	}

	return vaultList(ctx, client, o.CompartmentID, o.VaultDeletionWindowDays)
}

// vaultList paginates keymanagement.ListVaults and wraps every returned item as a Vault, each
// carrying the run's own deletionWindowDays value (threaded through from ocinuke.ListerOpts, the
// same shape instanceList threads client/compartmentID) so Remove() below never has to reach back
// into the run's settings on its own. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on
// purpose -- this is what the list test below exercises against a stub, with zero network access.
func vaultList(
	ctx context.Context,
	client vaultClient,
	compartmentID string,
	deletionWindowDays int,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := keymanagement.ListVaultsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListVaults(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Vault in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Vault{
				client:             client,
				vault:              resp.Items[i],
				deletionWindowDays: deletionWindowDays,
			})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Vault wraps one keymanagement.VaultSummary, plus the run's own deletionWindowDays (captured at
// list time, see vaultList above). Every identity/time/endpoint field on VaultSummary used here
// (Id, CompartmentId, ManagementEndpoint, LifecycleState, TimeCreated) is mandatory:"true"
// (verified this session, keymanagement/vault_summary.go) -- no nil-checks needed.
type Vault struct {
	client             vaultClient
	vault              keymanagement.VaultSummary
	deletionWindowDays int
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Vault) GetCompartmentID() string { return *r.vault.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Vault) UniqueKey() string { return *r.vault.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// keymanagement.VaultSummaryLifecycleStateEnum (the real type VaultSummary.LifecycleState is
// declared as -- NOT VaultLifecycleStateEnum, the sibling enum the fully-hydrated Vault type
// returned by GetVault/ScheduleVaultDeletion uses; verified this session directly against
// keymanagement/vault_summary.go) has ten values: Creating, Active, Deleting, Deleted,
// PendingDeletion, SchedulingDeletion, CancellingDeletion, Updating, BackupInProgress, Restoring
// (Go constant-name suffixes; see keymanagement.VaultSummaryLifecycleStateEnum's own doc comment
// for the wire-format string each maps to). Active/Updating/BackupInProgress/Restoring are
// present. PendingDeletion and SchedulingDeletion are excluded AND reported via
// ocinuke.ReportLeftover with scope.ReasonScheduledDeletion -- 05-CONTEXT.md's locked decision: a
// vault already scheduled for deletion is a distinct, non-error, non-silent outcome, never
// re-attempted and never colored as a failure. VaultSummary carries no TimeOfDeletion field at
// all (only the fully-hydrated Vault type returned by GetVault does), so the SkipEvent.Detail
// below cannot name the exact scheduled date without an extra GetVault call this plan does not
// make -- documented here rather than silently fabricating a field VaultSummary does not have.
// Every other state (Creating, Deleting, Deleted, CancellingDeletion) is excluded without
// reporting, matching 05-RESEARCH.md's own vetted example: Creating is arguably "present" too,
// but the vetted example deliberately treats it as excluded, and this file follows that example
// exactly rather than second-guessing it.
func (r *Vault) Filter() error {
	switch r.vault.LifecycleState {
	case keymanagement.VaultSummaryLifecycleStateActive,
		keymanagement.VaultSummaryLifecycleStateUpdating,
		keymanagement.VaultSummaryLifecycleStateBackupInProgress,
		keymanagement.VaultSummaryLifecycleStateRestoring:
		return nil
	case keymanagement.VaultSummaryLifecycleStatePendingDeletion,
		keymanagement.VaultSummaryLifecycleStateSchedulingDeletion:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonScheduledDeletion,
			ResourceType:  VaultResourceType,
			ResourceID:    *r.vault.Id,
			CompartmentID: *r.vault.CompartmentId,
			Detail:        "vault already scheduled for deletion",
		})
		return fmt.Errorf("Vault is %s, not available", r.vault.LifecycleState)
	default:
		return fmt.Errorf("Vault is %s, not available", r.vault.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *Vault) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.vault
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove always passes an explicit TimeOfDeletion -- 05-CONTEXT.md's locked decision: the
// mandatory scheduled-deletion window is never left to the SDK's own 30-day default. deletionWindow
// is computed from r.deletionWindowDays, clamped up to vaultDeletionWindowFloorDays whenever it is
// below that floor (defensive: a test-constructed Vault that never set the field, or any future caller
// that bypasses ocinuke.VaultDeletionWindowDaysFromSettings' own floor enforcement, must never compute a
// below-floor, zero, or negative duration -- T-05-06-02's second, independent enforcement point;
// production always has this field set to at least 7 via the settings default). Filter() above
// already excludes an already-PENDING_DELETION/SCHEDULING_DELETION vault before Remove() is ever
// reached, so no redundant already-scheduled check is added here (05-RESEARCH.md's Anti-Patterns
// list this explicitly as unnecessary, mirroring MountTarget/RetentionRule's own precedent of no
// redundant in-Remove() state check).
func (r *Vault) Remove(ctx context.Context) error {
	days := r.deletionWindowDays
	if days < vaultDeletionWindowFloorDays {
		days = vaultDeletionWindowFloorDays
	}
	deletionWindow := time.Duration(days) * 24 * time.Hour
	timeOfDeletion := common.SDKTime{Time: time.Now().Add(deletionWindow)}

	_, err := r.client.ScheduleVaultDeletion(ctx, keymanagement.ScheduleVaultDeletionRequest{
		VaultId: r.vault.Id,
		ScheduleVaultDeletionDetails: keymanagement.ScheduleVaultDeletionDetails{
			TimeOfDeletion: &timeOfDeletion,
		},
	})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper, plus management_endpoint -- the per-vault endpoint KmsKey's own
// lister uses to construct its own, non-region-keyed KmsManagementClient (resources/kms_key.go).
func (r *Vault) Properties() types.Properties {
	x := r.vault
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set("management_endpoint", x.ManagementEndpoint)
}
