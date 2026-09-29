#!/usr/bin/env bash
#
# Prints one word for a compartment's state: ACTIVE, CREATING, DELETING, DELETED, or GONE.
#
# GONE is this script's own word, for a compartment OCI will not tell us about at all. That is the
# normal end state here rather than an error: the harness principal is granted on the e2e parent
# and its descendants, and a descendant that has been deleted drops out of that grant, so
# `iam compartment get` answers NotAuthorizedOrNotFound. For a check asking "is this compartment
# gone", not being able to read it is the strongest evidence there is.
#
# It exists because the workflow did this inline and piped the CLI straight into jq. When the
# compartment really was deleted -- the run under test having done exactly what it was asked --
# the CLI printed a service error, jq said
#
#	jq: parse error: Invalid numeric literal at line 1, column 6
#
# and the step failed with exit 5 on a run where everything had worked.
#
#   compartment-state.sh <compartment-ocid>
#
# Environment: OCI_CLI_* for auth, E2E_REGION.
#
# Exit 0 with a state on stdout. Non-zero only when the compartment could not be read for a reason
# that is not "it is gone" -- an expired session, say, which must never be mistaken for either
# outcome.

set -uo pipefail

: "${E2E_REGION:?compartment-state: E2E_REGION must be set}"

compartment="${1:-}"
if [[ -z "${compartment}" ]]; then
  echo "::error::compartment-state: no compartment OCID given" >&2
  exit 1
fi

if out="$(oci --region "${E2E_REGION}" iam compartment get --compartment-id "${compartment}" 2>&1)"; then
  state="$(printf '%s' "${out}" | jq -r '.data["lifecycle-state"] // empty' 2>/dev/null)"
  if [[ -z "${state}" ]]; then
    echo "::error::compartment-state: ${compartment} returned no lifecycle state: ${out}" >&2
    exit 1
  fi
  printf '%s\n' "${state}"
  exit 0
fi

if printf '%s' "${out}" | grep -qE 'session has expired|NotAuthenticated'; then
  echo "::error::compartment-state: the OCI session has expired, so ${compartment}'s state is unknown -- neither present nor gone" >&2
  exit 1
fi

if printf '%s' "${out}" | grep -qE 'NotAuthorizedOrNotFound|"status": *404'; then
  printf 'GONE\n'
  exit 0
fi

echo "::error::compartment-state: could not read ${compartment}: ${out}" >&2
exit 1
