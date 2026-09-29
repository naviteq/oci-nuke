package ocinuke_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/settings"

	"github.com/naviteq/oci-nuke/pkg/config"
	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

const (
	testProtectSettingsKey  = "protect"
	testProtectTagsKey      = "tags"
	testProtectDefinedTag   = "Operations.Persistent"
	testProtectMinAgeErrPtr = "settings.protect.min-age"
	testProtectTagsErrPtr   = "settings.protect.tags[0]"
	testProtectTagValueTrue = "true"
	// testInstanceResourceType is the unrelated-resource-type-keyed settings.yaml entry every
	// "no reserved pseudo-type-name key present" fixture in this file uses (goconst: referenced
	// 3+ times across TestSafetyFilterConfigFromSettings_NoProtectKey and
	// TestDeleteCompartmentsEnabledFromSettings_NoCompartmentKey).
	testInstanceResourceType = "Instance"
	// testDisableDeletionProtectionKey is the resource-type-keyed setting field every
	// unrelated-resource-type fixture in this file uses (goconst: referenced 3+ times).
	testDisableDeletionProtectionKey = "disableDeletionProtection"
)

func TestSafetyFilterConfigFromSettings_NilSettings(t *testing.T) {
	cfg, err := ocinuke.SafetyFilterConfigFromSettings(nil)
	if err != nil {
		t.Fatalf("SafetyFilterConfigFromSettings(nil) returned error: %v", err)
	}
	if len(cfg.ProtectTags) != 0 || cfg.MinAge != 0 {
		t.Fatalf("SafetyFilterConfigFromSettings(nil) = %+v, want zero value", cfg)
	}
}

func TestSafetyFilterConfigFromSettings_NoProtectKey(t *testing.T) {
	s := settings.Settings{
		testInstanceResourceType: &settings.Setting{testDisableDeletionProtectionKey: true},
	}

	cfg, err := ocinuke.SafetyFilterConfigFromSettings(&s)
	if err != nil {
		t.Fatalf("SafetyFilterConfigFromSettings returned error: %v", err)
	}
	if len(cfg.ProtectTags) != 0 || cfg.MinAge != 0 {
		t.Fatalf("SafetyFilterConfigFromSettings with no protect key = %+v, want zero value", cfg)
	}
}

func TestSafetyFilterConfigFromSettings_MinAge(t *testing.T) {
	s := settings.Settings{
		testProtectSettingsKey: &settings.Setting{"min-age": "24h"},
	}

	cfg, err := ocinuke.SafetyFilterConfigFromSettings(&s)
	if err != nil {
		t.Fatalf("SafetyFilterConfigFromSettings returned error: %v", err)
	}
	if cfg.MinAge != 24*time.Hour {
		t.Errorf("MinAge = %v, want %v", cfg.MinAge, 24*time.Hour)
	}
}

func TestSafetyFilterConfigFromSettings_Tags(t *testing.T) {
	s := settings.Settings{
		testProtectSettingsKey: &settings.Setting{
			testProtectTagsKey: []interface{}{
				map[string]interface{}{"key": testProtectDefinedTag, "value": testProtectTagValueTrue, "defined": true},
			},
		},
	}

	cfg, err := ocinuke.SafetyFilterConfigFromSettings(&s)
	if err != nil {
		t.Fatalf("SafetyFilterConfigFromSettings returned error: %v", err)
	}
	want := []ocinuke.TagMatch{{Key: testProtectDefinedTag, Value: testProtectTagValueTrue, Defined: true}}
	if len(cfg.ProtectTags) != len(want) || cfg.ProtectTags[0] != want[0] {
		t.Errorf("ProtectTags = %+v, want %+v", cfg.ProtectTags, want)
	}
}

func TestSafetyFilterConfigFromSettings_InvalidMinAge(t *testing.T) {
	s := settings.Settings{
		testProtectSettingsKey: &settings.Setting{"min-age": "not-a-duration"},
	}

	_, err := ocinuke.SafetyFilterConfigFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for an invalid min-age value")
	}
	if got := err.Error(); !strings.Contains(got, testProtectMinAgeErrPtr) {
		t.Errorf("error = %q, want it to name %q", got, testProtectMinAgeErrPtr)
	}
}

