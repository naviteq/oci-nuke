package ociauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common/auth"
)

// oidcFetchTimeout bounds every GitHub Actions OIDC token fetch. Mirrors
// imdsProbeTimeout's bounded-by-design precedent (provider.go): a live callback that could
// hang indefinitely would defeat UPST renewal just as surely as one that returned a stale
// value would.
const oidcFetchTimeout = 10 * time.Second

// githubOIDCTokenIssuer implements auth.TokenIssuer (oci-go-sdk/v65/common/auth) as a live
// callback -- GetToken fetches a fresh GitHub Actions OIDC JWT on every invocation, never a
// value captured once at construction time. This distinction is load-bearing:
// tokenExchangeFederationClient.renewSecurityToken (federation_client.go:943-977, pinned
// oci-go-sdk/v65@v65.123.0, read directly during planning) calls tokenIssuer.GetToken() again
// on every UPST renewal, so a live callback is what makes renewal past Phase 6's
// 15-minute-plus retry floor actually work. Wrapping a one-shot token captured once at
// construction would silently defeat that renewal on a run outliving the UPST's lifetime.
var _ auth.TokenIssuer = (*githubOIDCTokenIssuer)(nil)

// githubOIDCTokenIssuer is the auth.TokenIssuer this package hands to
// auth.TokenExchangeConfigurationProviderFromIssuer. audience, when non-empty, is requested
// from GitHub's own OIDC provider on every fetch.
type githubOIDCTokenIssuer struct {
	audience string
}

// GetToken satisfies auth.TokenIssuer. Bounded by oidcFetchTimeout -- never an unbounded HTTP
// call, mirroring instanceMetadataReachable's bounded-probe precedent (provider.go).
func (i *githubOIDCTokenIssuer) GetToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), oidcFetchTimeout)
	defer cancel()
	return fetchGitHubOIDCToken(ctx, i.audience)
}

// fetchGitHubOIDCToken fetches a GitHub Actions OIDC JWT from the runner-injected
// ACTIONS_ID_TOKEN_REQUEST_URL/ACTIONS_ID_TOKEN_REQUEST_TOKEN env vars, present only inside a
// job with `permissions: id-token: write`. Their absence is reported as a named, specific
// error (AUTH-05's "report the underlying error" convention) rather than a generic auth
// failure.
//
// When audience is non-empty it is appended as "&audience=<value>" -- a literal "&", not a
// "?": on a real GitHub runner ACTIONS_ID_TOKEN_REQUEST_URL already carries its own query
// string, matching GitHub's own documented curl example and
// github.com/actions/toolkit's packages/core/src/oidc-utils.ts (both fetched directly during
// planning, not paraphrased from a summary).
func fetchGitHubOIDCToken(ctx context.Context, audience string) (string, error) {
	reqURL := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	reqToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if reqURL == "" || reqToken == "" {
		return "", fmt.Errorf(
			"ACTIONS_ID_TOKEN_REQUEST_URL/ACTIONS_ID_TOKEN_REQUEST_TOKEN not set -- " +
				"is this job missing `permissions: id-token: write`?")
	}

	u := reqURL
	if audience != "" {
		u += "&audience=" + url.QueryEscape(audience)
	}

	//nolint:gosec // G107: u is built from ACTIONS_ID_TOKEN_REQUEST_URL, a runner-injected env
	// var GitHub Actions itself sets (trusted infrastructure input, not attacker-controlled
	// network input) -- fetching this exact URL is the entire purpose of the OIDC JWT fetch.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("building GitHub OIDC token request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+reqToken)

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G107: see the construction site above
	if err != nil {
		return "", fmt.Errorf("requesting GitHub OIDC token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub OIDC token endpoint returned status %d", resp.StatusCode)
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding GitHub OIDC token response: %w", err)
	}
	if body.Value == "" {
		return "", fmt.Errorf("GitHub OIDC token response had an empty value field")
	}
	return body.Value, nil
}
