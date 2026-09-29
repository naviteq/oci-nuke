#!/usr/bin/env bash
#
# Tests for the harness's own assertion scripts, with no network and no OCI account.
#
# They exist because of what these scripts are: the things that decide whether a destructive run
# over real infrastructure passed. An assertion script that always passes is worse than no
# harness at all -- it converts "we have never proved the delete path works" into "we prove it
# whenever we label a pull request", which is a false statement that nobody re-examines.
#
# `oci` is stubbed on PATH from JSON fixtures written per case, so assert-subtree-empty.sh runs
# its real logic against controlled listings.
#
#   test/e2e/harness/harness_test.sh
#
# Run from anywhere; it locates itself.

set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

pass=0
fail=0

ok() {
  pass=$(( pass + 1 ))
  echo "  ok   $1"
}

bad() {
  fail=$(( fail + 1 ))
  echo "  FAIL $1"
  if [[ -n "${2:-}" ]]; then
    printf '       %s\n' "${2}"
  fi
}

# expect_exit runs a command and compares its exit status against 0 or non-zero. The output is
# captured and only shown on failure, so a green run stays readable.
expect_exit() {
  local want="$1" name="$2"
  shift 2
  local out rc=0
  out="$("$@" 2>&1)" || rc=$?
  case "${want}" in
    zero)
      if [[ "${rc}" -eq 0 ]]; then ok "${name}"; else bad "${name}" "exited ${rc}: ${out}"; fi ;;
    nonzero)
      if [[ "${rc}" -ne 0 ]]; then ok "${name}"; else bad "${name}" "exited 0, expected a failure. Output: ${out}"; fi ;;
    *) bad "${name}" "test bug: unknown expectation '${want}'" ;;
  esac
}

plan() {
  # plan <file> <entries-json>
  cat > "${work}/$1" <<JSON
{"body":{"schema_version":1,"entries":$2},"hash":"stub","generated_at":"2026-09-01T00:00:00Z"}
JSON
}

entry() {
  # entry <type> <state>
  printf '{"resource_type":"%s","resource_id":"ocid1.x.oc1..%s","compartment_id":"ocid1.compartment.oc1..c","region":"us-ashburn-1","state":"%s"}' \
    "$1" "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" "$2"
}

echo "plan-assert.sh"

plan full.json "[$(entry Vcn would-remove),$(entry Instance would-remove),$(entry Bucket removed)]"
expect_exit zero "covers passes when every expected type is present" \
  "${here}/plan-assert.sh" covers "${work}/full.json" "Vcn,Instance,Bucket"

expect_exit nonzero "covers fails when one expected type is absent" \
  "${here}/plan-assert.sh" covers "${work}/full.json" "Vcn,Instance,Bucket,NodePool"

# The distinction that matters most: a type the config filtered out is a type this run is not
# testing, so it must not count as covered. Counting it would let a config that excluded
# everything report full coverage.
plan filtered.json "[$(entry Vcn would-remove),$(entry Bucket filtered),$(entry Cluster skipped)]"
expect_exit nonzero "covers does not count a filtered type as covered" \
  "${here}/plan-assert.sh" covers "${work}/filtered.json" "Vcn,Bucket"
expect_exit nonzero "covers does not count a skipped type as covered" \
  "${here}/plan-assert.sh" covers "${work}/filtered.json" "Vcn,Cluster"

# An empty list of expectations would pass trivially, which is the single most likely way for
# this assertion to rot: the harness reads the list from a tofu output, and an output that
# resolved to "" must be a failure, not a pass.
expect_exit nonzero "covers refuses an empty expectation list" \
  "${here}/plan-assert.sh" covers "${work}/full.json" ""

plan empty.json "[]"
expect_exit nonzero "covers refuses a plan with no entries at all" \
  "${here}/plan-assert.sh" covers "${work}/empty.json" "Vcn"

expect_exit nonzero "covers refuses a plan file that does not exist" \
  "${here}/plan-assert.sh" covers "${work}/nope.json" "Vcn"

echo '{"not":"a plan"}' > "${work}/wrong.json"
expect_exit nonzero "covers refuses a file that is not a plan artifact" \
  "${here}/plan-assert.sh" covers "${work}/wrong.json" "Vcn"

