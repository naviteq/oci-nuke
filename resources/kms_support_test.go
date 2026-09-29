package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/keymanagement"
)

// stubKmsVaultClient implements kmsVaultLister against in-memory data -- zero network access.
type stubKmsVaultClient struct {
	items   []keymanagement.VaultSummary
	listErr error
}

func (s *stubKmsVaultClient) ListVaults(
	_ context.Context, _ keymanagement.ListVaultsRequest,
) (keymanagement.ListVaultsResponse, error) {
	if s.listErr != nil {
		return keymanagement.ListVaultsResponse{}, s.listErr
	}
	return keymanagement.ListVaultsResponse{Items: s.items}, nil
}

// stubKmsManagementClient implements kmsManagementClient against in-memory data -- zero network
// access. keyed independently per constructed instance, so kmsKeyList's clientFor closure (below)
// can return a DIFFERENT stub per managementEndpoint, proving per-vault (not per-region) client
// construction.
type stubKmsManagementClient struct {
	endpoint  string
	items     []keymanagement.KeySummary
	scheduled []keymanagement.ScheduleKeyDeletionRequest

	// listErr stands in for the endpoint being unreachable -- the DNS failure NR-780 was
	// reported for, and anything else that makes one vault's ListKeys fail.
	listErr error
}

func (s *stubKmsManagementClient) ListKeys(
	_ context.Context, _ keymanagement.ListKeysRequest,
) (keymanagement.ListKeysResponse, error) {
	if s.listErr != nil {
		return keymanagement.ListKeysResponse{}, s.listErr
	}
	return keymanagement.ListKeysResponse{Items: s.items}, nil
}

func (s *stubKmsManagementClient) ScheduleKeyDeletion(
	_ context.Context, req keymanagement.ScheduleKeyDeletionRequest,
) (keymanagement.ScheduleKeyDeletionResponse, error) {
	s.scheduled = append(s.scheduled, req)
	return keymanagement.ScheduleKeyDeletionResponse{}, nil
}

// TestKmsKeyList_NestedEnumeration proves kmsKeyList enumerates every vault, then every key per
// vault, wrapping each as a KmsKey with the compartmentID inherited from the OUTER loop's
// parameter (not read off the key itself).
func TestKmsKeyList_NestedEnumeration(t *testing.T) {
	compartmentID := testCompartmentOCID
	vaultID := testResourceOCID
	endpoint := testVaultManagementEndpointA
	keyID := "ocid1.key.oc1..a"
	timeCreated := common.SDKTime{Time: time.Now()}

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			{
				Id: &vaultID, CompartmentId: &compartmentID, ManagementEndpoint: &endpoint,
				LifecycleState: keymanagement.VaultSummaryLifecycleStateActive,
			},
		},
	}
	managementStub := &stubKmsManagementClient{
		endpoint: endpoint,
		items: []keymanagement.KeySummary{
			{Id: &keyID, CompartmentId: &compartmentID, LifecycleState: keymanagement.KeySummaryLifecycleStateEnabled, TimeCreated: &timeCreated},
		},
	}

	var calledWith []string
	clientFor := func(managementEndpoint string) (kmsManagementClient, error) {
		calledWith = append(calledWith, managementEndpoint)
		return managementStub, nil
	}

	got, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 14)
	if err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("kmsKeyList() returned %d resources, want 1", len(got))
	}
	k, ok := got[0].(*KmsKey)
	if !ok {
		t.Fatalf("kmsKeyList()[0] is %T, want *KmsKey", got[0])
	}
	if k.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q (inherited from the outer loop parameter)", k.GetCompartmentID(), compartmentID)
	}
	if k.UniqueKey() != keyID {
		t.Errorf("UniqueKey() = %q, want %q", k.UniqueKey(), keyID)
	}
	if k.deletionWindowDays != 14 {
		t.Errorf("deletionWindowDays = %d, want 14", k.deletionWindowDays)
	}
	if len(calledWith) != 1 || calledWith[0] != endpoint {
		t.Errorf("clientFor called with %v, want exactly [%q]", calledWith, endpoint)
	}
}

