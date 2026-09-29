package scope

import (
	"context"
	"strings"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// fakeRegionClient implements RegionClient purely in-memory -- no network, mirroring
// fakeIdentityClient's pattern in tree_test.go.
type fakeRegionClient struct {
	resp identity.ListRegionSubscriptionsResponse
}

func (f *fakeRegionClient) ListRegionSubscriptions(
	_ context.Context,
	_ identity.ListRegionSubscriptionsRequest,
) (identity.ListRegionSubscriptionsResponse, error) {
	return f.resp, nil
}

func regionSubscription(name string, isHome bool, status identity.RegionSubscriptionStatusEnum) identity.RegionSubscription {
	return identity.RegionSubscription{
		RegionName:   common.String(name),
		Status:       status,
		IsHomeRegion: common.Bool(isHome),
	}
}

// TestResolveRegions_HomeRegionFromSubscriptionsNotConnectionRegion directly encodes the live
// live-tenancy finding as a regression fixture: the fake RegionClient's IsHomeRegion=true entry
// (us-ashburn-1) differs from the value a provider.Region()-style connection-region stub would
// have returned (eu-frankfurt-1, also present but IsHomeRegion=false). ResolveRegions must
// return the subscriptions value, never the connection-region stub's.
func TestResolveRegions_HomeRegionFromSubscriptionsNotConnectionRegion(t *testing.T) {
	const connectionRegionStub = "eu-frankfurt-1" // what provider.Region() would have returned
	const actualHomeRegion = "us-ashburn-1"       // the IsHomeRegion=true entry

	client := &fakeRegionClient{resp: identity.ListRegionSubscriptionsResponse{
		Items: []identity.RegionSubscription{
			regionSubscription(connectionRegionStub, false, identity.RegionSubscriptionStatusReady),
			regionSubscription(actualHomeRegion, true, identity.RegionSubscriptionStatusReady),
		},
	}}

	_, homeRegion, err := ResolveRegions(context.Background(), client, "ocid1.tenancy.oc1..t", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if homeRegion != actualHomeRegion {
		t.Fatalf("expected homeRegion=%s (from IsHomeRegion=true), got %s", actualHomeRegion, homeRegion)
	}
	if homeRegion == connectionRegionStub {
		t.Fatalf("homeRegion must never equal the connection-region stub (%s)", connectionRegionStub)
	}
}

func TestResolveRegions_UnknownConfigRegionFails(t *testing.T) {
	client := &fakeRegionClient{resp: identity.ListRegionSubscriptionsResponse{
		Items: []identity.RegionSubscription{
			regionSubscription("us-ashburn-1", true, identity.RegionSubscriptionStatusReady),
		},
	}}

	validated, homeRegion, err := ResolveRegions(context.Background(), client, "ocid1.tenancy.oc1..t", []string{"ap-tokyo-1"})
	if err == nil {
		t.Fatal("expected an error for a config region absent from the subscribed set")
	}
	if !strings.Contains(err.Error(), "ap-tokyo-1") {
		t.Fatalf("expected error text to name the region %q, got: %v", "ap-tokyo-1", err)
	}
	if validated != nil || homeRegion != "" {
		t.Fatalf("expected zero-value returns on error, got validated=%v homeRegion=%q", validated, homeRegion)
	}
}

func TestResolveRegions_UnsubscribedStatusFails(t *testing.T) {
	client := &fakeRegionClient{resp: identity.ListRegionSubscriptionsResponse{
		Items: []identity.RegionSubscription{
			regionSubscription("us-ashburn-1", true, identity.RegionSubscriptionStatusReady),
			regionSubscription("ap-melbourne-1", false, identity.RegionSubscriptionStatusInProgress),
		},
	}}

	validated, homeRegion, err := ResolveRegions(context.Background(), client, "ocid1.tenancy.oc1..t", []string{"ap-melbourne-1"})
	if err == nil {
		t.Fatal("expected an error for a config region present but not READY")
	}
	if !strings.Contains(err.Error(), "ap-melbourne-1") {
		t.Fatalf("expected error text to name the region %q, got: %v", "ap-melbourne-1", err)
	}
	if !strings.Contains(err.Error(), string(identity.RegionSubscriptionStatusInProgress)) {
		t.Fatalf("expected error text to name the region's actual status, got: %v", err)
	}
	if validated != nil || homeRegion != "" {
		t.Fatalf("expected zero-value returns on error, got validated=%v homeRegion=%q", validated, homeRegion)
	}
}

func TestResolveRegions_NoHomeRegionEntryFails(t *testing.T) {
	client := &fakeRegionClient{resp: identity.ListRegionSubscriptionsResponse{
		Items: []identity.RegionSubscription{
			regionSubscription("us-ashburn-1", false, identity.RegionSubscriptionStatusReady),
			regionSubscription("eu-frankfurt-1", false, identity.RegionSubscriptionStatusReady),
		},
	}}

	validated, homeRegion, err := ResolveRegions(context.Background(), client, "ocid1.tenancy.oc1..t", nil)
	if err == nil {
		t.Fatal("expected an error when zero subscriptions have IsHomeRegion=true")
	}
	if homeRegion != "" {
		t.Fatalf("a zero-value homeRegion must never be treated as success, got %q", homeRegion)
	}
	if validated != nil {
		t.Fatalf("expected nil validated on error, got %v", validated)
	}
}

// TestResolveRegions_MultipleHomeRegionEntriesFails is the defensive counterpart: the API
// contract implies exactly one IsHomeRegion=true entry, but ResolveRegions must never assume
// that without checking.
func TestResolveRegions_MultipleHomeRegionEntriesFails(t *testing.T) {
	client := &fakeRegionClient{resp: identity.ListRegionSubscriptionsResponse{
		Items: []identity.RegionSubscription{
			regionSubscription("us-ashburn-1", true, identity.RegionSubscriptionStatusReady),
			regionSubscription("eu-frankfurt-1", true, identity.RegionSubscriptionStatusReady),
		},
	}}

	_, homeRegion, err := ResolveRegions(context.Background(), client, "ocid1.tenancy.oc1..t", nil)
	if err == nil {
		t.Fatal("expected an error when more than one subscription has IsHomeRegion=true")
	}
	if homeRegion != "" {
		t.Fatalf("expected empty homeRegion on error, got %q", homeRegion)
	}
}

// TestResolveRegions_ValidatedOrderMatchesConfigOrder proves validated's order follows
// configRegions's declaration order, not the API response's order -- this feeds Plan 03-05's
// scanner registration order deterministically.
func TestResolveRegions_ValidatedOrderMatchesConfigOrder(t *testing.T) {
	client := &fakeRegionClient{resp: identity.ListRegionSubscriptionsResponse{
		Items: []identity.RegionSubscription{
			regionSubscription("us-ashburn-1", true, identity.RegionSubscriptionStatusReady),
			regionSubscription("ap-tokyo-1", false, identity.RegionSubscriptionStatusReady),
			regionSubscription("eu-frankfurt-1", false, identity.RegionSubscriptionStatusReady),
			regionSubscription("uk-london-1", false, identity.RegionSubscriptionStatusReady),
		},
	}}

	configRegions := []string{"uk-london-1", "eu-frankfurt-1", "ap-tokyo-1"}
	validated, _, err := ResolveRegions(context.Background(), client, "ocid1.tenancy.oc1..t", configRegions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(validated) != len(configRegions) {
		t.Fatalf("expected %d validated regions, got %d: %v", len(configRegions), len(validated), validated)
	}
	for i, r := range configRegions {
		if validated[i] != r {
			t.Fatalf("expected validated[%d]=%s (config order), got %s", i, r, validated[i])
		}
	}
}
