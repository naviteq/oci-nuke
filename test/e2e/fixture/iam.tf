# OKE cannot build a node pool with only the caller's rights. Its service principal needs its
# own grants to attach VNICs, read the instances it just created and manage the load balancers it
# creates for services -- without them the pool fails at create with an authorization error from
# a principal nobody granted anything to.
#
# The policy lives in the PARENT compartment and grants on the run compartment, because a policy
# attached to a compartment may grant on that compartment's descendants. Attaching it to the run
# compartment and having it grant on itself is the arrangement OCI is fussy about; this one is
# unambiguous. The harness principal holds `manage all-resources` in the parent, which covers
# `policies`, so the fixture creates its own grant and destroys it with everything else -- no
# standing OKE grant is left behind in the tenancy between runs.
#
# The statements are the set the `demo` compartment already runs OKE with in this tenancy, minus
# the pod-network rules: those serve the OCI_VCN_IP_NATIVE CNI and this cluster stays on the
# flannel default. Copied rather than shortened because "proven to work here" beats a guess.
resource "oci_identity_policy" "oke_service" {
  compartment_id = var.parent_compartment_id
  name           = "${local.name_prefix}-oke-service"
  description    = "OKE service-principal grants for e2e run ${var.run_id}"

  statements = [
    "Allow service oke to manage vnics in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to manage virtual-network-family in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to use subnets in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to use private-ips in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to read network-security-groups in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to read instances in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to manage load-balancers in compartment id ${oci_identity_compartment.run.id}",
    "Allow service oke to manage network-load-balancers in compartment id ${oci_identity_compartment.run.id}",
  ]

  freeform_tags = local.freeform_tags
}

# A second policy, attached to the run compartment rather than the parent, so `Policy` is
# actually inside the subtree oci-nuke is pointed at.
#
# The policy above is not, and cannot be: it grants OKE's service principal rights *on* the run
# compartment and has to be attached above it. That means the run under test never sees it, which
# is correct -- it is outside the target subtree -- but it also means the fixture claimed coverage
# of a resource type it never put in scope. This one closes that gap.
#
# The statement is a deliberate duplicate of a grant the parent policy already makes, so this
# policy widens nothing: it exists to be deleted. `Policy` is a home-region-only type in this
# tool, like `Compartment`, which is the other reason it is worth having in scope -- see the
# fixture README on why the whole thing runs in us-ashburn-1.
resource "oci_identity_policy" "in_scope" {
  compartment_id = oci_identity_compartment.run.id
  name           = "${local.name_prefix}-in-scope"
  description    = "Exists to be deleted by the run under test. Grants nothing the parent policy does not already grant."

  statements = [
    "Allow service oke to read instances in compartment id ${oci_identity_compartment.run.id}",
  ]

  freeform_tags = local.freeform_tags
}
