package ociauth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
)

// fakeSessionProvider stands in for whatever Resolve returned. Only the four methods
// ExportSession reads are meaningful; the rest satisfy the interface.
type fakeSessionProvider struct {
	keyID    string
	keyIDErr error
	key      *rsa.PrivateKey
	keyErr   error
	tenancy  string
	region   string
}

func (f *fakeSessionProvider) KeyID() (string, error)                  { return f.keyID, f.keyIDErr }
func (f *fakeSessionProvider) PrivateRSAKey() (*rsa.PrivateKey, error) { return f.key, f.keyErr }
func (f *fakeSessionProvider) TenancyOCID() (string, error)            { return f.tenancy, nil }
func (f *fakeSessionProvider) UserOCID() (string, error)               { return "", nil }
func (f *fakeSessionProvider) KeyFingerprint() (string, error)         { return "", nil }
func (f *fakeSessionProvider) Region() (string, error)                 { return f.region, nil }
func (f *fakeSessionProvider) AuthType() (common.AuthConfig, error) {
	return common.AuthConfig{}, nil
}

// jwtWithExp builds a token shaped like a UPST: three dot-separated base64url segments, with a
// readable `exp` in the payload. Nothing verifies the signature, so the third segment is filler.
func jwtWithExp(t *testing.T, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": exp.Unix()})
	if err != nil {
		t.Fatalf("marshaling the test payload: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf("%s.%s.%s",
		enc([]byte(`{"alg":"RS256"}`)), enc(payload), enc([]byte("signature")))
}

func testSessionProvider(t *testing.T, keyID string) (*fakeSessionProvider, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	return &fakeSessionProvider{
		keyID:   keyID,
		key:     key,
		tenancy: "ocid1.tenancy.oc1..sessiontest",
		region:  testOIDCRegion,
	}, key
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %04o, want %04o -- a credential on a CI runner is not group- or world-readable", path, got, want)
	}
}

// TestExportSession_WritesAProfileTheSDKItselfAccepts is the assertion that matters. The written
// file is not checked field by field against a format this package invented: it is loaded back
// through the SDK's own session-token provider, which is the same code path the OpenTofu OCI
// provider takes for `auth = "SecurityToken"`. If the SDK can build a provider from it and the
// token and key survive the round trip, Tofu and the OCI CLI can use it too.
func TestExportSession_WritesAProfileTheSDKItselfAccepts(t *testing.T) {
	exp := time.Now().Add(47 * time.Minute).Truncate(time.Second).UTC()
	token := jwtWithExp(t, exp)
	provider, key := testSessionProvider(t, securityTokenKeyIDPrefix+token)

	dir := filepath.Join(t.TempDir(), "session")
	files, err := ExportSession(provider, dir, "E2E")
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}

	assertMode(t, files.ConfigPath, 0o600)
	assertMode(t, files.TokenPath, 0o600)
	assertMode(t, files.KeyPath, 0o600)
	if info, err := os.Stat(dir); err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("the session directory has mode %04o, want 0700", got)
	}

	loaded, err := common.ConfigurationProviderForSessionTokenWithProfile(files.ConfigPath, files.Profile, "")
	if err != nil {
		t.Fatalf("the SDK could not build a session-token provider from the exported profile: %v", err)
	}

	gotKeyID, err := loaded.KeyID()
	if err != nil {
		t.Fatalf("KeyID from the reloaded provider: %v", err)
	}
	if gotKeyID != securityTokenKeyIDPrefix+token {
		t.Errorf("reloaded KeyID = %q, want the exported token behind %q", gotKeyID, securityTokenKeyIDPrefix)
	}

	gotKey, err := loaded.PrivateRSAKey()
	if err != nil {
		t.Fatalf("PrivateRSAKey from the reloaded provider: %v", err)
	}
	if !gotKey.Equal(key) {
		t.Error("the reloaded private key is not the one exported, so nothing could sign with the token")
	}

	if !files.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %s, want %s", files.ExpiresAt, exp)
	}
}

// TestExportSession_RefusesAMethodWithNoSessionToken covers the api-key case, whose KeyID is
// tenancy/user/fingerprint. Exporting it would write a file the SDK's session-token provider
// cannot use, and the failure would surface inside Terraform rather than here.
func TestExportSession_RefusesAMethodWithNoSessionToken(t *testing.T) {
	provider, _ := testSessionProvider(t,
		"ocid1.tenancy.oc1..sessiontest/ocid1.user.oc1..sessiontest/aa:bb:cc")

	dir := filepath.Join(t.TempDir(), "session")
	_, err := ExportSession(provider, dir, "E2E")
	if err == nil {
		t.Fatal("expected a refusal for a provider whose key id is not a security token")
	}
	for _, want := range []string{"no session token", "api-key", securityTokenKeyIDPrefix} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %q so an operator knows which method to use; got: %v", want, err)
		}
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Error("nothing should have been written for a provider that cannot be exported")
	}
}