func TestSafetyFilterConfigFromSettings_TagMissingKey(t *testing.T) {
	s := settings.Settings{
		testProtectSettingsKey: &settings.Setting{
			testProtectTagsKey: []interface{}{
				map[string]interface{}{"value": testProtectTagValueTrue},
			},
		},
	}

	_, err := ocinuke.SafetyFilterConfigFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for a tags entry missing key")
	}
	if got := err.Error(); !strings.Contains(got, testProtectTagsErrPtr) {
		t.Errorf("error = %q, want it to name the entry's index %q", got, testProtectTagsErrPtr)
	}
}

func TestSafetyFilterConfigFromSettings_TagMissingValue(t *testing.T) {
	s := settings.Settings{
		testProtectSettingsKey: &settings.Setting{
			testProtectTagsKey: []interface{}{
				map[string]interface{}{"key": testProtectDefinedTag},
			},
		},
	}

	_, err := ocinuke.SafetyFilterConfigFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for a tags entry missing value")
	}
	if got := err.Error(); !strings.Contains(got, testProtectTagsErrPtr) {
		t.Errorf("error = %q, want it to name the entry's index %q", got, testProtectTagsErrPtr)
	}
}

const (
	testVaultSettingsKey            = "vault"
	testVaultDeletionWindowDaysKey  = "deletion-window-days"
	testVaultDeletionWindowDaysErr  = "settings.vault.deletion-window-days"
	testVaultDeletionWindowDefault  = 7
	testVaultDeletionWindowExplicit = 14
)

// TestVaultDeletionWindowDaysFromSettings_NilSettings proves s == nil returns the locked default
// (7, the earliest OCI allows) rather than zero -- 05-CONTEXT.md's decision, mirroring
// SafetyFilterConfigFromSettings' own nil-safe contract.
func TestVaultDeletionWindowDaysFromSettings_NilSettings(t *testing.T) {
	days, err := ocinuke.VaultDeletionWindowDaysFromSettings(nil)
	if err != nil {
		t.Fatalf("VaultDeletionWindowDaysFromSettings(nil) returned error: %v", err)
	}
	if days != testVaultDeletionWindowDefault {
		t.Fatalf("VaultDeletionWindowDaysFromSettings(nil) = %d, want %d", days, testVaultDeletionWindowDefault)
	}
}

// TestVaultDeletionWindowDaysFromSettings_NoVaultKey proves a settings block with no "vault" key
// at all (only an unrelated resource-type-keyed entry) also returns the default.
func TestVaultDeletionWindowDaysFromSettings_NoVaultKey(t *testing.T) {
	s := settings.Settings{
		"Bucket": &settings.Setting{testDisableDeletionProtectionKey: true},
	}

	days, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err != nil {
		t.Fatalf("VaultDeletionWindowDaysFromSettings returned error: %v", err)
	}
	if days != testVaultDeletionWindowDefault {
		t.Fatalf("VaultDeletionWindowDaysFromSettings with no vault key = %d, want %d", days, testVaultDeletionWindowDefault)
	}
}

// TestVaultDeletionWindowDaysFromSettings_Explicit proves an explicit, in-range value is parsed
// and returned unchanged.
func TestVaultDeletionWindowDaysFromSettings_Explicit(t *testing.T) {
	s := settings.Settings{
		testVaultSettingsKey: &settings.Setting{testVaultDeletionWindowDaysKey: testVaultDeletionWindowExplicit},
	}

	days, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err != nil {
		t.Fatalf("VaultDeletionWindowDaysFromSettings returned error: %v", err)
	}
	if days != testVaultDeletionWindowExplicit {
		t.Errorf("VaultDeletionWindowDaysFromSettings = %d, want %d", days, testVaultDeletionWindowExplicit)
	}
}

// TestVaultDeletionWindowDaysFromSettings_BelowFloor proves this function independently rejects a
// below-7 value even when it was never validated against the JSON Schema -- T-05-06-01's
// defense-in-depth requirement, distinct from pkg/config/schema_test.go's schema-level proof.
func TestVaultDeletionWindowDaysFromSettings_BelowFloor(t *testing.T) {
	s := settings.Settings{
		testVaultSettingsKey: &settings.Setting{testVaultDeletionWindowDaysKey: 3},
	}

	_, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for a below-floor deletion-window-days value")
	}
	if got := err.Error(); !strings.Contains(got, testVaultDeletionWindowDaysErr) {
		t.Errorf("error = %q, want it to name %q", got, testVaultDeletionWindowDaysErr)
	}
}

