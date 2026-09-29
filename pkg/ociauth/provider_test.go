package ociauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testGitHubOIDCTenancyID is the fixture tenancy OCID
// TestResolveGitHubOIDC_FullRoundTrip's fabricated UPST claims and VerifyTenancy check agree
// on. testOIDCRegion is the fixture --oidc-region value shared by every MethodGitHubOIDC test
// needing a non-empty region (goconst: referenced 2+ times across this file).
const (
	testGitHubOIDCTenancyID = "ocid1.tenancy.oc1..githuboidc"
	testOIDCRegion          = "us-ashburn-1"
)

// TestInstanceMetadataReachable_ClosedPortReturnsFastFalse pins the fix for the auto-detect
// hang: auth.InstancePrincipalConfigurationProvider() retries the IMDS call on network
// timeouts and took 6m1s to return an error on a developer laptop, during which `oci-nuke
// run` produced no output. Auto-detection must decide "not on an OCI instance" under a hard
// bound instead.
func TestInstanceMetadataReachable_ClosedPortReturnsFastFalse(t *testing.T) {
	// A listener that is opened and immediately closed yields an address nothing is bound
	// to, so the dial is refused rather than routed into a timeout. That exercises the
	// error path without depending on any particular unroutable address behaving the same
	// way on every developer machine and CI runner.
	closedAddr := reserveClosedAddr(t)

	restore := swapIMDS(t, closedAddr, imdsProbeTimeout)
	defer restore()

	start := time.Now()
	if instanceMetadataReachable(context.Background()) {
		t.Fatal("instanceMetadataReachable returned true for a port nothing is listening on")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("probe took %s; it must stay bounded so auto-detection cannot hang", elapsed)
	}
}

// TestInstanceMetadataReachable_ListeningPortReturnsTrue is the negative control: without
// it, a probe that always returned false would pass the test above and silently disable
// instance-principal auth on real OCI compute instances.
func TestInstanceMetadataReachable_ListeningPortReturnsTrue(t *testing.T) {
	listener := listen(t)
	defer func() { _ = listener.Close() }()

	restore := swapIMDS(t, listener.Addr().String(), imdsProbeTimeout)
	defer restore()

	if !instanceMetadataReachable(context.Background()) {
		t.Fatal("instanceMetadataReachable returned false for a listening port")
	}
}

// TestInstanceMetadataReachable_HonoursCancelledContext proves the probe is bounded by the
// caller's context too, not only by its own timeout.
func TestInstanceMetadataReachable_HonoursCancelledContext(t *testing.T) {
	restore := swapIMDS(t, imdsAddr, time.Minute)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if instanceMetadataReachable(ctx) {
		t.Fatal("instanceMetadataReachable returned true for a canceled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probe took %s with a canceled context; it must return immediately", elapsed)
	}
}

// TestResolve_AutoDetectFallsThroughToConfigFile is the end-to-end assertion that matters:
// on a machine that is not an OCI instance, auto-detection reaches the config file quickly
// rather than stalling on the instance-principal probe.
func TestResolve_AutoDetectFallsThroughToConfigFile(t *testing.T) {
	if _, set := os.LookupEnv("OCI_RESOURCE_PRINCIPAL_VERSION"); set {
		t.Skip("OCI_RESOURCE_PRINCIPAL_VERSION is set; auto-detection would pick workload identity")
	}

	closedAddr := reserveClosedAddr(t)

	restore := swapIMDS(t, closedAddr, imdsProbeTimeout)
	defer restore()

	configPath := filepath.Join(t.TempDir(), "config")
	writeConfig(t, configPath)

	start := time.Now()
	provider, method, err := Resolve(context.Background(), ResolveOptions{
		ConfigPath: configPath,
		Profile:    testProfile,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if method != MethodConfigFile {
		t.Fatalf("method = %q, want %q", method, MethodConfigFile)
	}
	if provider == nil {
		t.Fatal("Resolve returned a nil provider with a nil error")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("auto-detection took %s; the instance-principal probe is not bounded", elapsed)
	}

	tenancy, err := provider.TenancyOCID()
	if err != nil {
		t.Fatalf("TenancyOCID: %v", err)
	}
	if want := "ocid1.tenancy.oc1..test"; tenancy != want {
		t.Fatalf("tenancy = %q, want %q", tenancy, want)
	}
}

// listen opens a loopback listener on an arbitrary free port.
func listen(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	return listener
}

// reserveClosedAddr returns a loopback address that nothing is listening on, by opening a
// listener to claim a free port and closing it immediately. Dials to it are refused rather
// than left to time out, which keeps the test fast and independent of how any given machine
// routes unroutable addresses.
func reserveClosedAddr(t *testing.T) string {
	t.Helper()
	listener := listen(t)
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("closing the reserved listener: %v", err)
	}
	return addr
}

// swapIMDS points the probe at addr with the given timeout and returns a restore func.
func swapIMDS(t *testing.T, addr string, timeout time.Duration) func() {
	t.Helper()
	prevAddr, prevTimeout := imdsAddr, imdsProbeTimeout
	imdsAddr, imdsProbeTimeout = addr, timeout
	return func() { imdsAddr, imdsProbeTimeout = prevAddr, prevTimeout }
}

// writeConfig writes a syntactically valid OCI config file plus the key it references.
// The key is generated per-run rather than embedded as a fixture: a checked-in PEM private
// key is indistinguishable from a leaked one to any secret scanner, including the one this
// repository runs on every pull request.
func writeConfig(t *testing.T, path string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  pemBlockTypeRSAPrivateKey,
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	keyPath := filepath.Join(filepath.Dir(path), "key.pem")
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}

	body := "[TESTPROFILE]\n" +
		"user=ocid1.user.oc1..test\n" +
		"tenancy=ocid1.tenancy.oc1..test\n" +
		"region=eu-frankfurt-1\n" +
		"fingerprint=aa:bb:cc\n" +
		"key_file=" + keyPath + "\n"

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
}

