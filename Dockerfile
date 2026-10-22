# This image packages the binary goreleaser already built for the target
# platform; it does not compile Go. Digest pin is maintained by Renovate.
# checkov:skip=CKV_DOCKER_2: one-shot CLI container — there is no long-running process to health-check.
FROM cgr.dev/chainguard/wolfi-base:latest@sha256:1c451d46a0d5c4e9f2b38e0e8d3e299564a1aa95c21973efcc1980a9d1d2e73e

COPY oci-nuke /usr/local/bin/oci-nuke

# wolfi-base ships nonroot at 65532; oci-nuke needs no privileged access.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/oci-nuke"]
