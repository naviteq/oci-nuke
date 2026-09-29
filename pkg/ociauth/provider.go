// Package ociauth implements oci-nuke's four-provider OCI authentication abstraction
// (config file + profile, instance principal, OKE workload identity, GitHub Actions OIDC
// token exchange) plus the standalone tenancy verification gate. Deterministic auto-detection,
// no silent fallback (AUTH-01..05).
package ociauth

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
)

// The OCI Instance Metadata Service. Reachable only from an OCI compute instance; the
// address is link-local and unroutable everywhere else.
//
// These are variables rather than constants so tests can point the probe at a listener
// they control.
var (
	imdsAddr = "169.254.169.254:80"

	// imdsProbeTimeout bounds the reachability probe. On an OCI instance the IMDS answers
	// on the local link in single-digit milliseconds, so one second is generous.
	imdsProbeTimeout = time.Second
)

// Method identifies which OCI authentication mechanism produced a common.ConfigurationProvider.
type Method string

const (
	MethodConfigFile        Method = "config-file"
	MethodInstancePrincipal Method = "instance-principal"
	MethodWorkloadIdentity  Method = "workload-identity"

	// MethodGitHubOIDC exchanges a GitHub Actions OIDC JWT for an OCI User Principal Session
	// Token (UPST) via OCI Identity Domains' token exchange. Reachable only through an
	// explicit --auth github-oidc -- never selected by auto-detection (07-CONTEXT.md, locked):
	// picking the wrong principal silently is not a convenience worth having.
	//
	// This is NOT "no long-lived key anywhere": the token exchange itself is authorized by a
	// narrowly-scoped OAuth client secret (OCI_OIDC_CLIENT_SECRET) that, on its own, reaches
	// no OCI API without also presenting a fresh, matching GitHub-issued OIDC JWT (AUTH-06,
	// restated 2026-08-09). No comment, error message, or log line in this codebase may
	// describe this as fully keyless.
	MethodGitHubOIDC Method = "github-oidc"

	// MethodAPIKey authenticates with an ordinary OCI API signing key whose parts are supplied
	// as environment variables rather than as a ~/.oci/config profile and a key file on disk.
	// The everyday CI path for anyone who has not set up an Identity Propagation Trust and a
	// Confidential Application in their identity domain -- which is to say, most consumers.
	//
	// Reachable only through an explicit --auth api-key, never by auto-detection, for the same
	// reason MethodGitHubOIDC is not auto-detected: a credential left exported in a shell or
	// baked into a CI image must not silently decide which principal deletes things. The
	// three-rung auto-detect ladder in Resolve is deliberately unchanged by this method.
	//
	// This is the long-lived-key path. It exists because not every consumer can run
	// MethodGitHubOIDC, not because it is equivalent to it -- an API signing key reaches every
	// OCI API the user is entitled to, for as long as it is not revoked, and nothing about
	// passing it through an environment variable changes that.
	MethodAPIKey Method = "api-key"
)

// The environment variables --auth api-key reads its credentials from. Exported so the CLI
// reads them from here rather than repeating the strings, and so the names an error message
// tells an operator to set cannot drift from the names actually read.
//
// These are the names the `oci` CLI reads and the names Oracle's own
// oracle-actions/configure-oci-credentials exports. A caller with either of those already in
// place needs no new plumbing, which is the whole point of not inventing a private set.
//
// gosec's G101 fires on this block purely because the identifiers contain "Tenancy"/"User"/
// "Key" -- every value is the *name* of an environment variable, publicly documented in the
// README, and no credential appears anywhere in this file.
const (
	EnvAPIKeyTenancy     = "OCI_CLI_TENANCY"        //nolint:gosec // a variable name, not a credential
	EnvAPIKeyUser        = "OCI_CLI_USER"           //nolint:gosec // a variable name, not a credential
	EnvAPIKeyFingerprint = "OCI_CLI_FINGERPRINT"    //nolint:gosec // a variable name, not a credential
	EnvAPIKeyRegion      = "OCI_CLI_REGION"         //nolint:gosec // a variable name, not a credential
	EnvAPIKeyKeyContent  = "OCI_CLI_KEY_CONTENT"    //nolint:gosec // a variable name, not a credential
	EnvAPIKeyPassphrase  = "OCI_CLI_KEY_PASSPHRASE" //nolint:gosec // a variable name, not a credential
)