// TestKmsKeyList_PerVaultNotPerRegionClientConstruction proves kmsKeyList calls the clientFor
// closure once per DISTINCT managementEndpoint when two vaults exist in the same compartment
// (implicitly the same region) with different ManagementEndpoint values -- the acceptance
// criterion this plan's Cache.KmsManagement accessor exists to satisfy: per-vault, never
// per-region, client construction.
func TestKmsKeyList_PerVaultNotPerRegionClientConstruction(t *testing.T) {
	compartmentID := testCompartmentOCID
	vaultAID, vaultBID := "ocid1.vault.oc1..a", "ocid1.vault.oc1..b"
	endpointA := testVaultManagementEndpointA
	endpointB := "https://vault-b-kms.management.us-ashburn-1.oraclecloud.com"

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			{
				Id: &vaultAID, CompartmentId: &compartmentID, ManagementEndpoint: &endpointA,
				LifecycleState: keymanagement.VaultSummaryLifecycleStateActive,
			},
			{
				Id: &vaultBID, CompartmentId: &compartmentID, ManagementEndpoint: &endpointB,
				LifecycleState: keymanagement.VaultSummaryLifecycleStateActive,
			},
		},
	}

	var calledWith []string
	clientFor := func(managementEndpoint string) (kmsManagementClient, error) {
		calledWith = append(calledWith, managementEndpoint)
		return &stubKmsManagementClient{endpoint: managementEndpoint}, nil
	}

	if _, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 7); err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil", err)
	}

	if len(calledWith) != 2 {
		t.Fatalf("clientFor called %d times, want 2 (once per distinct vault endpoint)", len(calledWith))
	}
	if calledWith[0] == calledWith[1] {
		t.Fatalf("clientFor called with the same endpoint twice (%q) -- want two distinct per-vault endpoints", calledWith[0])
	}
}

// TestKmsKeyList_NoCascadeFromNonActiveVault proves 05-CONTEXT.md's locked no-cascade principle:
// a key whose owning vault is ITSELF in PENDING_DELETION is still independently enumerated --
// kmsKeyList never pre-filters by the vault's own lifecycle state.
func TestKmsKeyList_NoCascadeFromNonActiveVault(t *testing.T) {
	compartmentID := testCompartmentOCID
	vaultID := testResourceOCID
	endpoint := testVaultManagementEndpointA
	keyID := "ocid1.key.oc1..a"
	timeCreated := common.SDKTime{Time: time.Now()}

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			// The owning vault is PENDING_DELETION -- Vault.Filter() would exclude the VAULT
			// itself, but that must have zero bearing on whether kmsKeyList still enumerates
			// this vault's keys.
			{
				Id: &vaultID, CompartmentId: &compartmentID, ManagementEndpoint: &endpoint,
				LifecycleState: keymanagement.VaultSummaryLifecycleStatePendingDeletion,
			},
		},
	}
	managementStub := &stubKmsManagementClient{
		items: []keymanagement.KeySummary{
			{Id: &keyID, CompartmentId: &compartmentID, LifecycleState: keymanagement.KeySummaryLifecycleStateEnabled, TimeCreated: &timeCreated},
		},
	}
	clientFor := func(string) (kmsManagementClient, error) { return managementStub, nil }

	got, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 7)
	if err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("kmsKeyList() returned %d resources, want 1 -- a PENDING_DELETION vault's keys must still be enumerated", len(got))
	}
	if got[0].(*KmsKey).UniqueKey() != keyID {
		t.Errorf("UniqueKey() = %q, want %q", got[0].(*KmsKey).UniqueKey(), keyID)
	}
}

