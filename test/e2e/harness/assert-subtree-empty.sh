#!/usr/bin/env bash
#
# The independent verifier. Asks OCI, through the OCI CLI, whether anything is still alive in
# a compartment subtree -- and never reads oci-nuke's output to decide.
#
# That separation is the whole point. `scripts/oci-nuke.py`, the thing this project replaced,
# never crashed either: it exited 0 over a compartment that was still full. Any check that
# reads the tool's own report inherits the tool's own blind spots, so this one uses a different
# client, a different code path and a different vocabulary.
#
#   assert-subtree-empty.sh <compartment-ocid> [<compartment-ocid> ...]
#
# Environment:
#   OCI_CLI_CONFIG_FILE, OCI_CLI_PROFILE, OCI_CLI_AUTH  -- how the CLI authenticates
#   E2E_NAMESPACE                                       -- Object Storage namespace
#   E2E_REGION                                          -- region to query
#   E2E_EXPECT_VAULT_LEFTOVER=true                      -- a scheduled-for-deletion vault is
#                                                          expected and reported, not fatal
#
# Exit 0 means every listed compartment is empty of everything below. Non-zero names what is
# left, per compartment, per resource kind.

set -euo pipefail

die() {
  echo "::error::$*" >&2
  exit 1
}

: "${E2E_REGION:?assert-subtree-empty: E2E_REGION must be set}"
: "${E2E_NAMESPACE:?assert-subtree-empty: E2E_NAMESPACE must be set}"

# The lifecycle states that mean "gone as far as OCI is concerned". OCI keeps deleted objects
# addressable for a while, so presence in a list is not by itself a leftover -- state is.
#
# SCHEDULING_DELETION and PENDING_DELETION are NOT here. A KMS vault cannot be deleted, only
# scheduled 7-30 days out, so a scheduled vault is a real, billed leftover that the operator
# has to know about, and calling it "gone" would hide exactly the fact the vault case exists
# to surface.
readonly DEAD_STATES='DELETED|TERMINATED|TERMINATING|DELETING'

leftovers=0

# A vault that is scheduled for deletion is a real leftover and always reported. Whether it is
# *fatal* depends on whether the run asked for one: the fixture's include_kms_vault is off on the
# label-triggered run and on only when somebody asks for it, and in that run a pending-deletion
# correct, documented outcome rather than a bug. Set by the harness from the same input that
# seeded the vault, so the two can never disagree.
expect_vault_leftover="${E2E_EXPECT_VAULT_LEFTOVER:-false}"