// The environment variables --auth github-oidc reads. Exported for the same reason as the
// api-key names above, and now load-bearing for a second one: two commands read them, `run` and
// `auth export-session`, and a literal repeated across both is a transposition waiting to
// happen. None of the three is a CLI flag -- the client secret must never appear in a process
// listing, and the other two keep it company so all three arrive the same way.
const (
	EnvOIDCDomainURL    = "OCI_OIDC_DOMAIN_URL"
	EnvOIDCClientID     = "OCI_OIDC_CLIENT_ID"
	EnvOIDCClientSecret = "OCI_OIDC_CLIENT_SECRET" //nolint:gosec // a variable name, not a credential
)

// ResolveOptions configures Resolve. Method, when non-empty, forces a specific auth
// mechanism; an empty Method triggers deterministic auto-detection.
type ResolveOptions struct {
	Method     Method // explicit override; "" triggers auto-detection
	ConfigPath string // defaults to ~/.oci/config
	Profile    string // defaults to "DEFAULT"

	// The following five fields are GitHub-OIDC-only (Method == MethodGitHubOIDC); every
	// other method ignores them. OIDCDomainURL/OIDCClientID/OIDCClientSecret/OIDCRegion are
	// required together; OIDCAudience is optional.
	OIDCDomainURL    string // OCI Identity Domain URL, e.g. https://idcs-xxxx.identity.oraclecloud.com
	OIDCClientID     string // the Identity Domain confidential application's client ID
	OIDCClientSecret string // the confidential application's client secret
	OIDCRegion       string // required -- TokenExchangeConfigurationProvider does not auto-detect a region
	OIDCAudience     string // optional; passed to GitHub's OIDC provider as the requested audience

	// The following six fields are API-key-only (Method == MethodAPIKey); every other method
	// ignores them. All but APIKeyPassphrase are required together.
	//
	// Every one of them is read from an environment variable at the call site and never from a
	// CLI flag -- see the OCI_CLI_* comment beside --auth's registration in
	// pkg/commands/run/command.go. APIKeyPrivateKey in particular is the PEM's own bytes, so a
	// flag would put a private key in `ps` output and in an echoed CI command line.
	APIKeyTenancyOCID string // OCID of the tenancy the key belongs to
	APIKeyUserOCID    string // OCID of the user the key is registered against
	APIKeyFingerprint string // fingerprint of the uploaded public key half
	APIKeyRegion      string // region to address; the SDK validates the identifier's shape
	APIKeyPrivateKey  string // PEM-encoded private key CONTENT, not a path to one
	APIKeyPassphrase  string // optional; empty means the key is not passphrase-protected
}

