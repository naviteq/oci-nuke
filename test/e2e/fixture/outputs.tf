# Everything the harness needs to nuke the right thing and then check emptiness through its own
# path. The verifier must never read oci-nuke's output, so it needs these identifiers from here.

output "run_compartment_id" {
  description = "The compartment oci-nuke is pointed at. Its whole subtree is in scope."
  value       = oci_identity_compartment.run.id
}

output "nested_compartment_id" {
  description = "The child compartment, deleted before its parent or the run failed."
  value       = oci_identity_compartment.nested.id
}

output "bucket_name" {
  description = "Versioned bucket, for the CLI step that adds the uncommitted multipart upload and for the emptiness check."
  value       = oci_objectstorage_bucket.seeded.name
}

output "namespace" {
  description = "Object Storage namespace, which the CLI needs on every bucket call."
  value       = data.oci_objectstorage_namespace.ns.namespace
}

output "vault_id" {
  description = "The seeded vault, or null when include_kms_vault is false. The harness asserts a scheduled-deletion leftover only when this is set."
  value       = var.include_kms_vault ? oci_kms_vault.seeded[0].id : null
}

output "expected_resource_types" {
  description = <<-EOT
    What this fixture seeds, as oci-nuke resource-type names. The harness asserts the plan covers
    every one of them before it deletes anything: a fixture that grew a resource type the tool
    does not know about would otherwise read as a clean run.
  EOT
  # Checked against `oci-nuke resource-types`, not guessed: the first draft of this list said
  # Object, OkeCluster, OkeNodePool and KmsVault, and the registry has none of those names.
  value = concat([
    "Compartment",
    # oci_identity_policy.in_scope, attached to the run compartment. Home-region-only, like
    # Compartment. NOT the OKE service policy: that one is attached to the parent compartment
    # because it grants on the run compartment, so it is outside the scanned subtree by
    # necessity and `tofu destroy` is what removes it.
    "Policy",
    "Vcn",
    "Subnet",
    "InternetGateway",
    "NatGateway",
    "RouteTable",
    "NetworkSecurityGroup",
    "Instance",
    "LoadBalancer",
    "Bucket",
    "ObjectVersion",
    "MultipartUpload",
    "Cluster",
    "NodePool",
  ], var.include_kms_vault ? ["Vault", "KmsKey"] : [])
}