# A JSON array straight from `tofu output -json`, brackets and quotes and all.
expect_exit zero "covers accepts a JSON array as the expectation list" \
  "${here}/plan-assert.sh" covers "${work}/full.json" '["Vcn","Instance","Bucket"]'

plan leftover.json "[$(entry Vcn removed),$(entry Cluster leftover)]"
expect_exit zero "has-leftover passes when a leftover is reported" \
  "${here}/plan-assert.sh" has-leftover "${work}/leftover.json"
expect_exit nonzero "no-leftover fails when a leftover is reported" \
  "${here}/plan-assert.sh" no-leftover "${work}/leftover.json"

plan clean.json "[$(entry Vcn removed),$(entry Cluster removed)]"
expect_exit nonzero "has-leftover fails on a clean plan" \
  "${here}/plan-assert.sh" has-leftover "${work}/clean.json"
expect_exit zero "no-leftover passes on a clean plan" \
  "${here}/plan-assert.sh" no-leftover "${work}/clean.json"

plan vault.json "[$(entry Vcn removed),$(entry Vault leftover),$(entry KmsKey leftover)]"
expect_exit zero "only-leftover passes when every leftover is whitelisted" \
  "${here}/plan-assert.sh" only-leftover "${work}/vault.json" "Vault,KmsKey"
expect_exit nonzero "only-leftover fails on a leftover outside the whitelist" \
  "${here}/plan-assert.sh" only-leftover "${work}/vault.json" "Vault"
expect_exit nonzero "only-leftover refuses an empty whitelist" \
  "${here}/plan-assert.sh" only-leftover "${work}/vault.json" ""

expect_exit nonzero "an unknown mode is a failure, not a silent pass" \
  "${here}/plan-assert.sh" definitely-not-a-mode "${work}/clean.json"

echo "assert-subtree-empty.sh"

# The stub answers every listing with the same file, which is enough: each case sets that file to
# the shape under test. `oci --version` and the region argument are accepted and ignored.
stub_dir="${work}/stub"
mkdir -p "${stub_dir}"
cat > "${stub_dir}/oci" <<'STUB'
#!/usr/bin/env bash
# Test stub. Dispatches per resource kind so a case can make one listing interesting and leave
# the rest empty -- answering every call from one file made a pending-deletion *vault* fixture
# come back as a pending-deletion instance, subnet and load balancer too, and the difference
# between those is the whole point of the vault tolerance.
#
#   OCI_STUB_RAW      -> `raw-request` (create multipart upload)
#   OCI_STUB_BUCKETS  -> `os bucket list`
#   OCI_STUB_VAULTS   -> `kms management vault list`
#   OCI_STUB_UPLOADS  -> `os multipart list`
#   OCI_STUB_VERSIONS -> `os object list-object-versions`
#   OCI_STUB_LIST     -> everything else
# An unset variable means an empty listing.
#
# Mutating calls (`abort`, `delete`) write their arguments to OCI_STUB_CALLS instead of returning
# a listing, so a test can assert what would have been destroyed rather than only that the script
# exited 0. `bucket get` succeeds unless OCI_STUB_BUCKET_GONE is set.
set -u
args=" $* "
if [[ "${args}" == *" abort "* || "${args}" == *" delete "* ]]; then
  [[ -n "${OCI_STUB_CALLS:-}" ]] && echo "$*" >> "${OCI_STUB_CALLS}"
  exit 0
fi
if [[ -n "${OCI_STUB_EXPIRED:-}" ]]; then
  echo 'ERROR: This CLI session has expired, so it cannot currently be used to run commands'
  exit 1
fi
if [[ "${args}" == *" bucket get "* ]]; then
  if [[ -n "${OCI_STUB_BUCKET_GONE:-}" ]]; then
    echo 'ServiceError: BucketNotFound' >&2
    echo 'BucketNotFound'
    exit 1
  fi
  echo '{"data":{"name":"stub"}}'
  exit 0
fi
if [[ "${args}" == *" compartment get "* ]]; then
  case "${OCI_STUB_COMPARTMENT:-active}" in
    active)  echo '{"data":{"lifecycle-state":"ACTIVE"}}'; exit 0 ;;
    deleted) echo '{"data":{"lifecycle-state":"DELETED"}}'; exit 0 ;;
    gone)    echo 'ServiceError: {"code": "NotAuthorizedOrNotFound", "status": 404}'; exit 1 ;;
    garbage) echo 'ServiceError: not JSON at all'; exit 1 ;;
    nostate) echo '{"data":{}}'; exit 0 ;;
  esac
