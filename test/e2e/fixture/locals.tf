locals {
  # Deliberately NOT Persistent=true. That tag is what production configs protect on, and a
  # fixture carrying it would be skipped by the very run that is supposed to delete it.
  freeform_tags = {
    "oci-nuke-e2e" = var.run_id
    "Expendable"   = "true"
  }

  # Names are prefixed per run for the same reason the compartments are: two runs' leftovers must
  # never be mistaken for each other.
  name_prefix = "e2e-${var.run_id}"

  # The newest version OCI offers. Named here rather than inlined because the node pool has to
  # match the cluster and the node image has to match both.
  k8s_version = data.oci_containerengine_cluster_option.oke.kubernetes_versions[
    length(data.oci_containerengine_cluster_option.oke.kubernetes_versions) - 1
  ]

  # OKE's own images for that version, keyed by name. GPU and aarch64 variants are excluded: they
  # exist for the same Kubernetes version and would be picked at random by an unfiltered match,
  # and neither can boot on VM.Standard.E4.Flex.
  #
  # `length(split(needle, haystack)) > 1` rather than the `strcontains` this obviously wants, and
  # rather than `regexall`. Checkov's HCL parser (3.3.16) does not know `strcontains` and gives up
  # on the whole file -- which does not merely lose a check, it silently stops the scanner reading
  # locals.tf at all, and the graph checks resolve locals. `regexall` would parse, but the version
  # string contains dots and `-OKE-1.36.1-` as a pattern matches more than it should. `split` takes
  # a literal separator, so this means exactly what it reads as.
  oke_node_images = {
    for src in data.oci_containerengine_node_pool_option.oke.sources :
    src.source_name => src.image_id
    if length(split("-OKE-${trimprefix(local.k8s_version, "v")}-", src.source_name)) > 1
    && length(split("GPU", src.source_name)) == 1
    && length(split("aarch64", src.source_name)) == 1
  }

  # Newest first. The build date is inside the name (`...-2026.08.14-0-OKE-1.36.1-1699`), so a
  # reverse lexicographic sort is a date sort, and taking the first is taking the newest.
  #
  # If this ever fails with an index error, the cause is that OCI offers a Kubernetes version with
  # no matching node image yet -- it happens for a few days after a new minor lands. → README.md
  node_image_id = local.oke_node_images[reverse(sort(keys(local.oke_node_images)))[0]]
}
