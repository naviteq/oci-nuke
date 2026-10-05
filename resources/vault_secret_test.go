package resources

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/vault"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

type stubVaultSecretClient struct {
	pages       [][]vault.SecretSummary
	listReqs    []vault.ListSecretsRequest
	scheduled   []vault.ScheduleSecretDeletionRequest
	scheduleErr error
}

func (s *stubVaultSecretClient) ListSecrets(
	_ context.Context, req vault.ListSecretsRequest,
) (vault.ListSecretsResponse, error) {
	s.listReqs = append(s.listReqs, req)
	i := len(s.listReqs) - 1
	resp := vault.ListSecretsResponse{Items: s.pages[i]}
	if i < len(s.pages)-1 {
		next := "next"
		resp.OpcNextPage = &next
	}
	return resp, nil
}

func (s *stubVaultSecretClient) ScheduleSecretDeletion(
	_ context.Context, req vault.ScheduleSecretDeletionRequest,
) (vault.ScheduleSecretDeletionResponse, error) {
	s.scheduled = append(s.scheduled, req)
	return vault.ScheduleSecretDeletionResponse{}, s.scheduleErr
}

func testSecret(state vault.SecretSummaryLifecycleStateEnum) vault.SecretSummary {
	id, compartmentID, name, vaultID := testResourceOCID, testCompartmentOCID, "test-vpn-simple-tunnel-1-psk", "ocid1.vault.oc1..shared"
	return vault.SecretSummary{
		Id: &id, CompartmentId: &compartmentID, SecretName: &name, VaultId: &vaultID,
		LifecycleState: state, TimeCreated: &common.SDKTime{Time: time.Now()},
	}
}

// TestVaultSecretList_PaginatesAndCarriesTheWindow: every page is read with the compartment
// filter, and every secret carries the run's window.
func TestVaultSecretList_PaginatesAndCarriesTheWindow(t *testing.T) {
	stub := &stubVaultSecretClient{pages: [][]vault.SecretSummary{
		{testSecret(vault.SecretSummaryLifecycleStateActive)},
		{testSecret(vault.SecretSummaryLifecycleStateActive)},
	}}

	got, err := vaultSecretList(context.Background(), stub, testCompartmentOCID, 3)
	if err != nil {
		t.Fatalf("vaultSecretList() error = %v", err)
	}
	if len(got) != 2 || len(stub.listReqs) != 2 || *stub.listReqs[1].Page != "next" {
		t.Fatalf("got %d secrets over %d requests, want 2 over 2 with the second paged", len(got), len(stub.listReqs))
	}
	for _, req := range stub.listReqs {
		if *req.CompartmentId != testCompartmentOCID {
			t.Errorf("CompartmentId = %q, want %q", *req.CompartmentId, testCompartmentOCID)
		}
	}
	s := got[0].(*VaultSecret)
	if s.deletionWindowDays != 3 || s.GetCompartmentID() != testCompartmentOCID || s.UniqueKey() != testResourceOCID {
		t.Errorf("secret = %+v, want window 3 and the compartment and OCID it was listed with", s)
	}
}

