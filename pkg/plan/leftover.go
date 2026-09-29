package plan

import (
	"github.com/ekristen/libnuke/pkg/queue"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// ClassifyLeftover derives a scope.RefusalReason from item's own ItemState alone -- fully
// state-derivable, no resource-author cooperation required (03-RESEARCH.md Q6's "Observability
// of each new reason" table). It is the concrete function satisfying BuildFromQueue's
// leftoverReason extension point (pkg/plan/build.go's leftoverReason func(*queue.Item)
// scope.RefusalReason parameter, Plan 03-02's contract) and is only ever invoked by
// BuildFromQueue for the states it already routes to StateLeftover -- ClassifyLeftover does not
// need to re-derive New/Finished/Filtered handling, only refine the reason for the
// leftover-classified states.
//
// scheduled-deletion and retention-locked are deliberately NOT produced here: item.Reason only
// ever retains the flattened err.Error() string, never the original typed error (03-RESEARCH.md
// Q6), so ClassifyLeftover alone cannot recover them. A resource type whose Remove() detects one
// of those OCI-specific, non-retryable-this-run outcomes must report it through the structured
// ocinuke.ReportLeftover/scope.SkipEvent side channel instead (pkg/ocinuke/leftover.go), merged
// over this function's api-error default via MergeSkipEvents (Plan 03-02) -- Plan 03-07
// exercises that end-to-end path with a stubbed resource.
func ClassifyLeftover(item *queue.Item) scope.RefusalReason {
	switch item.GetState() {
	case queue.ItemStateFailed:
		// The default, state-derivable fallback -- no resource-author cooperation required.
		return scope.ReasonAPIError

	case queue.ItemStateNewDependency, queue.ItemStatePendingDependency:
		// State-derivable, meaningful once Parameters.WaitOnDependencies=true is set (Plan
		// 03-06's job) -- unreachable in production today with zero DependsOn-declaring
		// resource types registered, per 03-RESEARCH.md Q6.
		return scope.ReasonDependencyNotSatisfied

	case queue.ItemStateWaiting, queue.ItemStateHold, queue.ItemStatePending:
		// libnuke never populates item.Reason on its own for these three states (only
		// ItemStateFailed and ItemStatePendingDependency get one from libnuke itself) -- fill
		// in an operator-actionable detail naming the raw state, so a leftover report entry for
		// a resource stuck here is never left with a blank Detail. Left as-is if a caller (or a
		// future libnuke internal) already populated Reason.
		if item.GetReason() == "" {
			item.Reason = "left in state " + item.GetState().String() + ": max-wait-retries likely exhausted"
		}
		return scope.ReasonAPIError

	default:
		// Every other state BuildFromQueue can route here (concretely: ItemStateNew, when
		// noDryRun=true and the item never got its turn before the run ended) still needs a
		// non-empty reason rather than an unhandled zero value -- api-error is the documented
		// fallback for exactly this case, matching BuildFromQueue's own nil-leftoverReason
		// default.
		return scope.ReasonAPIError
	}
}