// TestVaultDeletionWindowDaysFromSettings_AboveCeiling is the above-30 companion to
// TestVaultDeletionWindowDaysFromSettings_BelowFloor.
func TestVaultDeletionWindowDaysFromSettings_AboveCeiling(t *testing.T) {
	s := settings.Settings{
		testVaultSettingsKey: &settings.Setting{testVaultDeletionWindowDaysKey: 45},
	}

	_, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for an above-ceiling deletion-window-days value")
	}
	if got := err.Error(); !strings.Contains(got, testVaultDeletionWindowDaysErr) {
		t.Errorf("error = %q, want it to name %q", got, testVaultDeletionWindowDaysErr)
	}
}

// TestVaultDeletionWindowDaysFromSettings_WrongType proves a non-integer value (e.g. a string) is
// rejected with an error naming the field, rather than a panic on the failed type assertion.
func TestVaultDeletionWindowDaysFromSettings_WrongType(t *testing.T) {
	s := settings.Settings{
		testVaultSettingsKey: &settings.Setting{testVaultDeletionWindowDaysKey: "not-an-int"},
	}

	_, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for a non-integer deletion-window-days value")
	}
	if got := err.Error(); !strings.Contains(got, testVaultDeletionWindowDaysErr) {
		t.Errorf("error = %q, want it to name %q", got, testVaultDeletionWindowDaysErr)
	}
}

// TestVaultDeletionWindowDaysFromSettings_WholeNumberFloat proves I-WR-01's fix: a whole-number
// float64 (exactly what yaml.v3 decodes `deletion-window-days: 14.0` into, and exactly what the
// JSON Schema's "type": "integer" already accepts at `config validate` time) is accepted and
// returned as the equivalent int, rather than failing the strict `raw.(int)` type assertion that
// previously made `config validate` and `run` disagree on this same file.
func TestVaultDeletionWindowDaysFromSettings_WholeNumberFloat(t *testing.T) {
	s := settings.Settings{
		testVaultSettingsKey: &settings.Setting{testVaultDeletionWindowDaysKey: float64(14)},
	}

	days, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err != nil {
		t.Fatalf("VaultDeletionWindowDaysFromSettings returned error: %v", err)
	}
	if days != testVaultDeletionWindowExplicit {
		t.Errorf("VaultDeletionWindowDaysFromSettings(float64(14)) = %d, want %d", days, testVaultDeletionWindowExplicit)
	}
}

// TestVaultDeletionWindowDaysFromSettings_FractionalFloat proves I-WR-01's fix is narrow: a
// genuinely fractional float64 (e.g. 14.5, which the JSON Schema's "type": "integer" would
// already reject at `config validate` time) is still rejected here too, with a clear message --
// this function must not silently truncate one reached by a path that bypasses schema
// validation.
func TestVaultDeletionWindowDaysFromSettings_FractionalFloat(t *testing.T) {
	s := settings.Settings{
		testVaultSettingsKey: &settings.Setting{testVaultDeletionWindowDaysKey: 14.5},
	}

	_, err := ocinuke.VaultDeletionWindowDaysFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for a fractional deletion-window-days value")
	}
	if got := err.Error(); !strings.Contains(got, testVaultDeletionWindowDaysErr) {
		t.Errorf("error = %q, want it to name %q", got, testVaultDeletionWindowDaysErr)
	}
}

// vaultDeletionWindowRoundTripConfigYAML is a real, on-disk YAML document -- schema-valid (mirrors
// pkg/config/schema_test.go's own TestValidate_settingsVaultValid fixture) -- proving yaml.v3
// decodes a bare YAML integer directly as Go int (no asStringMap-style named-map-type defect
// applies to a scalar value, unlike settings.protect.tags' nested-mapping case).
const vaultDeletionWindowRoundTripConfigYAML = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  vault:
    deletion-window-days: 14