fi
if [[ "${args}" == *" raw-request "* ]]; then
  if [[ -n "${OCI_STUB_RAW_FAILS:-}" ]]; then
    echo 'ServiceError: NotAuthorizedOrNotFound' >&2
    echo 'the stub was told to refuse this request'
    exit 1
  fi
  cat "${OCI_STUB_RAW:-/dev/null}" 2>/dev/null || echo '{}'
  exit 0
fi
if [[ "${args}" == *" list-object-versions "* ]]; then
  file="${OCI_STUB_VERSIONS:-}"
elif [[ "${args}" == *" multipart "* ]]; then
  file="${OCI_STUB_UPLOADS:-}"
elif [[ "${args}" == *" bucket "* ]]; then
  file="${OCI_STUB_BUCKETS:-}"
elif [[ "${args}" == *" vault "* ]]; then
  file="${OCI_STUB_VAULTS:-}"
else
  file="${OCI_STUB_LIST:-}"
fi
if [[ -n "${file}" && -f "${file}" ]]; then
  cat "${file}"
else
  echo '{"data":[]}'
fi
exit 0
STUB
chmod +x "${stub_dir}/oci"

export E2E_REGION="us-ashburn-1"
export E2E_NAMESPACE="stubns"
export PATH="${stub_dir}:${PATH}"

echo '{"data":[]}' > "${work}/list-empty.json"
echo '{"data":[{"id":"ocid1.instance.oc1..a","display-name":"seeded","lifecycle-state":"RUNNING"}]}' > "${work}/list-live.json"
echo '{"data":[{"id":"ocid1.instance.oc1..a","display-name":"seeded","lifecycle-state":"TERMINATED"}]}' > "${work}/list-dead.json"
echo '{"data":[{"id":"ocid1.vault.oc1..a","display-name":"seeded","lifecycle-state":"PENDING_DELETION"}]}' > "${work}/list-vault.json"
echo '{"data":[{"name":"seeded-bucket"}]}' > "${work}/buckets.json"

OCI_STUB_LIST="${work}/list-empty.json" OCI_STUB_BUCKETS="${work}/list-empty.json" \
  expect_exit zero "an empty subtree passes" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

OCI_STUB_LIST="${work}/list-live.json" OCI_STUB_BUCKETS="${work}/list-empty.json" \
  expect_exit nonzero "a running resource fails" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

# The distinction OCI forces on any verifier: a deleted resource stays addressable and keeps
# appearing in listings, so presence is not a leftover and only state is.
OCI_STUB_LIST="${work}/list-dead.json" OCI_STUB_BUCKETS="${work}/list-empty.json" \
  expect_exit zero "a TERMINATED resource is not a leftover" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

OCI_STUB_LIST="${work}/list-empty.json" OCI_STUB_BUCKETS="${work}/buckets.json" \
  expect_exit nonzero "a surviving bucket fails" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

# A vault scheduled for deletion is always reported, and fatal only when nobody asked for one.
OCI_STUB_VAULTS="${work}/list-vault.json" \
  expect_exit nonzero "a pending-deletion vault fails when no vault was seeded" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

OCI_STUB_VAULTS="${work}/list-vault.json" \
  E2E_EXPECT_VAULT_LEFTOVER=true \
  expect_exit zero "a pending-deletion vault is tolerated when one was seeded" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

# Not tolerated for anything else: the whitelist is one resource type wide, not a blanket amnesty.
OCI_STUB_LIST="${work}/list-live.json" \
  E2E_EXPECT_VAULT_LEFTOVER=true \
  expect_exit nonzero "the vault tolerance does not cover a running instance" \
  "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a

OCI_STUB_LIST="${work}/list-empty.json" OCI_STUB_BUCKETS="${work}/list-empty.json" \
  expect_exit nonzero "no compartments at all is a failure, not a trivial pass" \
  "${here}/assert-subtree-empty.sh"

OCI_STUB_LIST="${work}/list-empty.json" OCI_STUB_BUCKETS="${work}/list-empty.json" \
  expect_exit nonzero "an argument that is not a compartment OCID is a failure" \
  "${here}/assert-subtree-empty.sh" ocid1.tenancy.oc1..a

echo "empty-bucket.sh"

