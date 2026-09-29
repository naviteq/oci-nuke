package ociauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture identity --auth api-key's tests authenticate as. Nothing here reaches OCI: the
// method builds a provider from raw values and validates them locally, so a fabricated OCID
// and fingerprint are indistinguishable from real ones at this layer.
// The identifiers deliberately say "fixture" rather than "apiKey": both gosec's G101 and
// gitleaks' generic-api-key heuristic key off an identifier containing "key"/"user"/"tenancy"
// next to a quoted string, and .gitleaks.toml's own comment records this repository's
// preference -- rename the identifier so the heuristic stops firing, rather than allowlist a
// value and blunt the scanner for everything committed near it later.
const (
	testFixtureTenancy     = "ocid1.tenancy.oc1..fixture"
	testFixtureUser        = "ocid1.user.oc1..fixture"
	testFixtureFingerprint = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"
	testFixtureRegion      = "eu-frankfurt-1"

	// testProfile is the profile name writeConfig writes, shared by every test in this package
	// that resolves through a config file (goconst: referenced 3+ times across the package).
	testProfile = "TESTPROFILE"
)

// validAPIKeyOptions returns a fully-populated MethodAPIKey ResolveOptions carrying a freshly
// generated, unencrypted PKCS#1 key.
func validAPIKeyOptions(t *testing.T) ResolveOptions {
	t.Helper()
	return ResolveOptions{
		Method:            MethodAPIKey,
		APIKeyTenancyOCID: testFixtureTenancy,
		APIKeyUserOCID:    testFixtureUser,
		APIKeyFingerprint: testFixtureFingerprint,
		APIKeyRegion:      testFixtureRegion,
		APIKeyPrivateKey:  generatePEMKey(t),
	}
}

// generatePEMKey returns a PEM-encoded, unencrypted RSA private key. 2048 bits rather than
// 4096 purely for test runtime; the SDK's parser does not care about the modulus size.
func generatePEMKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  pemBlockTypeRSAPrivateKey,
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

// TestResolveAPIKey_MissingCredentialsNamesEveryVariable proves an empty MethodAPIKey request
// fails before touching anything and names all five required variables in one error. Naming
// them one per run would cost an operator wiring up a workflow five pipeline iterations to
// learn what a single message can say.
func TestResolveAPIKey_MissingCredentialsNamesEveryVariable(t *testing.T) {
	_, _, err := Resolve(context.Background(), ResolveOptions{Method: MethodAPIKey})
	if err == nil {
		t.Fatal("expected an error when every api-key credential field is empty")
	}
	for _, want := range []string{
		EnvAPIKeyTenancy,
		EnvAPIKeyUser,
		EnvAPIKeyFingerprint,
		EnvAPIKeyRegion,
		EnvAPIKeyKeyContent,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %s: %v", want, err)
		}
	}
	// The passphrase is genuinely optional, so listing it among the missing would send an
	// operator looking for a secret they do not need.
	if strings.Contains(err.Error(), EnvAPIKeyPassphrase) {
		t.Fatalf("error names the optional passphrase variable as missing: %v", err)
	}
}

// TestResolveAPIKey_PartialCredentialsNameOnlyWhatIsMissing proves the error is a real
// enumeration of what is absent, not a fixed sentence listing all five whatever the input.
func TestResolveAPIKey_PartialCredentialsNameOnlyWhatIsMissing(t *testing.T) {
	opts := validAPIKeyOptions(t)
	opts.APIKeyFingerprint = ""
	opts.APIKeyRegion = ""

	_, _, err := Resolve(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error when the fingerprint and region are empty")
	}
	for _, want := range []string{EnvAPIKeyFingerprint, EnvAPIKeyRegion} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %s: %v", want, err)
		}
	}
	for _, unwanted := range []string{EnvAPIKeyTenancy, EnvAPIKeyUser, EnvAPIKeyKeyContent} {
		if strings.Contains(err.Error(), unwanted) {
			t.Fatalf("error names %s, which was supplied: %v", unwanted, err)
		}
	}
}

// TestResolveAPIKey_FlattenedPrivateKeyIsRejected pins the failure this method's validation
// exists for. A PEM pasted into a CI secret and re-emitted without its newlines is the
// dominant way this path breaks, and common.NewRawConfigurationProvider validates nothing on
// its own -- it stores the six values and defers every check to the first request signature.
// Without the resolve-time check, the operator's first symptom is a signing error from inside
// some list call, with no mention of the key at all.
func TestResolveAPIKey_FlattenedPrivateKeyIsRejected(t *testing.T) {
	opts := validAPIKeyOptions(t)
	opts.APIKeyPrivateKey = strings.ReplaceAll(opts.APIKeyPrivateKey, "\n", " ")

	_, _, err := Resolve(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a private key whose newlines were flattened to spaces")
	}
	if !strings.Contains(err.Error(), EnvAPIKeyKeyContent) {
		t.Fatalf("error does not name %s, so it does not point at the key: %v", EnvAPIKeyKeyContent, err)
	}
}

