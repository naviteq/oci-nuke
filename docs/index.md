# oci-nuke

`oci-nuke` deletes every resource in an Oracle Cloud Infrastructure compartment subtree. It is
for tearing down sandbox, demo and CI tenancies — the OCI counterpart to `aws-nuke`,
`gcp-nuke` and `azure-nuke`, built on [libnuke](https://github.com/ekristen/libnuke) and the
official [oci-go-sdk](https://github.com/oracle/oci-go-sdk).

!!! danger "Read this page before you read an install command"
    This tool exists to destroy things. Its one non-negotiable property is that **it must never
    delete something the operator did not intend to delete.** Coverage breadth is worth nothing
    if the safety model leaks — so the safety model is documented first, and the
    [install instructions](getting-started.md) come after it on purpose.

--8<-- "README.md:safety"

## Where to go next

- **[What cannot be deleted](limitations.md)** — platform limits no configuration changes.
- **[Getting started](getting-started.md)** — install a signed archive or the container image.
- **[Configuration](configuration.md)** — the full `oci-nuke.yaml` reference, generated from the
  same JSON Schema `config validate` uses.
- **[Resource types](resources/index.md)** — every registered type, generated from the registry.
- **[Contributing](contributing.md)** — the resource scaffold and the contribution flow.