echo '{"data":[{"object":"uncommitted-multipart-upload","upload-id":"u-1"}]}' > "${work}/uploads.json"
echo '{"data":[{"name":"versioned.txt","version-id":"v-1"},{"name":"versioned.txt","version-id":"v-2"}]}' > "${work}/versions.json"

calls="${work}/calls.txt"
: > "${calls}"
OCI_STUB_UPLOADS="${work}/uploads.json" OCI_STUB_VERSIONS="${work}/versions.json" OCI_STUB_CALLS="${calls}" \
  expect_exit zero "empty-bucket aborts uploads and deletes versions" \
  "${here}/empty-bucket.sh" seeded-bucket

# The counts are the assertion. A script that exits 0 having called nothing would leave the
# bucket non-empty and the fallback destroy would fail exactly as it does today.
if [[ "$(grep -c 'multipart abort' "${calls}")" == "1" ]]; then
  ok "empty-bucket aborted the one upload"
else
  bad "empty-bucket aborted the one upload" "$(cat "${calls}")"
fi
# Versions, not objects: deleting the current version of a versioned object adds a delete marker
# and leaves the bucket non-empty, so both versions have to go by version id.
if [[ "$(grep -c 'version-id' "${calls}")" == "2" ]]; then
  ok "empty-bucket deleted both object versions by version id"
else
  bad "empty-bucket deleted both object versions by version id" "$(cat "${calls}")"
fi

: > "${calls}"
OCI_STUB_BUCKET_GONE=1 OCI_STUB_UPLOADS="${work}/uploads.json" OCI_STUB_CALLS="${calls}" \
  expect_exit zero "a bucket that is already gone is a success, not an error" \
  "${here}/empty-bucket.sh" seeded-bucket
if [[ ! -s "${calls}" ]]; then
  ok "nothing is deleted when the bucket is already gone"
else
  bad "nothing is deleted when the bucket is already gone" "$(cat "${calls}")"
fi

expect_exit nonzero "empty-bucket refuses an empty bucket name" \
  "${here}/empty-bucket.sh"

echo "seed-multipart-upload.sh"

echo '{"data":{"uploadId":"u-42","object":"uncommitted-multipart-upload"}}' > "${work}/raw-create.json"
echo '{"data":[{"object":"uncommitted-multipart-upload","upload-id":"u-42"}]}' > "${work}/uploads-u42.json"

OCI_STUB_RAW="${work}/raw-create.json" OCI_STUB_UPLOADS="${work}/uploads-u42.json" \
  expect_exit zero "seed-multipart-upload leaves an upload that shows up in the bucket's list" \
  "${here}/seed-multipart-upload.sh" seeded-bucket

# The verification is the point: a create that succeeded and an upload that is not listed would
# leave the MultipartUpload coverage assertion testing nothing at all, and it would pass.
OCI_STUB_RAW="${work}/raw-create.json" OCI_STUB_UPLOADS="${work}/list-empty.json" \
  expect_exit nonzero "seed-multipart-upload fails when the upload does not appear in the list" \
  "${here}/seed-multipart-upload.sh" seeded-bucket

echo '{"data":{"object":"no-id-here"}}' > "${work}/raw-noid.json"
OCI_STUB_RAW="${work}/raw-noid.json" \
  expect_exit nonzero "seed-multipart-upload fails when the create returns no upload id" \
  "${here}/seed-multipart-upload.sh" seeded-bucket

# The regression that cost a 40-minute run: the script reported only its own sentence and said
# nothing about why the CLI refused, so the log did not reveal that the command did not exist.
out="$(OCI_STUB_RAW_FAILS=1 "${here}/seed-multipart-upload.sh" seeded-bucket 2>&1)" && rc=0 || rc=$?
if [[ "${rc}" -ne 0 ]] && printf '%s' "${out}" | grep -q "the stub was told to refuse this request"; then
  ok "a failed create reports what the CLI actually said"
else
  bad "a failed create reports what the CLI actually said" "rc=${rc}: ${out}"
fi

expect_exit nonzero "seed-multipart-upload refuses an empty bucket name" \
  "${here}/seed-multipart-upload.sh"

echo "compartment-state.sh"

expect_exit zero "an ACTIVE compartment reads as ACTIVE" \
  "${here}/compartment-state.sh" ocid1.compartment.oc1..a
