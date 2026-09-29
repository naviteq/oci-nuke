package ociauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// fakeConfigProvider implements common.ConfigurationProvider directly (rather than going
// through common.NewRawConfigurationProvider's file-path-oriented signature) so the test
// controls every field explicitly, including a freshly generated RSA key the SDK's request
// signer needs (signing happens before the HTTP layer is reached, so PrivateRSAKey() must
// return a real key even though the request never leaves the process).
type fakeConfigProvider struct {
	tenancyOCID string
	key         *rsa.PrivateKey
}

func (f *fakeConfigProvider) TenancyOCID() (string, error)    { return f.tenancyOCID, nil }
func (f *fakeConfigProvider) UserOCID() (string, error)       { return "ocid1.user.oc1..test", nil }
func (f *fakeConfigProvider) KeyFingerprint() (string, error) { return "aa:bb:cc", nil }
func (f *fakeConfigProvider) Region() (string, error)         { return "us-ashburn-1", nil }
func (f *fakeConfigProvider) AuthType() (common.AuthConfig, error) {
	return common.AuthConfig{AuthType: common.UserPrincipal}, nil
}
func (f *fakeConfigProvider) KeyID() (string, error) {
	return f.tenancyOCID + "/ocid1.user.oc1..test/aa:bb:cc", nil
}
func (f *fakeConfigProvider) PrivateRSAKey() (*rsa.PrivateKey, error) { return f.key, nil }

// stubDispatcher implements common.HTTPRequestDispatcher (BaseClient.HTTPClient's exact
// interface) so the fake Identity API response never touches the network.
type stubDispatcher struct {
	statusCode int
	body       string
}

func (d *stubDispatcher) Do(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: d.statusCode,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     make(http.Header),
	}, nil
}

func withStubIdentityClient(t *testing.T, body string) {
	t.Helper()
	orig := newIdentityClient
	t.Cleanup(func() { newIdentityClient = orig })
	newIdentityClient = func(cp common.ConfigurationProvider) (identity.IdentityClient, error) {
		client, err := identity.NewIdentityClientWithConfigurationProvider(cp)
		if err != nil {
			return client, err
		}
		client.HTTPClient = &stubDispatcher{statusCode: 200, body: body}
		return client, nil
	}
}

func TestVerifyTenancy_Mismatch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeConfigProvider{tenancyOCID: "ocid1.tenancy.oc1..actual", key: key}
	withStubIdentityClient(t, `{"id":"ocid1.tenancy.oc1..actual","name":"actual-tenancy"}`)

	err = VerifyTenancy(context.Background(), provider, "ocid1.tenancy.oc1..expected")
	if err == nil {
		t.Fatal("expected a tenancy mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "tenancy mismatch") {
		t.Fatalf("expected 'tenancy mismatch' in error, got: %v", err)
	}
}

func TestVerifyTenancy_Match(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeConfigProvider{tenancyOCID: "ocid1.tenancy.oc1..same", key: key}
	withStubIdentityClient(t, `{"id":"ocid1.tenancy.oc1..same","name":"same-tenancy"}`)

	if err := VerifyTenancy(context.Background(), provider, "ocid1.tenancy.oc1..same"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
