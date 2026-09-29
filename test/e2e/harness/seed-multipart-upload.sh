#!/usr/bin/env bash
#
# Starts a multipart upload and never commits it.
#
# The fixture cannot do this. An uncommitted upload is a transient API state rather than a
# managed object, and the OCI Terraform provider has no resource for it -- which is also why it
# is worth testing: it is the one thing in the bucket `tofu destroy` cannot clean up either, so a
# bucket left with one refuses to be deleted by anybody. `MultipartUpload` is a registered
# oci-nuke resource type, and until this harness ran it had never had one to delete.
#
#   seed-multipart-upload.sh <bucket-name>
#
# Environment: OCI_CLI_* for auth, E2E_NAMESPACE, E2E_REGION.

set -euo pipefail

die() {
  echo "::error::$*" >&2
  exit 1
}

: "${E2E_REGION:?seed-multipart-upload: E2E_REGION must be set}"
: "${E2E_NAMESPACE:?seed-multipart-upload: E2E_NAMESPACE must be set}"

bucket="${1:-}"
[[ -n "${bucket}" ]] || die "seed-multipart-upload: no bucket name given"

object="uncommitted-multipart-upload"

# `oci raw-request`, not `oci os multipart create`, because there is no such command: `oci os
# multipart` offers exactly `abort` and `list`. The CLI can transparently split a large `object
# put` into parts, but it cannot start an upload and leave it uncommitted, which is the whole
# point here. raw-request signs an arbitrary request with the configured credentials and is
# documented as JSON-only -- CreateMultipartUpload is a JSON POST, so it fits.
#
# The host is built rather than passed because raw-request takes a full URI and no --region. OC1
# realm only; a different realm would need a different suffix.
uri="https://objectstorage.${E2E_REGION}.oraclecloud.com/n/${E2E_NAMESPACE}/b/${bucket}/u"

echo "starting a multipart upload of ${object} in ${bucket}"
if ! created="$(oci raw-request \
  --http-method POST \
  --target-uri "${uri}" \
  --request-body "{\"object\":\"${object}\"}" 2>&1)"; then
  # The CLI's own output, not just "could not start the upload". An earlier version of this
  # script reported only its own sentence, and the run that failed on it said nothing about the
  # cause -- which was that the command it called does not exist.
  die "seed-multipart-upload: could not start the upload. POST ${uri} said: ${created}"
fi

# raw-request answers in camelCase (`uploadId`); `os multipart list` answers in kebab-case
# (`upload-id`). Not a typo -- they are different code paths in the CLI.
upload_id="$(printf '%s' "${created}" | jq -r '.data.uploadId // empty')"
[[ -n "${upload_id}" ]] || die "seed-multipart-upload: the create call returned no upload id: ${created}"

# Deliberately no part is uploaded, and deliberately no commit.
#
# raw-request is JSON-only, so it cannot PUT a binary part -- and it does not need to. Verified
# live against us-ashburn-1: an upload with zero parts appears in `os multipart list` (which is
# what oci-nuke's MultipartUpload lister reads), and it blocks the bucket delete on its own:
#
#	409 BucketNotEmpty: "Bucket named '<bucket>' has pending multipart uploads.
#	                     Stop all multipart uploads first."
#
# Aborting it makes the bucket deletable again. So a zero-part upload exercises exactly the same
# lister, the same Remove(), and the same bucket-blocking property a multi-part one would, and it
# transfers no bytes.
found="$(oci --region "${E2E_REGION}" os multipart list \
  --namespace-name "${E2E_NAMESPACE}" \
  --bucket-name "${bucket}" --all \
  | jq -r --arg id "${upload_id}" '[(.data // [])[] | select(.["upload-id"] == $id)] | length')"

[[ "${found}" == "1" ]] \
  || die "seed-multipart-upload: upload ${upload_id} was created but does not appear in the bucket's upload list, so the MultipartUpload assertion downstream would be testing nothing"

echo "seed-multipart-upload: upload ${upload_id} left uncommitted in ${bucket}"
