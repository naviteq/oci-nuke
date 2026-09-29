// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
//
// kms_support.go is the one shared helper in this wave that legitimately combines two different
// patterns (Axis 2's nested enumeration + Axis 4's non-region-keyed client construction) --
// resources/kms_key.go's own List() is the CONSUMER side of the Cache.KmsManagement accessor
// pkg/clients/cache.go declares.
package resources

import (
	"context"
	"fmt"

	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/oracle/oci-go-sdk/v65/keymanagement"
	"github.com/sirupsen/logrus"
)

// kmsVaultLister is the narrow slice of keymanagement's KmsVaultClient kmsKeyList needs to
// enumerate every vault in a compartment -- the same ListVaults method resources/vault.go's own
// vaultClient interface exposes, declared separately here since this file's consumer (KmsKey) has
// no dependency on vaultClient's ScheduleVaultDeletion method at all.
type kmsVaultLister interface {
	ListVaults(ctx context.Context, req keymanagement.ListVaultsRequest) (keymanagement.ListVaultsResponse, error)
}

// kmsManagementClient is the narrow slice of keymanagement's KmsManagementClient kmsKeyList needs
// per vault -- ListKeys (enumeration) and ScheduleKeyDeletion (KmsKey.Remove()), the two calls
// this plan's per-vault, non-region-keyed client makes.
type kmsManagementClient interface {
	ListKeys(ctx context.Context, req keymanagement.ListKeysRequest) (keymanagement.ListKeysResponse, error)
	ScheduleKeyDeletion(
		ctx context.Context, req keymanagement.ScheduleKeyDeletionRequest,
	) (keymanagement.ScheduleKeyDeletionResponse, error)
}

