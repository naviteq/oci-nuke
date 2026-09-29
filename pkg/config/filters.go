package config

import (
	"fmt"

	"github.com/ekristen/libnuke/pkg/filter"
)

// ResolveFilters merges compartmentID's own filters with every named preset it references
// (CONF-04), mirroring libnuke's own Config.Filters(accountID) preset-merge pattern
// (pkg/config/config.go in the libnuke module) exactly, adjusted only for this package's
// compartment-OCID-keyed Config.Filters map instead of libnuke's account-keyed one.
//
// A compartment with no entry in c.Filters at all is not an error -- it simply has zero
// filters configured, and returns an empty (non-nil) filter.Filters. An entry that references
// a preset name absent from c.Presets is an error naming the unknown preset, since that is
// almost certainly an operator typo rather than an intentional no-op.
func (c *Config) ResolveFilters(compartmentID string) (filter.Filters, error) {
	filters := filter.Filters{}

	cf, ok := c.Filters[compartmentID]
	if !ok {
		return filters, nil // no entry for this compartment is not an error -- zero filters
	}

	if cf.Filters != nil {
		filters.Append(cf.Filters)
	}

	for _, presetName := range cf.Presets {
		preset, found := c.Presets[presetName]
		if !found {
			return nil, fmt.Errorf("compartment %s references unknown preset %q", compartmentID, presetName)
		}
		filters.Append(preset.Filters)
	}

	return filters, nil
}
