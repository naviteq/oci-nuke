#!/usr/bin/env bash
#
# Re-exchanges the GitHub OIDC token for a fresh OCI session and rewrites the profile that
# OpenTofu and the OCI CLI read.
#
# Called at the top of every step that talks to OCI at all -- OpenTofu and the OCI CLI alike --
# not once at the start, because a UPST is short-lived (an hour, typically) and this harness is
# not: seeding an OKE cluster and a node pool, then converging three destructive runs with a
# forty-minute wait budget each, takes about two hours.
#
# "Every step" is meant literally, and was learned the hard way. This comment previously said
# "every phase", and only the OpenTofu phases actually called it; the CLI verification steps
# inherited whatever the job had opened with. Nearly two hours in, the last of them reported
# "the OCI session has expired ... neither present nor gone" on a run where the tool under test
# had done everything correctly.
#
# A session that expires halfway through otherwise shows up as an authorization error, which reads
# like a permissions problem and is not one. Re-exchanging costs one HTTP round trip and removes
# the entire class of failure.
#
# GitHub's OIDC endpoint stays available for the whole job, so this works at any point after
# `permissions: id-token: write` has taken effect.
#
#   refresh-session.sh
#
# Environment:
#   OCI_NUKE                -- path to the built binary
#   E2E_SESSION_HOME        -- directory that plays the part of $HOME for the OCI provider
#   E2E_REGION              -- region to authenticate in
#   E2E_PROFILE             -- profile name to write (default E2E)
#   OCI_OIDC_DOMAIN_URL, OCI_OIDC_CLIENT_ID, OCI_OIDC_CLIENT_SECRET -- the confidential app

set -euo pipefail

: "${OCI_NUKE:?refresh-session: OCI_NUKE must point at the built binary}"
: "${E2E_SESSION_HOME:?refresh-session: E2E_SESSION_HOME must be set}"
: "${E2E_REGION:?refresh-session: E2E_REGION must be set}"

profile="${E2E_PROFILE:-E2E}"

# The path is not a choice. The OCI Terraform provider resolves its config file to
# <home>/.oci/config and exposes no argument and no environment variable for it -- verified in
# the provider's own GetSdkConfigProvider, which joins utils.GetHomeFolder() with ".oci/config"
# for both the profile and the SecurityToken paths. GetHomeFolder() reads TF_HOME_OVERRIDE first
# and the passwd entry second; notably NOT $HOME, so redirecting $HOME would not have worked.
# Pointing TF_HOME_OVERRIDE at a run-scoped directory is therefore the only way to hand the
# provider a session without writing into a real ~/.oci.
"${OCI_NUKE}" auth export-session \
  --auth github-oidc \
  --oidc-region "${E2E_REGION}" \
  --out-dir "${E2E_SESSION_HOME}/.oci" \
  --profile-name "${profile}"
