# Security Policy

## Supported versions

`oci-nuke` has not cut a tagged release yet, so there is no version support matrix to publish.
Until a first release ships, only the `main` branch is supported — report issues against the
latest commit there.

## Reporting a vulnerability

Please do not open a public GitHub issue for a suspected vulnerability. Email
**security@naviteq.io** instead, with as much detail as you can provide: the affected code path,
a reproduction if you have one, and the potential impact.

The highest-priority class of report is anything that weakens this project's core safety
guarantees:

- Dry-run being bypassed without the operator explicitly requesting `--no-dry-run`.
- The tenancy gate (`GetTenancy` verification) being skippable or spoofable.
- The compartment blocklist being bypassable — by name collision, race condition, or otherwise.

Reports in this category will be treated as the most urgent, regardless of how they arrived.

## What to expect

This project is currently maintained on a best-effort, single-maintainer basis. There is no
formal SLA for acknowledgment or fix turnaround. You will get a response as soon as reasonably
possible, and will be kept informed as a report is investigated and, if confirmed, fixed.

## Scope

**In scope:** this repository's own safety model (dry-run default, tenancy gate, compartment
blocklist), its deletion logic, and its CI workflows (`.github/workflows/`).

**Out of scope:** vulnerabilities in [`ekristen/libnuke`](https://github.com/ekristen/libnuke),
[`oracle/oci-go-sdk`](https://github.com/oracle/oci-go-sdk), or Oracle Cloud Infrastructure
itself. Please report those to their respective maintainers — `libnuke` via its own repository's
security process, and OCI platform issues via
[Oracle's security reporting process](https://www.oracle.com/corporate/security-practices/assurance/vulnerability/reporting.html).
