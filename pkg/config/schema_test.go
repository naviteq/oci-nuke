package config

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const validConfigYAML = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
  - eu-frankfurt-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
resource-types:
  includes:
    - instance
  excludes:
    - bucket
presets:
  no-terraform-state:
    filters:
      Bucket:
        - property: Name
          type: contains
          value: terraform-state
filters:
  ocid1.compartment.oc1..aaaaaaaaexampletarget:
    filters:
      Instance:
        - property: Name
          type: glob
          value: "keep-*"
    presets:
      - no-terraform-state
settings:
  Instance:
    disableDeletionProtection: true
`

func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// TestValidate_shippedExamples validates every example config the repository ships, from the
// repository root. They are the first thing an operator copies, and a key renamed in the schema
// leaves them silently wrong -- `config validate` is the only thing that would have told them.
func TestValidate_shippedExamples(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "config*.example.yaml"))
	if err != nil {
		t.Fatalf("globbing example configs: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("found no config*.example.yaml in the repository root; the examples moved or were deleted")
	}

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if err := Validate(path); err != nil {
				t.Errorf("Validate(%s) = %v, want nil", path, err)
			}
		})
	}
}

func TestValidate_valid(t *testing.T) {
	path := writeFixture(t, validConfigYAML)

	if err := Validate(path); err != nil {
		t.Fatalf("Validate(%s) = %v, want nil", path, err)
	}
}

func TestValidate_missingRequiredField(t *testing.T) {
	// compartment-blocklist is missing entirely.
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "missing property 'compartment-blocklist'") {
		t.Errorf("error = %q, want it to contain \"missing property 'compartment-blocklist'\"", err.Error())
	}
}

func TestValidate_wrongType(t *testing.T) {
	// tenancy-id given as a number instead of a string.
	const cfg = `
tenancy-id: 12345
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/tenancy-id") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/tenancy-id\"", err.Error())
	}
}

// TestValidate_emptyBlocklist proves the schema-level half of DoD refusal path 5: an empty
// compartment-blocklist array (present, but with zero entries) fails validation -- distinct
// from TestValidate_missingRequiredField, which covers the key being absent entirely. There is
// no safe default for the blocklist (02-CONTEXT.md locked requirement 3).
func TestValidate_emptyBlocklist(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist: []
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "compartment-blocklist") {
		t.Errorf("error = %q, want it to name \"compartment-blocklist\"", err.Error())
	}
}

// TestValidate_nameBasedBlocklistEntryRejected proves ROADMAP success criterion 2 is satisfied
// by the existing OCID-shape schema pattern, not by live name-to-OCID resolution: a plain
// display name (not an OCID) in compartment-blocklist fails config validate loudly, at
// config-validate time, with zero OCI API calls. See 02-03-PLAN.md's objective for why live
// resolution is rejected (it would require an OCI API call inside pkg/config, breaking the
// zero-API-call guarantee TestNoOCISDKImport enforces structurally).
func TestValidate_nameBasedBlocklistEntryRejected(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - sandbox
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "compartment-blocklist") {
		t.Errorf("error = %q, want it to name \"compartment-blocklist\"", err.Error())
	}
}

// TestValidate_settingsProtectValid proves a well-formed settings.protect block (both min-age
// and tags) validates successfully, alongside the pre-existing resource-type-keyed settings
// entry (Instance) already present in validConfigYAML -- settings.protect is a new, reserved
// pseudo-type-name inside that same map, not a parallel top-level structure.
func TestValidate_settingsProtectValid(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  Instance:
    disableDeletionProtection: true
  protect:
    min-age: 24h
    tags:
      - key: Operations.Persistent
        value: "true"
        defined: true
`
	path := writeFixture(t, cfg)

	if err := Validate(path); err != nil {
		t.Fatalf("Validate(%s) = %v, want nil", path, err)
	}
}

// TestValidate_settingsProtectUnknownKey proves an unknown key under settings.protect (a
// "protct" typo) fails validation naming the JSON-pointer path /settings/protect, matching
// TestValidate_unknownKey's existing assertion style (CONF-02 error quality).
func TestValidate_settingsProtectUnknownKey(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  protect:
    min-age: 24h
    protct:
      - key: Operations.Persistent
        value: "true"
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/protect") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/protect\"", err.Error())
	}
}

// TestValidate_settingsProtectTagUnknownField proves an unknown field inside a tags entry (e.g.
// a typo'd "keyy") fails validation naming the offending tags entry's JSON-pointer path.
func TestValidate_settingsProtectTagUnknownField(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  protect:
    tags:
      - keyy: Operations.Persistent
        value: "true"
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/protect/tags/0") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/protect/tags/0\"", err.Error())
	}
}

// TestValidate_settingsVaultValid proves a well-formed settings.vault.deletion-window-days block
// (within the OCI-enforced 7-30 day range) validates successfully, alongside the pre-existing
// settings.protect block -- both are sibling reserved pseudo-type-names inside the same map.
func TestValidate_settingsVaultValid(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  vault:
    deletion-window-days: 14
`
	path := writeFixture(t, cfg)

	if err := Validate(path); err != nil {
		t.Fatalf("Validate(%s) = %v, want nil", path, err)
	}
}