// Resolve returns a live common.ConfigurationProvider using an explicit method or
// deterministic auto-detection (AUTH-04). A provider that fails to construct returns its
// underlying error -- there is no silent fallback to a different method (AUTH-05).
//
// opts is deliberately passed by value, not by pointer: 07-01-PLAN.md's must_haves locks the
// exact call-site shape ociauth.Resolve(ctx, ociauth.ResolveOptions{...}) as a grep-checked
// pattern, so ResolveOptions growing past gocritic's hugeParam threshold with this plan's five
// new GitHub-OIDC-only fields is accepted rather than changed into a pointer-passing API.
//
//nolint:gocritic
func Resolve(ctx context.Context, opts ResolveOptions) (common.ConfigurationProvider, Method, error) {
	if opts.Method != "" {
		return resolveMethod(opts.Method, opts)
	}

	// 1. workload identity -- OCI_RESOURCE_PRINCIPAL_VERSION is set only inside an OKE pod
	//    with workload identity configured. Verified: oci-go-sdk/v65 common/auth source.
	if _, ok := os.LookupEnv("OCI_RESOURCE_PRINCIPAL_VERSION"); ok {
		return resolveMethod(MethodWorkloadIdentity, opts)
	}

	// 2. instance principal -- only succeeds on an actual OCI compute instance.
	//
	//    auth.InstancePrincipalConfigurationProvider() does NOT fail fast off an instance:
	//    it retries the IMDS call on network timeouts. Measured on a developer laptop, it
	//    took 6m1s to return an error, during which `oci-nuke run` produced no output at
	//    all. Probe reachability under a hard bound first, and only construct the provider
	//    if something is actually listening.
	if instanceMetadataReachable(ctx) {
		if provider, err := auth.InstancePrincipalConfigurationProvider(); err == nil {
			return provider, MethodInstancePrincipal, nil
		}
	}

	// 3. config file -- last resort so a stale ~/.oci/config baked into a CI image doesn't
	//    silently win over an environment that actually signals OCI compute/OKE.
	if _, err := os.Stat(configPathOrDefault(opts.ConfigPath)); err == nil {
		return resolveMethod(MethodConfigFile, opts)
	}

	return nil, "", fmt.Errorf(
		"could not auto-detect an OCI auth method: OCI_RESOURCE_PRINCIPAL_VERSION not set, " +
			"not running on an OCI compute instance, and no OCI config file found; " +
			"pass --auth explicitly -- in CI that is usually --auth github-oidc or " +
			"--auth api-key, neither of which is ever auto-detected")
}

// instanceMetadataReachable reports whether the OCI Instance Metadata Service accepts a
// TCP connection within imdsProbeTimeout. It answers "might we be on an OCI instance?",
// not "are these credentials good" -- that remains the SDK's job.
func instanceMetadataReachable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, imdsProbeTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", imdsAddr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// resolveMethod constructs a provider for an explicit Method, always surfacing the
// underlying SDK construction error rather than falling back to another method (AUTH-05).
// opts is passed by value for the same reason documented on Resolve above.
//
//nolint:gocritic
func resolveMethod(m Method, opts ResolveOptions) (common.ConfigurationProvider, Method, error) {
	switch m {
	case MethodConfigFile:
		provider, err := common.ConfigurationProviderFromFileWithProfile(
			configPathOrDefault(opts.ConfigPath), profileOrDefault(opts.Profile), "")
		if err != nil {
			return nil, "", fmt.Errorf("config-file auth (profile %q): %w", profileOrDefault(opts.Profile), err)
		}
		return provider, MethodConfigFile, nil

	case MethodInstancePrincipal:
		provider, err := auth.InstancePrincipalConfigurationProvider()
		if err != nil {
			return nil, "", fmt.Errorf("instance-principal auth: %w", err)
		}
		return provider, MethodInstancePrincipal, nil

	case MethodWorkloadIdentity:
		provider, err := auth.OkeWorkloadIdentityConfigurationProvider()
		if err != nil {
			return nil, "", fmt.Errorf("workload-identity auth: %w", err)
		}
		return provider, MethodWorkloadIdentity, nil

	case MethodGitHubOIDC:
		// Fail fast, before any client construction (07-01-PLAN.md's must_haves): Region is
		// not auto-detected by TokenExchangeConfigurationProvider (Pitfall 4, verified against
		// workload_identity_federation.go's Region() method).
		if opts.OIDCRegion == "" {
			return nil, "", fmt.Errorf("github-oidc auth: --oidc-region is required " +
				"(the OCI region is not auto-detected for this auth method)")
		}

		var missing []string
		if opts.OIDCDomainURL == "" {
			missing = append(missing, "OCI_OIDC_DOMAIN_URL")
		}
		if opts.OIDCClientID == "" {
			missing = append(missing, "OCI_OIDC_CLIENT_ID")
		}
		if opts.OIDCClientSecret == "" {
			missing = append(missing, "OCI_OIDC_CLIENT_SECRET")
		}
		if len(missing) > 0 {
			return nil, "", fmt.Errorf(
				"github-oidc auth: missing required environment variable(s): %s",
				strings.Join(missing, ", "))
		}

		// gosec's G101 flags this literal purely on the "Token"-shaped field names
		// (RequestedTokenType/SubjectTokenType/ClientSecret) -- every value here is either a
		// fixed, public OAuth2 grant-type constant or a field sourced from opts (validated
		// non-empty above, read from OCI_OIDC_CLIENT_SECRET, never a literal secret).
		provider, err := auth.TokenExchangeConfigurationProviderFromIssuer(
			&githubOIDCTokenIssuer{audience: opts.OIDCAudience},
			auth.TokenExchangeBuilder{ //nolint:gosec
				DomainUrl:          oidcTokenExchangeEndpoint(opts.OIDCDomainURL),
				ClientId:           opts.OIDCClientID,
				ClientSecret:       opts.OIDCClientSecret,
				Region:             opts.OIDCRegion,
				RequestedTokenType: "urn:oci:token-type:oci-upst",
				SubjectTokenType:   "jwt",
			},
		)
		if err != nil {
			return nil, "", fmt.Errorf("github-oidc auth: %w", err)
		}
		return provider, MethodGitHubOIDC, nil

	case MethodAPIKey:
		return resolveAPIKey(opts)

	default:
		return nil, "", fmt.Errorf("unknown auth method %q", m)
	}
}