`

// TestVaultDeletionWindowDaysFromSettings_RoundTripThroughRealYAML loads
// vaultDeletionWindowRoundTripConfigYAML through the real config.Load path (the exact function
// pkg/commands/run/command.go's execute() calls before runPipeline hands cfg.Settings to
// VaultDeletionWindowDaysFromSettings), proving the real decode path -- not a hand-built
// *settings.Settings literal -- produces an int, not a string or float64.
func TestVaultDeletionWindowDaysFromSettings_RoundTripThroughRealYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(vaultDeletionWindowRoundTripConfigYAML), 0o600); err != nil {
		t.Fatalf("writing config fixture: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load(%s) error = %v, want nil", path, err)
	}

	got, err := ocinuke.VaultDeletionWindowDaysFromSettings(cfg.Settings)
	if err != nil {
		t.Fatalf("VaultDeletionWindowDaysFromSettings(cfg.Settings) error = %v, want nil", err)
	}
	if got != testVaultDeletionWindowExplicit {
		t.Errorf("VaultDeletionWindowDaysFromSettings(cfg.Settings) = %d, want %d", got, testVaultDeletionWindowExplicit)
	}
}

// protectTagsRoundTripConfigYAML is a real, on-disk YAML document -- schema-valid (mirrors
// pkg/config/schema_test.go's own TestValidate_settingsProtectValid fixture exactly) -- containing
// a settings.protect block with both tags and min-age. It is the config-parsing shape the real
// binary reads (`config.Load` -> yaml.Unmarshal), never a hand-built *settings.Settings Go
// literal.
const protectTagsRoundTripConfigYAML = `
tenancy-id: ocid1.tenancy.oc1..aaaaaaaaexampletenancy
regions:
  - us-ashburn-1
compartment-blocklist:
  - ocid1.compartment.oc1..aaaaaaaaexampleblocklist
settings:
  protect:
    min-age: 24h
    tags:
      - key: role
        value: persistent_volume
      - key: Operations.Persistent
        value: "true"
        defined: true
