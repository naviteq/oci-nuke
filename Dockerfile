# This image packages the binary goreleaser already built for the target
# platform; it does not compile Go. Digest pin is maintained by Renovate.
# checkov:skip=CKV_DOCKER_2: one-shot CLI container — there is no long-running process to health-check.
FROM cgr.dev/chainguard/wolfi-base:latest@sha256:88a20e2d017bd20cdc0cb3910de603e7bea9512a6c351a8dd3c9df212ab03d73

COPY oci-nuke /usr/local/bin/oci-nuke

# wolfi-base ships nonroot at 65532; oci-nuke needs no privileged access.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/oci-nuke"]
