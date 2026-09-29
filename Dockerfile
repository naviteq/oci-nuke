# This image packages the binary goreleaser already built for the target
# platform; it does not compile Go. Digest pin is maintained by Renovate.
# checkov:skip=CKV_DOCKER_2: one-shot CLI container — there is no long-running process to health-check.
FROM cgr.dev/chainguard/wolfi-base:latest@sha256:ca263a0360cca48e8fe3f86c8af61c6d5b85e484809fe187440a4206a50efc06

COPY oci-nuke /usr/local/bin/oci-nuke

# wolfi-base ships nonroot at 65532; oci-nuke needs no privileged access.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/oci-nuke"]
