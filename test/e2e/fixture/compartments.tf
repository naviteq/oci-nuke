# Two levels, not one. The nested compartment is what makes deepest-first ordering observable:
# oci-nuke has to empty and delete it before it can delete its parent, and a run that got the
# order wrong would leave the parent behind with a non-empty child.

resource "oci_identity_compartment" "run" {
  # Compartments live at the tenancy level in IAM terms, but they are created *in* their parent.
  compartment_id = var.parent_compartment_id
  name           = "e2e-${var.run_id}"
  description    = "oci-nuke e2e run ${var.run_id}. Expendable: seeded to be deleted."

  # Without this, `tofu destroy` leaves the compartment behind and the next run's parent fills
  # up with dead compartments. Destroy is the fallback path for a run where the nuke did not
  # converge -- the harness still has to leave the tenancy as it found it.
  enable_delete = true

  freeform_tags = local.freeform_tags
}

resource "oci_identity_compartment" "nested" {
  compartment_id = oci_identity_compartment.run.id
  name           = "e2e-${var.run_id}-nested"
  description    = "Child of the run compartment, so child-before-parent ordering is exercised."
  enable_delete  = true

  freeform_tags = local.freeform_tags
}
