# Contributing to oci-nuke

This document is the end-to-end guide for adding a new resource type to `oci-nuke`. It assumes
no prior context from this project's planning history -- if you have not read any of
`.planning/phases/04-resource-framework-first-wave/`, you should still be able to follow this.

## 1. Adding a resource type

Every resource type lives in its own file under `resources/`, is registered with libnuke's
registry via exactly one call to `ocinuke.Register` inside an `init()` function, and implements
a small, fixed contract: a lister, `Filter()`, `Remove()`, `Properties()`, plus the two mandatory
accessors `GetCompartmentID()` and `UniqueKey()`.

### The one-command scaffold

`cmd/gen-resource` renders both the resource file and its test skeleton from a single invocation:

```sh
go run ./cmd/gen-resource resource \
  --name NatGateway \
  --sdk-package core --sdk-type NatGateway --client-accessor VirtualNetwork \
  --list-method ListNatGateways --list-request-field CompartmentId \
  --delete-method DeleteNatGateway --delete-id-field NatGatewayId \
  --lifecycle-field LifecycleState \
  --lifecycle-present Provisioning --lifecycle-present Available \
  --depends-on "" \
  --extra-property vcn_id=VcnId --extra-property block_traffic=BlockTraffic
```

This writes `resources/<snake_case_name>.go` and `resources/<snake_case_name>_test.go`. Flag
reference:

| Flag | Meaning |
|------|---------|
| `--name` | The Go type name, e.g. `NatGateway`. |
| `--sdk-package` | The `oci-go-sdk/v65` subpackage, e.g. `core`, `objectstorage`, `identity`. |
| `--sdk-type` | The SDK struct this type wraps, e.g. `core.NatGateway`. |
| `--client-accessor` | The `pkg/clients.Cache` method that returns the SDK client, e.g. `VirtualNetwork`. |
| `--list-method` / `--list-request-field` | The SDK's `List*` method and the compartment field on its request struct (almost always `CompartmentId`). |
| `--delete-method` / `--delete-id-field` | The SDK's `Delete*` method and the id field on its request struct. |
| `--lifecycle-field` | The struct field holding the lifecycle-state enum, e.g. `LifecycleState`. Omit only for a type with genuinely no lifecycle field -- see section 2. |
| `--lifecycle-present` | Repeatable. One value per "present" (not-yet-deleted) enum suffix, e.g. `--lifecycle-present Provisioning --lifecycle-present Available`. |
| `--depends-on` | Comma-separated resource type names this type depends on, or `""`. |
| `--extra-property` | Repeatable. `KEY=SDKField` pairs for properties beyond `baseProperties`'s fixed set (id/name/compartment_id/lifecycle_state/time_created/tags), e.g. `vcn_id=VcnId`. Reuses an existing `resources/support.go` `propX` constant automatically when the key is already shared 3+ times (see that file's own const block); otherwise emits a literal string key. |

**Where the flag values come from:** read the target OCI SDK subpackage's own generated Go
source directly -- the list/delete request-response files and the lifecycle enum block -- the
same way every domain plan in this phase did. Do not guess method/field names from other cloud
providers' SDKs or from memory; OCI's Go SDK is generated and its naming is exact but
service-specific.

**Trust the template, but verify against reality.** `cmd/gen-resource/dogfood_test.go`'s
`TestGeneratedNatGateway_MatchesLandedFile` is this generator's **only** proof of fidelity
against a real, reviewed, landed file (`resources/nat_gateway.go`) -- every other type in this
wave (`resources/*.go` for the other ~36 types) was hand-authored from this same document's prose
instead of by actually invoking the tool, because the generator (Plan 04-03) and the domain plans
that used its pattern (Plans 04-04 through 04-10) ran in the same wave, before the tool could be
depended on. If your scaffolded output looks different from a hand-authored sibling type in a way
that seems cosmetic, trust the template. If you find a **new** discrepancy between what the
generator produces and what the project's conventions actually require, do not route around it
by hand-patching your one file and moving on -- reconcile it the same way
`TestGeneratedNatGateway_MatchesLandedFile` did: fix `cmd/gen-resource/template.go.tmpl` if the
template is wrong (so every future type benefits), or fix your landed file if the template is
right and your understanding of the SDK was incomplete. Leaving the two silently diverged is
exactly the "archaeology" outcome this scaffold generator exists to prevent.