// TestExportSession_RefusesAnEmptyToken guards the shape where the prefix is there and the token
// is not. An empty security_token_file is accepted by the file format and rejected by OCI on the
// first request, which is the worst place to find out.
func TestExportSession_RefusesAnEmptyToken(t *testing.T) {
	provider, _ := testSessionProvider(t, securityTokenKeyIDPrefix)

	_, err := ExportSession(provider, filepath.Join(t.TempDir(), "session"), "E2E")
	if err == nil {
		t.Fatal("expected a refusal for a key id that is the prefix and nothing else")
	}
}

// TestExportSession_UnreadableExpiryIsNotAFailure pins the deliberate choice: a token whose
// expiry cannot be read is still a working token, and refusing to export it would trade a real
// capability for a log line.
func TestExportSession_UnreadableExpiryIsNotAFailure(t *testing.T) {
	provider, _ := testSessionProvider(t, securityTokenKeyIDPrefix+"not-a-jwt")

	files, err := ExportSession(provider, filepath.Join(t.TempDir(), "session"), "E2E")
	if err != nil {
		t.Fatalf("ExportSession should succeed with an opaque token: %v", err)
	}
	if !files.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %s, want the zero time for a token whose expiry cannot be read", files.ExpiresAt)
	}
	written, err := os.ReadFile(files.TokenPath)
	if err != nil {
		t.Fatalf("reading the exported token: %v", err)
	}
	if string(written) != "not-a-jwt" {
		t.Errorf("exported token = %q, want the token as given", written)
	}
}

// TestExportSession_RefusesWithoutAPrivateKey covers the provider that has a token and nothing
// to sign with. The pair is only useful together.
func TestExportSession_RefusesWithoutAPrivateKey(t *testing.T) {
	provider := &fakeSessionProvider{
		keyID:   securityTokenKeyIDPrefix + "token",
		tenancy: "ocid1.tenancy.oc1..sessiontest",
		region:  testOIDCRegion,
	}

	_, err := ExportSession(provider, filepath.Join(t.TempDir(), "session"), "E2E")
	if err == nil {
		t.Fatal("expected a refusal when the provider returns no private key")
	}
	if !strings.Contains(err.Error(), "no private key") {
		t.Errorf("the error should say the key is missing; got: %v", err)
	}
}

// TestExportSession_WritesItsOwnConfigFile pins the choice not to append to a shared
// ~/.oci/config: an export that fails half-way would otherwise leave a profile behind pointing
// at files that were never written.
func TestExportSession_WritesItsOwnConfigFile(t *testing.T) {
	provider, _ := testSessionProvider(t, securityTokenKeyIDPrefix+jwtWithExp(t, time.Now().Add(time.Hour)))

	dir := filepath.Join(t.TempDir(), "session")
	files, err := ExportSession(provider, dir, "HARNESS")
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	if got := filepath.Dir(files.ConfigPath); got != filepath.Dir(files.TokenPath) {
		t.Errorf("config and token should live in the same directory; got %s and %s", got, filepath.Dir(files.TokenPath))
	}

	raw, err := os.ReadFile(files.ConfigPath)
	if err != nil {
		t.Fatalf("reading the exported config: %v", err)
	}
	body := string(raw)
	if !strings.HasPrefix(body, "[HARNESS]\n") {
		t.Errorf("the config should hold exactly the requested profile and start with its header; got:\n%s", body)
	}
	// `fingerprint` has to be there: the SDK's session-token provider calls KeyFingerprint()
	// before it will return the token, so a profile without one fails with "fingerprint
	// configuration is missing from file". That is not obvious from the format, and it is the
	// mistake the round-trip test above caught.
	if !strings.Contains(body, "fingerprint=") {
		t.Errorf("the config must carry a fingerprint or the SDK refuses the profile; got:\n%s", body)
	}
	// `user`, on the other hand, is explicitly tolerated as absent, and an empty one would make
	// this read as a broken API-key profile.
	if strings.Contains(body, "user=") {
		t.Errorf("the config should not contain a user; got:\n%s", body)
	}
}
