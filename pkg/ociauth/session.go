package ociauth

import (
	"crypto/md5" //nolint:gosec // OCI's key fingerprint is defined as MD5; nothing here is hashing for security
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
)

// securityTokenKeyIDPrefix is what the SDK puts in front of a session token when it answers
// KeyID(). Verified in oci-go-sdk/v65@v65.123.0: federation_client.go:912 returns
// fmt.Sprintf("ST$%s", ...) for the token-exchange client, and
// instance_principal_key_provider.go:151 does the same. It is the only place a caller can reach
// the UPST -- the SDK exposes no accessor for the token itself.
const securityTokenKeyIDPrefix = "ST$"

// pemBlockTypeRSAPrivateKey is the PEM header the exported session key carries, and the one
// the SDK writes and reads for an API signing key.
const pemBlockTypeRSAPrivateKey = "RSA PRIVATE KEY"

// SessionFiles names everything ExportSession wrote, so a caller can hand the paths to another
// tool without re-deriving them.
type SessionFiles struct {
	// ConfigPath is the OCI config file holding the one profile. It is a file of its own rather
	// than an edit to ~/.oci/config: appending to a shared config makes an export that fails
	// half-way leave a profile pointing at files that do not exist.
	ConfigPath string
	// Profile is the profile name inside ConfigPath.
	Profile string
	// TokenPath and KeyPath are the two files the profile refers to.
	TokenPath string
	KeyPath   string
	// ExpiresAt is the session token's own expiry, read from the JWT's `exp` claim without
	// verifying the signature -- nothing here is trusting the value, it is reported so a caller
	// can decide whether one token will outlive the work it has queued. Zero when the token is
	// not a readable JWT, which is not treated as an error: the token is still usable, and
	// refusing to export a working credential because its expiry could not be printed would be
	// the wrong trade.
	ExpiresAt time.Time
}

// ExportSession writes provider's session token and ephemeral private key into dir, together
// with an OCI config profile that points at them, and returns the paths.
//
// This is how a session obtained by `--auth github-oidc` reaches tools that cannot perform the
// exchange themselves. The OpenTofu OCI provider offers ApiKey, SecurityToken,
// InstancePrincipal, ResourcePrincipal and OKEWorkloadIdentity and nothing GitHub-shaped --
// checked against the provider's own schema -- so a keyless CI run has to hand it a security
// token. Same for the OCI CLI.
//
// It does write a credential to disk, which `--auth api-key` deliberately refuses to do. The
// difference is what is written: a short-lived, exchange-scoped session token rather than a
// long-lived API signing key. The files are 0600 inside a 0700 directory, and the caller is
// expected to put that directory somewhere the runner discards -- RUNNER_TEMP, not $HOME.
func ExportSession(provider common.ConfigurationProvider, dir, profile string) (*SessionFiles, error) {
	if provider == nil {
		return nil, fmt.Errorf("export session: no configuration provider")
	}
	if profile == "" {
		return nil, fmt.Errorf("export session: profile name must not be empty")
	}

	token, err := sessionToken(provider)
	if err != nil {
		return nil, err
	}

	key, err := provider.PrivateRSAKey()
	if err != nil {
		return nil, fmt.Errorf("export session: reading the session's private key: %w", err)
	}
	if key == nil {
		return nil, fmt.Errorf("export session: the provider returned no private key, so nothing could sign a request with the exported token")
	}

	tenancy, err := provider.TenancyOCID()
	if err != nil {
		return nil, fmt.Errorf("export session: reading the tenancy OCID: %w", err)
	}
	region, err := provider.Region()
	if err != nil {
		return nil, fmt.Errorf("export session: reading the region: %w", err)
	}

	// 0700 before anything is written into it, so the two files below are never briefly
	// readable through a permissive directory.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("export session: creating %s: %w", dir, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("export session: resolving %s: %w", dir, err)
	}

	files := &SessionFiles{
		ConfigPath: filepath.Join(abs, "config"),
		Profile:    profile,
		TokenPath:  filepath.Join(abs, "session_token"),
		KeyPath:    filepath.Join(abs, "session_key.pem"),
		ExpiresAt:  tokenExpiry(token),
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  pemBlockTypeRSAPrivateKey,
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(files.KeyPath, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("export session: writing %s: %w", files.KeyPath, err)
	}
	if err := os.WriteFile(files.TokenPath, []byte(token), 0o600); err != nil {
		return nil, fmt.Errorf("export session: writing %s: %w", files.TokenPath, err)
	}

	// `fingerprint` is required even though a session token identifies the caller and nothing
	// signs with the fingerprint: the SDK's own session-token provider calls KeyFingerprint()
	// before it will hand back the token (configuration.go:766-775), so a profile without one
	// fails with "fingerprint configuration is missing from file". Derived from the key actually
	// written rather than copied from anywhere, so the value is true as well as present.
	//
	// No `user`: the same provider explicitly tolerates its absence when a security token file
	// is present, and an empty one would make this read as a broken API-key profile.
	fingerprint, err := keyFingerprint(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("export session: fingerprinting the session key: %w", err)
	}
	config := fmt.Sprintf(
		"[%s]\ntenancy=%s\nregion=%s\nfingerprint=%s\nkey_file=%s\nsecurity_token_file=%s\n",
		profile, tenancy, region, fingerprint, files.KeyPath, files.TokenPath,
	)
	if err := os.WriteFile(files.ConfigPath, []byte(config), 0o600); err != nil {
		return nil, fmt.Errorf("export session: writing %s: %w", files.ConfigPath, err)
	}

	return files, nil
}

// sessionToken pulls the UPST out of the provider's KeyID, and refuses clearly when the
// provider has no session to export.
func sessionToken(provider common.ConfigurationProvider) (string, error) {
	keyID, err := provider.KeyID()
	if err != nil {
		return "", fmt.Errorf("export session: reading the provider's key id: %w", err)
	}
	if !strings.HasPrefix(keyID, securityTokenKeyIDPrefix) {
		// The api-key path is the case that lands here, and it is worth naming: its KeyID is
		// tenancy/user/fingerprint, and there is no session token to hand anybody. A caller
		// wanting a profile for that credential already has the six values it was built from.
		return "", fmt.Errorf(
			"export session: this auth method has no session token to export -- its key id is not a %q "+
				"security token. Only the exchange-based methods (--auth github-oidc, instance principal) "+
				"produce one; with --auth api-key, point the other tool at the same six OCI_CLI_* values instead",
			securityTokenKeyIDPrefix)
	}
	token := strings.TrimPrefix(keyID, securityTokenKeyIDPrefix)
	if token == "" {
		return "", fmt.Errorf("export session: the provider's key id was %q with nothing after it", securityTokenKeyIDPrefix)
	}
	return token, nil
}

// tokenExpiry reads `exp` out of a JWT payload without verifying anything. Returns the zero time
// for a token this cannot read, which every caller treats as "unknown" rather than "expired".
func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	// JWT payloads are base64url without padding.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0).UTC()
}

// keyFingerprint returns the OCI fingerprint of a public key: the MD5 of its DER-encoded
// SubjectPublicKeyInfo, hex, colon-separated. That is Oracle's definition, not a choice made
// here -- `openssl rsa -pubout -outform DER | openssl md5 -c` produces the same string, and this
// implementation was checked against a real ~/.oci/config's own fingerprint before being used.
func keyFingerprint(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := md5.Sum(der) //nolint:gosec // the format is MD5 by definition; this is an identifier, not a digest anyone relies on
	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02x", b))
	}
	return strings.Join(parts, ":"), nil
}
