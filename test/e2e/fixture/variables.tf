variable "tenancy_ocid" {
  description = <<-EOT
    The tenancy, for the provider's API-key path only. Nothing in the fixture reads from the
    tenancy root any more -- every lookup goes to parent_compartment_id, so the harness principal
    needs no grant above its own subtree. Optional because a security-token profile carries the
    tenancy in the token itself. No default either way: this repository is going public, and a
    committed tenancy OCID is exactly the mistake .github/workflows/nuke-plan.yml already records
    having made once.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.tenancy_ocid == null || startswith(coalesce(var.tenancy_ocid, "x"), "ocid1.tenancy.")
    error_message = "tenancy_ocid must be an ocid1.tenancy.* OCID when set."
  }
}

variable "parent_compartment_id" {
  description = <<-EOT
    The permanent, hand-created parent compartment the fixture builds inside. It must be a
    sibling of any sandbox a nuke pipeline targets, never inside one -- a fixture seeded under a
    compartment that some other nightly scans would be deleted by that nightly instead of by the
    run under test. The service principal needs create and delete rights only within this
    subtree, which is the whole reason the parent is not created here.
  EOT
  type        = string

  validation {
    condition     = startswith(var.parent_compartment_id, "ocid1.compartment.")
    error_message = "parent_compartment_id must be an ocid1.compartment.* OCID."
  }
}

variable "run_id" {
  description = <<-EOT
    Unique per run, and part of every compartment and resource name. A fixture that reuses names
    cannot tell "the previous run's resources were never deleted" from "this run's resources were
    created", which is the one distinction the harness exists to make.
  EOT
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,20}$", var.run_id))
    error_message = "run_id must be 1-21 characters of lowercase letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "region" {
  description = "Region the fixture is seeded in. One region only: the harness proves the delete path, not the region fan-out, which has its own unit coverage."
  type        = string
}

variable "include_kms_vault" {
  description = <<-EOT
    Seed a KMS vault. Off by default, and reachable only from a manual dispatch: a vault cannot
    be deleted, only scheduled for deletion 7-30 days out, so every run that seeded one would
    leave another billed vault behind. Turn it on deliberately to exercise the vault paths -- the
    expected `scheduled-deletion` leftover, and the relocate-before-scheduling path.
  EOT
  type        = bool
  default     = false
}

variable "instance_shape" {
  description = "Flexible shape for the single seeded instance. The point is that an instance exists and has to be terminated, not what it can compute."
  type        = string
  default     = "VM.Standard.E4.Flex"
}

variable "instance_ocpus" {
  description = "OCPUs for the seeded instance. One is the minimum a flexible shape accepts."
  type        = number
  default     = 1
}

variable "instance_memory_gbs" {
  description = "Memory for the seeded instance, in GB. E4.Flex requires at least 1 GB per OCPU and this is the floor Oracle accepts for one OCPU."
  type        = number
  default     = 6
}

variable "node_pool_size" {
  description = "Nodes in the seeded OKE pool. One node still exercises the pool-before-cluster ordering, and each extra node is another billed instance."
  type        = number
  default     = 1
}

variable "config_file_profile" {
  description = "Profile in ~/.oci/config to authenticate with. For local runs; leave null in CI and pass the API-key values instead."
  type        = string
  default     = null
}

variable "user_ocid" {
  description = "API-key auth: the user the key belongs to. Null when config_file_profile is used."
  type        = string
  default     = null
}

variable "fingerprint" {
  description = "API-key auth: the key's fingerprint. Null when config_file_profile is used."
  type        = string
  default     = null
}

variable "private_key" {
  description = "API-key auth: the private key's own PEM bytes, not a path. Null when config_file_profile is used."
  type        = string
  default     = null
  sensitive   = true
}

variable "auth" {
  description = <<-EOT
    Provider auth mode, passed straight through. Null leaves the provider's own default
    (`ApiKey`), which is what a local run with `config_file_profile` wants.

    CI passes `SecurityToken`. The harness has no API key and does not want one: it authenticates
    with GitHub OIDC, exchanges that for a UPST, and writes the session out with
    `oci-nuke auth export-session`. The OCI provider has no GitHub-OIDC mode of its own -- only
    ApiKey, SecurityToken, InstancePrincipal, ResourcePrincipal and OKEWorkloadIdentity -- so
    `SecurityToken` plus a profile pointing at that exported session is how a keyless run reaches
    Terraform at all.

    One trap comes with it: the provider resolves the config file to
    `<home>/.oci/config` and offers no argument or environment variable for the path
    (`GetSdkConfigProvider` in the provider's own source hardcodes it). `<home>` comes from
    `TF_HOME_OVERRIDE` when that is set, and from the passwd entry -- not `$HOME` -- when it is
    not. The harness therefore sets `TF_HOME_OVERRIDE` and writes the session under it, so
    nothing is ever written into a real `~/.oci`.
  EOT
  type        = string
  default     = null

  validation {
    condition = var.auth == null || contains(
      ["ApiKey", "SecurityToken", "InstancePrincipal", "ResourcePrincipal", "OKEWorkloadIdentity"],
      coalesce(var.auth, "ApiKey")
    )
    error_message = "auth must be one of ApiKey, SecurityToken, InstancePrincipal, ResourcePrincipal, OKEWorkloadIdentity."
  }
}