// TestKmsKeyList_NilManagementEndpointSkipsVault proves a vault whose ManagementEndpoint comes
// back nil (should not happen -- mandatory:"true" -- but defended against) skips that vault's key
// enumeration entirely, rather than panicking or calling clientFor with an empty string.
func TestKmsKeyList_NilManagementEndpointSkipsVault(t *testing.T) {
	compartmentID := testCompartmentOCID
	vaultID := testResourceOCID

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			{Id: &vaultID, CompartmentId: &compartmentID, ManagementEndpoint: nil, LifecycleState: keymanagement.VaultSummaryLifecycleStateActive},
		},
	}

	clientForCalled := false
	clientFor := func(string) (kmsManagementClient, error) {
		clientForCalled = true
		return &stubKmsManagementClient{}, nil
	}

	got, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 7)
	if err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("kmsKeyList() returned %d resources, want 0 for a nil-ManagementEndpoint vault", len(got))
	}
	if clientForCalled {
		t.Error("clientFor was called for a nil-ManagementEndpoint vault, want it never called")
	}
}

// TestKmsKeyList_DeletedVaultSkippedWithoutAContactAttempt is NR-780's first half. OCI tears a
// vault's management endpoint down with the vault, so its hostname stops resolving while
// ListVaults keeps returning the record. The call must not be attempted at all: there is nothing
// left to schedule in a DELETED vault, and attempting it is what produced the error line.
//
// The active vault in the same page is the point of the test. Before this fix the deleted
// vault's failure returned from kmsKeyList, so the compartment reported ZERO keys -- a silent
// coverage hole in the one resource type whose job is scheduling key deletion.
func TestKmsKeyList_DeletedVaultSkippedWithoutAContactAttempt(t *testing.T) {
	compartmentID := testCompartmentOCID
	deletedVaultID := "ocid1.vault.oc1..deleted"
	activeVaultID := "ocid1.vault.oc1..active"
	deletedEndpoint := "https://deleted-management.kms.eu-frankfurt-1.oraclecloud.com"
	activeEndpoint := "https://active-management.kms.eu-frankfurt-1.oraclecloud.com"
	keyID := testResourceOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			{
				Id: &deletedVaultID, CompartmentId: &compartmentID,
				ManagementEndpoint: &deletedEndpoint,
				LifecycleState:     keymanagement.VaultSummaryLifecycleStateDeleted,
			},
			{
				Id: &activeVaultID, CompartmentId: &compartmentID,
				ManagementEndpoint: &activeEndpoint,
				LifecycleState:     keymanagement.VaultSummaryLifecycleStateActive,
			},
		},
	}

	var contacted []string
	clientFor := func(endpoint string) (kmsManagementClient, error) {
		contacted = append(contacted, endpoint)
		if endpoint == deletedEndpoint {
			// Reached only on a regression. Mirrors the real failure rather than returning an
			// empty result, so a regression fails loudly instead of merely reporting no keys.
			return nil, errors.New("dial tcp: lookup deleted-management.kms.eu-frankfurt-1.oraclecloud.com: no such host")
		}
		return &stubKmsManagementClient{
			endpoint: endpoint,
			items: []keymanagement.KeySummary{
				{Id: &keyID, CompartmentId: &compartmentID, LifecycleState: keymanagement.KeySummaryLifecycleStateEnabled, TimeCreated: &timeCreated},
			},
		}, nil
	}

	got, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 7)
	if err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil -- one deleted vault must not fail the compartment", err)
	}
	if len(got) != 1 {
		t.Fatalf("kmsKeyList() returned %d resources, want 1 (the active vault's key)", len(got))
	}
	for _, endpoint := range contacted {
		if endpoint == deletedEndpoint {
			t.Errorf("clientFor was called for a DELETED vault's endpoint %s; it must be skipped before any client is constructed", deletedEndpoint)
		}
	}
}

