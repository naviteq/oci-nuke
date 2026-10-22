# ADR-0004: The plan hash covers the decision set, not the whole plan

## Status

Accepted

## Context

An apply run re-scans the approved scope and refuses unless the rescan's plan hash equals the
approved one. Until this ADR the hash covered the whole plan body: every entry, in every state,
with its free-text `detail`.

Most of a real plan is not a decision. In the sandbox `terraform-modules` cleans, a plan of 516
entries held 14 `would-remove` resources and one compartment deferred as `compartment-not-empty`.
The other 501 were `filtered` (terminated volumes, object versions, config-filtered resources) or
`skipped` (too young, protected by tag, scheduled for deletion, compartments already `DELETED`).

That sandbox is live: CI test runs create and destroy resources in it all day. Between a plan and
its approval something among those 501 entries nearly always changes, and the gate refused. Of
eight consecutive refusals in that sandbox's approval queue, two caught a genuine change to
what the run would delete: resources that became `would-remove` after the plan. The other six
were:

- a deferred compartment whose `detail` named the same blockers in a different order — libnuke's
  scanners run concurrently and `DeferNonEmptyCompartments` listed blockers in arrival order;
- a `too-young` resource ageing past `min-age` into `filtered` between plan and apply;
- instances, boot volumes and clusters reaching `TERMINATED` and moving between `skipped` and
  `filtered`, or dropping out of the listing.

None of them changed what the run would delete. A gate that refuses for reasons unrelated to its
purpose gets routed around — re-plan and approve again without reading — which is worse than no
gate at all.

## Decision

The hash covers `plan.DecisionBody`: every entry **except** `filtered` entries and `skipped`
entries whose reason is not `compartment-not-empty`, each with `detail` blanked. Separately,
`DeferNonEmptyCompartments` sorts its blocker list, so `detail` is deterministic for the human
reading it even though it is no longer hashed.

- `would-remove` entries are the decision itself.
- A compartment deferred as `compartment-not-empty` stays in, because libnuke still calls `Remove()`
  on it; whether it is deferred or deletable is part of what is being approved.
- `reason` stays hashed. It is a closed vocabulary, so it cannot drift on wording.
- `detail` does not. It is free text — OCI's error strings, blocker lists — and two scans of an
  unchanged tenancy are not guaranteed to word it identically.

The exclusion list is negative on purpose. A state added later (`removed`, `leftover` and any new
one) is hashed until someone decides otherwise, so forgetting to classify it produces a spurious
refusal rather than a silent pass.

The mismatch report diffs the two decision sets, so every line it prints is one that moved the
hash.

Rejected alternatives:

- **Only sort the blocker list.** Fixes the most frequent cause and leaves the gate refusing
  whenever a resource in a live sandbox ages or terminates.
- **Hash only `would-remove` entries.** Drops deferred compartments, which libnuke still targets.
- **Keep hashing everything and retry the rescan on mismatch.** Hides drift instead of deciding
  which drift matters, and doubles an already doubled scan cost.

## Consequences

**This changes a documented safety property**, so it is declared as a breaking change under the
project's rule that any change to the safety gates is one regardless of semver.

What still holds, covered by `pkg/plan/plan_test.go`:

- Any change to the set of resources the run would delete refuses the apply: a resource appearing,
  disappearing, changing region or compartment, a `skipped` or `filtered` resource becoming
  `would-remove`, or a deferred compartment becoming deletable.
- A filter removed from the config between plan and apply surfaces as a new `would-remove` entry
  and refuses.
- The hash is still order-independent and independent of `generated_at`.
- `--approved-plan` still refuses an artifact whose stored hash does not match its own body.

What no longer refuses: a change confined to entries the run leaves alone. A resource that was
`too-young` at plan time and `filtered` at apply time was never going to be deleted by either run.

Hashes are not comparable across this change. An artifact written by an earlier release fails
`--approved-plan`'s self-consistency check and has to be re-planned. The shared workflows hand
the plan run's binary to the apply run, so a plan and its apply already use the same release.
