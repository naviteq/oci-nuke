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
	"github.com/oracle/oci-go-sdk/v65/vault"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// secretDeletionWindowFloorDays is the shortest window OCI accepts for a scheduled secret
// deletion. Remove() clamps to it, as Vault.Remove() clamps to its own floor.
const secretDeletionWindowFloorDays = 1

// vaultSecretClient is the slice of vault.VaultsClient VaultSecret needs, satisfied in production
// by pkg/clients.Cache.Vaults.
type vaultSecretClient interface {
	ListSecrets(ctx context.Context, req vault.ListSecretsRequest) (vault.ListSecretsResponse, error)
	ScheduleSecretDeletion(
		ctx context.Context, req vault.ScheduleSecretDeletionRequest,
	) (vault.ScheduleSecretDeletionResponse, error)
}

// VaultSecretResourceType is the registry.Registration.Name for VaultSecret.
const VaultSecretResourceType = "VaultSecret"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VaultSecretResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &VaultSecret{},
		Lister:   &vaultSecretLister{},
		// No DependsOn. KmsKey and Vault wait for secrets instead: a secret is encrypted by a
		// key and lives in a vault, so it goes first.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type vaultSecretLister struct{}

// List satisfies registry.Lister. Secrets are listed by the compartment they belong to, which
// need not be the compartment of the vault holding them: a fixture's secret in a shared vault
// elsewhere is still this compartment's secret.
func (l *vaultSecretLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("vaultSecretLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Vaults(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing VaultsClient for %s: %w", o.Region, err)
	}

	return vaultSecretList(ctx, client, o.CompartmentID, o.SecretDeletionWindowDays)
}

// vaultSecretList paginates ListSecrets and wraps every item, carrying the run's deletion window.
func vaultSecretList(
	ctx context.Context,
	client vaultSecretClient,
	compartmentID string,
	deletionWindowDays int,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := vault.ListSecretsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListSecrets(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing VaultSecret in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &VaultSecret{
				client:             client,
				secret:             resp.Items[i],
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

// VaultSecret wraps one vault.SecretSummary. Id, CompartmentId, SecretName, LifecycleState and
// TimeCreated are mandatory on it, so they are dereferenced without nil checks.
type VaultSecret struct {
	client             vaultSecretClient
	secret             vault.SecretSummary
	deletionWindowDays int
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped.
func (r *VaultSecret) GetCompartmentID() string { return *r.secret.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter -- the OCID, never the secret name.
func (r *VaultSecret) UniqueKey() string { return *r.secret.Id }

// Filter treats ACTIVE, CREATING, UPDATING, FAILED and a deletion being called off as present. A secret
// that is CREATING or UPDATING refuses a schedule with a 409, which holdOn409 turns into a wait
// until it settles.
//
// SCHEDULING_DELETION and PENDING_DELETION are excluded and reported as scheduled-deletion, with
// the date when OCI gives one: the secret, and its name, are still there until that date, and the
// compartment holding it cannot be deleted before it either.
//
// A replica is excluded without a report. OCI removes it with its source secret, in the source's
// region, and refuses to schedule it directly.
func (r *VaultSecret) Filter() error {
	if r.secret.IsReplica != nil && *r.secret.IsReplica {
		return fmt.Errorf("VaultSecret is a replica; it is deleted with its source secret")
	}

	switch r.secret.LifecycleState {
	case vault.SecretSummaryLifecycleStateActive,
		vault.SecretSummaryLifecycleStateCreating,
		vault.SecretSummaryLifecycleStateUpdating,
		vault.SecretSummaryLifecycleStateCancellingDeletion,
		vault.SecretSummaryLifecycleStateFailed:
		return nil
	case vault.SecretSummaryLifecycleStatePendingDeletion,
		vault.SecretSummaryLifecycleStateSchedulingDeletion:
		detail := "secret " + *r.secret.SecretName + " already scheduled for deletion"
		if r.secret.TimeOfDeletion != nil {
			detail += " at " + r.secret.TimeOfDeletion.UTC().Format(time.RFC3339)
		}
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonScheduledDeletion,
			ResourceType:  VaultSecretResourceType,
			ResourceID:    *r.secret.Id,
			CompartmentID: *r.secret.CompartmentId,
			Detail:        detail,
		})
		return fmt.Errorf("VaultSecret is %s, not available", r.secret.LifecycleState)
	default:
		return fmt.Errorf("VaultSecret is %s, not available", r.secret.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated.
func (r *VaultSecret) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.secret
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove schedules the deletion with an explicit TimeOfDeletion, never the SDK's 30-day default.
// Once it lands, the next re-list sees PENDING_DELETION and Filter() converges the item.
func (r *VaultSecret) Remove(ctx context.Context) error {
	days := r.deletionWindowDays
	if days < secretDeletionWindowFloorDays {
		days = secretDeletionWindowFloorDays
	}
	timeOfDeletion := common.SDKTime{Time: time.Now().Add(time.Duration(days) * 24 * time.Hour)}

	_, err := r.client.ScheduleSecretDeletion(ctx, vault.ScheduleSecretDeletionRequest{
		SecretId: r.secret.Id,
		ScheduleSecretDeletionDetails: vault.ScheduleSecretDeletionDetails{
			TimeOfDeletion: &timeOfDeletion,
		},
	})
	return holdOn409(err)
}

// Properties exposes baseProperties, with the secret name as name, plus the vault and key the
// secret belongs to -- vault_id is what a filter keeps a shared vault's own secrets out with.
func (r *VaultSecret) Properties() types.Properties {
	x := r.secret
	return baseProperties(x.Id, x.SecretName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set("vault_id", x.VaultId).
		Set("key_id", x.KeyId)
}
