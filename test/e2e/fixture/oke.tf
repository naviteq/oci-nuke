# The slowest and most expensive piece of the fixture, and the reason the schedule is weekly
# rather than nightly. It is here because the node pool must be deleted before the cluster, and
# both deletions are asynchronous -- the ordering NR-680 claims to handle and has never run.
resource "oci_containerengine_cluster" "seeded" {
  # checkov:skip=CKV2_OCI_6: PodSecurityPolicy was removed in Kubernetes 1.25, so enabling it on
  # a current OKE version is at best ignored and at worst rejected at apply.
  compartment_id     = oci_identity_compartment.run.id
  kubernetes_version = local.k8s_version
  name               = "${local.name_prefix}-oke"
  vcn_id             = oci_core_vcn.main.id

  endpoint_config {
    subnet_id            = oci_core_subnet.main.id
    is_public_ip_enabled = false
    nsg_ids              = [oci_core_network_security_group.main.id]
  }

  options {
    service_lb_subnet_ids = [oci_core_subnet.main.id]
  }

  freeform_tags = local.freeform_tags

  # The grant has to exist before OKE acts on this compartment. Ordered against the cluster
  # rather than the node pool so the several minutes the cluster takes to come up double as the
  # policy's propagation window -- an OCI policy is not effective the instant the API returns.
  depends_on = [oci_identity_policy.oke_service]
}

resource "oci_containerengine_node_pool" "seeded" {
  cluster_id         = oci_containerengine_cluster.seeded.id
  compartment_id     = oci_identity_compartment.run.id
  kubernetes_version = oci_containerengine_cluster.seeded.kubernetes_version
  name               = "${local.name_prefix}-pool"
  node_shape         = var.instance_shape

  node_shape_config {
    ocpus         = var.instance_ocpus
    memory_in_gbs = var.instance_memory_gbs
  }

  node_source_details {
    # NOT data.oci_core_images.ol -- that is a platform image and OKE rejects it outright.
    # → data.tf, locals.tf
    image_id    = local.node_image_id
    source_type = "image"
  }

  node_config_details {
    size = var.node_pool_size

    # CKV2_OCI_5, and free.
    is_pv_encryption_in_transit_enabled = true

    nsg_ids = [oci_core_network_security_group.main.id]

    placement_configs {
      availability_domain = data.oci_identity_availability_domains.ads.availability_domains[0].name
      # Not oci_core_subnet.main: that one is a service-LB subnet and OKE refuses node pools in
      # one. → network.tf
      subnet_id = oci_core_subnet.nodes.id
    }
  }

  freeform_tags = local.freeform_tags
}
