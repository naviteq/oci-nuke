package ocinuke

import "github.com/naviteq/oci-nuke/pkg/scope"

// LeftoverReporter is the shape a resource type's Remove() implementation holds a stable
// reference to, so it can report a scope.SkipEvent-carried leftover reason immediately before
// returning a plain error -- structurally identical to scopedLister's own onSkip func(*scope.
// SkipEvent) field, named as its own type here so it has a documentable, referenceable identity
// at the resource-author-facing seam rather than only ever appearing as an inline field type.
type LeftoverReporter func(*scope.SkipEvent)

// ReportLeftover is the convention a resource type's Remove() implementation calls immediately
// before returning a plain error for a scheduled-deletion or retention-locked outcome --
// never liberrors.ErrHoldResource. ErrHoldResource is retried every round unconditionally, with
// no separate retry budget of its own (unlike ItemStateFailed, which IS bounded by libnuke's
// ~3-round give-up threshold), so a resource that always returns ErrHoldResource for "already
// scheduled for deletion, nothing more to do" would keep the queue permanently non-empty and
// hang the run forever -- the opposite of the intended "report and move on" behavior
// (03-RESEARCH.md Q6). A plain error routes the item to ItemStateFailed instead, which Plan
// 03-03's ClassifyLeftover (pkg/plan/leftover.go) defaults to api-error for -- ReportLeftover is
// what lets a resource author override that default with the more specific reason it actually
// observed, via the same structured side channel already established for out-of-scope/
// blocklisted (scopedLister's onSkip), since item.Reason itself only ever retains the flattened
// err.Error() string, never a typed reason recoverable after the fact.
//
// ReportLeftover has no production call site as of this phase. resources/ is still empty --
// Phase 4's shared resource base is its intended first consumer, exactly as ocinuke.Evaluate was
// scaffolded ahead of its own first consumer in Phase 2. Do not assume this is already wired
// into any run path simply because it exists and is tested.
//
// reporter is nil-safe: a resource type constructed without one (or a call site that has not
// wired reporting yet) can call ReportLeftover unconditionally without a nil-pointer check of
// its own. evt is taken by pointer, mirroring Evaluate's own *scope.SkipEvent return convention
// and LeftoverReporter's own pointer parameter, so this call site never copies the (heavier)
// struct by value. A caller that constructs evt fresh per call (as every existing call site
// does) and never mutates it afterward hands reporter a pointer it is safe to retain.
func ReportLeftover(reporter LeftoverReporter, evt *scope.SkipEvent) {
	if reporter != nil && evt != nil {
		reporter(evt)
	}
}
