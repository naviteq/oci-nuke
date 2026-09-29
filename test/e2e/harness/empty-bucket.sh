#!/usr/bin/env bash
#
# Aborts every multipart upload and deletes every object version in a bucket, so `tofu destroy`
# can delete the bucket itself.
#
# This exists because two of the things in that bucket are not Terraform's. The harness starts an
# uncommitted multipart upload with the OCI CLI, because no Terraform resource exists for one, and
# a versioned bucket keeps non-current versions that the object resources do not track. Object
# Storage refuses to delete a bucket while anything at all is in it, so without this the fallback
# `tofu destroy` fails on the bucket every time the nuke did not finish -- which is precisely when
# the fallback is the thing standing between a failed run and a leaked bucket.
#
# It runs only in cleanup, after every assertion has already been made, so it cannot mask a
# failure of the tool under test: whether oci-nuke emptied the bucket was decided by the
# assert-subtree-empty.sh run before this.
#
#   empty-bucket.sh <bucket-name>
#
# Environment: OCI_CLI_* for auth, E2E_NAMESPACE, E2E_REGION.
#
# Exits 0 when the bucket is empty or already gone. A bucket that is gone is the good case: it
# means the run under test deleted it.

set -uo pipefail

: "${E2E_REGION:?empty-bucket: E2E_REGION must be set}"
: "${E2E_NAMESPACE:?empty-bucket: E2E_NAMESPACE must be set}"

bucket="${1:-}"
if [[ -z "${bucket}" ]]; then
  echo "::error::empty-bucket: no bucket name given" >&2
  exit 1
fi

oci_os() {
  oci --region "${E2E_REGION}" os "$@" --namespace-name "${E2E_NAMESPACE}"
}

# gone reports whether the bucket is absent, which is a success and not an error worth retrying.
if ! head="$(oci_os bucket get --bucket-name "${bucket}" 2>&1)"; then
  if printf '%s' "${head}" | grep -qE 'BucketNotFound|NotAuthorizedOrNotFound|"status": *404'; then
    echo "empty-bucket: ${bucket} is already gone, which is the outcome the run was after"
    exit 0
  fi
  if printf '%s' "${head}" | grep -qE 'session has expired|NotAuthenticated'; then
    echo "::warning::empty-bucket: the OCI session has expired, so ${bucket} was not emptied and tofu destroy will fail on it with 409-BucketNotEmpty"
    exit 0
  fi
  echo "::warning::empty-bucket: could not read ${bucket}, leaving it to tofu destroy: ${head}"
  exit 0
fi

aborted=0
while IFS=$'\t' read -r object upload; do
  [[ -n "${object}" && -n "${upload}" ]] || continue
  if oci_os multipart abort --bucket-name "${bucket}" \
    --object-name "${object}" --upload-id "${upload}" --force >/dev/null 2>&1; then
    aborted=$(( aborted + 1 ))
  else
    echo "::warning::empty-bucket: could not abort upload ${upload} of ${object}"
  fi
done < <(oci_os multipart list --bucket-name "${bucket}" --all 2>/dev/null \
  | jq -r '(.data // [])[] | [.object, .["upload-id"]] | @tsv')

deleted=0
# Versions, not objects: `os object list` shows only current versions, and deleting those on a
# versioned bucket adds a delete marker instead of removing anything. The bucket stays non-empty
# and the destroy still fails, which is a confusing way to find this out.
while IFS=$'\t' read -r name version; do
  [[ -n "${name}" && -n "${version}" ]] || continue
  if oci_os object delete --bucket-name "${bucket}" \
    --object-name "${name}" --version-id "${version}" --force >/dev/null 2>&1; then
    deleted=$(( deleted + 1 ))
  else
    echo "::warning::empty-bucket: could not delete ${name} version ${version}"
  fi
done < <(oci_os object list-object-versions --bucket-name "${bucket}" --all 2>/dev/null \
  | jq -r '(.data // [])[] | [.name, .["version-id"]] | @tsv')

echo "empty-bucket: ${bucket} -- aborted ${aborted} upload(s), deleted ${deleted} object version(s)"