// resolveAPIKey builds a provider from --auth api-key's six environment-sourced values. Split
// out of resolveMethod rather than inlined as one more case: the validation this method needs
// pushed that function past its cyclomatic-complexity budget, and the checks below are the
// substance of the method rather than plumbing around it.
//
// opts is passed by value for the same reason documented on Resolve.
//
//nolint:gocritic
func resolveAPIKey(opts ResolveOptions) (common.ConfigurationProvider, Method, error) {
	// Fail fast and name every missing variable at once, rather than one per run: an operator
	// wiring six secrets into a workflow wants the whole list, not six iterations of the same
	// pipeline to discover it one line at a time.
	var missing []string
	for _, v := range []struct {
		name  string
		value string
	}{
		{EnvAPIKeyTenancy, opts.APIKeyTenancyOCID},
		{EnvAPIKeyUser, opts.APIKeyUserOCID},
		{EnvAPIKeyFingerprint, opts.APIKeyFingerprint},
		{EnvAPIKeyRegion, opts.APIKeyRegion},
		{EnvAPIKeyKeyContent, opts.APIKeyPrivateKey},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return nil, "", fmt.Errorf(
			"api-key auth: missing required environment variable(s): %s",
			strings.Join(missing, ", "))
	}

	// A nil passphrase and a pointer to an empty string are not the same thing to the SDK's
	// PrivateKeyFromBytesWithPassword: the former means "this key is not encrypted", the latter
	// means "decrypt it with an empty password". An unset variable must mean the former.
	var passphrase *string
	if opts.APIKeyPassphrase != "" {
		passphrase = &opts.APIKeyPassphrase
	}

	provider := common.NewRawConfigurationProvider(
		opts.APIKeyTenancyOCID,
		opts.APIKeyUserOCID,
		opts.APIKeyRegion,
		opts.APIKeyFingerprint,
		opts.APIKeyPrivateKey,
		passphrase,
	)

	// Validate here, before any OCI call. NewRawConfigurationProvider itself validates nothing
	// -- it stores the six values and defers every check to the first request signature. The
	// dominant failure of this path is a private key that lost its newlines on the way through
	// a CI secret, and deferring it means the operator sees an opaque signing failure somewhere
	// inside the first list call instead of a sentence naming the variable to fix.
	// IsConfigurationProviderValid runs TenancyOCID/UserOCID/KeyFingerprint/Region/KeyID and
	// then PrivateRSAKey, so a malformed region identifier is caught by the same call.
	if _, err := common.IsConfigurationProviderValid(provider); err != nil {
		return nil, "", fmt.Errorf(
			"api-key auth: the supplied credentials are not usable: %w (%s must be the PEM's "+
				"own bytes, BEGIN/END lines and real newlines included -- a key flattened onto "+
				"one line is the usual cause)", err, EnvAPIKeyKeyContent)
	}
	return provider, MethodAPIKey, nil
}