// TestResolveGitHubOIDC_MissingRegion proves --oidc-region is required and checked before any
// client construction -- TokenExchangeConfigurationProvider does not auto-detect a region
// (Pitfall 4, verified against workload_identity_federation.go's Region() method).
func TestResolveGitHubOIDC_MissingRegion(t *testing.T) {
	_, _, err := Resolve(context.Background(), ResolveOptions{
		Method:           MethodGitHubOIDC,
		OIDCDomainURL:    "https://idcs-xxxx.identity.oraclecloud.com",
		OIDCClientID:     "client-id",
		OIDCClientSecret: "client-secret",
	})
	if err == nil {
		t.Fatal("expected an error when --oidc-region is empty")
	}
	if !strings.Contains(err.Error(), "--oidc-region") {
		t.Fatalf("error does not name --oidc-region: %v", err)
	}
}

// TestResolveGitHubOIDC_MissingCredentials proves a missing domain URL/client ID/client secret
// fails fast, naming all three env vars, before any HTTP call is attempted.
func TestResolveGitHubOIDC_MissingCredentials(t *testing.T) {
	_, _, err := Resolve(context.Background(), ResolveOptions{
		Method:     MethodGitHubOIDC,
		OIDCRegion: testOIDCRegion,
	})
	if err == nil {
		t.Fatal("expected an error when the OIDC credential fields are empty")
	}
	for _, want := range []string{"OCI_OIDC_DOMAIN_URL", "OCI_OIDC_CLIENT_ID", "OCI_OIDC_CLIENT_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %s: %v", want, err)
		}
	}
}

// TestOidcTokenExchangeEndpoint proves oidcTokenExchangeEndpoint normalizes any operator input
// down to the bare domain root -- the shape TokenExchangeBuilder.DomainUrl actually requires,
// per this task's live-execution finding against the pinned SDK (see the function's own doc
// comment): a DomainUrl carrying any path fails common.BaseClient's own prepareRequest
// validation ("host is invalid... must not contain... path"), so both a bare root and an
// already-full endpoint URL must normalize to the identical bare-root result.
func TestOidcTokenExchangeEndpoint(t *testing.T) {
	const wantRoot = "https://idcs-xxxx.identity.oraclecloud.com"

	tests := []struct {
		name  string
		input string
	}{
		{name: "bare domain root", input: "https://idcs-xxxx.identity.oraclecloud.com"},
		{name: "trailing slash", input: "https://idcs-xxxx.identity.oraclecloud.com/"},
		{name: "already the full endpoint", input: "https://idcs-xxxx.identity.oraclecloud.com/oauth2/v1/token"},
		{name: "full endpoint with trailing slash", input: "https://idcs-xxxx.identity.oraclecloud.com/oauth2/v1/token/"},
		// WR-04 (07-REVIEW.md) regression cases: a query string or fragment (e.g. copied from a
		// browser address bar, or an Oracle console deep link with tracking parameters) must be
		// stripped too -- common.BaseClient.prepareRequest hard-rejects a Host carrying either,
		// per this function's own doc comment.
		{name: "bare root with a query string", input: "https://idcs-xxxx.identity.oraclecloud.com?utm_source=console"},
		{name: "bare root with a fragment", input: "https://idcs-xxxx.identity.oraclecloud.com#section"},
		{name: "full endpoint with a query string", input: "https://idcs-xxxx.identity.oraclecloud.com/oauth2/v1/token?utm_source=console"},
		{name: "full endpoint with a fragment", input: "https://idcs-xxxx.identity.oraclecloud.com/oauth2/v1/token#section"},
		{
			name:  "full endpoint with both a query string and a fragment",
			input: "https://idcs-xxxx.identity.oraclecloud.com/oauth2/v1/token?utm_source=console#section",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := oidcTokenExchangeEndpoint(tc.input); got != wantRoot {
				t.Fatalf("oidcTokenExchangeEndpoint(%q) = %q, want %q", tc.input, got, wantRoot)
			}
		})
	}
}

