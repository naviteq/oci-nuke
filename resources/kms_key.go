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

// KmsKeyResourceType is the registry.Registration.Name for KmsKey.
const KmsKeyResourceType = "KmsKey"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     KmsKeyResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &KmsKey{},
		Lister:   &kmsKeyLister{},
		// A secret is encrypted by a key, so the key waits for secrets. Vault declares its own
		// KmsKey edge (resources/vault.go).
		DependsOn: []string{VaultSecretResourceType},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type kmsKeyLister struct{}

// List satisfies registry.Lister. The nested vault/key enumeration logic itself lives in
// kmsKeyList (resources/kms_support.go), kept separate so it is unit-testable against stub
// clients without ever constructing real KmsVaultClient/KmsManagementClient instances. KmsKey is
// Regional, like Vault.
func (l *kmsKeyLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("kmsKeyLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	vaultClient, err := o.Clients.KmsVault(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing KmsVaultClient for %s: %w", o.Region, err)
	}

	// clientFor is the CONSUMER side of pkg/clients.Cache.KmsManagement, the codebase's first
	// non-region-keyed accessor: keyed per-vault by that vault's own ManagementEndpoint, never a
	// bare region string. Captured as a closure so kmsKeyList (resources/kms_support.go) stays
	// isolated from ocinuke.ListerOpts/pkg/clients.Cache -- the same isolation shape every other
	// nested-enumeration lister in this package uses.
	clients := o.Clients
	clientFor := func(managementEndpoint string) (kmsManagementClient, error) {
		return clients.KmsManagement(managementEndpoint)
	}

	return kmsKeyList(ctx, vaultClient, clientFor, o.CompartmentID, o.VaultDeletionWindowDays)
}

// KmsKey wraps one keymanagement.KeySummary, plus the compartmentID inherited from the vault
// enumeration loop (kms_support.go's kmsKeysForVault -- KeySummary.CompartmentId IS
// mandatory:"true" on its own, this is kept for consistency with this wave's other
// nested-enumeration types) and the run's own deletionWindowDays (mirroring resources/vault.go's
// identical Remove()-time need).
type KmsKey struct {
	client             kmsManagementClient
	key                keymanagement.KeySummary
	compartmentID      string
	deletionWindowDays int
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. Returns the inherited field, not a re-derivation from r.key.CompartmentId.
func (r *KmsKey) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *KmsKey) UniqueKey() string { return *r.key.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment.
//
// keymanagement.KeySummaryLifecycleStateEnum (verified this session directly against
// keymanagement/key_summary.go) has THIRTEEN values, not the eleven this plan's own read_first
// note counted (it omitted BackupInProgress and Restoring -- documented here as a correction,
// not silently worked around): Creating, Enabling, Enabled, Disabling, Disabled, Deleting,
// Deleted, PendingDeletion, SchedulingDeletion, CancellingDeletion, Updating, BackupInProgress,
// Restoring (Go constant-name suffixes). Present = Creating, Enabling, Enabled, Disabling,
// Disabled, CancellingDeletion -- exactly the six states this plan's own action text names,
// narrower than Vault's own present set (which includes Updating/BackupInProgress/Restoring).
// PendingDeletion and SchedulingDeletion are excluded AND reported via ocinuke.ReportLeftover
// with scope.ReasonScheduledDeletion, identical to Vault.Filter()'s shape. Every other state --
// Deleting, Deleted, and the two states this plan's own read_first note did not account for
// (Updating, BackupInProgress, Restoring) -- falls to the default branch, excluded without
// reporting: "any state not explicitly listed here excludes" (resources/instance.go's own
// Filter() doc comment), the project's standing fail-safe convention for a state this plan does
// not explicitly classify as removable.
func (r *KmsKey) Filter() error {
	switch r.key.LifecycleState {
	case keymanagement.KeySummaryLifecycleStateCreating,
		keymanagement.KeySummaryLifecycleStateEnabling,
		keymanagement.KeySummaryLifecycleStateEnabled,
		keymanagement.KeySummaryLifecycleStateDisabling,
		keymanagement.KeySummaryLifecycleStateDisabled,
		keymanagement.KeySummaryLifecycleStateCancellingDeletion:
		return nil
	case keymanagement.KeySummaryLifecycleStatePendingDeletion,
		keymanagement.KeySummaryLifecycleStateSchedulingDeletion:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonScheduledDeletion,
			ResourceType:  KmsKeyResourceType,
			ResourceID:    *r.key.Id,
			CompartmentID: r.compartmentID,
			Detail:        "key already scheduled for deletion",
		})
		return fmt.Errorf("KmsKey is %s, not available", r.key.LifecycleState)
	default:
		return fmt.Errorf("KmsKey is %s, not available", r.key.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *KmsKey) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.key
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove always passes an explicit TimeOfDeletion, identical shape to Vault.Remove() -- see that
// method's doc comment for the full T-05-06-02 floor-clamp rationale, which applies unchanged
// here. The setting name stays vault.deletion-window-days even though it governs both Vault and
// KmsKey scheduling (05-CONTEXT.md's decision covers "the mandatory scheduled-deletion window" as
// one concept, not two independently configurable ones) -- no second settings key is added for
// keys. Filter() above already excludes an already-PENDING_DELETION/SCHEDULING_DELETION key
// before Remove() is ever reached, so no redundant already-scheduled check is added here, mirroring
// Vault.Remove()'s own precedent.
func (r *KmsKey) Remove(ctx context.Context) error {
	days := r.deletionWindowDays
	if days < vaultDeletionWindowFloorDays {
		days = vaultDeletionWindowFloorDays
	}
	deletionWindow := time.Duration(days) * 24 * time.Hour
	timeOfDeletion := common.SDKTime{Time: time.Now().Add(deletionWindow)}

	_, err := r.client.ScheduleKeyDeletion(ctx, keymanagement.ScheduleKeyDeletionRequest{
		KeyId: r.key.Id,
		ScheduleKeyDeletionDetails: keymanagement.ScheduleKeyDeletionDetails{
			TimeOfDeletion: &timeOfDeletion,
		},
	})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper (using the inherited compartmentID field, not r.key.CompartmentId
// directly, matching GetCompartmentID's own choice), plus vault_id -- the owning vault's OCID,
// KeySummary's own field (mandatory:"true").
func (r *KmsKey) Properties() types.Properties {
	x := r.key
	return baseProperties(x.Id, x.DisplayName, &r.compartmentID, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set("vault_id", x.VaultId)
}
