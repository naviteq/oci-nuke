# ADR-0001: Build on libnuke rather than adopt OCI-SuperDelete or ociextirpater

## Status

Accepted

## Context

Naviteq's immediate driver is internal: `terraform-modules` currently cleans up OCI sandbox
and CI tenancies with a hand-rolled `scripts/oci-nuke.py`. That script is the thing `oci-nuke`
replaces. Before starting from scratch, two existing open-source OCI-specific deletion tools
were evaluated as a potential base: **OCI-SuperDelete** and **ociextirpater**.

Both were rejected as a base for the same three structural reasons:

- **No filter engine.** Neither tool has a general-purpose way to include/exclude resources by
  name, tag, age, or pattern. Any filtering has to be hand-coded per resource type, which does
  not scale to the tenancy-wide coverage this project targets (compute, network, storage, load
  balancing, OKE/OCIR, databases, platform services, IAM, KMS).
- **No dependency graph.** Deleting OCI resources in the wrong order routinely produces
  "resource in use" errors (a VCN cannot be deleted while a subnet exists, a subnet cannot be
  deleted while an instance exists, and so on). Neither tool models these dependencies or
  retries in dependency order; both rely on ad hoc retry loops or manual ordering.
- **Dry-run as an afterthought.** Given this project's core value — *the tool must never delete
  something the operator did not intend to delete* — a dry-run mode that is bolted on rather
  than structural is disqualifying. In both tools, "dry-run" is a flag threaded loosely through
  delete calls rather than a control-flow guarantee enforced by the runner itself.

Rewriting either tool to add a filter engine, a dependency graph, and a structural dry-run
guarantee would cost more than adopting a library that already has all three, tested against
years of production use by sibling tools.

## Decision

Build `oci-nuke` on [`github.com/ekristen/libnuke`](https://github.com/ekristen/libnuke)
`v1.3.0` (MIT license, core extracted from `aws-nuke` v3, and shared by `gcp-nuke` and
`azure-nuke`). `libnuke` owns resource scan/removal orchestration, dependency ordering, the
filter engine, and the dry-run gate:

- `Nuke.Run()` short-circuits before any `Remove()` call when dry-run is active — this is a
  structural, testable guarantee (see `resources_test/dryrun_test.go`), not a flag threaded
  through individual delete calls.
- `Registration.DependsOn` + `Parameters.WaitOnDependencies` give topological scan ordering and
  dependency-aware removal ordering for free.
- `filter.Filters` provides `exact`/`glob`/`regex`/`contains`/`dateOlderThan` matching, shared
  and tested across every tool built on `libnuke`.

`oci-nuke` code owns only what is genuinely OCI-specific: the compartment/tenancy scope model,
OCI authentication (config file, instance principal, OKE workload identity), the tenancy gate,
and the per-service resource listers/removers themselves.

## Consequences

- `oci-nuke` inherits `libnuke`'s filter/queue/dry-run/config semantics for free, and stays
  structurally consistent with `aws-nuke`, `gcp-nuke`, and `azure-nuke` — a contributor familiar
  with any of those three tools already understands most of `oci-nuke`'s runtime model.
- `libnuke` is now a **load-bearing dependency for deletion semantics**. A version bump that
  silently changes queue ordering, dry-run behavior, or filter evaluation would be a direct
  threat to this project's core value. `renovate.json` excludes `github.com/ekristen/libnuke`
  from automerge and requires explicit dashboard approval plus a documented review checklist
  (re-run the dry-run/tenancy-gate test suite) before any bump merges — see OPS-04.
- Any OCI-specific behavior that `libnuke`'s generic model does not anticipate (the tenancy gate
  being the clearest example — none of `libnuke`'s existing consumers need a live network
  round-trip before their first destructive action) has to be layered on top in `oci-nuke`'s own
  code rather than inside the library.