## 2. The `Filter()` contract, read twice

`Filter()` is called from **two structurally different contexts**, and the exact same method
body must correctly serve both:

- **At scan time**, a non-nil return means "never attempt removal of this resource" -- it is
  excluded from the run entirely.
- **During `HandleWait`'s post-`Remove()` polling** (libnuke re-lists the resource type and
  re-runs `Filter()` against every still-returned match), a non-nil return on a resource that is
  STILL being returned by `List()` means "this is already handled, converge now" -- libnuke marks
  the item finished rather than waiting for it to disappear from a future list call.

**The operative rule: write an allow-list of the one "present" state, never a deny-list of
terminal ones.**

```go
func (r *NatGateway) Filter() error {
	switch r.natGateway.LifecycleState {
	case core.NatGatewayLifecycleStateProvisioning, core.NatGatewayLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("NatGateway is %s, not available", r.natGateway.LifecycleState)
	}
}
```

Any state not explicitly listed as present -- including one a future SDK release adds that you
have never heard of -- excludes. This fails safe: a resource in an unknown state is treated as
"not eligible for removal this round," never as "gone."

**Get this backwards and you reintroduce Phase 3's hang trap:** if `Filter()` returns nil
unconditionally (or fails to exclude a terminal/in-flight state such as `TERMINATING`), a resource
that is still being listed after `Remove()` succeeded will never be recognized as handled.
libnuke's round-based queue loop has no external timeout -- it polls forever, and a destructive
run against a real tenancy hangs indefinitely.

