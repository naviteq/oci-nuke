package run

import "testing"

// TestReadAPIKeyEnv pins the six documented variable names against the six os.Getenv keys.
// A typo in one of them is otherwise invisible: the run reports that variable as missing, so
// the operator goes looking at their CI secret rather than at readAPIKeyEnv. The values are
// deliberately distinct so a pair of transposed keys fails rather than canceling out.
func TestReadAPIKeyEnv(t *testing.T) {
	t.Setenv("OCI_CLI_TENANCY", "tenancy-value")
	t.Setenv("OCI_CLI_USER", "user-value")
	t.Setenv("OCI_CLI_FINGERPRINT", "fingerprint-value")
	t.Setenv("OCI_CLI_REGION", "region-value")
	t.Setenv("OCI_CLI_KEY_CONTENT", "key-content-value")
	t.Setenv("OCI_CLI_KEY_PASSPHRASE", "passphrase-value")

	got := readAPIKeyEnv()
	for _, tc := range []struct {
		field string
		got   string
		want  string
	}{
		{"tenancyOCID", got.tenancyOCID, "tenancy-value"},
		{"userOCID", got.userOCID, "user-value"},
		{"fingerprint", got.fingerprint, "fingerprint-value"},
		{"region", got.region, "region-value"},
		{"privateKey", got.privateKey, "key-content-value"},
		{"passphrase", got.passphrase, "passphrase-value"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// TestReadAPIKeyEnv_UnsetIsEmpty proves an absent environment leaves every field empty rather
// than picking up a default from anywhere, so ociauth.Resolve's missing-variable enumeration is
// what an operator sees -- not a partially-populated credential that fails later.
func TestReadAPIKeyEnv_UnsetIsEmpty(t *testing.T) {
	for _, name := range []string{
		"OCI_CLI_TENANCY",
		"OCI_CLI_USER",
		"OCI_CLI_FINGERPRINT",
		"OCI_CLI_REGION",
		"OCI_CLI_KEY_CONTENT",
		"OCI_CLI_KEY_PASSPHRASE",
	} {
		t.Setenv(name, "")
	}

	if got := readAPIKeyEnv(); got != (apiKeyCredentials{}) {
		t.Fatalf("readAPIKeyEnv() = %+v, want the zero value", got)
	}
}