// TestKmsKeyList_UnreachableVaultDoesNotCostTheCompartment is NR-780's second half. A vault that
// is NOT deleted but whose keys cannot be listed -- a permission gap, a throttle, an endpoint
// down for some other reason -- is a real gap an operator should see. It is still only that one
// vault's gap: the compartment's other vaults must be reported.
func TestKmsKeyList_UnreachableVaultDoesNotCostTheCompartment(t *testing.T) {
	compartmentID := testCompartmentOCID
	brokenVaultID := "ocid1.vault.oc1..broken"
	goodVaultID := "ocid1.vault.oc1..good"
	brokenEndpoint := "https://broken-management.kms.eu-frankfurt-1.oraclecloud.com"
	goodEndpoint := "https://good-management.kms.eu-frankfurt-1.oraclecloud.com"
	keyID := testResourceOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			{
				Id: &brokenVaultID, CompartmentId: &compartmentID,
				ManagementEndpoint: &brokenEndpoint,
				LifecycleState:     keymanagement.VaultSummaryLifecycleStateActive,
			},
			{
				Id: &goodVaultID, CompartmentId: &compartmentID,
				ManagementEndpoint: &goodEndpoint,
				LifecycleState:     keymanagement.VaultSummaryLifecycleStateActive,
			},
		},
	}

	clientFor := func(endpoint string) (kmsManagementClient, error) {
		if endpoint == brokenEndpoint {
			return &stubKmsManagementClient{
				endpoint: endpoint,
				listErr:  errors.New("Error returned by Vault Service. Http Status Code: 403. Error Code: NotAuthorized"),
			}, nil
		}
		return &stubKmsManagementClient{
			endpoint: endpoint,
			items: []keymanagement.KeySummary{
				{Id: &keyID, CompartmentId: &compartmentID, LifecycleState: keymanagement.KeySummaryLifecycleStateEnabled, TimeCreated: &timeCreated},
			},
		}, nil
	}

	got, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 7)
	if err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil -- one unreachable vault must not fail the compartment", err)
	}
	if len(got) != 1 {
		t.Fatalf("kmsKeyList() returned %d resources, want 1 (the reachable vault's key)", len(got))
	}
}

// TestKmsKeyList_ClientConstructionFailureIsAlsoPerVault covers the other failure point in the
// same path: clientFor itself failing, rather than ListKeys. Same rule applies -- one vault's
// problem, not the compartment's.
func TestKmsKeyList_ClientConstructionFailureIsAlsoPerVault(t *testing.T) {
	compartmentID := testCompartmentOCID
	badVaultID := "ocid1.vault.oc1..badclient"
	goodVaultID := "ocid1.vault.oc1..good"
	badEndpoint := "https://bad-management.kms.eu-frankfurt-1.oraclecloud.com"
	goodEndpoint := "https://good-management.kms.eu-frankfurt-1.oraclecloud.com"
	keyID := testResourceOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	vaultClient := &stubKmsVaultClient{
		items: []keymanagement.VaultSummary{
			{
				Id: &badVaultID, CompartmentId: &compartmentID,
				ManagementEndpoint: &badEndpoint,
				LifecycleState:     keymanagement.VaultSummaryLifecycleStateUpdating,
			},
			{
				Id: &goodVaultID, CompartmentId: &compartmentID,
				ManagementEndpoint: &goodEndpoint,
				LifecycleState:     keymanagement.VaultSummaryLifecycleStateActive,
			},
		},
	}

	clientFor := func(endpoint string) (kmsManagementClient, error) {
		if endpoint == badEndpoint {
			return nil, errors.New("constructing KmsManagementClient: bad endpoint")
		}
		return &stubKmsManagementClient{
			endpoint: endpoint,
			items: []keymanagement.KeySummary{
				{Id: &keyID, CompartmentId: &compartmentID, LifecycleState: keymanagement.KeySummaryLifecycleStateEnabled, TimeCreated: &timeCreated},
			},
		}, nil
	}

	got, err := kmsKeyList(context.Background(), vaultClient, clientFor, compartmentID, 7)
	if err != nil {
		t.Fatalf("kmsKeyList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("kmsKeyList() returned %d resources, want 1 (the good vault's key)", len(got))
	}
}
