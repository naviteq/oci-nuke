# CI identity federation setup: OCI Identity Domains + GitHub Actions OIDC

**This document is a recommendation for whoever administers the tenancy's OCI Identity
Domain. Every object described below cannot be provisioned by this repository — creating a
Confidential Application, an Identity Propagation Trust, or Service Users all require Identity
Domain Administrator privileges that `oci-nuke`'s own code, CI workflows, and read-only research
credentials do not have and must not be given.** `github-workflows`' `oci-nuke-plan.yaml`/`oci-nuke-apply.yaml`
(`--auth github-oidc`, Plan 07-01/07-03) is written to consume the objects described here once a
domain administrator creates them; it does not create them itself, and it has not yet been
exercised against a real trust (see "What this document does not prove" at the end).

## Why this exists

`oci-nuke run --auth github-oidc` exchanges a GitHub Actions OIDC JWT for an OCI User Principal
Session Token (UPST) via OCI Identity Domains' JWT-to-UPST token exchange
(`POST https://<domainURL>/oauth2/v1/token`, `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`).
That exchange is brokered by two Identity Domain objects a domain administrator must create
out-of-band, plus ordinary OCI IAM underneath them. None of this is `oci-nuke`-specific
configuration — it is standard OCI Identity Domain administration, the same shape any other
GitHub-Actions-to-OCI federation setup would need.

## 1. Confidential Application

Create one **Confidential Application** (an OAuth client) in the Identity Domain:

- Client Credentials grant enabled — this is the only grant the token exchange needs.
- **Deliberately grant it no Identity Domain Administrator role.** Its only job is
  authenticating the token-exchange call itself (via HTTP Basic auth, client ID + client
  secret); it has no need for, and must not receive, any administrative capability over the
  domain.
- The resulting client ID and client secret become `OCI_OIDC_CLIENT_ID` and
  `OCI_OIDC_CLIENT_SECRET` in GitHub Actions secrets (see the README's "CI: GitHub Actions OIDC
  federation" section for how `oci-nuke` consumes them — env vars only, never CLI flags).

The client secret is the one static credential this whole setup stores in CI. Treat it with the
same handling rigor as any other CI secret: least-privilege environment scoping (below), a
rotation plan, and never log or print it. Its blast radius is bounded by the trust rule in the
next section — alone, it authenticates nothing against any OCI API.

## 2. Identity Propagation Trust

Create one **Identity Propagation Trust**:

| Field | Value |
|---|---|
| `type` | `JWT` |
| `issuer` | `https://token.actions.githubusercontent.com` |
| `publicKeyEndpoint` | `https://token.actions.githubusercontent.com/.well-known/jwks` |
| `oauthClients` | the Confidential Application's client ID from step 1 |
| `allowImpersonation` | `true` |

Plus two `impersonatingServiceUsers` rules mapping a GitHub Actions `sub` claim to a target
Service User (step 3). A job running under a named GitHub Environment produces
`sub = repo:ORG/REPO:environment:ENVIRONMENT-NAME`, so a plan/apply Environment split maps
directly onto a rule per Environment:

```
Rule 1: sub eq 'repo:naviteq/oci-nuke:environment:plan'   -> read-only Service User
Rule 2: sub eq 'repo:naviteq/oci-nuke:environment:apply'  -> delete-capable Service User
```

**The exact repository name and Environment names above are illustrative.** They match this
repository's own `plan_environment`/`apply_environment` inputs used as-is, but any
caller of the reusable workflow — Phase 8's `terraform-modules` migration included — needs its
own rule pointing at its own repository and Environment names, mapped to its own pair of Service
Users if it targets a different compartment scope than this repository does.

## 3. Service Users, groups, and policies

Create two ordinary OCI IAM Service Users, each in its own IAM group with a scoped policy —
nothing new to this phase's federation mechanism, standard OCI IAM underneath the trust:

- **Plan Service User** (impersonation target for the `plan` Environment's `sub` rule):
  read-only verbs (`inspect`, `read`) against the target compartment subtree only. This
  principal physically cannot delete anything, independent of any bug in `oci-nuke` itself.
- **Apply Service User** (impersonation target for the `apply` Environment's `sub` rule):
  `manage`/delete-capable verbs against the target compartment subtree only — **never
  tenancy-wide**. Scope the policy to the specific compartment OCID(s) `oci-nuke` will target,
  matching the same least-privilege posture the compartment blocklist already enforces inside
  `oci-nuke` itself.

Draft policy statements belong in each caller's own IAM configuration, scoped to that caller's
real compartment topology — this document sketches the shape, not literal, ready-to-apply policy
text, since the exact compartment OCIDs are per-caller and not something this repository's
research session had visibility into.

## 4. Audience recommendation

Request an explicit `--oidc-audience` value when invoking `oci-nuke run --auth github-oidc`,
defaulting to the Identity Domain's own URL (e.g. `https://idcs-xxxx.identity.oraclecloud.com`),
rather than leaving it unset. This mirrors the convention AWS (`sts.amazonaws.com`) and Azure
(`api://AzureADTokenExchange`) both use: an explicit, service-specific audience value on the
requested OIDC token.

This is a **defensive default, not a confirmed enforcement mechanism** — this phase's research
could not determine whether OCI's Identity Propagation Trust object exposes or enforces an `aud`
(audience) restriction at all; every primary source found describes issuer + JWKS + `sub`-rule
matching, and none discuss audience validation on OCI's side. Requesting an explicit audience
costs nothing and is easy for a domain administrator to start enforcing later without any
`oci-nuke` code change, so it is recommended regardless of whether OCI currently checks it.

## 5. What this document does not prove

**None of the objects described above have been created or exercised against a real tenancy this
phase.** A live, read-only probe (2026-08-09) confirmed the tenancy has exactly one Identity
Domain (`Default`, Free tier, `ACTIVE`, home region `us-ashburn-1`) and found no evidence one way
or the other about any existing Confidential Application or Identity Propagation Trust — the
IDCS admin API needed to check either requires credentials this research session did not have
and, per the read-only/no-mutating-calls constraint, must not request. `--auth github-oidc` is
implemented and unit-tested end-to-end against stubbed HTTP servers standing in for both GitHub's
OIDC endpoint and OCI's `/oauth2/v1/token` endpoint (Plan 07-01), and `github-workflows`' `oci-nuke-plan.yaml`/`oci-nuke-apply.yaml`
is `actionlint`-clean and invokes `oci-nuke` with exactly those flags (Plan 07-03) — but the full
chain (a real GitHub Actions job → a real JWT → a real token exchange → a real UPST →
`VerifyTenancy` → a real OCI API call) has never run end to end. Phase 8's e2e harness, once
`terraform-modules` becomes the real caller and a domain administrator has provisioned the trust
for real, is where that live proof happens — not this document, and not any Phase 7 plan's DoD.

## 6. Fork-PR warning (caller-side responsibility)

The shared workflows' declared secrets (`oidc_domain_url`/`oidc_client_id`/
`oidc_client_secret`) become reachable by whatever triggered the *calling* workflow. **Any caller
of `oci-nuke-plan.yaml` or `oci-nuke-apply.yaml` MUST NOT invoke them from a
`pull_request`-triggered job with `secrets: inherit` against untrusted forks.** Doing so would let
fork-authored code run with access to `OCI_OIDC_CLIENT_SECRET`, the one static credential this whole
setup depends on keeping confidential. No workflow YAML can enforce a caller's trigger choice; it
can only be documented — here, and in `github-workflows`' `docs/oci-nuke.md` — and enforced by
whoever configures each calling workflow's trigger.