// TestValidate_settingsVaultDeletionWindowDaysBelowFloor proves a below-floor
// settings.vault.deletion-window-days value (OCI's own hard minimum is 7 days) is rejected at
// config-validate time, by JSON-pointer path, before any OCI API call -- T-05-06-01.
func TestValidate_settingsVaultDeletionWindowDaysBelowFloor(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  vault:
    deletion-window-days: 3
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/vault/deletion-window-days") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/vault/deletion-window-days\"", err.Error())
	}
}

// TestValidate_settingsVaultDeletionWindowDaysAboveCeiling proves an above-ceiling
// settings.vault.deletion-window-days value (OCI's own hard maximum is 30 days) is rejected at
// config-validate time, by JSON-pointer path, before any OCI API call -- T-05-06-01.
func TestValidate_settingsVaultDeletionWindowDaysAboveCeiling(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  vault:
    deletion-window-days: 45
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/vault/deletion-window-days") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/vault/deletion-window-days\"", err.Error())
	}
}

// TestValidate_settingsVaultUnknownKey proves an unknown key under settings.vault (a
// "deletion-window-dayz" typo) fails validation naming the JSON-pointer path
// /settings/vault, matching TestValidate_settingsProtectUnknownKey's existing assertion style.
func TestValidate_settingsVaultUnknownKey(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  vault:
    deletion-window-dayz: 14
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/vault") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/vault\"", err.Error())
	}
}

// TestValidate_settingsCompartmentValid proves a well-formed settings.compartment.delete block
// validates successfully, alongside the pre-existing settings.protect/settings.vault blocks --
// another sibling reserved pseudo-type-name inside the same map (06-03-PLAN.md).
func TestValidate_settingsCompartmentValid(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  compartment:
    delete: true
`
	path := writeFixture(t, cfg)

	if err := Validate(path); err != nil {
		t.Fatalf("Validate(%s) = %v, want nil", path, err)
	}
}

// TestValidate_settingsCompartmentWrongType proves a non-boolean settings.compartment.delete
// value is rejected at config-validate time, by JSON-pointer path.
func TestValidate_settingsCompartmentWrongType(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  compartment:
    delete: "yes"
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/compartment/delete") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/compartment/delete\"", err.Error())
	}
}

// TestValidate_settingsCompartmentUnknownKey proves an unknown key under settings.compartment (a
// "deletee" typo) fails validation naming the JSON-pointer path /settings/compartment, matching
// TestValidate_settingsVaultUnknownKey's existing assertion style.
func TestValidate_settingsCompartmentUnknownKey(t *testing.T) {
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  compartment:
    deletee: true
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/settings/compartment") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/settings/compartment\"", err.Error())
	}
}

func TestValidate_unknownKey(t *testing.T) {
	// "includez" is a typo of "includes" inside resource-types.
	const cfg = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
resource-types:
  includez:
    - instance
`
	path := writeFixture(t, cfg)

	err := Validate(path)
	if err == nil {
		t.Fatalf("Validate(%s) = nil, want error", path)
	}
	if !strings.Contains(err.Error(), "/resource-types") {
		t.Errorf("error = %q, want it to contain the JSON-pointer path \"/resource-types\"", err.Error())
	}
}

// TestNoOCISDKImport is the structural proof behind CLI-04/CONF-03's "zero OCI API calls"
// guarantee: pkg/config must never import any oracle/oci-go-sdk package, so that
// config.Validate (and config.Load) cannot possibly make a cloud API call, no matter how the
// package evolves. This parses every non-test .go file in this package's directory and fails
// if any import path contains "oci-go-sdk".
func TestNoOCISDKImport(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquoting import path in %s: %v", name, err)
			}
			if strings.Contains(path, "oci-go-sdk") {
				t.Errorf("%s imports %q -- pkg/config must never import any oci-go-sdk package (CLI-04/CONF-03 structural guarantee)", name, path)
			}
		}
	}
}
