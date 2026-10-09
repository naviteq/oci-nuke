# oci-nuke

`oci-nuke` is a standalone Go CLI that deletes every resource in an Oracle Cloud
Infrastructure (OCI) compartment subtree. It targets platform and DevOps engineers who need to
tear down sandbox, demo, and CI tenancies reliably — the OCI counterpart to `aws-nuke`,
`azure-nuke`, and `gcp-nuke`.

Documentation: <https://naviteq.github.io/oci-nuke/>. Read the safety model below before anything else.

<!-- --8<-- [start:safety] -->
## Safety Model

**The tool must never delete something the operator did not intend to delete.** This is the
project's core value, and it is enforced structurally, not by convention:

- **Dry-run is the default.** Every `run` (or its alias `nuke`) reports what it *would* delete
  without deleting anything. `--no-dry-run` is the only way to make the tool actually remove a
  resource. There is no other flag, config setting, or environment variable that bypasses this.
- **The tenancy gate is a hard failure.** Before any resource is listed, `oci-nuke` makes a live
  call to the OCI Identity API (`GetTenancy`) to verify the credentials in use actually belong to
  the tenancy named in the config file. A mismatch exits non-zero immediately — the gate does not
  degrade to a warning, and it runs before the first listing call, not after.
- **The compartment blocklist is OCID-keyed, never name-keyed, and non-negotiable.** OCI
  compartment names are unique only within their parent, so matching by name is a real bypass
  vector (a differently-scoped compartment can share a name with a protected one). Every
  blocklist entry and every tenancy comparison is by OCID.
- **A destructive run requires the operator to type the target compartment's display name.**
  Before `--no-dry-run` deletes anything, `oci-nuke` prints the live-resolved display name of
  the target compartment and requires it to be typed back exactly, mirroring `aws-nuke`'s own
  account-alias confirmation. `--force` is the only way to skip this, and doing so is always
  recorded in the run output as a `force=true` log line.
- **Filters are resolved per compartment, never shared across a run.** A subtree can span many
  compartments with different protection needs; each one gets its own resolved filter set, so
  a filter configured for one compartment can never silently apply to another.

Every guarantee above is enforced by a dedicated, still-passing test:

1. Dry-run is the default — `TestRunPipeline_EmptyPlanSucceeds` (`pkg/commands/run`) exercises
   the full pipeline with `--no-dry-run` unset and asserts nothing is deleted.
2. The tenancy gate is a hard failure —
   `TestRunPipeline_TenancyMismatchBlocksBeforeListing` (`pkg/commands/run`) asserts a tenancy
   mismatch short-circuits before any compartment is listed.
3. The blocklist is OCID-keyed and pruned during traversal, never as a post-filter —
   `TestRunPipeline_BlocklistedAncestorRefused` and
   `TestRunPipeline_ResolvesSubtreeAndSkipsBlocklistedDescendant` (`pkg/commands/run`), plus
   `pkg/scope`'s own `Tree.Resolve`/`Tree.AncestorBlocklisted` unit tests.
4. The tenancy root can never be a target — `TestRunPipeline_TenancyRootTargetRefused`
   (`pkg/commands/run`).
5. Per-resource scope re-verification cannot be disabled —
   `TestRegister_PanicsWhenResourceDoesNotImplementCompartmentScoped` and
   `TestRegister_WrapsListerSoRegistryGetListerAppliesScoping` (`pkg/ocinuke`) assert every
   registered resource type is both required to implement the compartment accessor and wrapped
   by `ScopedLister` at registration time, with no opt-out.
6. `--force` usage on a destructive run is always logged, never silent —
   `TestRunPipeline_ForceIsLoggedWhenUsed` (`pkg/commands/run`) asserts a `force=true` log entry
   is emitted on every `--force` run. See "Protection filters" below for the full
   `--force`/`--force-sleep` behavior and the confirmation prompt's own full-line read (fixing a
   real whitespace-truncation bug in an earlier draft).
7. Protection filters (protect-by-tag, minimum age) are off by default and documented as
   strongly recommended — see "Protection filters" below.
8. Every skip carries a machine-readable reason (`out-of-scope`, `blocklisted`,
   `protected-by-tag`, `too-young`) — `pkg/scope.RefusalReason` / `pkg/scope.SkipEvent`.
9. Filters are resolved independently per compartment, never shared across a run —
   `TestBuildNukes_AppliesPerCompartmentFilters` (`pkg/commands/run`).

### Protection filters

**`--force` / `--force-sleep`.** A destructive run (`--no-dry-run`) normally stops and requires
the operator to type the target compartment's live display name before proceeding. `--force`
skips that prompt entirely — intended for CI, never for interactive use — and every `--force` run
logs a `force=true` line recording that the confirmation was bypassed. `--force-sleep` (default
and minimum: 3 seconds) is how long `oci-nuke` waits after a `--force` run starts before
continuing, giving a human watching CI output a last chance to cancel.

```sh
oci-nuke run --config config.yaml --compartment-id <ocid> --no-dry-run --force --force-sleep 10
```

**Protect-by-tag and minimum age.** Both are ordinary filters under the existing `filters`/
`presets` config keys — no separate schema section — using the reserved `__global__` filter key,
which applies to every resource type uniformly:

```yaml
filters:
  ocid1.compartment.oc1..demo:
    filters:
      __global__:
        - property: "freeform_tags.Persistent"
          type: exact
          value: "true"
        - property: "time-created"
          type: dateOlderThan
          value: "24h"
```

The first entry protects any resource tagged `Persistent=true` (freeform or defined tags both
work the same way). The second is the minimum-age filter, and its polarity is easy to get
backwards: **`dateOlderThan` with `value: "24h"` protects anything YOUNGER than 24 hours, not
anything older.** Worked example, straight from the filter engine's own arithmetic
(`fieldTime + duration`, then compared against now):

- A resource created **1 hour ago** against `dateOlderThan: "24h"`: `1h-ago + 24h` lands 23 hours
  in the future, which is after now — the filter matches, and the resource is **protected**.
- A resource created **30 days ago** against the same `dateOlderThan: "24h"`: `30d-ago + 24h`
  still lands 29 days in the past, which is before now — the filter does **not** match, and the
  resource is **eligible for deletion**.

In other words: `dateOlderThan: "24h"` reads as "protect anything not yet 24 hours old," not "only
delete things older than 24 hours." Get this backwards in a config and a "protect anything
deployed in the last hour" filter can silently protect nothing.

Both protections are off by default, and both apply per compartment via the same
`ResolveFilters(compartmentID)` merge every other filter goes through — see guarantee 9 above.

### Deleting the compartment itself

Deleting the target compartment itself, after everything inside it is gone, is **off by
default**. Enable it via `--delete-compartments` or `settings.compartment.delete: true` in the
config file — whichever source enables it **wins**: this is a pure union, never an exclusive
choice, so the flag can never turn *off* a config key that already turned the behavior on.

```sh
oci-nuke run --config config.yaml --compartment-id <ocid> --no-dry-run --delete-compartments
```

A compartment delete needs real time: OCI's own `DeleteCompartment` work request is asynchronous,
and OKE/load-balancer/NAT-gateway cleanup inside it can take minutes. Enabling this requires
`--max-wait-retries * <run-sleep>` to be at least 15 minutes — the default `--max-wait-retries`
(180, at the fixed 5-second run-sleep interval) already satisfies this floor; lowering it below
the floor while `--delete-compartments` is set refuses the run before any API call.

The target compartment and its whole in-scope subtree are deleted **bottom-up, deepest-first** —
every descendant compartment's own removal completes (or exhausts its retry budget) before the
next level up is even attempted, and the operator's original target is always last. This means a
deeply nested tree's total wall-clock ceiling is the **sum**, not the max, of every level's own
retry budget — each level can spend its entire budget before the next level starts, since
sibling-subtree deletion is deliberately sequential in this first implementation. Size CI or
automation timeouts accordingly for deeply nested targets: three nesting levels at the 15-minute
floor is up to 45 minutes, not 15.

A compartment is only deleted once it is actually empty. While the run is still removing things
in it, its delete waits, without calling OCI. If anything will outlive the run — a vault, key or
secret only scheduled for deletion, a protected or too-young resource, backup residue, a config
filter's catch, a child compartment that stays — it is not attempted at all and is reported as
`compartment-not-empty`, naming what holds it; the dry-run plan says so too, so nobody approves a
delete that cannot happen. If OCI refuses the delete of a compartment the run had nothing in
(something in an unscanned region, or a type `oci-nuke` does not know), that refusal is final for
the run rather than retried for the whole wait budget. Each of these is picked up again by a later
run. A compartment with a blocklisted descendant is never attempted either.

`--delete-compartments` includes the target compartment itself. To clean a parent's empty
children but keep the parent, filter the parent's own `Compartment` out by `id` under its
`filters` block.