// kmsKeyList enumerates every vault in compartmentID (paginated), then -- for EVERY vault,
// regardless of the vault's own lifecycle state (05-CONTEXT.md's locked no-cascade principle: a
// key inside a vault that is itself PENDING_DELETION may still need independent scheduling, never
// assumed to be handled as a side effect of the vault's own eventual deletion) -- constructs a
// per-vault keymanagement.KmsManagementClient via clientFor(vault.ManagementEndpoint) and fully
// paginates keymanagement.ListKeys against it, wrapping every returned KeySummary as a KmsKey
// with an INHERITED compartmentID (mirroring resources/export.go's inherited-CompartmentId
// shape -- kept for consistency with this wave's other nested-enumeration types, even though
// KeySummary.CompartmentId IS mandatory:"true" on its own, unlike ExportSummary's genuinely
// absent field). deletionWindowDays is threaded straight through to every constructed KmsKey, the
// same shape vaultList threads it to every constructed Vault.
//
// A vault whose ManagementEndpoint comes back nil (mandatory:"true" on VaultSummary -- should
// never happen on a real API response, but defends against an SDK/API inconsistency) skips that
// vault's key enumeration entirely rather than panicking or constructing a client against an
// empty endpoint string. This is a deliberate trade-off, documented here rather than silently
// applied: an unreachable vault's keys become a scan gap for that ONE vault only, not a
// scan-aborting error for the whole compartment -- matching this project's general
// fail-closed-on-safety-but-keep-scanning philosophy (an operator still gets every other vault's
// keys enumerated and scheduled, rather than losing the whole compartment's KMS coverage to one
// malformed record).
//
// TWO more vault-level cases reach the same "gap for one vault, not for the compartment"
// outcome, and until NR-780 only the nil-endpoint one above actually did (the intent was written
// down here; one of its three paths was implemented, and the other two returned the error and
// took the whole compartment's KMS coverage with them):
//
//  1. A DELETED vault is skipped BEFORE any client is constructed. OCI tears the management
//     endpoint down with the vault, so its hostname stops resolving while ListVaults keeps
//     returning the record -- observed as `dial tcp: lookup
//     envgzuclaaaqk-management.kms.eu-frankfurt-1.oraclecloud.com: no such host` on
//     terraform-modules run 33411740088. There is nothing left to schedule in a DELETED vault,
//     so this is skipped silently: a warning per deleted vault on every run would recreate the
//     log-noise problem one level down (NR-775).
//
//     This is NOT the no-cascade principle being weakened (05-CONTEXT.md, locked: a key inside a
//     vault that is itself being deleted may still need independent scheduling). PENDING_DELETION,
//     SCHEDULING_DELETION and every other non-terminal state keep a live endpoint and are still
//     fully enumerated -- TestKmsKeyList_NoCascadeFromNonActiveVault pins exactly that, and would
//     fail if this had been written as the much easier "skip vaults that are being deleted".
//
//  2. Any OTHER vault whose key enumeration fails is logged at Warn, naming the vault, and
//     scanning continues with the next one. That covers a genuine permission gap, a throttle, or
//     an endpoint unreachable for a reason that is not "the vault is gone" -- all things an
//     operator should see, none of which justify reporting zero keys for a compartment that has
//     them.
//
// Matching on the DNS resolver's error text was considered for case 1 and rejected: the
// lifecycle state is the honest signal, and a string match on a resolver message would silence a
// real outage as readily as an expected teardown.
func kmsKeyList(
	ctx context.Context,
	vaultClient kmsVaultLister,
	clientFor func(managementEndpoint string) (kmsManagementClient, error),
	compartmentID string,
	deletionWindowDays int,
) ([]resource.Resource, error) {
	var out []resource.Resource

	vReq := keymanagement.ListVaultsRequest{CompartmentId: &compartmentID}
	for {
		vResp, err := vaultClient.ListVaults(ctx, vReq)
		if err != nil {
			return nil, fmt.Errorf("listing Vault (for KmsKey enumeration) in %s: %w", compartmentID, err)
		}

		for i := range vResp.Items {
			v := vResp.Items[i]
			if v.ManagementEndpoint == nil {
				continue
			}
			// Terminal state: the endpoint is torn down with the vault and no key survives it.
			if v.LifecycleState == keymanagement.VaultSummaryLifecycleStateDeleted {
				logrus.WithFields(logrus.Fields{
					"vault_id":            derefOr(v.Id, "unknown"),
					"management_endpoint": *v.ManagementEndpoint,
				}).Debug("KmsKey: skipping key enumeration for a DELETED vault; its management endpoint no longer resolves")
				continue
			}

			keys, err := kmsKeysForVault(ctx, clientFor, *v.ManagementEndpoint, compartmentID, deletionWindowDays)
			if err != nil {
				// One vault's failure must not cost the compartment its KMS coverage.
				logrus.WithError(err).WithFields(logrus.Fields{
					"vault_id":            derefOr(v.Id, "unknown"),
					"vault_state":         string(v.LifecycleState),
					"management_endpoint": *v.ManagementEndpoint,
				}).Warn("KmsKey: enumerating this vault's keys failed; continuing with the remaining vaults, so this vault's keys are a scan gap")
				continue
			}
			out = append(out, keys...)
		}

		if vResp.OpcNextPage == nil {
			break
		}
		vReq.Page = vResp.OpcNextPage
	}

	return out, nil
}

// kmsKeysForVault constructs a per-vault keymanagement.KmsManagementClient via
// clientFor(managementEndpoint) and fully paginates keymanagement.ListKeys against it, wrapping
// every returned item as a KmsKey.
func kmsKeysForVault(
	ctx context.Context,
	clientFor func(managementEndpoint string) (kmsManagementClient, error),
	managementEndpoint, compartmentID string,
	deletionWindowDays int,
) ([]resource.Resource, error) {
	client, err := clientFor(managementEndpoint)
	if err != nil {
		return nil, fmt.Errorf("constructing KmsManagementClient for endpoint %s: %w", managementEndpoint, err)
	}

	var out []resource.Resource
	kReq := keymanagement.ListKeysRequest{CompartmentId: &compartmentID}
	for {
		kResp, err := client.ListKeys(ctx, kReq)
		if err != nil {
			return nil, fmt.Errorf("listing KmsKey for vault endpoint %s: %w", managementEndpoint, err)
		}
		for j := range kResp.Items {
			out = append(out, &KmsKey{
				client:             client,
				key:                kResp.Items[j],
				compartmentID:      compartmentID,
				deletionWindowDays: deletionWindowDays,
			})
		}
		if kResp.OpcNextPage == nil {
			break
		}
		kReq.Page = kResp.OpcNextPage
	}
	return out, nil
}

// derefOr returns *p, or fallback when p is nil. VaultSummary.Id is mandatory:"true", so this
// only ever matters for a log line built from a malformed API response -- which is exactly the
// situation where a nil dereference in the logging path would be the least helpful failure.
func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}