// TestResolveGitHubOIDC_FullRoundTrip proves the whole provider chain -- GitHub OIDC JWT fetch
// -> OCI token exchange -> common.ConfigurationProvider -> ociauth.VerifyTenancy -- is
// structurally correct against stubbed HTTP servers standing in for both the GitHub OIDC
// endpoint and OCI's /oauth2/v1/token endpoint. This is the strongest proof this plan can offer
// without a real Identity Propagation Trust (07-CONTEXT.md's environment note): the live
// federation proof is deferred to Phase 8's e2e harness.
func TestResolveGitHubOIDC_FullRoundTrip(t *testing.T) {
	const wantBearer = "gh-runner-token"

	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "bearer "+wantBearer {
			t.Errorf("Authorization header = %q, want %q", got, "bearer "+wantBearer)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":"fake-github-oidc-jwt"}`)
	}))
	defer oidcServer.Close()

	fakeUPST := fabricateJWT(t, map[string]interface{}{
		"tenant": testGitHubOIDCTenancyID,
		"sub":    "ocid1.dynamicgroup.oc1..svcuser",
		"exp":    float64(time.Now().Add(24 * time.Hour).Unix()),
	})

	tokenExchangeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Proves oidcTokenExchangeEndpoint is exercised, not bypassed -- a wrong DomainUrl
		// (the bare domain root Oracle's own docs describe) would hit "/" here, not this path.
		if r.URL.Path != "/oauth2/v1/token" {
			t.Fatalf("token-exchange request path = %q, want /oauth2/v1/token", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q}`, fakeUPST)
	}))
	defer tokenExchangeServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", wantBearer)

	provider, method, err := Resolve(context.Background(), ResolveOptions{
		Method:           MethodGitHubOIDC,
		OIDCRegion:       testOIDCRegion,
		OIDCAudience:     "https://idcs-test.identity.oraclecloud.com",
		OIDCDomainURL:    tokenExchangeServer.URL,
		OIDCClientID:     "test-client-id",
		OIDCClientSecret: "test-client-secret",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if method != MethodGitHubOIDC {
		t.Fatalf("method = %q, want %q", method, MethodGitHubOIDC)
	}
	if provider == nil {
		t.Fatal("Resolve returned a nil provider with a nil error")
	}

	withStubIdentityClient(t, fmt.Sprintf(`{"id":%q,"name":"github-oidc-tenancy"}`, testGitHubOIDCTenancyID))

	if err := VerifyTenancy(context.Background(), provider, testGitHubOIDCTenancyID); err != nil {
		t.Fatalf("VerifyTenancy against the github-oidc provider: %v", err)
	}
}

// TestResolve_AutoDetectNeverSelectsGitHubOIDC proves MethodGitHubOIDC is reachable ONLY
// through an explicit opts.Method -- even with every GitHub Actions runner env var AND a
// valid, existing config file simultaneously present, auto-detection (opts.Method == "") falls
// through to MethodConfigFile exactly as it did before this plan, because Resolve's ladder has
// no branch referencing ACTIONS_ID_TOKEN_REQUEST_URL/_TOKEN at all.
func TestResolve_AutoDetectNeverSelectsGitHubOIDC(t *testing.T) {
	if _, set := os.LookupEnv("OCI_RESOURCE_PRINCIPAL_VERSION"); set {
		t.Skip("OCI_RESOURCE_PRINCIPAL_VERSION is set; auto-detection would pick workload identity")
	}

	closedAddr := reserveClosedAddr(t)
	restore := swapIMDS(t, closedAddr, imdsProbeTimeout)
	defer restore()

	// Simulate running inside an actual GitHub Actions job with id-token: write.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "http://127.0.0.1:0/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "gh-token")

	configPath := filepath.Join(t.TempDir(), "config")
	writeConfig(t, configPath)

	provider, method, err := Resolve(context.Background(), ResolveOptions{
		ConfigPath: configPath,
		Profile:    testProfile,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if method == MethodGitHubOIDC {
		t.Fatal("auto-detection selected MethodGitHubOIDC -- it must be reachable only via an explicit --auth github-oidc")
	}
	if method != MethodConfigFile {
		t.Fatalf("method = %q, want %q", method, MethodConfigFile)
	}
	if provider == nil {
		t.Fatal("Resolve returned a nil provider with a nil error")
	}
}

// fabricateJWT builds a syntactically valid (unsigned) JWT string -- header.payload.sig,
// base64url segments -- carrying claims. parseJwt (oci-go-sdk/v65@.../common/auth/jwt.go:35-64,
// verified this session) performs zero signature verification client-side, only
// base64-decode + JSON-unmarshal, so a fabricated, unsigned token is a legitimate test double
// for the exchanged UPST.
func fabricateJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshaling JWT header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshaling JWT payload: %v", err)
	}

	enc := base64.RawURLEncoding
	return enc.EncodeToString(header) + "." + enc.EncodeToString(payload) + ".sig"
}
