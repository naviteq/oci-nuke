# Flexible shape at its 10 Mbps floor. The classic LB is the cheapest way to seed a resource type
# whose deletion is asynchronous and whose work request has to be waited on.
resource "oci_load_balancer_load_balancer" "seeded" {
  compartment_id = oci_identity_compartment.run.id
  display_name   = "${local.name_prefix}-lb"
  shape          = "flexible"
  subnet_ids     = [oci_core_subnet.main.id]
  is_private     = true

  shape_details {
    minimum_bandwidth_in_mbps = 10
    maximum_bandwidth_in_mbps = 10
  }

  freeform_tags = local.freeform_tags
}
