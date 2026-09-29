# A warning before the reasoning, because it cost a run: a wrong `parent_compartment_id` does not
# reliably fail here. Three extra characters on the OCID made both
# `oci_identity_availability_domains` and `oci_core_images` return an *empty body with no error*,
# so the apply died on `Attempt to index null value` over in compute.tf and named the wrong file.
# Only `oci_objectstorage_namespace` answered 400-InvalidCompartmentId. The harness now checks the
# compartment with one `oci iam compartment get` before Terraform runs at all; if you are running
# this module by hand, check it yourself first.
#
# Every lookup reads from the parent compartment, never the tenancy root. Availability domains,
# platform images and the Object Storage namespace are all tenancy-wide facts, and their APIs
# accept any compartment the caller can read in -- so asking the root buys nothing and costs a
# grant. The harness principal is confined to this subtree on purpose
# (naviteq/internal-infrastructure#100); a fixture that needed `read` on the root would have
# widened it straight back out just to fetch an image list.

data "oci_identity_availability_domains" "ads" {
  compartment_id = var.parent_compartment_id
}

# Filtered to the shape, because an image that the shape cannot boot is a run that fails in the
# fixture instead of in the code under test.
data "oci_core_images" "ol" {
  compartment_id           = var.parent_compartment_id
  operating_system         = "Oracle Linux"
  operating_system_version = "9"
  shape                    = var.instance_shape
  sort_by                  = "TIMECREATED"
  sort_order               = "DESC"
}

data "oci_objectstorage_namespace" "ns" {
  compartment_id = var.parent_compartment_id
}

data "oci_containerengine_cluster_option" "oke" {
  cluster_option_id = "all"
}

# An OKE node pool will not boot a platform image. `data.oci_core_images.ol` above is the right
# source for the standalone instance and the wrong one for the pool: OKE answers CreateNodePool
# with `400-InvalidParameter, Invalid nodeSourceDetails.imageId: Node image not supported.` for
# anything that is not one of its own build-numbered images, e.g.
# `Oracle-Linux-9.8-2026.08.14-0-OKE-1.36.1-1699`. This is the list of those, and locals.tf picks
# from it by the cluster's Kubernetes version.
data "oci_containerengine_node_pool_option" "oke" {
  node_pool_option_id = "all"
  compartment_id      = var.parent_compartment_id
}