// TestVaultSecret_Filter covers the present states, the scheduled ones that report, and the rest.
func TestVaultSecret_Filter(t *testing.T) {
	cases := []struct {
		state   vault.SecretSummaryLifecycleStateEnum
		present bool
		reports bool
	}{
		{vault.SecretSummaryLifecycleStateActive, true, false},
		{vault.SecretSummaryLifecycleStateCreating, true, false},
		{vault.SecretSummaryLifecycleStateUpdating, true, false},
		{vault.SecretSummaryLifecycleStateCancellingDeletion, true, false},
		{vault.SecretSummaryLifecycleStateFailed, true, false},
		{vault.SecretSummaryLifecycleStatePendingDeletion, false, true},
		{vault.SecretSummaryLifecycleStateSchedulingDeletion, false, true},
		{vault.SecretSummaryLifecycleStateDeleting, false, false},
		{vault.SecretSummaryLifecycleStateDeleted, false, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			var got []*scope.SkipEvent
			restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
				got = append(got, evt)
			})
			defer restore()

			s := testSecret(tc.state)
			deletion := common.SDKTime{Time: time.Date(2026, 10, 6, 13, 19, 37, 0, time.UTC)}
			s.TimeOfDeletion = &deletion
			err := (&VaultSecret{secret: s}).Filter()

			if (err == nil) != tc.present {
				t.Fatalf("Filter() = %v, want present=%v", err, tc.present)
			}
			if (len(got) == 1) != tc.reports || len(got) > 1 {
				t.Fatalf("reported %d events, want reports=%v", len(got), tc.reports)
			}
			if tc.reports && (got[0].Reason != scope.ReasonScheduledDeletion || got[0].ResourceType != VaultSecretResourceType ||
				!strings.Contains(got[0].Detail, "2026-10-06T13:19:37Z")) {
				t.Errorf("event = %+v, want scheduled-deletion for VaultSecret naming the deletion time", got[0])
			}
		})
	}
}

// TestVaultSecret_Filter_ReplicaIsLeftToItsSource: an ACTIVE replica is not removed directly.
func TestVaultSecret_Filter_ReplicaIsLeftToItsSource(t *testing.T) {
	s := testSecret(vault.SecretSummaryLifecycleStateActive)
	replica := true
	s.IsReplica = &replica

	if err := (&VaultSecret{secret: s}).Filter(); err == nil {
		t.Fatal("Filter() = nil for a replica, want it excluded")
	}
}

// TestVaultSecret_Remove_SchedulesWithTheWindow: the window is passed explicitly, and a zero one
// is clamped to OCI's one-day floor.
func TestVaultSecret_Remove_SchedulesWithTheWindow(t *testing.T) {
	for _, tc := range []struct{ days, want int }{{days: 5, want: 5}, {days: 0, want: 1}} {
		stub := &stubVaultSecretClient{}
		before := time.Now()

		secret := &VaultSecret{client: stub, secret: testSecret(vault.SecretSummaryLifecycleStateActive), deletionWindowDays: tc.days}
		if err := secret.Remove(context.Background()); err != nil {
			t.Fatalf("Remove() = %v", err)
		}

		if len(stub.scheduled) != 1 {
			t.Fatalf("ScheduleSecretDeletion called %d times, want 1", len(stub.scheduled))
		}
		req := stub.scheduled[0]
		got := req.TimeOfDeletion.Sub(before)
		want := time.Duration(tc.want) * 24 * time.Hour
		if *req.SecretId != testResourceOCID || got < want || got > want+time.Minute {
			t.Errorf("days=%d: scheduled %s for %v ahead, want %s for %v", tc.days, *req.SecretId, got, testResourceOCID, want)
		}
	}
}

// TestVaultSecret_Remove_409Holds: a secret still CREATING or UPDATING answers 409, which waits.
func TestVaultSecret_Remove_409Holds(t *testing.T) {
	stub := &stubVaultSecretClient{scheduleErr: &conflictError{statusCode: http.StatusConflict, code: "IncorrectState"}}

	secret := &VaultSecret{client: stub, secret: testSecret(vault.SecretSummaryLifecycleStateUpdating), deletionWindowDays: 1}
	err := secret.Remove(context.Background())

	var hold liberrors.ErrHoldResource
	if !errors.As(err, &hold) {
		t.Fatalf("Remove() = %v, want ErrHoldResource", err)
	}
}

// TestVaultSecret_KeysAndVaultsWaitForSecrets pins the ordering: secret, then key, then vault.
func TestVaultSecret_KeysAndVaultsWaitForSecrets(t *testing.T) {
	for _, typ := range []string{KmsKeyResourceType, VaultResourceType} {
		reg := registry.GetRegistration(typ)
		found := false
		for _, dep := range reg.DependsOn {
			found = found || dep == VaultSecretResourceType
		}
		if !found {
			t.Errorf("%s DependsOn = %v, want it to include %s", typ, reg.DependsOn, VaultSecretResourceType)
		}
	}
}
