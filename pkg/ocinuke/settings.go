package ocinuke

import (
	"fmt"
	"math"
	"time"

	"github.com/ekristen/libnuke/pkg/settings"
)

// protectSettingsKey is the reserved pseudo-type-name under settings.yaml's "settings" map that
// this package reads its safety-filter configuration from. Every OTHER key in that map is a
// resource-type name (e.g. "Instance": {disableDeletionProtection: true}); "protect" is
// deliberately reserved as a non-type-name so it can never collide with a real resource type
// (every real registry.Registration.Name is a Go-exported-style CamelCase type name, never the
// lowercase word "protect").
const protectSettingsKey = "protect"

// vaultSettingsKey is the reserved pseudo-type-name under settings.yaml's "settings" map that
// VaultDeletionWindowDaysFromSettings reads the mandatory scheduled-deletion window from --
// mirroring protectSettingsKey's reservation exactly, so "vault" can never collide with a real
// resource type name either (05-CONTEXT.md's KMS decision).
const vaultSettingsKey = "vault"

// vaultDeletionWindowDaysKey is the field name inside settings.vault this package reads.
const vaultDeletionWindowDaysKey = "deletion-window-days"

// secretDeletionWindowDaysKey is the field inside settings.vault that VaultSecret's window is read
// from. It sits under "vault" because a secret lives in one; there is no separate settings block.
const secretDeletionWindowDaysKey = "secret-deletion-window-days"

// compartmentSettingsKey is the reserved pseudo-type-name under settings.yaml's "settings" map
// that DeleteCompartmentsEnabledFromSettings reads the config-file mirror of --delete-compartments
// from -- mirroring protectSettingsKey/vaultSettingsKey's reservation exactly (06-03-PLAN.md).
const compartmentSettingsKey = "compartment"

// compartmentDeleteKey is the field name inside settings.compartment this package reads.
const compartmentDeleteKey = "delete"

// defaultVaultDeletionWindowDays is the minimum window OCI allows for a scheduled Vault/KmsKey
// deletion (7 days) -- 05-CONTEXT.md's locked decision: default to the earliest OCI permits,
// passed explicitly, rather than inheriting the SDK's own 30-day default.
const defaultVaultDeletionWindowDays = 7

// minVaultDeletionWindowDays and maxVaultDeletionWindowDays are OCI's own hard-enforced bounds
// for ScheduleVaultDeletionDetails.TimeOfDeletion / ScheduleKeyDeletionDetails.TimeOfDeletion
// (verified against keymanagement/schedule_vault_deletion_details.go's doc comment: "The
// specified time must be between 7 and 30 days from the time when the request is received").
// pkg/config/schema/config.schema.json's settings.vault.deletion-window-days already rejects an
// out-of-range value at `config validate` time -- these constants are this function's own
// independent, defense-in-depth re-validation (T-05-06-01) for any path that bypasses schema
// validation entirely (e.g. a test constructing *settings.Settings by hand).
const (
	minVaultDeletionWindowDays = 7
	maxVaultDeletionWindowDays = 30
)

// defaultSecretDeletionWindowDays, minSecretDeletionWindowDays and maxSecretDeletionWindowDays are
// OCI's bounds for ScheduleSecretDeletionDetails.TimeOfDeletion: one to thirty days. The default is
// the floor, for the reason defaultVaultDeletionWindowDays is.
const (
	defaultSecretDeletionWindowDays = 1
	minSecretDeletionWindowDays     = 1
	maxSecretDeletionWindowDays     = 30
)

// VaultDeletionWindowDaysFromSettings parses the settings.vault.deletion-window-days block of a
// *settings.Settings into an int, mirroring SafetyFilterConfigFromSettings's exact
// zero-value-safe shape: s == nil, a missing "vault" key, and an empty settings.vault block all
// return defaultVaultDeletionWindowDays (7), never zero -- 05-CONTEXT.md's locked default is the
// earliest OCI allows, not an unset/zero duration a caller might mistake for "immediate."
//
// The returned value is independently re-validated against [minVaultDeletionWindowDays,
// maxVaultDeletionWindowDays] regardless of how it was parsed -- config.Validate's JSON Schema
// already enforces this range for any config file that went through `config validate`, but this
// function must not silently accept an out-of-range value reached by any other path (T-05-06-01).
func VaultDeletionWindowDaysFromSettings(s *settings.Settings) (int, error) {
	return windowDaysFromSettings(s, vaultDeletionWindowDaysKey,
		defaultVaultDeletionWindowDays, minVaultDeletionWindowDays, maxVaultDeletionWindowDays)
}

