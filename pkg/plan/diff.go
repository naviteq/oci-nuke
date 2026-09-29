package plan

import (
	"fmt"
	"sort"
	"strings"
)

// diffKey builds the composite key Diff and Body.Sort both key entries by: resource type,
// resource ID, region, compartment ID. Matching Body.Sort's own tiebreak fields means Diff's
// walk order is deterministic without inventing a second, independent ordering scheme. e is
// taken by pointer (gocritic hugeParam: Entry is 112 bytes) purely to avoid the copy -- diffKey
// never mutates *e.
func diffKey(e *Entry) string {
	return e.ResourceType + "|" + e.ResourceID + "|" + e.Region + "|" + e.CompartmentID
}

// Diff reports the structural differences between two already-typed plan bodies -- the
// mismatch report an approved-plan verification gate (07-CONTEXT.md) prints when a forced
// rescan's hash no longer matches an approved artifact's hash. This is a domain-specific
// structural diff over an already-typed, already-sorted []Entry slice (07-RESEARCH.md's "Don't
// Hand-Roll" table), not a generic text-diff problem, so no external diff library is used.
//
// Diff never mutates old or new: both Entries slices are copied into per-key maps before any
// comparison happens. The returned lines are always in stable, sorted-by-key order --
// independent of the order either Body's Entries arrived in, since callers may build a Body by
// ranging a map or by any other order-preserving means Diff cannot assume anything about.
//
// Output shape, one line per differing key:
//
//	"+ <type> <id> (<region>/<compartment>): <state>"   -- only in new
//	"- <type> <id> (<region>/<compartment>): <state>"   -- only in old
//	"~ <type> <id> (<region>/<compartment>): <what changed>" -- present in both, State/Reason/
//	                                                            Detail differs (WR-01, 07-REVIEW.md:
//	                                                            Hash covers the whole Entry, so a
//	                                                            same-State pair whose Reason or
//	                                                            Detail differs is still a genuine
//	                                                            hash-affecting difference and must
//	                                                            not print an empty diff)
//	"! <type> <id> (<region>/<compartment>): <note>"     -- WR-05, 07-REVIEW.md: this key appeared
//	                                                         more than once within a single body;
//	                                                         Diff only compared the last occurrence
//
// A key present in both with an identical State, Reason, and Detail produces no line at all.
func Diff(old, newBody Body) []string {
	oldByKey, oldDuplicates := diffByKeyDetectingDuplicates(old.Entries)
	newByKey, newDuplicates := diffByKeyDetectingDuplicates(newBody.Entries)

	keys := make([]string, 0, len(oldByKey)+len(newByKey))
	seen := make(map[string]struct{}, len(oldByKey)+len(newByKey))
	for k := range oldByKey {
		if _, ok := seen[k]; !ok {
			keys = append(keys, k)
			seen[k] = struct{}{}
		}
	}
	for k := range newByKey {
		if _, ok := seen[k]; !ok {
			keys = append(keys, k)
			seen[k] = struct{}{}
		}
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		oldEntry, hasOld := oldByKey[k]
		newEntry, hasNew := newByKey[k]

		switch {
		case hasOld && hasNew:
			if desc := diffDescription(&oldEntry, &newEntry); desc != "" {
				lines = append(lines, formatDiffLine("~", &newEntry, desc))
			}
		case hasNew:
			lines = append(lines, formatDiffLine("+", &newEntry, string(newEntry.State)))
		case hasOld:
			lines = append(lines, formatDiffLine("-", &oldEntry, string(oldEntry.State)))
		}

		if n, ok := oldDuplicates[k]; ok {
			lines = append(lines, formatDiffLine("!", &oldEntry, fmt.Sprintf(
				"duplicate key: %d entries share this ResourceType|ResourceID|Region|CompartmentID "+
					"in the OLD body -- Diff only compared the last one", n)))
		}
		if n, ok := newDuplicates[k]; ok {
			lines = append(lines, formatDiffLine("!", &newEntry, fmt.Sprintf(
				"duplicate key: %d entries share this ResourceType|ResourceID|Region|CompartmentID "+
					"in the NEW body -- Diff only compared the last one", n)))
		}
	}

	return lines
}

// diffByKeyDetectingDuplicates builds the same diffKey-indexed map Diff has always built, plus a
// second map recording, for every key that appeared more than once in entries, how many total
// occurrences it had (WR-05, 07-REVIEW.md). Diff itself only ever sees the LAST entry for a given
// key (a later assignment into byKey silently overwrites an earlier one) -- that half of the
// behavior is unchanged, and is documented as an accepted limitation matching Body.Sort's own
// tiebreak-uniqueness assumption (a composite key should never repeat within one well-formed
// Body). What changes is that the collision is now detectable and reported as its own diagnostic
// line, rather than silently dropping the other occurrence(s) with no trace in the output.
func diffByKeyDetectingDuplicates(entries []Entry) (byKey map[string]Entry, duplicateCounts map[string]int) {
	byKey = make(map[string]Entry, len(entries))
	counts := make(map[string]int, len(entries))
	for i := range entries {
		k := diffKey(&entries[i])
		counts[k]++
		byKey[k] = entries[i]
	}

	duplicateCounts = make(map[string]int)
	for k, n := range counts {
		if n > 1 {
			duplicateCounts[k] = n
		}
	}
	return byKey, duplicateCounts
}

// diffDescription builds the "what changed" description Diff prints for a same-key entry present
// in both bodies (WR-01, 07-REVIEW.md). Comparing State alone missed genuine differences: Hash's
// CanonicalJSON input covers the whole Entry, so two entries with identical
// ResourceType|ResourceID|Region|CompartmentID|State but a different Reason or Detail (e.g. a
// leftover whose blocking cause changed between approval and rescan, or a skip whose reason moved
// from blocklisted to protected-by-tag) genuinely hash differently, but a State-only comparison
// would print no line for them at all -- an operator would see "tenancy state has drifted since
// approval:" followed by nothing. Each differing field gets its own clause; an empty return means
// old and new are identical across all three fields (no line should be printed for this key).
// old/newEntry are taken by pointer for the same hugeParam reason as diffKey/formatDiffLine
// above; diffDescription never mutates either.
func diffDescription(old, newEntry *Entry) string {
	var clauses []string
	if old.State != newEntry.State {
		clauses = append(clauses, fmt.Sprintf("%s -> %s", old.State, newEntry.State))
	}
	if old.Reason != newEntry.Reason {
		clauses = append(clauses, fmt.Sprintf("reason %q -> %q", old.Reason, newEntry.Reason))
	}
	if old.Detail != newEntry.Detail {
		clauses = append(clauses, fmt.Sprintf("detail %q -> %q", old.Detail, newEntry.Detail))
	}
	return strings.Join(clauses, "; ")
}

// formatDiffLine renders one Diff output line for entry, with the given prefix and state
// description -- the single formatting path every branch of Diff's switch above shares, so the
// line shapes cannot drift apart. entry is taken by pointer for the same hugeParam reason as
// diffKey above; formatDiffLine never mutates *entry.
func formatDiffLine(prefix string, entry *Entry, stateDescription string) string {
	return fmt.Sprintf("%s %s %s (%s/%s): %s",
		prefix, entry.ResourceType, entry.ResourceID, entry.Region, entry.CompartmentID, stateDescription)
}
