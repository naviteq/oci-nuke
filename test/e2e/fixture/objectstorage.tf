# Versioning on, and one object written twice, so the bucket holds object *versions* rather than
# just objects. A delete path that removes current objects and stops leaves a bucket that still
# refuses to be deleted, and the failure looks like an unrelated bucket error.
resource "oci_objectstorage_bucket" "seeded" {
  # checkov:skip=CKV_OCI_9: a customer-managed key needs a KMS vault, and the vault is off by
  # default on purpose -- it cannot be deleted, only scheduled. → README.md
  compartment_id = oci_identity_compartment.run.id
  namespace      = data.oci_objectstorage_namespace.ns.namespace
  name           = "${local.name_prefix}-bucket"
  versioning     = "Enabled"

  # CKV_OCI_7, and free.
  object_events_enabled = true

  freeform_tags = local.freeform_tags
}

resource "oci_objectstorage_object" "first" {
  bucket    = oci_objectstorage_bucket.seeded.name
  namespace = data.oci_objectstorage_namespace.ns.namespace
  object    = "versioned.txt"
  content   = "first version, run ${var.run_id}\n"

  # Waiting on the cluster, which has nothing to do with objects.
  #
  # The first live run created the bucket successfully and then failed the very next call with
  # `404-BucketNotFound, Either the bucket ... does not exist ... or you are not authorized to
  # access it` on PutObject. The bucket did exist; the compartment holding it was forty seconds
  # old. Object Storage's data plane authorizes against cached compartment and policy state, and
  # a policy that grants on an ancestor is not effective for a brand-new descendant the instant
  # CreateCompartment returns -- the same fact oke.tf already orders around for OKE's service
  # principal.
  #
  # The cluster takes about thirteen minutes and is created in parallel with everything else, so
  # depending on it costs no wall clock and buys a propagation window nothing else here can.
  depends_on = [oci_containerengine_cluster.seeded]
}

# A second write to the same key is what actually creates a non-current version.
resource "oci_objectstorage_object" "second" {
  bucket    = oci_objectstorage_bucket.seeded.name
  namespace = data.oci_objectstorage_namespace.ns.namespace
  object    = "versioned.txt"
  content   = "second version, run ${var.run_id}\n"

  depends_on = [oci_objectstorage_object.first]
}

# NOT seeded here: the uncommitted multipart upload the ticket calls for. The OCI Terraform
# provider has no resource for one -- an upload that is never committed is a transient API state,
# not a managed object -- so the harness creates it with the OCI CLI after this module applies.
# Written down rather than left as a silent gap, because a reader will look for it here.