// SecretDeletionWindowDaysFromSettings parses settings.vault.secret-deletion-window-days, the
// window VaultSecret schedules deletions with. Same shape and same default rule as the vault
// window: the earliest OCI allows, which for a secret is one day. The secret's name stays taken
// until the window ends, so a long one blocks whatever recreates it under the same name.
func SecretDeletionWindowDaysFromSettings(s *settings.Settings) (int, error) {
	return windowDaysFromSettings(s, secretDeletionWindowDaysKey,
		defaultSecretDeletionWindowDays, minSecretDeletionWindowDays, maxSecretDeletionWindowDays)
}

// windowDaysFromSettings reads one integer day count from settings.vault and checks it against
// [lowest, highest]. A missing value yields def.
func windowDaysFromSettings(s *settings.Settings, key string, def, lowest, highest int) (int, error) {
	if s == nil {
		return def, nil
	}

	setting := s.Get(vaultSettingsKey)
	if setting == nil || len(*setting) == 0 {
		return def, nil
	}

	raw, ok := (*setting)[key]
	if !ok {
		return def, nil
	}

	var days int
	switch v := raw.(type) {
	case int:
		days = v
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("settings.vault.%s: expected a whole number, got %v", key, v)
		}
		days = int(v)
	default:
		return 0, fmt.Errorf("settings.vault.%s: expected an integer, got %T", key, raw)
	}
	if days < lowest || days > highest {
		return 0, fmt.Errorf(
			"settings.vault.%s: %d is outside the OCI-enforced range [%d,%d]",
			key, days, lowest, highest,
		)
	}

	return days, nil
}

// DeleteCompartmentsEnabledFromSettings parses the settings.compartment.delete block of a
// *settings.Settings into a bool, mirroring SafetyFilterConfigFromSettings's exact
// zero-value-safe shape: s == nil, a missing "compartment" key, and a settings.compartment block
// with no "delete" key all return false, nil -- the safe default, matching --delete-compartments'
// own off-by-default value.
//
// Unlike VaultDeletionWindowDaysFromSettings, no int/float64 type-assertion hazard (I-WR-01)
// applies here: a YAML boolean literal (`true`/`false`) decodes directly into interface{} as Go
// bool through yaml.v3, never as a numeric type needing a whole-number-float switch. A non-bool
// value (e.g. the string "yes") is a hard error naming the field and the wrong type, never
// silently coerced or defaulted to false -- the caller (runPipeline) unions this result with the
// --delete-compartments CLI flag, so a parse error here must surface as a refusal, not a silent
// "feature stayed off."
func DeleteCompartmentsEnabledFromSettings(s *settings.Settings) (bool, error) {
	if s == nil {
		return false, nil
	}

	setting := s.Get(compartmentSettingsKey)
	if setting == nil || len(*setting) == 0 {
		return false, nil
	}

	raw, ok := (*setting)[compartmentDeleteKey]
	if !ok {
		return false, nil
	}

	enabled, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("settings.compartment.delete: expected a boolean, got %T", raw)
	}

	return enabled, nil
}