See [`docs/adr/0001-build-on-libnuke.md`](https://github.com/naviteq/oci-nuke/blob/main/docs/adr/0001-build-on-libnuke.md) for why this
project builds on [`ekristen/libnuke`](https://github.com/ekristen/libnuke) rather than
adopting or forking an existing OCI-specific deletion script, and
[`docs/adr/0002-cobra-over-urfave-cli.md`](https://github.com/naviteq/oci-nuke/blob/main/docs/adr/0002-cobra-over-urfave-cli.md) for the CLI
framework decision.

<!-- --8<-- [end:safety] -->
<!-- --8<-- [start:limitations] -->
## What cannot be deleted

Not every resource this tool finds can actually be removed on the run that finds it. The
platform itself imposes a handful of limitations, each reported through a machine-readable
`scope.Reason*` value so an operator never has to guess why something is still there:

- **`Vault`, `KmsKey` and `VaultSecret` scheduled-deletion window** (`scope.ReasonScheduledDeletion`).
  OCI enforces a 7-30 day deletion window on vaults and keys, and a 1-30 day one on secrets, that
  a run cannot shorten. `oci-nuke` schedules the deletion and reports the resource as a leftover
  rather than treating the delay as a failure. A secret's name stays taken until its window
  ends, which is why secrets default to the one-day floor.
- **`RetentionRule` permanent lock** (`scope.ReasonRetentionLocked`). A retention rule locks
  permanently 14 days after creation and, once locked, can only be removed by deleting the
  bucket it belongs to — never directly. Because `Bucket` declares `RetentionRule` as a
  dependency, a locked rule also blocks its own bucket from being deleted this run.
- **`TagNamespace` retire-then-cascade** (no dedicated `Reason` — a two-step `Remove()`). A tag
  namespace cannot be deleted directly; it must first be retired (`IsRetired: true`), and the
  cascade-delete of every tag definition and value beneath it runs asynchronously afterward.
  `oci-nuke` performs both steps, but the cascade can still be in flight when a run ends.
- **`MySQLDbSystem` service-level delete protection** (`scope.ReasonDeleteProtected`). When
  `DeletionPolicy.IsDeleteProtected` is set on the DB system, OCI's own service refuses the
  delete — independent of any `oci-nuke` configuration, and not bypassed by `--force`.
- **`Compartment` that will not be empty** (`scope.ReasonCompartmentNotEmpty`). A compartment
  with a blocklisted descendant can never physically empty, so `oci-nuke` never attempts to delete
  it. Nor does it attempt one that still holds anything the run will leave behind — a scheduled
  deletion, a protected or too-young resource, backup residue, a child compartment that stays —
  or one whose `DeleteCompartment` OCI refused while the run had nothing of its own in it. The
  plan already shows such a compartment as skipped and names what holds it; a later run deletes
  it once that is gone.
- **Database backup residue for `AutonomousDatabase`, `DbSystem`, and `MySQLDbSystem`**
  (`scope.ReasonBackupResidue`). Terminating one of these database types leaves an automatic
  post-termination backup behind that Oracle itself retains for 72 hours to 95 days.
  `oci-nuke` enumerates these backups for visibility but never attempts to delete them — this is
  a deliberate scope decision, not a bug.

See [`docs/resources/`](https://github.com/naviteq/oci-nuke/tree/main/docs/resources/) and each type's `Filter()` doc comment in
[`resources/`](https://github.com/naviteq/oci-nuke/tree/main/resources/) for the authoritative, per-type detail behind each bullet above.

<!-- --8<-- [end:limitations] -->
## About

`oci-nuke` is built on [`ekristen/libnuke`](https://github.com/ekristen/libnuke) (MIT, the
core extracted from `aws-nuke` v3, shared with `gcp-nuke` and `azure-nuke`) plus the official
[`oracle/oci-go-sdk`](https://github.com/oracle/oci-go-sdk). It replaced a hand-rolled Python
script that Naviteq used to tear down its OCI sandboxes and CI compartments.

`oci-nuke` currently registers **58 resource types** across compute, storage, networking,
OKE/OCIR, databases, IAM, KMS and the platform services — plus the target compartment itself
behind an explicit opt-in. `oci-nuke resource-types` lists them, and [`docs/resources`](./docs/resources)
documents each one.

The delete path is not taken on trust. [`test/e2e`](./test/e2e/) seeds a
throwaway compartment subtree with real OCI resources, runs `oci-nuke --no-dry-run` over it, and
then asks OCI — through a different client, on a different code path — whether the subtree is
actually empty. It also asserts the negative: a run that cannot converge has to exit non-zero and
say what is left. The script this tool replaced never crashed either; it exited 0
over a compartment that was still full.

<!-- --8<-- [start:install] -->
## Installation

Four routes, all producing the same binary: a release archive, Distillery and a GitHub Action,
both of which install that archive for you, and a container image. None of them needs a
credential.

### 1. Release archive

Every release publishes `linux/amd64`, `linux/arm64` and `darwin/arm64` archives named
`oci-nuke-vX.Y.Z-<os>-<arch>.tar.gz`, plus a `checksums.txt` covering them. **`darwin/amd64` is
deliberately not built** — Intel macOS is not a supported target. Each archive also ships a
`.sig`/`.pem` pair produced by keyless cosign signing under the release workflow's own GitHub
OIDC identity, so an archive can be verified against Sigstore as well as against `checksums.txt`.

The binary sits at the root of the archive, alongside `README.md`, `LICENSE` and `CHANGELOG.md`
— there is no wrapping directory.

```sh
tag=v2.2.0
archive="oci-nuke-${tag}-linux-amd64.tar.gz"
base="https://github.com/naviteq/oci-nuke/releases/download/${tag}"

curl -fsSLO "${base}/${archive}"
curl -fsSLO "${base}/checksums.txt"

# Verify before extracting. On macOS use `shasum -a 256 -c -` instead of `sha256sum -c -`.
grep "  ${archive}\$" checksums.txt | sha256sum -c -

tar -xzf "${archive}" oci-nuke
sudo install -m 0755 oci-nuke /usr/local/bin/oci-nuke
oci-nuke version
```

### 2. Distillery

[Distillery](https://dist.sh) (`dist`), by the author of libnuke, installs from this
repository's releases directly. It picks the archive for the host's OS and architecture, checks
it against `checksums.txt` and its cosign signature, and links the binary into
`~/.distillery/bin`, which you add to `PATH` yourself; the installer does not.

```sh
curl --proto '=https' --tlsv1.2 -LsSf https://get.dist.sh | sh
export PATH="$HOME/.distillery/bin:$PATH"

dist install naviteq/oci-nuke            # the latest release
dist install naviteq/oci-nuke@v2.2.0     # an exact version
oci-nuke version
```

`dist` checks the signature against the certificate that ships next to it in the same release,
not against the identity the certificate was issued to. To confirm the archive was signed by this
repository's release workflow, run `cosign` on the release's `checksums.txt` once, then compare
the archive against it as in route 1:

```sh
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/naviteq/oci-nuke/\.github/workflows/goreleaser\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Intel macOS has no archive to pick, so `dist install` fails there.

### 3. GitHub Action

`action.yml` at the root of this repository is a composite action that does the download,
checksum verification and `PATH` setup above:

```yaml
- uses: naviteq/oci-nuke@v2.2.0
  with:
    # An exact vX.Y.Z tag. "latest" is rejected by name: plan and apply are separate
    # runs, and a moving ref would change the binary between them.
    version: v2.2.0
    # The release is public, but the action insists on a token; the job's own one is enough.
    token: ${{ github.token }}
```

It verifies the archive's sha256 against the release's own `checksums.txt` and will not install
an archive it cannot verify. It does **not** verify the cosign signature: cosign is absent from
GitHub-hosted runners, and installing it would be a step of its own. That is a follow-up, and
the action's description says so rather than leaving the impression the signature was checked.

### 4. Container image

```sh
docker run --rm ghcr.io/naviteq/oci-nuke:v2.2.0 version
```

The tag is a multi-arch manifest covering `linux/amd64` and `linux/arm64`, and the image is
cosign-signed by the same keyless workflow identity as the archives.

### `go install`

```sh
go install github.com/naviteq/oci-nuke@latest
```

This builds the latest commit on `main`, not a release, and `oci-nuke version` prints `dev`. A tag
cannot be selected this way: from `v2.0.0` on, Go expects the module path to end in `/v2`, and
it does not. Use one of the four routes above for a pinned version.

<!-- --8<-- [end:install] -->
## Configuration

Copy [`config.example.yaml`](./config.example.yaml), replace the placeholder OCIDs, and
validate before the first run — `config validate` issues zero OCI API calls:

```sh
cp config.example.yaml config.yaml
oci-nuke config validate --config config.yaml
```

Three keys are required:

```yaml
# The tenancy these credentials must belong to. Checked against a live GetTenancy call
# before any resource is listed; a mismatch exits non-zero immediately.
tenancy-id: ocid1.tenancy.oc1..aaaa...

# Resources are enumerated once per region. Global types (Policy, DynamicGroup,
# TagNamespace, TagDefault, Compartment) are enumerated once, in the home region only.
regions:
  - eu-frankfurt-1

# Compartments that must never be touched, by OCID — never by display name, because
# compartment names are unique only within their parent. Blocklisting a compartment
# also protects everything beneath it.
compartment-blocklist:
  - ocid1.compartment.oc1..aaaa...
```

**Do not put the tenancy root in `compartment-blocklist`.** It is the first entry most
operators reach for, and it breaks every run: every compartment descends from the root, so
a root entry makes any target refuse with `target compartment ... is blocklisted`. It buys
no protection either — the tenancy root is already rejected as a target by a separate,
non-negotiable refusal that no config key can switch off. List the compartments you
actually want to protect.

**Include the tenancy's home region in `regions` if you want IAM coverage.** `Policy`,
`DynamicGroup`, `TagNamespace`, `TagDefault` and `Compartment` are home-region-only — they
are enumerated once, in the home region, rather than once per region. If `regions` omits it,
those five types are scanned nowhere and the plan simply has nothing to say about them. The
run warns when this happens, but the config is the place to prevent it. The home region is
often not the region your credentials connect through: a tenancy homed in `us-ashburn-1` can
perfectly well be used through `eu-frankfurt-1`.

`settings` (protect-by-tag, minimum age, vault deletion window, compartment deletion) and
per-compartment `filters` are optional; `config.example.yaml` documents both inline.

**Objects carry no tags, so protect-by-tag cannot reach them.** In OCI, freeform and defined
tags exist on the bucket, never on the objects inside it. `settings.protect.tags` therefore
protects a `Bucket` and leaves every `ObjectVersion` in it listed for deletion — the bucket
survives, its contents do not, which is worse than either outcome on purpose. Objects are
excluded by filtering `ObjectVersion` on its `bucket` property instead. Tag namespaces are a
different problem again: deleting one cascades through every tag definition and value under
it, including tags on resources outside the run's compartment, so they are excluded by type
rather than scoped by filter. [`config.protect.example.yaml`](./config.protect.example.yaml)
is a worked example of all three.

## Usage

Dry-run report against a compartment (default; deletes nothing):

```sh
oci-nuke run --config config.yaml --compartment-id <compartment-ocid>
```

Actually delete the resources listed by the dry-run report above:

```sh
oci-nuke run --config config.yaml --compartment-id <compartment-ocid> --no-dry-run
```

Other commands:

```sh
oci-nuke resource-types          # list every registered resource type
oci-nuke config validate --config config.yaml   # validate a config file; issues zero OCI API calls
oci-nuke version                 # print version, commit, and build date
```

See `oci-nuke run --help` for the full flag set (`--profile`, `--auth`, `--log-level`,
`--quiet`, `--include`, `--exclude`, `--force`, `--force-sleep`, `--plan-out`,
`--max-wait-retries`, `--fail-on-leftover`, `--delete-compartments`).

`--quiet` drops the per-resource line for anything a filter excluded, which on a real
compartment is most of the scan. It changes the log and nothing else: the resource is still
scanned, still filtered, still in the queue, and still written to the `--plan-out` artifact
with its reason. Read the plan file, not the log, when the question is why something was
spared. The shared CI workflows pass it by default for exactly this reason.

## Authentication

`--auth` picks the credential source. Auto-detection is deliberately not offered for every
method, and a provider chosen with `--auth` never silently falls back to another one. Five
methods:

| Method | `--auth` value | How it authenticates |
|---|---|---|
| Config file | `config-file` (default when detectable) | Reads `~/.oci/config` (or `--profile`'s named profile). The everyday local/interactive path. |
| Instance principal | `instance-principal` | Authenticates as the OCI compute instance `oci-nuke` is running on, via IMDS. |
| OKE workload identity | `workload-identity` | Authenticates as the Kubernetes service account `oci-nuke` runs as, when running inside an OKE cluster. |
| GitHub Actions OIDC | `github-oidc` | Exchanges a GitHub Actions OIDC JWT for an OCI User Principal Session Token (UPST) via OCI Identity Domains' token exchange. **Only reachable with an explicit `--auth github-oidc`** — never auto-detected, so a GitHub Actions env var present by accident can never silently pick the wrong principal. |
| API key | `api-key` | An ordinary OCI API signing key whose parts arrive as environment variables instead of as a `~/.oci/config` profile and a key file. The CI path for anyone without an Identity Propagation Trust. **Only reachable with an explicit `--auth api-key`** — never auto-detected, for the same reason as `github-oidc`. |

Every method goes through the same tenancy gate afterward (`VerifyTenancy`, a live `GetTenancy`
round-trip against the resolved credentials) — federation does not weaken that guarantee.

### CI: GitHub Actions OIDC federation

**This documentation deliberately avoids any framing that implies every secret has been
eliminated from CI, because it has not been.** `--auth github-oidc` removes the OCI API signing
key and the long-lived OCI user credential from CI — neither one is stored anywhere. What CI
*does* store, in GitHub Actions encrypted secrets,
is a narrowly-scoped OAuth client secret (`OCI_OIDC_CLIENT_SECRET`) that authorises calling OCI
Identity Domains' token-exchange endpoint. On its own, that secret reaches no OCI API at all: the
exchange also requires a fresh, GitHub-issued OIDC JWT whose `sub` claim matches the tenancy's
configured Identity Propagation Trust. That is a genuine, load-bearing difference from AWS's
`sts:AssumeRoleWithWebIdentity`, which needs no stored secret at all — OCI's trust object
validates the GitHub JWT's own signature directly, but a second, separate credential still gates
who may even attempt the exchange. See `docs/ci-identity-federation-setup.md` for the tenancy-side
setup this property depends on.

Required flags and environment variables:

- `--oidc-region` (required) — `TokenExchangeConfigurationProvider.Region()` is not
  auto-detected; the target OCI region must be supplied explicitly.
- `--oidc-audience` (optional) — requested as the `audience` parameter when fetching the GitHub
  OIDC JWT; defaults to unset (no `audience` param sent).
- `OCI_OIDC_DOMAIN_URL`, `OCI_OIDC_CLIENT_ID`, `OCI_OIDC_CLIENT_SECRET` — environment variables
  only, **never CLI flags**. All three are credential-adjacent (a domain endpoint plus an OAuth
  client ID/secret pair); a CLI flag would put them in process listings and shell history, which
  an environment variable read once at process start does not.

### CI: API key from environment variables

`--auth github-oidc` needs a Confidential Application and an Identity Propagation Trust inside
an OCI Identity Domain, which is domain-administrator work and not something every consumer can
get done — see `docs/ci-identity-federation-setup.md` for what it involves. `--auth api-key` is
the path for everyone else: an ordinary API signing key, supplied as environment variables so
nothing has to write a `~/.oci/config` and a key file first.

This is the long-lived-key path, and it is worth being blunt about what that means. An API
signing key reaches every OCI API its user is entitled to, for as long as nobody revokes it, and
passing it through an environment variable changes none of that. Prefer `--auth github-oidc`
where the identity domain allows it. Where it does not, put the key on a service user whose IAM
policy grants delete permissions **only** inside the compartment subtree being cleaned — the
tool's own scope checks and this tool's blocklist are a second line of defence, not the first.

Six environment variables, no CLI flags of any kind:

| Variable | Required | Value |
|---|---|---|
| `OCI_CLI_TENANCY` | yes | OCID of the tenancy the key belongs to. |
| `OCI_CLI_USER` | yes | OCID of the user the key is registered against. |
| `OCI_CLI_FINGERPRINT` | yes | Fingerprint of the uploaded public key half. |
| `OCI_CLI_REGION` | yes | Region to address, e.g. `eu-frankfurt-1`. |
| `OCI_CLI_KEY_CONTENT` | yes | The private key's **own PEM bytes** — `BEGIN`/`END` lines and real newlines included. Not a path to a key file. |
| `OCI_CLI_KEY_PASSPHRASE` | no | Only for a passphrase-protected key. |

Those are the names the `oci` CLI reads and the names Oracle's own
`oracle-actions/configure-oci-credentials` exports, so a workflow already using either needs no
new plumbing. Nothing is reachable through a flag — not even the four values that are merely
identifiers rather than secrets. Splitting one credential across two input channels is how the
fifth ends up on a command line too, and `OCI_CLI_KEY_CONTENT` holds a private key.

All five required variables are checked before any OCI call, and the error names every one that
is missing at once rather than one per run. The PEM is parsed at the same point, so the single
most common failure of this path — a key flattened onto one line on its way through a CI secret
— is reported as a problem with `OCI_CLI_KEY_CONTENT` instead of surfacing later as an opaque
signing error inside the first list call.

```yaml
- uses: naviteq/oci-nuke@v2.2.0
  with:
    version: v2.2.0
    token: ${{ github.token }}

- run: oci-nuke run --config oci-nuke.yaml --compartment-id "$COMPARTMENT_ID" --auth api-key
  env:
    OCI_CLI_TENANCY: ${{ secrets.OCI_CLI_TENANCY }}
    OCI_CLI_USER: ${{ secrets.OCI_CLI_USER }}
    OCI_CLI_FINGERPRINT: ${{ secrets.OCI_CLI_FINGERPRINT }}
    OCI_CLI_REGION: eu-frankfurt-1
    OCI_CLI_KEY_CONTENT: ${{ secrets.OCI_CLI_KEY_CONTENT }}
```

Dry-run is still the default here, exactly as everywhere else: that run scans and reports, and
deletes nothing until `--no-dry-run` is added.

### Handing the session to another tool: `auth export-session`

`--auth github-oidc` exchanges a GitHub Actions OIDC token for an OCI session, and until now
only this process could use the result. Other tools in the same job cannot repeat the exchange:
the OpenTofu OCI provider's `auth` options are `ApiKey`, `SecurityToken`, `InstancePrincipal`,
`ResourcePrincipal` and `OKEWorkloadIdentity` — nothing GitHub-shaped — and the OCI CLI is the
same. The alternative was a long-lived API key for the tools that create infrastructure while
the tool that deletes it stayed keyless, which is backwards.

```sh
oci-nuke auth export-session \
  --auth github-oidc --oidc-region us-ashburn-1 \
  --out-dir "$RUNNER_TEMP/oci-session" --profile-name E2E
```

It resolves credentials through exactly the same code path as `run` — an export cannot succeed
under credentials a run would have refused — and writes three files:

| File | |
|---|---|
| `session_token` | the UPST |
| `session_key.pem` | the ephemeral key the token is bound to |
| `config` | an OCI profile naming both |

Then point anything that speaks OCI config at it:

```sh
export OCI_CLI_CONFIG_FILE="$RUNNER_TEMP/oci-session/config"
export TF_VAR_config_file_profile=E2E     # provider "oci" { auth = "SecurityToken" }
oci --config-file "$OCI_CLI_CONFIG_FILE" --profile E2E --auth security_token os ns get
```

The written profile is not a format invented here: the test for this loads it back through the
SDK's own session-token provider, which is the code path the OpenTofu provider takes for
`auth = "SecurityToken"`.

**It writes a credential to disk, which `--auth api-key` deliberately refuses to do.** The
difference is what gets written — a short-lived, exchange-scoped session token instead of a
long-lived signing key — and the files are `0600` inside a `0700` directory. Point `--out-dir` at
somewhere the runner discards, `$RUNNER_TEMP` rather than `$HOME/.oci`. The command prints paths
and an expiry, never the token.

That expiry is worth reading rather than ignoring. A UPST is short-lived, so a long job can
outlive one: run the export again before each phase rather than once at the start. The command
reports `session_expires_at` and `session_expires_in` for exactly that decision.

`--auth api-key` is refused here, and says so: its key id is tenancy/user/fingerprint and there
is no session to hand anybody. A caller on that method already has the six `OCI_CLI_*` values and
can give them to the other tool directly.

## Running against a real sandbox

Once OCI credentials exist on the operating machine (an `~/.oci/config` file, instance
principal, or OKE workload identity), the command to run against a real sandbox compartment is:

```sh
oci-nuke run --config config.yaml --compartment-id <compartment-ocid>
```

Two scans of an unchanged compartment produce the same plan hash even though their
`generated_at` timestamps differ, which is the property `--approved-plan` depends on. Pointing
credentials at a config whose `tenancy-id` does not match exits non-zero before any listing,
and the error names both OCIDs.

The dry-run and tenancy-gate guarantees are additionally proven by fake-provider tests
(`resources_test/`, `pkg/ociauth/`, `pkg/commands/run/`) against the real, unmocked
`libnuke.Nuke.Run()` control flow, so they stay checked in CI where no credentials exist.

## Plan artifact

Every `run` (or `nuke`) invocation — dry-run or destructive — writes one canonical,
versioned JSON document to `--plan-out` (default `oci-nuke-plan.json` in the current
directory) and prints the same information as a human-readable table, grouped by region and
resource type. This is deliberate, not incidental: the dry-run plan and the post-apply
leftover report are built by the **exact same writer** (`pkg/plan.BuildFromQueue` →
`pkg/plan.NewArtifact`), walking `n.Queue.GetItems()` immediately after each compartment's
`*libnuke.Nuke.Run()` call returns — never two independent code paths that could disagree
with each other or race a second `Scan()` against live, possibly-changed tenant state. See
`TestRunPipeline_SameArtifactSchemaForLeftoverReport` (`pkg/commands/run`) for the test
proving this structurally.

The artifact's top-level shape:

```json
{
  "body": {
    "schema_version": 1,
    "entries": [
      {
        "resource_type": "...",
        "resource_id": "...",
        "compartment_id": "...",
        "region": "...",
        "state": "would-remove",
        "reason": "",
        "detail": ""
      }
    ]
  },
  "hash": "<sha256 of the canonical JSON encoding of body>",
  "generated_at": "2026-08-08T00:00:00Z"
}
```

`schema_version` is bumped only on a breaking change to `Entry` or `Body`. `hash` is a
SHA-256 over `body` alone, computed after entries are sorted by (region, resource type,
compartment, resource ID) — deterministic across runs and across machines, and independent
of `generated_at` (metadata only, never part of the hashed bytes) — the value a future
approval workflow can diff a stored plan against a freshly generated one with.

Every entry's `state` is one of:

| State | Meaning |
|---|---|
| `would-remove` | A dry run scanned this resource and would delete it. |
| `removed` | A destructive run successfully removed this resource. |
| `filtered` | A configured filter excluded this resource from the run entirely. |
| `leftover` | A destructive run finished and this resource is still present. |
| `skipped` | This resource (or compartment subtree) never became a candidate at all — out-of-scope, blocklisted, or another skip reason. |

A `leftover` or `skipped` entry's `reason` is one of the thirteen `pkg/scope.RefusalReason`
values: `out-of-scope`, `blocklisted`, `protected-by-tag`, `too-young`, `api-error`,
`scheduled-deletion`, `retention-locked`, `dependency-not-satisfied`, `delete-protected`,
`backup-residue`, `compartment-not-empty`, `compartment-not-active`, `compartment-still-deleting`.
The first four are the skip reasons from "Safety Model" guarantee 8 above.
`compartment-not-empty` is reported either when a compartment's own `DeleteCompartment` work
request fails because it still contains resources (with the queue-derived blocker list — every
still-present resource's type and OCID — in `detail`) or when it will not be empty and is never
attempted (see `pkg/scope/reason.go`'s own doc comment for the full reconciliation).

The last two distinguish two things `compartment-not-empty` used to absorb:

| Reason | Means |
|---|---|
| `compartment-not-active` | The compartment was never examined, because its own `lifecycleState` is not `ACTIVE`. `detail` names the state. A non-`ACTIVE` **target** is refused outright (exit `2`) rather than reported — an empty scan must never read like a clean one. |
| `compartment-still-deleting` | The `DeleteCompartment` work request was accepted and was still `ACCEPTED`/`IN_PROGRESS` when the wait budget ran out. The compartment is not "not empty" — it is not finished. `detail` names the work request and its status. |

`detail` carries whatever the API itself said, verbatim, and the human-readable table prints it
in its own `DETAIL` column next to `REASON`. The two are separate on purpose: `reason` is a closed
vocabulary tooling can match on, `detail` is free text from OCI. Every leftover is also logged as
it happens, with both fields, as soon as its compartment finishes — not only in the table at the
end of the run.

## CI: plan -> approve -> apply

The pipeline is **not in this repository**. It lives in
[`naviteq/github-actions`](https://github.com/naviteq/github-actions) as three reusable
workflows, next to the `aws-nuke` set that has the same shape:

| Workflow | Phase |
|---|---|
| `oci-nuke-plan.yaml` | Dry run. Opens an audit issue listing the planned resource set, grouped by type, with the plan hash embedded. Deletes nothing. |
| `oci-nuke-parse.yaml` | Parses the approval comment and the issue body for a caller's `issue_comment` trampoline. |
| `oci-nuke-apply.yaml` | Re-fetches CODEOWNERS, re-verifies the plan hash, then runs destructively. |

Each workflow declares its inputs and secrets, with a description for every one, at the top of
its file under `.github/workflows/`. Read those before wiring a caller. What follows here is
about the tool's own flags and the guarantees the pipeline builds on them.

The tenancy-side setup those workflows depend on — Confidential Application, Identity Propagation
Trust, service users — is described in [`docs/ci-identity-federation-setup.md`](docs/ci-identity-federation-setup.md).

### The approval gate is an issue, not an Environment

The plan workflow opens a GitHub issue; approving means commenting exactly
`/approve oci-nuke-<plan-run-id>` on it. The caller's `issue_comment` trampoline picks that up,
and the shared apply workflow re-validates the comment, the commenter's membership in the
caller's `.github/CODEOWNERS` on the default branch, and the plan hash before it deletes
anything. An issue works on every GitHub plan, which required reviewers on an Environment do
not: not every plan offers them to a private repository.

**`.github/CODEOWNERS` is therefore load-bearing.** A handle listed there can authorise the
destruction of a compartment subtree.

### The Environments still matter

`plan_environment` and `apply_environment` remain required inputs, but they are not the gate. The
Environment name lands in the OIDC token's `sub` claim, and the tenancy's Identity Propagation Trust
matches a rule on exactly that string — so the Environment is what selects *which OCI principal the
job becomes*. The plan principal holds `read` and physically cannot delete; the apply principal holds
`manage`. Renaming an Environment silently breaks the match, so compare the trust rule against the
`sub` claim of a real token after any rename.

### `--approved-plan`, `--max-plan-age`, and the doubled scan cost

The apply job passes `--approved-plan oci-nuke-plan.json --max-plan-age 24h`. Before touching any
resource, `oci-nuke` reads the downloaded artifact, refuses if it is older than `--max-plan-age`
(zero OCI API calls spent), then runs a **second, full, forced-dry-run scan** of the identical
scope and compares its hash against the approved artifact's hash. **The apply run scans twice** —
once to verify the tenancy still matches what was approved, once to actually apply. This is not
an accidental inefficiency: `libnuke.Nuke.Run()` exposes no hook between its scan pass and its
removal pass, so a comparison trustworthy enough to gate a destructive run has to be a real
second pass through the same pipeline, not a cached value read out of the first run's artifact.
Budget CI job timeouts for roughly double a plan-only run's wall-clock and OCI API-call volume.
A hash mismatch aborts the run before any resource is removed, printing which entries diverged.

### What `--force` means here

The apply job passes `--force`. **The confirmation is meant to be moved, not skipped.** Interactive
use requires typing the target compartment's display name back before a destructive run proceeds;
unattended CI cannot do that, so `--force` substitutes two things that together satisfy the same
intent:

- the approved plan artifact — a human reviewed and approved this exact resource set on the audit
  issue, and `--approved-plan` proves the tenancy still matches it;
- GitHub's own record of who commented `/approve oci-nuke-<run-id>`, when, and that the shared apply
  workflow verified that handle against CODEOWNERS on the default branch before proceeding.

The safety check has moved from an interactive prompt to a named human approval plus a cryptographic
hash comparison. For an unattended run that is a stronger guarantee, not a weaker one.

Two things still have to hold for the paragraph above to be true, and neither is enforceable from
the workflows: `.github/CODEOWNERS` must list only people who should be able to authorise a
destructive run, and the approver must not be the person who dispatched the plan. The second is a
convention, not a check.

### The compartment blocklist requirement is not new

The apply job's config still needs a non-empty compartment blocklist, exactly like every other
`--no-dry-run` run — `oci-nuke` refuses to run at all when the config supplies no blocklist. CI
apply inherits this guarantee automatically; it is not additional code this pipeline had to add.

### Fork-PR exposure is a caller-side responsibility

The pipeline's secrets become reachable by whatever triggered the *calling* workflow. Never invoke
it from a `pull_request`-triggered job with `secrets: inherit` where forks can open PRs.

<!-- --8<-- [start:exit-codes] -->
## Exit codes

`oci-nuke run` exits one of four documented, distinct codes (`pkg/commands/run.ExitError`) —
**never exit 0 after silently swallowing an error**, the specific failure mode of the Python
script this tool replaces:

| Code | Meaning |
|---|---|
| `0` | Clean: the run completed with nothing left unresolved that `--fail-on-leftover` would care about, or `--fail-on-leftover` was not set. |
| `1` | Uncategorized internal error — the default for every error not explicitly classified below (an OCI API failure outside the safety-model gates, a plan-artifact marshaling failure, an unexplained `*libnuke.Nuke.Run()` failure with no corresponding leftover entry, ...). |
| `2` | Refused: a pre-scan safety-model gate rejected the run before any resource was ever listed — the tenancy root as a target, an empty compartment blocklist, a tenancy mismatch, an invalid or unsubscribed `regions` entry, an unknown target compartment, or a target nested under a blocklisted ancestor. |
| `3` | Unresolved leftovers, and `--fail-on-leftover` was set. |

**A leftover-containing run without `--fail-on-leftover` still exits `0`.** This is
intentional, not a bug: the plan artifact and the printed table report every leftover
unconditionally, regardless of this flag — `--fail-on-leftover` only changes whether the
process's exit code reflects that fact, so an operator or CI pipeline can opt in to treating
leftovers as fatal without losing visibility into them either way. See
`TestRunPipeline_LeftoverWithoutFailOnLeftoverExitsClean` and
`TestRunPipeline_FailOnLeftoverExitsNonZeroWithMatchingCode` (`pkg/commands/run`).

When a single run's compartments produce more than one kind of outcome — say, one
compartment refused nothing but left a resource behind while another compartment's
`*libnuke.Nuke.Run()` failed for a reason unrelated to any leftover — the worst outcome wins,
with precedence `internal (1) > leftovers (3) > clean (0)`: an unexplained internal failure
is never downgraded to a mere leftover just because `--fail-on-leftover` also happens to be
set. `pkg/commands/run.classifyOutcome` is the single function this decision is made in, and
`TestClassifyOutcome_TableDriven` proves every meaningful combination, including that exact
precedence case.

<!-- --8<-- [end:exit-codes] -->
## License

MIT — see [`LICENSE`](LICENSE).