// oidcTokenExchangeEndpoint normalizes an operator-supplied Identity Domain URL into the bare
// domain root TokenExchangeBuilder.DomainUrl must be -- NOT the full /oauth2/v1/token endpoint.
//
// This corrects an assumption 07-RESEARCH.md's static source read got wrong, caught only by
// live-executing the pinned SDK against a stub server during this task (not by re-reading the
// source more carefully): newTokenExchangeToken (federation_client.go:998) does build its
// httpRequest.URL directly from fc.client.Host, but that request is then dispatched through
// fc.client.Call -> common.BaseClient.CallWithDetails -> prepareRequest
// (oci-go-sdk/v65@v65.123.0/common/client.go:512-549), which RE-PARSES client.Host and hard
// -rejects it if the parsed URL carries any path, query, or fragment at all
// ("host is invalid. endpoint must not contain user info, path, query, or fragment") --
// confirmed live: a DomainUrl with "/oauth2/v1/token" already appended fails every retry with
// exactly that error and the provider never reaches the token endpoint. With a bare root
// (empty path), prepareRequest instead derives the correct request path from client.BasePath,
// which newTokenExchangeFederationClient always hardcodes to "/oauth2/v1/token"
// (federation_client.go:884) regardless of what DomainUrl contains -- also confirmed live: the
// stub server receives exactly "/oauth2/v1/token" when DomainUrl is the bare root.
//
// Net effect: Oracle's own documentation (bare domain root) was right; 07-RESEARCH.md's
// "must be the full endpoint" correction was itself incorrect. This function accepts either
// shape from --oidc-domain-url/OCI_OIDC_DOMAIN_URL regardless, stripping a trailing
// "/oauth2/v1/token" and any trailing slash so both a bare root and a full URL work
// identically for the operator.
//
// WR-04 (07-REVIEW.md): prepareRequest's own rejection is not limited to a non-empty path --
// its documented check is "must not contain user info, path, query, or fragment" (comment
// above, confirmed from oci-go-sdk/v65@v65.123.0/common/client.go:535-537). The original
// implementation only ever trimmed a fixed "/oauth2/v1/token" path suffix, so a domain URL
// copied with a trailing query string or fragment (a browser address bar, an Oracle console
// deep link with tracking parameters) was left untouched and still hard-rejected by the SDK,
// with a generic low-level error that gives the operator no hint --oidc-domain-url/
// OCI_OIDC_DOMAIN_URL is the actual problem. Parsing and rebuilding from scheme+host alone
// (clearing Path, RawQuery, Fragment, and User) handles every component prepareRequest
// rejects, not only the one this function happened to already know about, and generalizes
// past the "/oauth2/v1/token"-specific suffix trim: ANY path is stripped, not only that exact
// one.
func oidcTokenExchangeEndpoint(domainURL string) string {
	trimmed := strings.TrimRight(domainURL, "/")

	u, err := url.Parse(trimmed)
	if err != nil {
		// Malformed input: return it unchanged rather than guessing -- the SDK's own request
		// construction/validation surfaces a clear error for a truly malformed URL, and this
		// function has no better fallback to offer.
		return domainURL
	}

	u.Path, u.RawQuery, u.Fragment, u.User = "", "", "", nil
	return strings.TrimRight(u.String(), "/")
}

// configPathOrDefault returns path, or $HOME/.oci/config when path is empty.
func configPathOrDefault(path string) string {
	if path != "" {
		return path
	}
	home, _ := os.UserHomeDir()
	return home + "/.oci/config"
}

// profileOrDefault returns profile, or "DEFAULT" when profile is empty.
func profileOrDefault(profile string) string {
	if profile != "" {
		return profile
	}
	return "DEFAULT"
}
