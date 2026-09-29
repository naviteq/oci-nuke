# ADR-0002: cobra over urfave/cli

## Status

Accepted

## Context

`oci-nuke` needs a CLI command tree: `run` (alias `nuke`), `resource-types`, `config validate`,
and `version`, each with its own flag set.

All three living sibling tools built on `libnuke` use `urfave/cli`, not `cobra`:

- `aws-nuke` v3 uses `urfave/cli/v3`.
- `gcp-nuke` — the structural reference this project copies its `execute()`/`ListerOpts`/
  `Prompt` shape from — also uses `urfave/cli/v3`.
- `azure-nuke` (stale, pinned to `libnuke v0.24.5`) uses the older `urfave/cli/v2`.

This is worth stating plainly: **every structural precedent `oci-nuke` is modeled on uses
`urfave/cli`.** `urfave/cli/v3`'s `cli.EnvVars(...)` source binding and flatter command struct
are a reasonable fit for `libnuke`-style, flag-heavy commands, and following it would keep
`oci-nuke` closer to upstream convention if shared tooling is ever contributed back to the
`ekristen` ecosystem.

## Decision

`oci-nuke` uses [`github.com/spf13/cobra`](https://github.com/spf13/cobra) `v1.10.2`, per the
original project ticket (NR-675). This is a **deliberate divergence** from all three sibling
tools, not an oversight — recorded here so a future reader (or a future comparison against
`aws-nuke`/`gcp-nuke`/`azure-nuke` command wiring) does not mistake it for one. The choice
reflects `cobra`'s broader ecosystem and general familiarity within the team over strict
structural alignment with the sibling tools.

## Consequences

- Every command in `oci-nuke` is translated mechanically from the `urfave/cli/v3` shape that
  `gcp-nuke` uses as its structural reference: `cmd.String("flag")` → `cmd.Flags().GetString
  ("flag")`, `cli.Command{Action: execute}` → `cobra.Command{RunE: execute}`. The flag-reading
  calls differ; command registration and the `RunE`/`Action` shape are close to 1:1.
- Any future comparison against a sibling tool's command wiring — porting a bug fix, copying a
  new command, or diffing behavior during a `libnuke` version review — requires this mechanical
  `urfave/cli` → `cobra` translation step. It is not automatic, and reviewers should expect the
  flag-parsing lines (not the command logic) to be the part that looks different.
- `oci-nuke` does not benefit from `urfave/cli/v3`'s environment-variable flag binding
  (`cli.EnvVars(...)`) out of the box; `cobra`/`pflag` requires wiring env var fallback
  explicitly per flag if that behavior is ever needed.