# report_alive filters a CLI listing to the entries that are not in a dead state and prints
# each one. Returns the count so the caller can accumulate.
report_alive() {
  local kind="$1" compartment="$2" json="$3"
  local alive
  alive="$(printf '%s' "${json}" | jq -r --arg dead "${DEAD_STATES}" '
    [ (.data // [])[]
      | select((."lifecycle-state" // ."lifecycle_state" // "ACTIVE") | test("^(" + $dead + ")$") | not)
    ] | .[] | "\(.["display-name"] // .name // .id) [\(.["lifecycle-state"] // "?")]"
  ')"
  if [[ -n "${alive}" ]]; then
    local n
    n="$(printf '%s\n' "${alive}" | grep -c . || true)"
    if [[ "${kind}" == "Vault" && "${expect_vault_leftover}" == "true" ]]; then
      echo "  expected ${kind} in ${compartment}: ${n} (scheduled for deletion; a vault cannot be deleted outright)"
      printf '    %s\n' "${alive}"
      return 0
    fi
    echo "  LEFTOVER ${kind} in ${compartment}: ${n}"
    printf '    %s\n' "${alive}"
    leftovers=$(( leftovers + n ))
  fi
}

# oci_list runs one CLI listing and tolerates the two failures that are not leftovers:
# a 404 (the compartment itself is gone, which is the best possible outcome) and a 401/403
# on a compartment that no longer exists. Anything else is a real error and must not be
# swallowed -- a listing that silently failed would report an empty compartment.
oci_list() {
  local out rc=0
  out="$(oci --region "${E2E_REGION}" "$@" 2>&1)" || rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    if printf '%s' "${out}" | grep -qE 'NotAuthorizedOrNotFound|"status": *404'; then
      printf '{"data":[]}'
      return 0
    fi
    # An expired session is neither a leftover nor a bug in this script, and it used to be
    # reported as "failed and it was not a 404" once per listing -- twelve near-identical errors
    # for one cause. A UPST lasts about an hour and cleanup can outlive one, so say what happened
    # and stop.
    if printf '%s' "${out}" | grep -qE 'session has expired|NotAuthenticated'; then
      die "assert-subtree-empty: the OCI session has expired, so this check could not run and the tenancy is UNVERIFIED. Refresh it (test/e2e/harness/refresh-session.sh) before reading anything into this: ${out}"
    fi
    # A usage error is this script being wrong, not the tenancy being wrong, and the two read
    # very differently when a run goes red. Named separately so nobody spends an
    # hour looking for a leftover that was never there.
    if printf '%s' "${out}" | grep -qE 'Usage:|No such command|no such option|Error: Missing option'; then
      die "assert-subtree-empty: the OCI CLI rejected 'oci $*' as a command. That is a bug in this script, not a leftover: ${out}"
    fi
    die "assert-subtree-empty: 'oci $*' failed and it was not a 404: ${out}"
  fi
  if [[ -z "${out}" ]]; then
    printf '{"data":[]}'
    return 0
  fi
  printf '%s' "${out}"
}

check_compartment() {
  local c="$1"
  echo "checking ${c}"

  # Compute, network and IAM, one listing per registered type the fixture can seed. Each is a
  # separate call on purpose: a single "list all resources" search would go through OCI's
  # Resource Search index, which is eventually consistent and would let a just-deleted
  # resource read as gone before it is.
  report_alive Instance             "${c}" "$(oci_list compute instance list --compartment-id "${c}" --all)"
  report_alive Vcn                  "${c}" "$(oci_list network vcn list --compartment-id "${c}" --all)"
  report_alive Subnet               "${c}" "$(oci_list network subnet list --compartment-id "${c}" --all)"
  report_alive InternetGateway      "${c}" "$(oci_list network internet-gateway list --compartment-id "${c}" --all)"
  report_alive NatGateway           "${c}" "$(oci_list network nat-gateway list --compartment-id "${c}" --all)"
  report_alive NetworkSecurityGroup "${c}" "$(oci_list network nsg list --compartment-id "${c}" --all)"
  report_alive LoadBalancer         "${c}" "$(oci_list lb load-balancer list --compartment-id "${c}" --all)"
  report_alive Cluster              "${c}" "$(oci_list ce cluster list --compartment-id "${c}" --all)"
  report_alive NodePool             "${c}" "$(oci_list ce node-pool list --compartment-id "${c}" --all)"
  report_alive Policy               "${c}" "$(oci_list iam policy list --compartment-id "${c}" --all)"
  report_alive Vault                "${c}" "$(oci_list kms management vault list --compartment-id "${c}" --all)"

  # Route tables and security lists: OCI creates a default of each with every VCN and refuses
  # to delete it separately, so they only exist while their VCN does. The Vcn check above
  # already covers them, and listing them needs a VCN id that may be gone.

  # Buckets are listed differently -- namespace-scoped, no lifecycle state -- and every bucket
  # found is a leftover. Objects and uncommitted multipart uploads inside one are what the
  # ObjectVersion and MultipartUpload types exist for, so a surviving bucket is reported with
  # its contents, since "bucket still there" and "bucket still full" are different bugs.
  local buckets
  buckets="$(oci_list os bucket list --compartment-id "${c}" --namespace-name "${E2E_NAMESPACE}" --all)"
  local names
  names="$(printf '%s' "${buckets}" | jq -r '(.data // [])[].name')"
  local b
  while IFS= read -r b; do
    [[ -n "${b}" ]] || continue
    echo "  LEFTOVER Bucket in ${c}: ${b}"
    leftovers=$(( leftovers + 1 ))
    local objects uploads
    objects="$(oci_list os object list --namespace-name "${E2E_NAMESPACE}" --bucket-name "${b}" --all \
      | jq -r '(.data // []) | length')"
    uploads="$(oci_list os multipart list --namespace-name "${E2E_NAMESPACE}" --bucket-name "${b}" --all \
      | jq -r '(.data // []) | length')"
    echo "    with ${objects} current object(s) and ${uploads} uncommitted multipart upload(s)"
  done <<< "${names}"
}

main() {
  [[ "$#" -gt 0 ]] || die "assert-subtree-empty: no compartments given -- an empty argument list would pass trivially"

  local c
  for c in "$@"; do
    [[ "${c}" == ocid1.compartment.* ]] || die "assert-subtree-empty: '${c}' is not a compartment OCID"
    check_compartment "${c}"
  done

  if [[ "${leftovers}" -gt 0 ]]; then
    die "assert-subtree-empty: ${leftovers} live resource(s) remain across $# compartment(s). oci-nuke reported success over a subtree that is not empty, which is the exact failure this harness exists to catch."
  fi
  echo "assert-subtree-empty: $# compartment(s) empty, verified through the OCI CLI and not through oci-nuke's own report"
}

main "$@"
