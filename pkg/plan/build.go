package plan

import (
	"strings"

	"github.com/ekristen/libnuke/pkg/queue"
	"github.com/ekristen/libnuke/pkg/resource"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// BuildFromQueue maps every queue.Item left in n.Queue.GetItems() after a *libnuke.Nuke's Run()
// call returns into an Entry -- the only externally observable state n.run (the removal loop,
// unexported) leaves behind, per 03-RESEARCH.md Q4/Q5/Q6. noDryRun distinguishes "this Nuke was
// never given a chance to remove anything" (every ItemStateNew/ItemStateNewDependency item is
// still exactly as Scan() left it: a plan candidate) from "removal already ran" (the same states
// now mean the item never got its turn: a leftover). leftoverReason classifies any item this
// function decides is a leftover; it is injected rather than derived from item.Reason itself,
// since libnuke only ever retains the flattened err.Error() string there, never a typed reason.
// When leftoverReason is nil, every leftover defaults to scope.ReasonAPIError rather than
// panicking or leaving Reason empty.
func BuildFromQueue(items []*queue.Item, noDryRun bool, leftoverReason func(*queue.Item) scope.RefusalReason) []Entry {
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		entries = append(entries, buildEntry(item, noDryRun, leftoverReason))
	}
	return entries
}

func buildEntry(item *queue.Item, noDryRun bool, leftoverReason func(*queue.Item) scope.RefusalReason) Entry {
	region, compartmentID := splitOwner(item.Owner)

	entry := Entry{
		ResourceType:  item.Type,
		ResourceID:    resourceID(item),
		CompartmentID: compartmentID,
		Region:        region,
		Detail:        item.GetReason(),
	}

	switch item.GetState() {
	case queue.ItemStateFiltered:
		// Excluded by a configured filter -- never a removal candidate this run, regardless of
		// dry-run vs. destructive mode.
		entry.State = StateFiltered
	case queue.ItemStateFinished:
		// Only reachable once n.run(ctx) has actually executed (noDryRun=true) and this item's
		// removal converged.
		entry.State = StateRemoved
	case queue.ItemStateNew, queue.ItemStateNewDependency:
		if noDryRun {
			// The removal loop ran and this item never got its turn before the run ended --
			// still genuinely a leftover, not a "would remove".
			entry.State = StateLeftover
			entry.Reason = classifyLeftover(item, leftoverReason)
		} else {
			entry.State = StateWouldRemove
		}
	default:
		// ItemStateHold, ItemStatePending, ItemStatePendingDependency, ItemStateWaiting,
		// ItemStateFailed: on a destructive run these are genuinely stuck/unresolved states a
		// finished n.Run(ctx) can leave behind. On a dry run these should not naturally occur at
		// all (n.run never executed) -- treated as leftovers defensively in both modes, since
		// silently reclassifying an unexpected state as "would remove" would be worse than an
		// over-cautious leftover.
		entry.State = StateLeftover
		entry.Reason = classifyLeftover(item, leftoverReason)
	}

	return entry
}

func classifyLeftover(item *queue.Item, leftoverReason func(*queue.Item) scope.RefusalReason) scope.RefusalReason {
	if leftoverReason == nil {
		return scope.ReasonAPIError
	}
	return leftoverReason(item)
}

// splitOwner recovers region and compartment ID from queue.Item.Owner's own documented
// "region/subscription" convention (already this project's established shape --
// resources_test/dryrun_test.go's newTestNuke helper builds Owner exactly this way). strings.Cut
// on the first '/', not strings.Split -- a compartment OCID never contains a '/', so cutting on
// the first occurrence is safe and avoids breaking on any future Owner value that legitimately
// contains more than one segment after the region.
func splitOwner(owner string) (region, compartmentID string) {
	region, compartmentID, _ = strings.Cut(owner, "/")
	return region, compartmentID
}

