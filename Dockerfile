# This image packages the binary goreleaser already built for the target
# platform; it does not compile Go. Digest pin is maintained by Renovate.
# checkov:skip=CKV_DOCKER_2: one-shot CLI container — there is no long-running process to health-check.
FROM cgr.dev/chainguard/wolfi-base:latest@sha256:2c12da5496e21e99003b3db3b9246f025e53d533c4df334205391260f1e8f85b

COPY oci-nuke /usr/local/bin/oci-nuke

# wolfi-base ships nonroot at 65532; oci-nuke needs no privileged access.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/oci-nuke"]
