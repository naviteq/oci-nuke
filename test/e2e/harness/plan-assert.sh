#!/usr/bin/env bash
#
# Assertions over an oci-nuke plan artifact (pkg/plan.Artifact JSON).
#
# These read the tool's own output, which makes them a weaker class of check than
# assert-subtree-empty.sh -- a tool that lies about what it did would pass here. They are
# still worth having, because they catch the failure the emptiness check cannot see: a
# fixture that grew a resource type oci-nuke does not know about scans clean, deletes
# nothing, and looks like a success from the outside.
#
#   plan-assert.sh covers            <plan.json> <type>[,<type>...]
#   plan-assert.sh has-leftover      <plan.json>
#   plan-assert.sh no-leftover       <plan.json>
#   plan-assert.sh only-leftover     <plan.json> <type>[,<type>...]
#
# Every mode prints what it checked, not just whether it passed: a green line that names
# nothing is indistinguishable from a check that ran over an empty list.

set -euo pipefail

die() {
  echo "::error::$*" >&2
  exit 1
}

require_plan() {
  local plan="$1"
  [[ -n "${plan}" ]] || die "plan-assert: no plan artifact path given"
  [[ -f "${plan}" ]] || die "plan-assert: ${plan} does not exist -- the run that should have written it did not get that far"
  jq -e 'has("body") and (.body.entries | type == "array")' "${plan}" >/dev/null \
    || die "plan-assert: ${plan} is not a plan artifact (no .body.entries array)"
}

# covers asserts every named resource type appears in the plan as something the run intends
# to delete, or already deleted. `filtered` and `skipped` deliberately do not count: a type
# the config filtered out is a type this run is not testing.
mode_covers() {
  local plan="$1" wanted="${2:-}"
  require_plan "${plan}"
  [[ -n "${wanted}" ]] || die "plan-assert covers: no expected resource types given -- an empty list would pass trivially"

  local total
  total="$(jq '.body.entries | length' "${plan}")"
  [[ "${total}" -gt 0 ]] || die "plan-assert covers: ${plan} has no entries at all, so it covers nothing"

  local missing=() found=() type count
  # A comma- or whitespace-separated list either way, so the caller can pass a JSON array
  # flattened by jq -r or a hand-written string without thinking about it.
  while IFS= read -r type; do
    [[ -n "${type}" ]] || continue
    count="$(jq --arg t "${type}" \
      '[.body.entries[] | select(.resource_type == $t and (.state == "would-remove" or .state == "removed"))] | length' \
      "${plan}")"
    if [[ "${count}" -eq 0 ]]; then
      missing+=("${type}")
    else
      found+=("${type}=${count}")
    fi
  # printf with a trailing newline, not without: `read` discards a final line that has no
  # terminator, so `printf '%s'` here silently dropped the last expected type and a list of
  # four was checked as a list of three. The harness's own tests caught it.
  done < <(printf '%s\n' "${wanted}" | tr -d '"[]' | tr -s ',[:space:]' '\n')

  echo "plan-assert covers: ${#found[@]} of $(( ${#found[@]} + ${#missing[@]} )) expected types present in ${total} entries"
  local entry
  for entry in "${found[@]:-}"; do
    [[ -n "${entry}" ]] && echo "  ok   ${entry}"
  done
  for entry in "${missing[@]:-}"; do
    [[ -n "${entry}" ]] && echo "  MISS ${entry}"
  done

  if [[ "${#missing[@]}" -gt 0 ]]; then
    die "plan-assert covers: the fixture seeded ${missing[*]} and the plan does not intend to delete it. Either the resource type is not registered, or the scanner does not reach it in this compartment/region."
  fi
}

# has-leftover is the negative case's assertion. A run that was told to delete a compartment
# whose contents were excluded must report the compartment as a leftover -- not exit 0 with a
# clean sheet, which is precisely how the shell script this tool replaced used to behave.
mode_has_leftover() {
  local plan="$1"
  require_plan "${plan}"
  local count
  count="$(jq '[.body.entries[] | select(.state == "leftover")] | length' "${plan}")"
  [[ "${count}" -gt 0 ]] \
    || die "plan-assert has-leftover: ${plan} reports no leftover. The planted blocker was supposed to make convergence impossible, so either it was deleted after all or the leftover was never recorded."
  echo "plan-assert has-leftover: ${count} leftover entr$([[ "${count}" -eq 1 ]] && echo y || echo ies) reported"
  jq -r '.body.entries[] | select(.state == "leftover") | "  leftover \(.resource_type) \(.resource_id) \(.detail // "")"' "${plan}"
}

mode_no_leftover() {
  local plan="$1"
  require_plan "${plan}"
  local count
  count="$(jq '[.body.entries[] | select(.state == "leftover")] | length' "${plan}")"
  if [[ "${count}" -gt 0 ]]; then
    jq -r '.body.entries[] | select(.state == "leftover") | "  leftover \(.resource_type) \(.resource_id) \(.detail // "")"' "${plan}" >&2
    die "plan-assert no-leftover: ${plan} reports ${count} leftover entries"
  fi
  echo "plan-assert no-leftover: none reported across $(jq '.body.entries | length' "${plan}") entries"
}

# only-leftover is the KMS vault case. A vault cannot be deleted, only scheduled for deletion
# 7-30 days out, so a run that seeded one converges with the vault (and its keys) still present
# and that is the correct outcome, not a failure. Anything else left over is still a failure --
# which is why this is a whitelist rather than a blanket `|| true` on the run's exit code.
mode_only_leftover() {
  local plan="$1" allowed="${2:-}"
  require_plan "${plan}"
  [[ -n "${allowed}" ]] || die "plan-assert only-leftover: no allowed types given -- use no-leftover if nothing is allowed to remain"

  local pattern
  pattern="$(printf '%s' "${allowed}" | tr -d '"[]' | tr -s ',[:space:]' '|' | sed 's/|$//')"
  local unexpected
  unexpected="$(jq -r --arg allowed "${pattern}" '
    .body.entries[]
    | select(.state == "leftover" and ((.resource_type | test("^(" + $allowed + ")$")) | not))
    | "  leftover \(.resource_type) \(.resource_id) \(.detail // "")"' "${plan}")"

  if [[ -n "${unexpected}" ]]; then
    printf '%s\n' "${unexpected}" >&2
    die "plan-assert only-leftover: leftovers outside the allowed set (${allowed})"
  fi
  echo "plan-assert only-leftover: every leftover is one of ${allowed}"
  jq -r '.body.entries[] | select(.state == "leftover") | "  allowed leftover \(.resource_type) \(.resource_id)"' "${plan}"
}

main() {
  local mode="${1:-}"
  shift || true
  case "${mode}" in
    covers) mode_covers "${1:-}" "${2:-}" ;;
    has-leftover) mode_has_leftover "${1:-}" ;;
    no-leftover) mode_no_leftover "${1:-}" ;;
    only-leftover) mode_only_leftover "${1:-}" "${2:-}" ;;
    *) die "plan-assert: unknown mode '${mode}' (want covers|has-leftover|no-leftover)" ;;
  esac
}

main "$@"