If your SDK type genuinely has **no lifecycle-state-shaped field at all** (verified by reading the
SDK struct's source directly, not by assumption), `Filter()` correctly returns `nil`
unconditionally -- "gone" is entirely "absent from `List()`." `resources_test/filter_contract_test.go`
enforces this structurally: it AST-walks every `Filter()` in `resources/` and fails the build for
any unconditional `return nil` that is not on its explicitly reviewed allow-list
(`InstanceConfiguration`, `Bucket`, `ObjectVersion`, `MultipartUpload`,
`PreauthenticatedRequest`, `ReplicationPolicy`). Adding your new type to that allow-list requires
the same citation discipline those six already have -- read the underlying SDK type's source and
confirm it has no `LifecycleState`-shaped field before adding an entry.

## 3. Patterns Phase 4 never needed: Global geography, no-substitute protection, and tenancy-root gaps

Phase 5's second coverage wave (OKE/OCIR, databases, platform services, IAM, KMS) introduced
resource-authoring patterns Phase 4's 37 types never needed. Each is documented here so a future
contributor does not have to re-discover any of them from scratch.

### 3a. Authoring a `Global`-geography type

Most resource types are `Regional`: `oci-nuke` fans a scanner out across every subscribed region
and each region's scanner calls the type's `List()` independently. IAM resources (policies,
dynamic groups, tag namespaces, tag defaults) are different -- they are visible identically from
every region (reads are not home-region-gated; see `.planning/research/PITFALLS.md` Pitfall 7's
corrected wording), but write operations against them are only reliable from the tenancy's home
region, and fanning a lister out across every region would redundantly re-discover (and risk a
delete-race on) the exact same resource N times.

The fix is `pkg/ocinuke.ListerOpts.BeforeList(ocinuke.Global)`, called as the **first statement**
of `List()`, before any client is constructed:

```go
func (l *policyLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	client, err := o.Clients.Identity(o.Region)
	// ...
}
```

`BeforeList(ocinuke.Global)` returns `liberrors.ErrSkipRequest` whenever the scanner's own
`Region != HomeRegion`; libnuke's `scanner.list()` treats that as "not applicable to this scope
instance" (logged at Debug, never counted as a failure), so the type is only ever actually listed
once per run, from the home-region scanner. `resources/policy.go` is the reference example.

`cmd/gen-resource` cannot produce this shape -- it has no `--geography` flag and no template
branch for the guard. A `Global`-geography type must be hand-written (or hand-added to a
generator-scaffolded file). `resources_test/iam_global_geography_test.go`'s
`TestIAMTypesRegisteredAsGlobal_NotRegional` structurally enforces the guard's presence and
ordering for every file added to its own reviewed list -- add your new type's filename there too.

### 3b. The `AutonomousDatabase`/`DbSystem` (Oracle) no-invented-protection-mechanism gap

`MySQLDbSystem` has a real, SDK-exposed delete-protection field:
`mysql.DbSystemSummary.DeletionPolicy.IsDeleteProtected`. `resources/mysql_db_system.go`'s
`Filter()` checks it before the lifecycle-state switch and reports `scope.ReasonDeleteProtected`.

`database.AutonomousDatabaseSummary` and `database.DbSystemSummary` (the two Oracle-managed
database types, a **different** OCI service package from MySQL's) carry **no delete-protection
field at all** in the pinned `oci-go-sdk/v65@v65.123.0` -- every field on both structs was
enumerated and the whole `database` package grepped for `Protect`/`Lock` during Phase 5's
research, and neither exists. `IsBackupRetentionLocked` is a **different concept** (it bounds how
far the backup retention window can be shortened) and is **not** a delete-protection substitute --
do not read it as one.

`AutonomousDatabase.Filter()` and `DbSystem.Filter()` (Oracle) therefore have **no** protection
branch at all -- this is a documented, user-approved gap, not an oversight. For these two types,
`ocinuke.Evaluate`'s protect-by-tag and min-age settings are the operator's only protection
mechanism. If you are adding a new resource type and find its SDK summary struct has no
delete-protection-shaped field, do the same: state that plainly in the type's `Filter()` doc
comment and do not invent a substitute (an extra `Get*` call hunting for a `Locks` field, a
repurposed unrelated boolean field, etc.). If this is ever revisited, re-verify against the
then-current SDK version first -- a future OCI SDK release could add the field for real.

### 3c. Read-only residue enumeration: a leftover that is never a deletion target

Some resources are worth reporting but must never be deleted by this tool: Autonomous
Database/DbSystem/MySQLDbSystem automatic post-termination backups survive termination for
72h-95 days per Oracle's own retention policy, and `oci-nuke` deliberately never deletes them
(05-CONTEXT.md's locked "residue reporting only for now" decision).

The pattern, reference example `resources/database_backup_support.go`: a read-only-residue type is
**never** registered via `ocinuke.Register` -- it has no `Filter()`, no `Remove()`, and does not
appear in `oci-nuke resource-types`' output. Instead, its enumeration is invoked as a **side
effect from an already-registered type's `List()` method** (e.g. `AutonomousDatabase.List()` also
lists that database's backups), reporting each one through the exact same
`ocinuke.ReportLeftover`/`scope.SkipEvent` channel every other leftover reason already uses --
here with `scope.ReasonBackupResidue`, which `pkg/plan.MergeSkipEvents` folds into a distinct
`StateSkipped` entry in the plan/leftover output, `pkg/plan/leftover_test.go`- and
`resources_test/database_backup_residue_test.go`-proven never to collide with the registered
primary resource's own entry. Do not invent a parallel reporting mechanism (a second output file,
a bespoke summary field, ...) for a future read-only-residue case -- reuse this one.

### 3d. Registering a tenancy-root-only type honestly

Some OCI resource types can *only* live at the tenancy root. `DynamicGroup` is this wave's
example: OCI permits creating a dynamic group nowhere else, so `identity.DynamicGroup.CompartmentId`
is always the tenancy OCID. The resolved in-scope compartment set built by `pkg/scope`'s resolver
never contains the tenancy root itself -- SAFE-06 refuses it as a target outright -- so every
`DynamicGroup` instance a real tenancy has is, structurally, permanently out of scope.

The pattern is: **do not invent a substitute.** `DynamicGroup.GetCompartmentID()` returns exactly
what the SDK struct says (`*r.dynamicGroup.CompartmentId`), never a special-cased or synthesized
value. That is what lets the existing, unmodified `scopedLister` "compartment not in resolved
scope" fail-closed check (`pkg/ocinuke/scoped_lister.go`) drop every `DynamicGroup` instance
automatically, exactly as it would for any other out-of-scope resource -- reported as a
`ReasonOutOfScope` `SkipEvent`, visible to the operator in the leftover output rather than
silently absent. `oci-nuke resource-types` still lists `DynamicGroup` as a registered type, and
that is intentional and honest: the type is real and its `List()`/`Filter()`/`Remove()` are fully
implemented, it is simply that every instance OCI can ever produce fails the same scope check
every other type is also subject to.

Whether such a type is ever actually reachable is then the operator's decision, not the resource
author's: the `tenancy-root-types` config key names the types a run may scan and remove at the
root, and `scopedLister` admits exactly those, at exactly this run's verified tenancy OCID, after
the resolved in-scope set has already refused them. Absent or empty -- the default -- nothing is
admitted and the drop above is the whole story. This does **not** make the root a target:
`RefuseTenancyRoot` still refuses a `--compartment-id` that names the tenancy, before any API
call, and protect-by-tag, min-age, the blocklist and `resource-types.excludes` all still apply.
See `docs/adr/0003-tenancy-root-type-allowance.md` for the full safety argument and the
alternatives it rejects. Earlier revisions of this section said no such opt-in existed; that was
true when written and is the thing ADR-0003 changed.

Nothing about the pattern above changes because of it. A new tenancy-root-only type still returns
its SDK struct's own `CompartmentId`, still gets dropped by default, and becomes reachable only if
an operator names it. `resources/dynamic_group.go`'s `GetCompartmentID` doc comment is the
reference example -- read it in full before applying this pattern to a future tenancy-root-only
type.

## 4. `DependsOn` conventions

`DependsOn` is declared **by the dependent type, never by the type depended upon**. If `Bucket`
cannot be deleted while an `ObjectVersion` still exists, `Bucket`'s own registration declares
`DependsOn: []string{"ObjectVersion", ...}` -- `ObjectVersion`'s own registration never mentions
`Bucket`.

`DependsOn` entries are **bare string literals**, matching another registered type's `Name`
constant value -- never a cross-file Go import of that constant. This keeps every `resources/*.go`
file independently compilable without needing to know about its dependents' or dependencies'
package-level identifiers.

```go
DependsOn: []string{"ObjectVersion", "MultipartUpload", "PreauthenticatedRequest", "RetentionRule", "ReplicationPolicy"},
```

Two things this project's test suite enforces so a typo here does not ship silently:

- `resources_test/registration_test.go`'s `TestEveryDependsOnEntryNamesARegisteredType` fails the
  build if any `DependsOn` entry names a type that was never actually registered.
- `DependsOn` alone only orders **scanning**. Removal ordering additionally requires
  `libnuke.Parameters.WaitOnDependencies: true` at the call site that constructs the `*libnuke.Nuke`
  -- this is libnuke's own "experimental" flag, and it gates on **all** resources of the dependent
  type across the whole run, not on a specific instance-to-instance dependency. Do not assume
  declaring `DependsOn` alone is sufficient for correct removal order.

## 5. Shared helpers

Every resource type's `init()` and methods route through the same small set of shared helpers --
call them, do not reimplement their logic:

- **`resources/support.go`'s `baseProperties(...)`** builds the `id`/`compartment_id`/`name`/
  `lifecycle_state`/`time_created`/tag `Properties()` entries every type shares. Call it from your
  `Properties()` method, then chain any extra `.Set(...)` calls your type needs beyond that set
  (reuse an existing `propX` constant from the same file if the key is already used 3+ times
  elsewhere, per `goconst`'s threshold; otherwise a literal string key is fine).
- **`resources/support.go`'s `flattenDefinedTags(...)`** converts the OCI SDK's nested
  `map[string]map[string]interface{}` defined-tags shape into the flattened
  `"<namespace>.<key>"` form `ocinuke.Evaluate`'s `definedTags` parameter (and your type's own
  `SafetyTags()`, below) expects. This is a deliberately different convention from
  `baseProperties`'s own tag rendering -- do not conflate the two.
- **`SafetyTags() (freeform, defined map[string]string, createdAt time.Time)`** is the method
  every resource type must implement to satisfy `ocinuke.SafetyEvaluated` -- checked by
  `ocinuke.Register` at `init()` time, exactly like `GetCompartmentID()`/`CompartmentScoped`: a
  missing implementation panics at process start, not at runtime. `ocinuke`'s `scopedLister`
  calls it once per resource, **at scan time**, feeding the three returned values (plus
  `GetCompartmentID()`/`UniqueKey()`, both already mandatory) into `ocinuke.Evaluate` to apply
  protect-by-tag and minimum-age safety uniformly, with zero per-type opt-in risk, before the
  resource can ever become a queue.Item or appear in the plan artifact as would-remove. This
  moved here (was previously applied from inside `Remove()`, via a now-removed
  `ocinuke.EvaluateAndRemove` helper) because libnuke's `Nuke.Run()` never reaches `Remove()`
  during a dry run -- a Remove()-time-only check was invisible to the plan a dry run produces
  (see `pkg/ocinuke/scoped_lister.go`'s `SafetyEvaluated` doc comment for the full account). Your
  `Remove()` method should therefore do nothing except make the one SDK delete call; leftover
  reporting for anything other than protect-by-tag/min-age (scheduled-deletion,
  retention-locked, a FAULTY-equivalent api-error, ...) still goes through
  `ocinuke.ReportLeftover` from `Filter()`/`Remove()` as before.
- **`pkg/ocinuke.CurrentScope` / `pkg/ocinuke.CurrentReporter`** are the function-value indirection
  every real `resources/*.go` file's `init()` must pass as `ocinuke.Register`'s `inScope`/`onSkip`
  arguments -- never a locally-constructed closure. `SetRunContext` installs the real, run-scoped
  functions these two delegate to once an actual run starts; passing the indirection instead of a
  closure is what lets `init()` (which runs at process startup, before any run exists) register
  correctly regardless of when the real run context becomes available.

## 6. Testing a new type

Every resource file's generated (or hand-written) test skeleton follows the same three-test
shape, each exercised against a hand-written stub client with **zero network access**:

1. **List test** -- proves your list-and-wrap helper function returns the expected number of
   wrapped resources from a stub client's canned response, and that a spot-checked `Properties()`
   key (usually `id`) matches.
2. **Filter test** -- table-driven over every lifecycle-state value the SDK enum defines: every
   declared "present" state must return `nil`, and every other state must return non-nil. This is
   the fault-injection test that catches an inverted-polarity `Filter()` bug before it ships --
   see section 2's hang-trap warning.
3. **Remove test** -- proves the SDK delete call fires with the correct id parameter, against a
   stub client that records what it was called with rather than touching the network.

Stub clients implement the same narrow, hand-written interface your list helper depends on (e.g.
`natGatewayClient`) -- never the full OCI SDK client type. This is what makes every resource type
unit-testable without network access at all, and it is not optional polish: it is what makes the
per-type list/filter/remove test requirement possible in the first place.

### Properties() exact-key-set correctness: an explicit override

ROADMAP.md's Phase 4 success criterion 4 reads "every registered resource type has a
`Properties()` test asserting its exact property keys." The list test above only spot-checks one
key (usually `id`) -- deliberately, per this document's own convention -- so read literally, that
criterion is **not** met by 34 of the 37 types' own `_test.go` files. Phase 4 verification
(`04-VERIFICATION.md` Gap 1) caught this honestly and flagged it as a real gap, not a false
positive: `RouteTable` and `SecurityList` had **zero** `Properties()` assertions of any kind
(fixed -- both now have a dedicated `TestXxx_Properties` asserting the exact key set via
`reflect.DeepEqual` against a `types.NewProperties()...Set(...)`-built expectation, the same shape
`bucket_test.go`'s own `TestBucket_Properties` already used). The other ~32 types remain
spot-check-only in their own `_test.go` files.

**This is an accepted, formal override of the literal per-type wording, not silent
acceptance of the gap:**

```yaml
overrides:
  - must_have: "Every registered resource type has a Properties() test asserting its exact property keys"
    reason: >
      List tests spot-check one Properties() key (id) per CONTRIBUTING.md's own convention.
      Exact-key-set correctness for the wave at large is instead enforced by ONE shared test --
      tools/generate-docs/freshness_test.go's TestPropertyKeys_MatchCommittedDocs_AllRegisteredTypes
      -- which re-derives every registered type's Properties() key set FRESH from its real
      resources/*.go source (the identical go/ast extraction the doc generator itself uses) and
      diffs it against the committed docs/resources/<Type>.md for all 37 types, every test run.
      A key silently added, renamed, or removed fails this one test immediately, naming the
      type(s) affected, without requiring a hand-written exact-key assertion to have been added to
      that type's own test file in advance. RouteTable and SecurityList additionally got dedicated
      per-type tests because 04-VERIFICATION.md found those two had no Properties() assertion at
      all (a stronger gap than "spot-checked but not exhaustive"), not because the wider pattern
      needed 32 more hand-written duplicates.
    accepted_by: "AlexD"
    accepted_at: "2026-08-08"
```

The reasoning for choosing "strengthen one shared test" over "add 34 more `TestXxx_Properties`
functions": the goal ROADMAP.md's wording is actually protecting against is a `Properties()` key
silently disappearing (or a new one silently never getting documented) without anyone noticing --
not literally "one Go test function exists per type." A hand-typed exact-key list in each of 34
files is itself just as capable of drifting out of sync with its type's real `Properties()` method
as the doc it would be duplicating; the AST-based freshness check instead re-derives its
expectation from the real source on every run, so it cannot itself go stale the way a hand-written
list can. If you are adding a new resource type and want its `Properties()` correctness proven
per-type rather than relying on the shared freshness check, that is welcome (follow
`bucket_test.go`'s or `route_table_test.go`'s `TestXxx_Properties` shape) but is not required by
this convention.

## 7. Running the doc generator

After adding or changing a resource type, regenerate `docs/resources/`:

```sh
go run ./tools/generate-docs
```

This walks every type registered with the real registry and emits one `docs/resources/<Type>.md`
per type, listing its `Scope`, `DependsOn`, and the exact `Properties()` key set -- parsed back out
of your type's own source via `go/ast`, never hand-duplicated. It is deterministic (sorted output)
and safe to re-run repeatedly. `docs/resources/generated_test.go`'s coverage assertion fails the
build the moment a new registered type's documentation is missing or stale, so this is a required
step, not an optional one, for any change that adds or renames a resource type.

## 8. Releasing

Releases are automatic, driven entirely by PR titles -- there is no manual tagging step.

1. **PR titles must be Conventional Commits.** `.github/workflows/pr-lint.yml` enforces this on
   every pull request (`opened`, `edited`, `synchronize`, `reopened`); a title that does not parse
   as `type: subject` or `type(scope): subject` fails the check. This repository squash-merges, so
   the PR title becomes the merge commit's subject, and that subject is what release-please reads.
2. **The `type` decides the version bump.** `feat` bumps the minor version, `fix`/`perf` bump the
   patch version, and every other recognised type (`docs`, `chore`, `ci`, `style`, `refactor`,
   `test`, `build`, `revert`) is changelog-only or invisible and bumps nothing. A type outside that
   set produces **no** version bump at all, silently -- there is no rejection, the commit is simply
   invisible to release-please. `.github/workflows/pr-lint.yml`'s `types` input is kept identical to
   `release-please-config.json`'s `changelog-sections` list for exactly this reason; if you add a
   new Conventional Commit type to one, add it to the other in the same change.
3. **Merging to `main` maintains a release PR, it does not publish anything by itself.**
   `.github/workflows/release.yml` runs `release-please` on every push to `main`. It opens (or
   updates) a PR titled `chore(main): release vX.Y.Z` that accumulates every merged change since the
   last release into a changelog and a version bump. No tag exists yet at this point, and no
   artifacts have been built.
4. **Merging that release PR does _not_ cut the tag.** `skip-github-release: "true"` on the
   shared `create-release.yaml` call reduces release-please to maintaining the release PR and
   nothing else: it creates neither the tag nor the GitHub Release, and its `release_created` /
   `tag_name` outputs are empty as a consequence, so nothing may depend on them. What the merge
   does is land the new `version.txt`, `.release-please-manifest.json` and `CHANGELOG.md` on
   `main`.
5. **The `tag` job in `.github/workflows/release.yml` cuts the tag.** It runs on every push to
   `main`, reads `version.txt` off the commit that triggered the run, and pushes
   `refs/tags/vX.Y.Z` only when that tag does not exist yet -- so it is idempotent by
   construction and does nothing on an ordinary merge. The push uses a short-lived GitHub App
   token (via the org-level `NAVITEQ_RELEASER_ID` / `NAVITEQ_RELEASER_PRIVATE_KEY` secrets), not
   the default `GITHUB_TOKEN`. That is still the load-bearing detail it always was, only the job
   that owns it has moved: a tag pushed with `GITHUB_TOKEN` does not trigger other workflows on
   this platform, so `.github/workflows/goreleaser.yml` (`on: push: tags: v*`) would never fire
   if the tag were pushed any other way.
6. **The tag triggers `goreleaser.yml`, and goreleaser owns the Release.** It builds the
   `linux/amd64`, `linux/arm64` and `darwin/arm64` archives, signs them and the multi-arch
   `ghcr.io` image with keyless cosign, and creates the GitHub Release **as a draft**
   (`release: draft: true` in `.goreleaser.yml`), uploading the archives, `checksums.txt` and the
   signatures into that draft. The workflow then publishes it with
   `gh release edit "$GITHUB_REF_NAME" --draft=false --latest`.

   Draft, then upload, then publish is the order GitHub documents for immutable releases, and
   this org enforces immutability. Immutability is kept deliberately rather than switched off:
   a release that cannot gain content after publication is a release whose assets can be
   trusted. What it costs is that nothing may create and publish the Release before goreleaser
   has finished with it -- which is exactly what release-please used to do, and why the v1.0.3
   run failed every asset upload with `422 Cannot upload assets to an immutable release` after
   building the binaries, the signatures and the images successfully. Steps 4 and 6 are
   therefore one decision: if release-please ever starts creating the Release again, this goes
   straight back into the 422.

**While this repository is private**, all three install routes still require an authenticated
token to fetch. The release archives are not anonymously downloadable; the root `action.yml`
needs a `token` input with read access for its `gh release download`; and `docker pull` /
`docker login` need a credential with read access to the `ghcr.io` package. `go install`/`go get`
cannot read a private module at all. This is not yet a public, unauthenticated download;
NR-682 (public release) is what changes that.

### Three configuration decisions worth knowing the reasoning for

`release-please-config.json` differs from the org's own single-package template
(`naviteq/github-actions`) in three deliberate ways. JSON cannot carry comments, so the
reasoning lives here instead:

- **`"bump-minor-pre-major": true`** -- below `1.0.0`, a `feat` bumps the minor version, not the
  major. Without this, release-please's default behavior treats every `0.x` release as a major bump
  candidate, which is the wrong signal for a project that has not yet reached `1.0.0`.
- **`"bootstrap-sha": "595789bf7fbb571c5bbab92465f50014e3cb3256"`** -- anchors the first release at
  the commit that was `main`'s HEAD when this automation was added. This repository's merge history
  before that point is inconsistently Conventional (`fix:`/`chore:` alongside subjects like
  `NR-676: compartment scope resolver and safety model` that carry no recognisable type), so without
  a bootstrap SHA, release-please would walk the entire history and produce a changelog that reads
  as a fabrication of only the recognisable half of the project's actual development. The first
  release therefore covers everything merged after that commit, and nothing before it.
- **`"include-v-in-tag": true`** -- `.github/workflows/goreleaser.yml` only triggers on
  `push: tags: v*`, and `.goreleaser.yml`'s own templates already render `v{{ .Version }}` into
  archive names and image tags. These two must agree on the `v` prefix or a release-please tag would
  silently never trigger a build.

With `.release-please-manifest.json` and `version.txt` both seeded at `0.0.0`, and
`bump-minor-pre-major` in effect, **the first `feat` PR merged after the bootstrap commit produces
`v0.1.0`** -- not `v1.0.0`. That is intentional: `oci-nuke` has never performed a real deletion
outside of stubbed clients (see NR-726), and `1.0.0` would overstate that.
