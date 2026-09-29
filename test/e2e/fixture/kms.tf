# Off unless asked for. A vault cannot be deleted, only scheduled for deletion 7-30 days out, so
# a scheduled run that seeds one leaves a billed vault behind every week. → see README.md
resource "oci_kms_vault" "seeded" {
  count = var.include_kms_vault ? 1 : 0

  compartment_id = oci_identity_compartment.run.id
  display_name   = "${local.name_prefix}-vault"
  vault_type     = "DEFAULT"

  freeform_tags = local.freeform_tags
}
