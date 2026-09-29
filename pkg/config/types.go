// Package config defines oci-nuke's own configuration shape (CONF-01). It does not embed
// libnuke's config.Config verbatim -- libnuke's Config.Accounts map[string]*Account model
// (keyed by a generic "account" string) doesn't line up field-for-field with CONF-01's required
// top-level keys (tenancy-id, compartment-blocklist, a top-level filters map keyed by
// compartment OCID). This package reuses libnuke's filter.Filters, settings.Settings, and
// types.Collection so the filter engine, preset merge, and settings machinery stay shared with
// the rest of the ecosystem.
package config

import (
	"github.com/ekristen/libnuke/pkg/filter"
	"github.com/ekristen/libnuke/pkg/settings"
	"github.com/ekristen/libnuke/pkg/types"
)

// Config is oci-nuke's one-YAML-document configuration shape (CONF-01).
type Config struct {
	TenancyID            string                        `yaml:"tenancy-id" json:"tenancy-id"`
	Regions              []string                      `yaml:"regions" json:"regions"`
	CompartmentBlocklist []string                      `yaml:"compartment-blocklist" json:"compartment-blocklist"`
	ResourceTypes        ResourceTypes                 `yaml:"resource-types" json:"resource-types"`
	Presets              map[string]Preset             `yaml:"presets" json:"presets"`
	Filters              map[string]CompartmentFilters `yaml:"filters" json:"filters"` // keyed by compartment OCID
	Settings             *settings.Settings            `yaml:"settings" json:"settings"`
	// TenancyRootTypes names the resource types this run may scan and remove at the tenancy
	// root. Empty or absent -- the default -- means none, which is exactly the behavior that
	// predates the key. It does NOT make the tenancy root a target: --compartment-id naming the
	// tenancy is still refused before any API call.
	// See docs/adr/0003-tenancy-root-type-allowance.md.
	TenancyRootTypes []string `yaml:"tenancy-root-types" json:"tenancy-root-types"`
}

// ResourceTypes controls which resource types are in scope for a run.
type ResourceTypes struct {
	Includes types.Collection `yaml:"includes" json:"includes"`
	Excludes types.Collection `yaml:"excludes" json:"excludes"`
}

// Preset is a named, reusable group of filters that a CompartmentFilters entry can reference
// by name.
type Preset struct {
	Filters filter.Filters `yaml:"filters" json:"filters"`
}

// CompartmentFilters holds the filters and named presets that apply when a run targets this
// compartment OCID (the map key in Config.Filters). Phase 2 decides how this interacts with
// blocklist/subtree resolution; Phase 1 only needs the shape to be schema-stable.
type CompartmentFilters struct {
	Filters filter.Filters `yaml:"filters" json:"filters"`
	Presets []string       `yaml:"presets" json:"presets"`
}
