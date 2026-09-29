// Package scope resolves the set of OCI compartments a run is permitted to touch, and reports
// why a resource or compartment was excluded from that set.
package scope

// RefusalReason is a machine-readable reason a resource or compartment was skipped rather than
// acted on. Every skip in the safety model carries one of these, so downstream consumers (the
// CLI's own summary output, and Phase 3's plan-artifact writer) never have to parse free text to
// know why something was left alone.
type RefusalReason string

// The ten refusal reasons the safety model and the leftover walk can produce: the original four
// from 02-CONTEXT.md locked requirement 8 (out-of-scope, blocklisted, protected-by-tag,
// too-young), the four leftover-specific reasons 03-CONTEXT.md locked requirement 4 names
// (scheduled-deletion, retention-locked, dependency-not-satisfied, api-error), and two Phase-5
// additions (delete-protected, backup-residue -- see each constant's own doc comment below for
// why neither existing reason cleanly fits).
//
// Reconciliation against the ticket's leftover vocabulary (03-RESEARCH.md Q6's "extend, do not
// duplicate" table -- restated here so this file is the single place both vocabularies are
// visibly cross-referenced):
//   - Ticket's "protected" is satisfied by the already-shipped ReasonProtectedByTag below --
//     not renamed, not aliased. "protected" is an informal category name, not a distinct reason.
//   - ReasonBlocklisted has no ticket equivalent. It is kept as-is: a real, distinct,
//     compartment-level reason with 8 passing tests already referencing it by name
//     (02-VERIFICATION.md); renaming or conflating it with "protected" would break shipped,
//     tested behavior for no benefit.
//   - scheduled-deletion, retention-locked, dependency-not-satisfied, and api-error are net-new
//     additions from Phase 3, extending this same type rather than starting a second, competing
//     enum -- one reason vocabulary is the whole point of this reconciliation.
//   - delete-protected and backup-residue are net-new additions from Phase 5
//     (05-second-coverage-wave Plan 02, following ReasonAPIError's own precedent of extending
//     this same const block rather than starting a second enum): delete-protected covers an
//     OCI-service-enforced refusal (MySQLDbSystem's DeletionPolicy.IsDeleteProtected) distinct
//     from ReasonProtectedByTag's operator-configured tag match; backup-residue covers a
//     never-attempted, purely informational leftover (an automatic post-termination database
//     backup) distinct from ReasonScheduledDeletion's already-attempted-but-delayed resource and
//     from ReasonRetentionLocked's already-attempted-but-blocked resource.
//   - compartment-not-empty is a net-new addition from Phase 6 (06-compartment-delete-last Plan
//     01, same extend-this-const-block precedent): a user override of the original proposal to
//     reuse ReasonDependencyNotSatisfied (06-CONTEXT.md) -- "this compartment did not empty" and
//     "this resource is waiting on its own DependsOn dependency" call for different operator
//     actions, and a distinct reason keeps the report greppable.
const (
	// ReasonOutOfScope means the resource's own compartmentId is not in the resolved in-scope
	// set — the per-resource re-verification defense (SAFE-05), independent of blocklisting.
	ReasonOutOfScope RefusalReason = "out-of-scope"
	// ReasonBlocklisted means the compartment itself, or an ancestor of it, is listed in
	// compartment-blocklist (SCOPE-02).
	ReasonBlocklisted RefusalReason = "blocklisted"
	// ReasonProtectedByTag means a protect-by-tag safety filter matched the resource.
	ReasonProtectedByTag RefusalReason = "protected-by-tag"
	// ReasonTooYoung means a minimum-age safety filter matched the resource.
	ReasonTooYoung RefusalReason = "too-young"
	// ReasonAPIError means a resource could not be removed and no more specific leftover
	// classification applies -- the default fallback pkg/plan.BuildFromQueue assigns to any
	// leftover whose caller-supplied classifier is nil or itself returns nothing more specific.
	// Added ahead of this phase's other new leftover reasons (scheduled-deletion,
	// retention-locked, dependency-not-satisfied -- see 03-RESEARCH.md Q6's reconciliation
	// table) because pkg/plan's leftover-reason fallback needs a defined constant to compile
	// against; the remaining three land in a later plan of this same phase.
	ReasonAPIError RefusalReason = "api-error"
	// ReasonScheduledDeletion means the resource has already been scheduled for a future,
	// delayed deletion by the OCI service itself (e.g. a KMS vault's 7-30 day deletion window)
	// and cannot be removed any sooner this run. Cannot be recovered from item.Reason after the
	// fact (only the flattened err.Error() string survives, never a typed error -- 03-RESEARCH.md
	// Q6) -- a resource type's Remove() must report this through the structured
	// ocinuke.ReportLeftover/SkipEvent side channel (pkg/ocinuke/leftover.go), immediately before
	// returning a plain error, never liberrors.ErrHoldResource (see that file's doc comment for
	// why ErrHoldResource would hang the run indefinitely instead).
	ReasonScheduledDeletion RefusalReason = "scheduled-deletion"
	// ReasonRetentionLocked means a retention rule with a lock (e.g. an Object Storage bucket
	// retention rule) makes the resource permanently unremovable, not merely delayed. Same
	// reporting mechanism and same ErrHoldResource hazard as ReasonScheduledDeletion above.
	ReasonRetentionLocked RefusalReason = "retention-locked"
	// ReasonDependencyNotSatisfied means the item is still waiting on another resource it
	// depends on to be removed first -- state-derivable directly from ItemStateNewDependency /
	// ItemStatePendingDependency, meaningful once Parameters.WaitOnDependencies=true is set.
	// Unreachable in production today with zero DependsOn-declaring resource types registered
	// (03-RESEARCH.md Q6), but state-derivable with no resource-author cooperation needed the
	// moment one exists.
	ReasonDependencyNotSatisfied RefusalReason = "dependency-not-satisfied"
	// ReasonDeleteProtected means the OCI SERVICE ITSELF refuses deletion via its own
	// service-level delete-protection flag -- currently only MySQLDbSystem's
	// DeletionPolicy.IsDeleteProtected (05-second-coverage-wave Plan 02). This is deliberately
	// NOT ReasonProtectedByTag: that reason means a config-driven settings.protect.tags match
	// evaluated by ocinuke.Evaluate at scan time -- an operator-configured, this-tool-enforced
	// decision. ReasonDeleteProtected means a fundamentally different actor (OCI's own API)
	// already refuses the deletion outright, independent of any oci-nuke configuration.
	// --force/NoDryRun never bypasses this: MySQLDbSystem.Filter() is a scan-time exclusion, and
	// Filter() runs identically regardless of dry-run mode, so no code path needs an explicit
	// --force check -- a future reader should not add one.
	ReasonDeleteProtected RefusalReason = "delete-protected"
	// ReasonBackupResidue means the reported item was never itself a deletion target in this
	// run -- it is an automatic post-termination database backup (AutonomousDatabase, DbSystem,
	// or MySQLDbSystem) enumerated purely for visibility, per 05-CONTEXT.md's locked "residue
	// reporting only for now" decision. This is deliberately distinct from BOTH:
	//   - ReasonScheduledDeletion: that resource WAS a deletion target, and OCI itself has
	//     already scheduled its future removal (e.g. a KMS vault's 7-30 day window). A backup was
	//     never targeted for removal by this tool at all.
	//   - ReasonRetentionLocked: that resource's removal WAS attempted and blocked by an active
	//     lock. A backup's removal is never attempted in the first place -- Remove() is never
	//     called, and no backup type is ever registered as an independently deletable resource.
	ReasonBackupResidue RefusalReason = "backup-residue"
	// ReasonCompartmentNotEmpty means a Compartment resource's DeleteCompartment work request
	// FAILED because the compartment still contains resources (verified via GetWorkRequest, never
	// a synchronous DeleteCompartment error -- 06-RESEARCH.md Contradiction 1), OR the compartment
	// has a blocklisted descendant and was never attempted at all (Filter()-time exclusion,
	// HasBlocklistedDescendant above). Deliberately distinct from ReasonDependencyNotSatisfied
	// (06-CONTEXT.md, a user override of the original reuse-ReasonDependencyNotSatisfied
	// proposal): "this compartment did not empty" and "this resource is waiting on its own
	// DependsOn dependency" call for different operator actions, and a distinct reason keeps the
	// report greppable. Carries the same ErrHoldResource hazard as ReasonScheduledDeletion/
	// ReasonRetentionLocked above: a resource type reporting this must do so via a plain error
	// (routing to ItemStateFailed) or pkg/plan's Compartment-aware leftover enrichment (added in a
	// later plan of this phase), never via liberrors.ErrHoldResource -- ErrHoldResource retries
	// every round unconditionally with no budget of its own and would hang the run forever
	// (pkg/ocinuke/leftover.go's doc comment explains why). Unlike the other delayed-outcome
	// reasons, a Compartment item reporting this is specifically designed to re-arm itself back to
	// ItemStatePending every round (resources/compartment.go's HandleWait FAILED branch, via
	// HandleQueue's ItemStateFailed branch re-issuing DeleteCompartment in the same pass) before
	// handleFailure's ~3-round threshold can ever accumulate -- see
	// resources_test/compartment_fault_injection_test.go for the proof.
	ReasonCompartmentNotEmpty RefusalReason = "compartment-not-empty"
	// ReasonCompartmentNotActive means a compartment was never examined because its own
	// lifecycleState is not ACTIVE. Tree.Resolve has always pruned these (Assumption A2,
	// ACTIVE-only default) and that prune is correct -- what was wrong is that it was the one
	// prune with no record of itself, so a subtree could go unexamined with nothing in the
	// artifact saying so. NR-787: a DELETING compartment is the deadlock case and a CREATING one
	// is a race, and both read identically to "scanned, found clean" without this.
	//
	// Deliberately distinct from ReasonCompartmentNotEmpty: that one means a delete was
	// attempted and the compartment still had contents. This one means no delete was attempted
	// and no scan happened.
	ReasonCompartmentNotActive RefusalReason = "compartment-not-active"
	// ReasonCompartmentStillDeleting means the DeleteCompartment work request was accepted and
	// was still ACCEPTED or IN_PROGRESS when the run's wait budget ran out. The compartment is
	// not "not empty" -- it is not finished. NR-787 observed the difference costing real time:
	// a run gave up at "max wait retries of 200 exceeded" reporting compartment-not-empty, and
	// the compartment reached DELETED three minutes later. The report was honest and named the
	// wrong thing, which sent the operator looking for contents that were already gone.
	ReasonCompartmentStillDeleting RefusalReason = "compartment-still-deleting"
)

// SkipEvent records a single skip decision. It is deliberately flat and JSON-serializable
// without any further transformation, since Phase 3's plan-artifact writer consumes this shape
// directly.
type SkipEvent struct {
	Reason        RefusalReason `json:"reason"`
	ResourceType  string        `json:"resource_type,omitempty"`
	ResourceID    string        `json:"resource_id,omitempty"`
	CompartmentID string        `json:"compartment_id"`
	Detail        string        `json:"detail,omitempty"`
}
