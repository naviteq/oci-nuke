# In the NESTED compartment on purpose. Deepest-first is only observable if the deeper
# compartment holds something that has to be deleted before its parent can go.
resource "oci_core_instance" "seeded" {
  availability_domain = data.oci_identity_availability_domains.ads.availability_domains[0].name
  compartment_id      = oci_identity_compartment.nested.id
  display_name        = "${local.name_prefix}-instance"
  shape               = var.instance_shape

  shape_config {
    ocpus         = var.instance_ocpus
    memory_in_gbs = var.instance_memory_gbs
  }

  source_details {
    source_type = "image"
    source_id   = data.oci_core_images.ol.images[0].id
  }

  create_vnic_details {
    subnet_id        = oci_core_subnet.main.id
    assign_public_ip = false
    nsg_ids          = [oci_core_network_security_group.main.id]
  }

  # CKV_OCI_4 and CKV_OCI_5. Neither changes what this instance is for, so they are satisfied
  # rather than skipped. The setting is given twice on purpose: the top-level argument is what
  # the API takes on create, and launch_options is where CKV_OCI_4 looks for it.
  is_pv_encryption_in_transit_enabled = true

  launch_options {
    is_pv_encryption_in_transit_enabled = true

    # Not optional, and not related to encryption: OCI answers LaunchInstance with
    # `400-InvalidParameter, If LaunchOptions is provided, NetworkType must be specified` the
    # moment a launch_options block exists at all. PARAVIRTUALIZED is what a flexible VM shape
    # gets by default, so this states the default rather than changing anything -- the block is
    # only here because CKV_OCI_4 looks for the encryption flag inside it.
    network_type = "PARAVIRTUALIZED"
  }

  instance_options {
    are_legacy_imds_endpoints_disabled = true
  }

  # The boot volume outlives a terminated instance unless this is set, and a stray boot volume is
  # a leftover the harness would report. Deleting it with the instance is what a real teardown
  # does, so the fixture does not manufacture a leftover the tool is not responsible for.
  preserve_boot_volume = false

  freeform_tags = local.freeform_tags
}
