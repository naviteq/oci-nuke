# ADR-0003: Admit named resource types at the tenancy root, without making the root a target

## Status

Accepted

## Context

OCI permits a handful of resource types nowhere except the tenancy root. `DynamicGroup` is the
one this tool implements today: `identity.DynamicGroup.CompartmentId` is always the tenancy OCID,
because OCI refuses to create a dynamic group anywhere else.

The tool's scope model has no room for such a type. SAFE-06 (`pkg/scope.RefuseTenancyRoot`)
refuses the tenancy root as a target before any API call is made, so the resolved in-scope
compartment set never contains it. `scopedLister`'s fail-closed "compartment not in resolved
scope" check therefore drops every `DynamicGroup` instance a real tenancy can produce, reported
as a `ReasonOutOfScope` skip.

That was deliberate and correctly documented — `resources/dynamic_group.go`'s `GetCompartmentID`
doc comment and `CONTRIBUTING.md` §3d both spelled it out, the latter noting that an opt-in was
out of scope "since it would require weakening the tenancy gate". The consequence, though, is
that `DynamicGroup` is registered, listed and dropped, and **no configuration can delete one**.
`List`, `Filter` and `Remove` are all implemented and all unreachable.

This stopped being theoretical. `terraform-modules`' OKE test cases create tenancy-level dynamic
groups named after the cluster's `state_id`. A test run whose destroy does not finish leaves them
behind, and because their names became deterministic in `terraform-oci-oke` v2.0.0, the next run
fails outright:

```
409-IdcsConversionError ... "DynamicResourceGroup with the same displayName already exists."
```

Two nuke runs over that sandbox left the groups untouched, exactly as designed. Clearing them
needs a human with tenancy IAM rights, which is precisely the manual toil this tool exists to
remove.

## Decision

Add an explicit, type-restricted, per-run allowance: a new optional top-level config key,

```yaml
tenancy-root-types:
  - DynamicGroup
```

When it is non-empty the run gains **one additional Nuke for the tenancy root**, whose resource
type set is the intersection of the named types and the run's normally-resolved types, and
`scopedLister` admits a resource whose own compartment is the verified tenancy OCID **if and only
if** its type was named.

Rejected alternatives:

- **Add the tenancy root to the resolved in-scope set** when the key is present. One line, but it
  widens the check for every resource type at once. Any type that reports the root as its own
  compartment would then be admitted, and the fail-closed property that makes `scopedLister`
  trustworthy would hold for every compartment except the most dangerous one.
- **Relax `RefuseTenancyRoot`** so an operator can target the root directly. This is the reading
  that genuinely weakens the tenancy gate, and it would let a single `--compartment-id` delete a
  whole tenancy. Not done, and the guard is untouched by this ADR.
- **Special-case `GetCompartmentID`** to return the run's target instead of the truth. This is
  what `CONTRIBUTING.md` §3d rightly forbids: a resource that lies about where it lives defeats
  the re-verification the scope seam exists to perform.
- **Reap the groups outside the tool** with a scheduled script. Smallest change, but it puts a
  second deletion mechanism beside the first, with its own credentials, its own safety rules, and
  no plan artifact.

## Consequences

**This is a change to a documented safety property**, and by `PROJECT.md`'s own rule — "any change
that weakens dry-run, the tenancy gate or the blocklist is a breaking change regardless of
semver" — it is declared as one, even though the gate itself is not modified. `CONTRIBUTING.md`
§3d is rewritten: the honest-registration pattern stays, the claim that no opt-in exists does not.

What still holds, and is covered by tests in `pkg/ocinuke/tenancy_root_allowance_test.go`:

- `RefuseTenancyRoot` is unchanged. A `--compartment-id` naming the tenancy is still refused,
  before any API call. The allowance is not a target; it is an admission rule for resources the
  run already listed.
- The allowance is opt-in and empty by default, so every existing config behaves exactly as it did
  before this key existed.
- It matches the run's **verified** tenancy OCID exactly — never a prefix, never another tenancy's
  root, never an empty string against an empty compartment id.
- A type not named is refused at the root like any other out-of-scope resource.
- `resource-types.excludes` and `--exclude` still win: the root Nuke scans the intersection, so an
  operator can never re-admit an excluded type by naming it here.
- Protect-by-tag and min-age still apply. The allowance short-circuits the scope check only, and
  `scopedLister` evaluates safety afterwards on whatever it admitted — a `Persistent=true` dynamic
  group is still protected.
- The compartment blocklist is unaffected; it prunes the compartment tree, which never contained
  the root.
- Dry-run remains the default, and the approved-plan rescan installs the same allowance the real
  pass will use, so a verified plan cannot disagree with the run it authorises.

The residual risk is the honest one: an operator who names a type here can delete instances of
that type at the tenancy root, which no configuration could do before. That is the entire point,
it requires an explicit edit to a committed config file, and the resulting deletions appear in the
plan artifact for approval like any other.