// TestResolveAPIKey_GarbagePrivateKeyIsRejected covers the other shape of the same mistake --
// a variable holding a key *path* (the ~/.oci/config habit) or an empty-after-trim value
// rather than the PEM's bytes.
func TestResolveAPIKey_GarbagePrivateKeyIsRejected(t *testing.T) {
	for name, value := range map[string]string{
		"a path instead of the content": "/home/runner/.oci/oci_api_key.pem",
		"base64 without the PEM armour": "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ",
	} {
		t.Run(name, func(t *testing.T) {
			opts := validAPIKeyOptions(t)
			opts.APIKeyPrivateKey = value

			if _, _, err := Resolve(context.Background(), opts); err == nil {
				t.Fatalf("expected an error for a private key given as %s", name)
			}
		})
	}
}

// TestResolveAPIKey_MalformedRegionIsRejected proves the region identifier is checked at
// resolve time too. The SDK's own Region() runs canStringBeRegion, so a typo surfaces here
// rather than as a DNS failure against a hostname that does not exist.
func TestResolveAPIKey_MalformedRegionIsRejected(t *testing.T) {
	opts := validAPIKeyOptions(t)
	opts.APIKeyRegion = "not a region"

	if _, _, err := Resolve(context.Background(), opts); err == nil {
		t.Fatal("expected an error for a malformed region identifier")
	}
}

// TestResolveAPIKey_ValidCredentials is the happy path: the resolved provider reports
// MethodAPIKey and hands back exactly the identity it was given, so the principal_ocid the run
// logs for audit is the supplied user and not something derived elsewhere.
func TestResolveAPIKey_ValidCredentials(t *testing.T) {
	provider, method, err := Resolve(context.Background(), validAPIKeyOptions(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if method != MethodAPIKey {
		t.Fatalf("method = %q, want %q", method, MethodAPIKey)
	}
	if provider == nil {
		t.Fatal("Resolve returned a nil provider with a nil error")
	}

	for _, tc := range []struct {
		field string
		got   func() (string, error)
		want  string
	}{
		{"tenancy OCID", provider.TenancyOCID, testFixtureTenancy},
		{"user OCID", provider.UserOCID, testFixtureUser},
		{"key fingerprint", provider.KeyFingerprint, testFixtureFingerprint},
		{"region", provider.Region, testFixtureRegion},
	} {
		got, err := tc.got()
		if err != nil {
			t.Fatalf("reading %s: %v", tc.field, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, got, tc.want)
		}
	}
}

// TestResolveAPIKey_PassphraseProtectedKey proves OCI_CLI_KEY_PASSPHRASE is actually applied,
// and that omitting it for an encrypted key fails rather than silently producing a provider
// that cannot sign.
func TestResolveAPIKey_PassphraseProtectedKey(t *testing.T) {
	const passphrase = "correct horse battery staple"

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	// x509.EncryptPEMBlock is deprecated because the legacy PEM encryption it implements is
	// weak. It is used here regardless, and only here, because it is the one encrypted-PEM
	// shape the Go standard library can still *produce*, and the SDK's
	// PrivateKeyFromBytesWithPassword reads it (x509.IsEncryptedPEMBlock ->
	// x509.DecryptPEMBlock). Nothing in oci-nuke writes keys; this fixture exists to prove the
	// read path honors a passphrase.
	block, err := x509.EncryptPEMBlock( //nolint:staticcheck
		rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key),
		[]byte(passphrase), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("x509.EncryptPEMBlock: %v", err)
	}
	encrypted := string(pem.EncodeToMemory(block))

	opts := validAPIKeyOptions(t)
	opts.APIKeyPrivateKey = encrypted

	if _, _, err := Resolve(context.Background(), opts); err == nil {
		t.Fatal("expected an error for an encrypted key with no OCI_CLI_KEY_PASSPHRASE")
	}

	opts.APIKeyPassphrase = passphrase
	_, method, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve with a passphrase: %v", err)
	}
	if method != MethodAPIKey {
		t.Fatalf("method = %q, want %q", method, MethodAPIKey)
	}
}

// TestResolve_AutoDetectNeverSelectsAPIKey is the safety property, and the reason this method
// is opt-in. Complete api-key credentials present with no explicit Method must still walk the
// ordinary ladder: an API signing key left exported in a shell, or baked into a CI image, must
// not silently decide which principal deletes things.
func TestResolve_AutoDetectNeverSelectsAPIKey(t *testing.T) {
	if _, set := os.LookupEnv("OCI_RESOURCE_PRINCIPAL_VERSION"); set {
		t.Skip("OCI_RESOURCE_PRINCIPAL_VERSION is set; auto-detection would pick workload identity")
	}

	closedAddr := reserveClosedAddr(t)
	restore := swapIMDS(t, closedAddr, imdsProbeTimeout)
	defer restore()

	configPath := filepath.Join(t.TempDir(), "config")
	writeConfig(t, configPath)

	opts := validAPIKeyOptions(t)
	opts.Method = ""
	opts.ConfigPath = configPath
	opts.Profile = testProfile

	_, method, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if method == MethodAPIKey {
		t.Fatal("auto-detection selected MethodAPIKey -- it must be reachable only via an explicit --auth api-key")
	}
	if method != MethodConfigFile {
		t.Fatalf("method = %q, want %q", method, MethodConfigFile)
	}
}
