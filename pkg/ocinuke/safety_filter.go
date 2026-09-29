package ocinuke

import (
	"time"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// TagMatch describes one protect-by-tag condition (02-CONTEXT.md locked requirement 7):
// protect a resource whose tags contain Key=Value. Defined distinguishes OCI's two tag kinds --
// freeform tags (a flat map[string]string) and defined tags (namespaced, e.g. an "Operations"
// namespace containing a "Persistent" key). For a Defined TagMatch, Key holds the composite
// "<namespace>.<key>" form (e.g. "Operations.Persistent"), matched against the equally
// flattened definedTags map Evaluate receives -- see Evaluate's own doc comment for why this
// flattened convention was chosen over a nested map[string]map[string]string.
type TagMatch struct {
	Key     string
	Value   string
	Defined bool
}

// SafetyFilterConfig holds the two named, off-by-default safety protections 02-CONTEXT.md locked
// requirement 7 calls out explicitly: protect-by-tag and minimum-age. Both are zero-value-safe:
// an empty ProtectTags and a zero MinAge mean neither protection is configured, matching "both
// configurable, both off by default" from the locked requirement.
type SafetyFilterConfig struct {
	ProtectTags []TagMatch
	MinAge      time.Duration
}

// Evaluate is the typed, first-class check for the two safety-critical protections
// 02-CONTEXT.md names by name: protect-by-tag and minimum-age. It exists specifically because
// libnuke's built-in filter engine (the correct, "don't hand-roll a filter engine" tool for
// CONF-05's general operator-authored filter vocabulary) always sets Item.Reason to the single
// generic string "filtered by config" -- it cannot report which filter matched, so it cannot
// produce the distinguishable scope.ReasonProtectedByTag / scope.ReasonTooYoung Phase 3 needs
// (02-RESEARCH.md Q7). Evaluate reuses the built-in engine's own dateOlderThan match arithmetic
// for the age check (see the comment directly above that check below) so the two code paths
// agree on the exact same non-obvious polarity, rather than risking a second, divergent
// implementation.
//
// Evaluate's production call site is scopedLister.List (scoped_lister.go), which calls it once
// per resource, at SCAN time, for every resource type registered through Register -- never from
// inside a resource type's own Remove() (that was this phase's original wiring, moved here by the
// 04-13 plan/apply divergence fix: libnuke's Nuke.Run() never reaches Remove() during a dry run,
// so a Remove()-time-only check was invisible to the plan artifact). See SafetyEvaluated's doc
// comment in scoped_lister.go for the full account.
//
// definedTags uses the flattened "<namespace>.<key>" -> value convention documented on TagMatch,
// not OCI SDK's own nested map[string]map[string]interface{} DefinedTags shape -- callers (the
// Phase 4 shared resource base) are responsible for flattening the SDK response before calling
// Evaluate.
func Evaluate(
	compartmentID, resourceType, resourceID string,
	freeformTags, definedTags map[string]string,
	createdAt time.Time,
	cfg SafetyFilterConfig,
) *scope.SkipEvent {
	for _, tm := range cfg.ProtectTags {
		tags := freeformTags
		if tm.Defined {
			tags = definedTags
		}
		if tags == nil {
			continue
		}
		if v, ok := tags[tm.Key]; ok && v == tm.Value {
			return &scope.SkipEvent{
				Reason:        scope.ReasonProtectedByTag,
				ResourceType:  resourceType,
				ResourceID:    resourceID,
				CompartmentID: compartmentID,
				Detail:        "protect-by-tag matched " + tm.Key + "=" + tm.Value,
			}
		}
	}

	if cfg.MinAge > 0 {
		// dateOlderThan's match semantics, replicated exactly (libnuke@v1.3.0
		// pkg/filter/filter.go): fieldTimeWithOffset := fieldTime.Add(duration);
		// return fieldTimeWithOffset.After(time.Now()). This protects resources YOUNGER than
		// MinAge -- do not invert this. Worked example: a resource created 1h ago with
		// MinAge=24h: 1h-ago + 24h = 23h-in-the-future, which is After(now) -> true -> protected.
		// A resource created 30 days ago against the same MinAge: 30d-ago + 24h = 29d-ago, which
		// is NOT After(now) -> false -> not protected, eligible for deletion. The name reads
		// backwards from this behavior; see 02-03-PLAN.md's objective for the full worked example
		// this comment mirrors.
		if createdAt.Add(cfg.MinAge).After(time.Now()) {
			return &scope.SkipEvent{
				Reason:        scope.ReasonTooYoung,
				ResourceType:  resourceType,
				ResourceID:    resourceID,
				CompartmentID: compartmentID,
			}
		}
	}

	return nil
}