if [[ "$("${here}/compartment-state.sh" ocid1.compartment.oc1..a 2>/dev/null)" == "ACTIVE" ]]; then
  ok "and prints exactly that word"
else
  bad "and prints exactly that word" "$("${here}/compartment-state.sh" ocid1.compartment.oc1..a 2>&1)"
fi

if [[ "$(OCI_STUB_COMPARTMENT=deleted "${here}/compartment-state.sh" ocid1.compartment.oc1..a 2>/dev/null)" == "DELETED" ]]; then
  ok "a DELETED compartment reads as DELETED"
else
  bad "a DELETED compartment reads as DELETED" ""
fi

# The regression. A compartment the run under test successfully deleted stops being readable at
# all, because this principal's grant covers the parent's descendants and a deleted descendant is
# no longer one. Piping that straight into jq produced "parse error: Invalid numeric literal" and
# failed the step on a run where everything had worked.
if [[ "$(OCI_STUB_COMPARTMENT=gone "${here}/compartment-state.sh" ocid1.compartment.oc1..a 2>/dev/null)" == "GONE" ]]; then
  ok "an unreadable compartment reads as GONE, not as a jq parse error"
else
  bad "an unreadable compartment reads as GONE, not as a jq parse error" \
    "$(OCI_STUB_COMPARTMENT=gone "${here}/compartment-state.sh" ocid1.compartment.oc1..a 2>&1)"
fi

# An expired session must never be read as either present or gone -- both would be a guess. The
# assertion is on the dedicated branch's own words, not merely on a non-zero exit: deleting that
# branch still fails, because the generic error path quotes the CLI, so a laxer check let the
# mutation through. What matters is that the reason is named, and named before the 404 check --
# if an expiry message ever also matched that pattern, order would decide between "gone" and
# "unknown".
out="$(OCI_STUB_EXPIRED=1 "${here}/compartment-state.sh" ocid1.compartment.oc1..a 2>&1)" && rc=0 || rc=$?
if [[ "${rc}" -ne 0 ]] && printf '%s' "${out}" | grep -q "neither present nor gone"; then
  ok "an expired session is neither present nor gone"
else
  bad "an expired session is neither present nor gone" "rc=${rc}: ${out}"
fi

expect_exit nonzero "an unreadable compartment for any other reason is an error" \
  env OCI_STUB_COMPARTMENT=garbage "${here}/compartment-state.sh" ocid1.compartment.oc1..a
expect_exit nonzero "a response with no lifecycle state is an error, not an empty string" \
  env OCI_STUB_COMPARTMENT=nostate "${here}/compartment-state.sh" ocid1.compartment.oc1..a
expect_exit nonzero "compartment-state refuses an empty OCID" \
  "${here}/compartment-state.sh"

echo "expired sessions"

# An expired session is neither a leftover nor a clean tenancy, and the difference matters: the
# sweep is the only thing that decides whether a run left something behind, so a session it cannot
# authenticate with has to be loud rather than either silent or misattributed.
out="$(OCI_STUB_EXPIRED=1 "${here}/assert-subtree-empty.sh" ocid1.compartment.oc1..a 2>&1)" && rc=0 || rc=$?
if [[ "${rc}" -ne 0 ]] && printf '%s' "${out}" | grep -q "session has expired"; then
  ok "an expired session fails the sweep and says so"
else
  bad "an expired session fails the sweep and says so" "rc=${rc}: ${out}"
fi
if printf '%s' "${out}" | grep -q "UNVERIFIED"; then
  ok "the sweep says the tenancy is unverified rather than clean"
else
  bad "the sweep says the tenancy is unverified rather than clean" "${out}"
fi

# empty-bucket, by contrast, is best-effort: it warns and yields to tofu destroy rather than
# failing the cleanup step it runs inside.
: > "${calls}"
out="$(OCI_STUB_EXPIRED=1 OCI_STUB_CALLS="${calls}" "${here}/empty-bucket.sh" seeded-bucket 2>&1)" && rc=0 || rc=$?
if [[ "${rc}" -eq 0 ]] && printf '%s' "${out}" | grep -q "session has expired"; then
  ok "empty-bucket warns about an expired session instead of failing cleanup"
else
  bad "empty-bucket warns about an expired session instead of failing cleanup" "rc=${rc}: ${out}"
fi

echo
echo "${pass} passed, ${fail} failed"
[[ "${fail}" -eq 0 ]]