`

// TestSafetyFilterConfigFromSettings_TagsRoundTripThroughRealYAML is the regression test for
// 04-VERIFICATION.md Gap 2: every other test in this file constructs *settings.Settings as a Go
// literal directly, which never exercises yaml.v3's actual decode path for a nested sequence of
// mappings inside a *settings.Setting -- the exact path that crashed at real-run time with
// "settings.protect.tags[0]: expected an object, got settings.Setting" (yaml.v3 decodes each
// tags[i] entry as settings.Setting there, not map[string]interface{}; see parseProtectTags' doc
// comment in settings.go for the confirmed root cause). This test writes
// protectTagsRoundTripConfigYAML to a real file, loads it through config.Load (the exact function
// pkg/commands/run/command.go's execute() calls before runPipeline hands cfg.Settings to
// SafetyFilterConfigFromSettings), and asserts the resulting SafetyFilterConfig -- proving the fix
// against the real decode path, not a hand-built stand-in for it.
func TestSafetyFilterConfigFromSettings_TagsRoundTripThroughRealYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(protectTagsRoundTripConfigYAML), 0o600); err != nil {
		t.Fatalf("writing config fixture: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load(%s) error = %v, want nil", path, err)
	}

	got, err := ocinuke.SafetyFilterConfigFromSettings(cfg.Settings)
	if err != nil {
		t.Fatalf("SafetyFilterConfigFromSettings(cfg.Settings) error = %v, want nil", err)
	}

	if got.MinAge != 24*time.Hour {
		t.Errorf("MinAge = %v, want %v", got.MinAge, 24*time.Hour)
	}

	want := []ocinuke.TagMatch{
		{Key: "role", Value: "persistent_volume", Defined: false},
		{Key: testProtectDefinedTag, Value: testProtectTagValueTrue, Defined: true},
	}
	if len(got.ProtectTags) != len(want) {
		t.Fatalf("ProtectTags = %+v, want %+v", got.ProtectTags, want)
	}
	for i := range want {
		if got.ProtectTags[i] != want[i] {
			t.Errorf("ProtectTags[%d] = %+v, want %+v", i, got.ProtectTags[i], want[i])
		}
	}
}

const (
	testCompartmentSettingsKey = "compartment"
	testCompartmentDeleteKey   = "delete"
)

// TestDeleteCompartmentsEnabledFromSettings_NilSettings proves s == nil returns false, nil --
// mirroring SafetyFilterConfigFromSettings' nil-safe contract (06-03-PLAN.md's <behavior> block).
func TestDeleteCompartmentsEnabledFromSettings_NilSettings(t *testing.T) {
	enabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(nil)
	if err != nil {
		t.Fatalf("DeleteCompartmentsEnabledFromSettings(nil) returned error: %v", err)
	}
	if enabled {
		t.Fatal("DeleteCompartmentsEnabledFromSettings(nil) = true, want false")
	}
}

// TestDeleteCompartmentsEnabledFromSettings_NoCompartmentKey proves a settings block with no
// "compartment" key at all also returns false, nil.
func TestDeleteCompartmentsEnabledFromSettings_NoCompartmentKey(t *testing.T) {
	s := settings.Settings{
		testInstanceResourceType: &settings.Setting{testDisableDeletionProtectionKey: true},
	}

	enabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(&s)
	if err != nil {
		t.Fatalf("DeleteCompartmentsEnabledFromSettings returned error: %v", err)
	}
	if enabled {
		t.Fatal("DeleteCompartmentsEnabledFromSettings with no compartment key = true, want false")
	}
}

// TestDeleteCompartmentsEnabledFromSettings_NoDeleteKey proves a settings.compartment block with
// no "delete" key returns false, nil.
func TestDeleteCompartmentsEnabledFromSettings_NoDeleteKey(t *testing.T) {
	s := settings.Settings{
		testCompartmentSettingsKey: &settings.Setting{},
	}

	enabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(&s)
	if err != nil {
		t.Fatalf("DeleteCompartmentsEnabledFromSettings returned error: %v", err)
	}
	if enabled {
		t.Fatal("DeleteCompartmentsEnabledFromSettings with no delete key = true, want false")
	}
}

// TestDeleteCompartmentsEnabledFromSettings_DeleteTrue proves settings.compartment.delete: true
// returns true, nil.
func TestDeleteCompartmentsEnabledFromSettings_DeleteTrue(t *testing.T) {
	s := settings.Settings{
		testCompartmentSettingsKey: &settings.Setting{testCompartmentDeleteKey: true},
	}

	enabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(&s)
	if err != nil {
		t.Fatalf("DeleteCompartmentsEnabledFromSettings returned error: %v", err)
	}
	if !enabled {
		t.Fatal("DeleteCompartmentsEnabledFromSettings with delete: true = false, want true")
	}
}

// TestDeleteCompartmentsEnabledFromSettings_DeleteFalse proves settings.compartment.delete:
// false returns false, nil.
func TestDeleteCompartmentsEnabledFromSettings_DeleteFalse(t *testing.T) {
	s := settings.Settings{
		testCompartmentSettingsKey: &settings.Setting{testCompartmentDeleteKey: false},
	}

	enabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(&s)
	if err != nil {
		t.Fatalf("DeleteCompartmentsEnabledFromSettings returned error: %v", err)
	}
	if enabled {
		t.Fatal("DeleteCompartmentsEnabledFromSettings with delete: false = true, want false")
	}
}

// TestDeleteCompartmentsEnabledFromSettings_WrongType proves a non-boolean value (e.g. the
// string "yes") is rejected with an error naming the field, rather than silently defaulting to
// false or panicking on a failed type assertion -- the safe value here is an error, not a guess.
func TestDeleteCompartmentsEnabledFromSettings_WrongType(t *testing.T) {
	s := settings.Settings{
		testCompartmentSettingsKey: &settings.Setting{testCompartmentDeleteKey: "yes"},
	}

	enabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(&s)
	if err == nil {
		t.Fatal("expected a non-nil error for a non-boolean settings.compartment.delete value")
	}
	if enabled {
		t.Fatal("expected enabled=false alongside a non-nil error")
	}
	const wantErrSubstr = "settings.compartment.delete"
	if got := err.Error(); !strings.Contains(got, wantErrSubstr) {
		t.Errorf("error = %q, want it to name %q", got, wantErrSubstr)
	}
	if !strings.Contains(err.Error(), "string") {
		t.Errorf("error = %q, want it to name the wrong type (string)", err.Error())
	}
}
