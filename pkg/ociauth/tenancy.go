package ociauth

import (
	"context"
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// newIdentityClient is a package-level seam so tests can swap in a client whose HTTPClient
// is a stub dispatcher, without changing VerifyTenancy's exported signature (CONTEXT.md
// requires the exact signature below). Common Go pattern for exactly this situation (cf.
// `var osExit = os.Exit` in stdlib-adjacent code).
var newIdentityClient = identity.NewIdentityClientWithConfigurationProvider

// VerifyTenancy resolves the tenancy behind provider via a live Identity API round-trip and
// hard-fails if it does not match expectedTenancyOCID. It is deliberately a standalone,
// directly-callable function -- not logic embedded only inside a RegisterValidateHandler
// closure -- because Phase 2's compartment walk and Phase 3's region discovery both need an
// independently-callable gate before their own Identity API calls run.
//
// provider.TenancyOCID() is not used as the sole check: it is a pure local read (a field out
// of ~/.oci/config or a certificate claim) with zero network validation. GetTenancy is the
// actual proof the credentials are live and belong to the claimed tenancy.
func VerifyTenancy(ctx context.Context, provider common.ConfigurationProvider, expectedTenancyOCID string) error {
	claimedTenancy, err := provider.TenancyOCID()
	if err != nil {
		return fmt.Errorf("resolving tenancy OCID from credentials: %w", err)
	}

	identityClient, err := newIdentityClient(provider)
	if err != nil {
		return fmt.Errorf("creating identity client: %w", err)
	}

	// oci-go-sdk/v65 does not retry by default; an un-retried 429 on this, the very first
	// OCI API call the tool makes, would otherwise become an immediate hard failure.
	retryPolicy := common.DefaultRetryPolicy()
	identityClient.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &retryPolicy})

	resp, err := identityClient.GetTenancy(ctx, identity.GetTenancyRequest{
		TenancyId: common.String(claimedTenancy),
	})
	if err != nil {
		return fmt.Errorf("verifying tenancy %s against OCI Identity API: %w", claimedTenancy, err)
	}

	if resp.Id == nil || *resp.Id != expectedTenancyOCID {
		got := "<nil>"
		if resp.Id != nil {
			got = *resp.Id
		}
		return fmt.Errorf("tenancy mismatch: credentials resolve to %q, expected %q", got, expectedTenancyOCID)
	}

	return nil
}
