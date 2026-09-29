package ocinuke

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ekristen/libnuke/pkg/filter"
	"github.com/ekristen/libnuke/pkg/queue"
)

// FilterWarning names one configured filter that matched zero resources of its resource type
// across the full scanned population of that type -- SAFE-08's "a filter that matches no
// resource is reported as a warning rather than silently ignored". Filter is the concrete
// filter.Filter value (not merely its index), so callers can inspect Property/Type/Value/Invert
// directly without a second lookup into the original filter.Filters map.
type FilterWarning struct {
	ResourceType string
	Filter       filter.Filter
}

// DetectUnmatchedFilters re-runs libnuke's own exported filter.Filters.Get / (*filter.Filter).Match
// -- the SAME functions nuke.go's filterWithoutGroups/filterWithGroups call internally -- once,
// explicitly, purely for diagnostic purposes, against every scanned item of a filter's resource
// type. This is PITFALLS.md's top-ranked confirmed real-world failure in this tool family: an
// operator writes a filter to protect something, a typo'd property name or a stale value means it
// silently matches nothing, and the thing they meant to protect is deleted while the operator
// believes it is safe.
//
// There is no libnuke hook for this -- filterWithoutGroups/filterWithGroups only ever set
// Item.State = ItemStateFiltered, with no per-filter attribution retained anywhere. Reusing
// libnuke's own exported Match/Get (rather than a parallel filter-matching reimplementation) is
// deliberate: a second implementation of match semantics risks silent drift from what actually
// decided each resource's fate (e.g. getting dateOlderThan's inverted-sounding polarity wrong a
// second time -- see safety_filter.go's own comment on the same hazard).
//
// items must be the FULL scanned population of the run (n.Queue.GetItems(), called after
// n.Run(ctx) returns -- see 03-RESEARCH.md Q4-Q6's timing constraint), not only the items that
// ended up ItemStateFiltered. The entire point of SAFE-08 is to catch a filter that matched ZERO
// resources, which requires checking against every resource it *could* have matched, not just the
// ones it happened to catch.
//
// The set of resource types checked is every distinct Item.Type present in items UNION every
// resource-type key present in nukeFilters other than filter.Global. The union's second half
// matters on its own: a filter configured for a resource type that was never scanned this run (a
// typo'd resource type name, or a type excluded from this run's --resource-type flags) is itself
// worth flagging as unmatched -- it must not be silently skipped merely because the type has no
// entry in the scanned-items grouping.
func DetectUnmatchedFilters(nukeFilters filter.Filters, items []*queue.Item) []FilterWarning {
	byType := map[string][]*queue.Item{}
	for _, it := range items {
		byType[it.Type] = append(byType[it.Type], it)
	}

	resourceTypeSet := map[string]struct{}{}
	for t := range byType {
		resourceTypeSet[t] = struct{}{}
	}
	for t := range nukeFilters {
		if t == filter.Global {
			continue // evaluated per concrete type below via Filters.Get, not standalone
		}
		resourceTypeSet[t] = struct{}{}
	}

	// Deterministic iteration order: Go map iteration order is intentionally randomized, and
	// this function's output should not depend on it -- there is no correctness requirement for
	// sorting here (each resource type's warnings are independent), but a stable order makes the
	// diagnostic output (and its tests) reproducible across runs, matching this project's general
	// bias against depending on map iteration order anywhere output is produced.
	resourceTypes := make([]string, 0, len(resourceTypeSet))
	for t := range resourceTypeSet {
		resourceTypes = append(resourceTypes, t)
	}
	sort.Strings(resourceTypes)

	var warnings []FilterWarning
	for _, resourceType := range resourceTypes {
		for _, f := range nukeFilters.Get(resourceType) { // merges __global__ + type-specific, same as libnuke
			matched := false
			for _, item := range byType[resourceType] {
				prop, err := item.GetProperty(f.Property)
				if err != nil {
					// Same "unable to get property" tolerance libnuke's own filterWithoutGroups
					// uses -- one item's property miss must not fail the whole detection pass.
					continue
				}
				m, matchErr := f.Match(prop)
				if matchErr == nil && (m != f.Invert) { // honor Invert exactly as filterWithoutGroups does
					matched = true
					break
				}
			}
			if !matched {
				warnings = append(warnings, FilterWarning{ResourceType: resourceType, Filter: f})
			}
		}
	}
	return warnings
}

// FormatWarnings renders each FilterWarning as one human-readable line, phrased as a warning --
// never as an error or a fatal condition. Plan 03-06 prints these to stdout via the run's logger
// at Warn level; DetectUnmatchedFilters itself has no production call site as of this plan.
func FormatWarnings(warnings []FilterWarning) []string {
	lines := make([]string, 0, len(warnings))
	for _, w := range warnings {
		value := w.Filter.Value
		if len(w.Filter.Values) > 0 {
			value = strings.Join(w.Filter.Values, ",")
		}
		lines = append(lines, fmt.Sprintf(
			"filter on %s.%s (%s=%q) matched no resources this run",
			w.ResourceType, w.Filter.Property, w.Filter.Type, value,
		))
	}
	return lines
}