// resourceID prefers resource.UniqueKeyGetter (SAFE-09: every resource type Phase 4+ registers
// is expected to implement it) and falls back to item.GetProperty(""), the LegacyStringer path
// libnuke itself falls back to, when a resource type does not implement UniqueKeyGetter -- a
// tolerated but discouraged fallback.
func resourceID(item *queue.Item) string {
	if getter, ok := item.Resource.(resource.UniqueKeyGetter); ok {
		return getter.UniqueKey()
	}
	id, err := item.GetProperty("")
	if err != nil {
		return ""
	}
	return id
}

// MergeSkipEvents folds a run's accumulated scope.SkipEvent stream into entries.
// scopedLister.List() drops out-of-scope/blocklisted resources before they ever become a
// queue.Item, so those skips are invisible to BuildFromQueue and must be merged in separately
// (03-RESEARCH.md Q4). For each SkipEvent, if an existing entry matches by ResourceID (when the
// SkipEvent carries one) or by CompartmentID alone (a compartment-level skip, e.g. blocklisting):
//
//   - A StateLeftover match keeps its own State and has only its Reason/Detail overwritten -- a
//     Remove()-time report is strictly more specific than the generic api-error default, but the
//     item genuinely was a removal candidate the run ran out of time for, so "leftover" remains
//     the right word.
//   - A StateFiltered match is converted IN PLACE to StateSkipped (Reason/Detail set from the
//     SkipEvent), never left as a second, separate entry. This is 06-REVIEW.md WR-01: some
//     Filter() implementations (Compartment's blocklisted-descendant branch, the block storage
//     types' FAULTY-state branch) both return a non-nil error --
//     which alone would route the queue item to a generic, reason-less StateFiltered entry -- AND
//     call ocinuke.ReportLeftover with a specific scope.SkipEvent for the exact same resource.
//     Before this fix, mergeSkipEvent only ever matched StateLeftover entries, so that SkipEvent
//     fell through to the append-a-new-entry branch below, producing two entries (one "filtered",
//     one "skipped") for one resource. A Filter()-excluded item that also carries its own
//     SkipEvent never became a removal candidate at all -- exactly StateSkipped's documented
//     meaning (plan.go: "this resource never became a candidate at all -- out-of-scope,
//     blocklisted, or another scope.SkipEvent-carried reason") -- so the filtered entry is the
//     one that gets replaced, not kept alongside a new skipped one.
//
// Any other State (StateWouldRemove, StateRemoved, or an entry with no match at all) is left
// untouched, and instead causes a new Entry{State: StateSkipped} to be appended.
func MergeSkipEvents(entries []Entry, skips []scope.SkipEvent) []Entry {
	merged := append([]Entry(nil), entries...)
	for i := range skips {
		merged = mergeSkipEvent(merged, &skips[i])
	}
	return merged
}

func mergeSkipEvent(entries []Entry, skip *scope.SkipEvent) []Entry {
	for i := range entries {
		if !skipMatchesEntry(&entries[i], skip) {
			continue
		}
		switch entries[i].State {
		case StateLeftover:
			// Keep State as-is -- see MergeSkipEvents' doc comment.
		case StateFiltered:
			// Collapse into the single StateSkipped entry -- see MergeSkipEvents' doc comment
			// (06-REVIEW.md WR-01).
			entries[i].State = StateSkipped
		case StateSkipped:
			// Idempotent, not a second entry: a duplicate SkipEvent matching an
			// already-StateSkipped entry (07-REVIEW.md CR-03 -- e.g. a future caller that reports
			// the same skip twice) updates Reason/Detail in place on the existing entry rather
			// than falling through to the append-a-new-entry branch below, which would otherwise
			// produce two StateSkipped entries for the same resource.
		default:
			continue
		}
		entries[i].Reason = skip.Reason
		entries[i].Detail = skip.Detail
		return entries
	}

	return append(entries, Entry{
		ResourceType:  skip.ResourceType,
		ResourceID:    skip.ResourceID,
		CompartmentID: skip.CompartmentID,
		State:         StateSkipped,
		Reason:        skip.Reason,
		Detail:        skip.Detail,
	})
}

func skipMatchesEntry(entry *Entry, skip *scope.SkipEvent) bool {
	if skip.ResourceID != "" {
		return entry.ResourceID == skip.ResourceID
	}
	return entry.CompartmentID == skip.CompartmentID
}
