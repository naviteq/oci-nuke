package plan

import (
	"strings"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// compartmentEntryResourceType MUST equal resources.CompartmentResourceType's value exactly
// ("Compartment") -- it is deliberately a bare string literal here, not an import of the
// resources package's own constant, since pkg/plan is the generic, resource-type-agnostic
// artifact writer (pkg/plan.go's own package doc comment) and must never import a single
// resource type's package. Every other place in this codebase that needs this exact string
// outside resources/compartment.go itself (pkg/commands/run/command.go's
// compartmentResourceTypeName) follows the identical bare-literal-with-cross-reference-comment
// convention, established by Plan 06-03.
const compartmentEntryResourceType = "Compartment"

// EnrichCompartmentLeftover finds compartmentID's own Compartment entry inside entries (if it
// exists and is StateLeftover) and overwrites its Reason/Detail using the OTHER entries already
// present in the SAME slice that share its CompartmentID and never reached StateRemoved --
// 06-CONTEXT.md's "the blocker list comes from the round's own queue state ... no second scan:
// the queue already knows." entries is expected to be one Nuke's own []Entry, built by
// BuildFromQueue from that Nuke's own n.Queue.GetItems() -- every entry in it already shares one
// compartment (06-RESEARCH.md: "each in-scope compartment gets its own Nuke"), so compartmentID
// is the caller's own entries[0].CompartmentID, not re-derived here.
//
// This deliberately layers ON TOP of the generic, resource-type-agnostic plan.ClassifyLeftover
// (pkg/plan/leftover.go) rather than becoming a case inside its switch statement -- ClassifyLeftover
// must stay fully state-derivable with no resource-author cooperation required (06-RESEARCH.md
// Contradiction 5); a Compartment-aware override belongs in a separate post-processing step, the
// same way Vault/KmsKey's scheduled-deletion override layers on top via MergeSkipEvents rather than
// teaching ClassifyLeftover about scheduled deletions.
//
// Three cases, per this plan's <behavior> contract:
//  1. No Compartment entry at all, or one that is not StateLeftover (e.g. StateRemoved,
//     StateWouldRemove) -- entries returned UNCHANGED. There is nothing to enrich.
//  2. A StateLeftover Compartment entry with at least one OTHER entry sharing its CompartmentID
//     whose State != StateRemoved -- that blocking set IS the answer: the Compartment entry's
//     Reason becomes scope.ReasonCompartmentNotEmpty and Detail lists every blocker's
//     ResourceType and ResourceID ("still present: <Type> <ID>; <Type> <ID>; ...").
//  3. A StateLeftover Compartment entry where EVERY other entry sharing its CompartmentID already
//     reached StateRemoved (or there are no other entries at all) -- "a refusal the queue cannot
//     explain" (06-CONTEXT.md). entries returned UNCHANGED: ClassifyLeftover's generic
//     scope.ReasonAPIError assignment is left exactly as-is, NEVER silently overwritten with a
//     specific-sounding but false scope.ReasonCompartmentNotEmpty and an empty blocker list. This
//     is the exact coverage-gap signal this phase exists to surface (T-06-04-01).
//
// Mutates entries in place (the same slice/backing array the caller passed in) and returns it --
// mirroring pkg/plan/build.go's mergeSkipEvent's own in-place-overwrite convention for an
// already-built StateLeftover entry.
func EnrichCompartmentLeftover(entries []Entry, compartmentID string) []Entry {
	// compartmentEntry is taken by pointer directly into entries' own backing array, inside the
	// same range loop gosec can prove bounded (mirrors pkg/plan/build.go's mergeSkipEvent) --
	// deliberately not a bare int index held past the loop and reused later, which trips gosec's
	// G602 "slice index out of range" heuristic even though compartmentIdx == -1 is checked first.
	var compartmentEntry *Entry
	for i := range entries {
		if entries[i].ResourceType == compartmentEntryResourceType &&
			entries[i].CompartmentID == compartmentID &&
			entries[i].State == StateLeftover {
			compartmentEntry = &entries[i]
			break
		}
	}
	if compartmentEntry == nil {
		return entries
	}

	var blockers []Entry
	for i := range entries {
		// Exclude every Compartment entry (by ResourceType, not only the one identified above by
		// pointer identity) -- 06-REVIEW.md WR-03. Exactly one Compartment entry per compartment
		// is the current invariant, so this is currently equivalent to the pointer check it
		// replaces, but a pointer-only exclusion is fragile to any future regression
		// reintroducing a second, sibling Compartment entry for the same CompartmentID (the exact
		// defect CR-01 was): that sibling would otherwise be listed as its own "still present"
		// blocker, producing a confusing, self-referential-looking report ("Compartment X still
		// blocked by: Compartment X") instead of surfacing the real defect.
		if entries[i].ResourceType == compartmentEntryResourceType {
			continue
		}
		if entries[i].CompartmentID != compartmentID || entries[i].State == StateRemoved {
			continue
		}
		blockers = append(blockers, entries[i])
	}
	if len(blockers) == 0 {
		// "A refusal the queue cannot explain" -- leave ClassifyLeftover's generic
		// scope.ReasonAPIError assignment untouched (T-06-04-01).
		return entries
	}

	parts := make([]string, 0, len(blockers))
	for _, b := range blockers {
		parts = append(parts, b.ResourceType+" "+b.ResourceID)
	}

	compartmentEntry.Reason = scope.ReasonCompartmentNotEmpty
	compartmentEntry.Detail = "still present: " + strings.Join(parts, "; ")

	return entries
}