// SafetyFilterConfigFromSettings parses the settings.protect block of a *settings.Settings into
// a SafetyFilterConfig -- the config-file home for ocinuke.SafetyFilterConfig's two named
// protections (protect-by-tag and minimum-age), closing 04-RESEARCH.md's Open Question 1 (a
// SafetyFilterConfig with zero production call sites for two phases is dead code by default).
//
// s == nil (no settings block at all in the config file) and a settings block with no "protect"
// key both return the same zero value, nil error -- matching Evaluate's own zero-value-safe
// contract (both protections off). settings.Settings.Get never returns nil; it returns an empty
// *Setting on a missing key, so the "no protect key" case is checked via len(*setting) == 0, not
// a nil check (settings.Settings.Get's own implementation, read directly this session).
func SafetyFilterConfigFromSettings(s *settings.Settings) (SafetyFilterConfig, error) {
	if s == nil {
		return SafetyFilterConfig{}, nil
	}

	setting := s.Get(protectSettingsKey)
	if setting == nil || len(*setting) == 0 {
		return SafetyFilterConfig{}, nil
	}

	var cfg SafetyFilterConfig

	if raw, ok := (*setting)["min-age"]; ok {
		minAgeStr, ok := raw.(string)
		if !ok {
			return SafetyFilterConfig{}, fmt.Errorf("settings.protect.min-age: expected a string duration, got %T", raw)
		}
		minAge, err := time.ParseDuration(minAgeStr)
		if err != nil {
			return SafetyFilterConfig{}, fmt.Errorf("settings.protect.min-age: %w", err)
		}
		cfg.MinAge = minAge
	}

	if raw, ok := (*setting)["tags"]; ok {
		tags, err := parseProtectTags(raw)
		if err != nil {
			return SafetyFilterConfig{}, err
		}
		cfg.ProtectTags = tags
	}

	return cfg, nil
}

// parseProtectTags parses the settings.protect.tags YAML sequence, unmarshaled by yaml.v3 into
// raw as []interface{}, into a []TagMatch. Each entry's "defined" key is optional and defaults to
// false when absent.
//
// Each sequence entry does NOT always decode as plain map[string]interface{}, even though that is
// yaml.v3's usual default target for a mapping decoded into interface{}. Live-reproduced defect
// (04-VERIFICATION.md Gap 2): when the ENCLOSING map being decoded is itself a *settings.Setting
// (= map[string]interface{}, a named type, not the bare generic one) -- which is exactly
// SafetyFilterConfigFromSettings' own *setting value above -- yaml.v3's decoder remembers that
// named map type for the remainder of that mapping's decode (gopkg.in/yaml.v3's decode.go,
// (*decoder).mapping: it assigns the enclosing map's own type to d.stringMapType while decoding
// that map's values, and reuses d.stringMapType for any string-keyed mapping subsequently decoded
// into interface{} within that same call tree, rather than resetting to the generic
// map[string]interface{}). The net effect: each "tags[i]" mapping item decodes as settings.Setting,
// not map[string]interface{} -- confirmed with an isolated yaml.Unmarshal repro this session.
// asStringMap below accepts both shapes so this parses correctly regardless of which one yaml.v3
// produces for a given enclosing-map context; settings_test.go's Go-literal-only tests never
// exercised this because they construct *settings.Settings by hand, never round-tripping through
// yaml.Unmarshal the way config.Load (the real run pipeline's entry point) does -- see
// TestSafetyFilterConfigFromSettings_TagsRoundTripThroughRealYAML for the regression test that
// closes that gap.
func parseProtectTags(raw interface{}) ([]TagMatch, error) {
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("settings.protect.tags: expected an array, got %T", raw)
	}

	tags := make([]TagMatch, 0, len(items))
	for i, item := range items {
		entry, ok := asStringMap(item)
		if !ok {
			return nil, fmt.Errorf("settings.protect.tags[%d]: expected an object, got %T", i, item)
		}

		key, ok := entry["key"].(string)
		if !ok || key == "" {
			return nil, fmt.Errorf("settings.protect.tags[%d]: missing or non-string \"key\"", i)
		}
		value, ok := entry["value"].(string)
		if !ok || value == "" {
			return nil, fmt.Errorf("settings.protect.tags[%d]: missing or non-string \"value\"", i)
		}

		defined, _ := entry["defined"].(bool) // absent defaults to false

		tags = append(tags, TagMatch{Key: key, Value: value, Defined: defined})
	}

	return tags, nil
}

// asStringMap normalizes a single decoded settings.protect.tags sequence entry into
// map[string]interface{}, regardless of whether yaml.v3 decoded it as the plain generic map or as
// settings.Setting -- see parseProtectTags' doc comment for exactly which enclosing-map context
// produces which shape. settings.Setting's underlying type is map[string]interface{} itself, so
// converting it back is a type assertion / conversion, never a value copy or field-by-field
// transform -- both branches return the exact same underlying data.
func asStringMap(v interface{}) (map[string]interface{}, bool) {
	switch m := v.(type) {
	case map[string]interface{}:
		return m, true
	case settings.Setting:
		return map[string]interface{}(m), true
	default:
		return nil, false
	}
}
