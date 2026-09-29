# The fixture seeds real, billed infrastructure. It is deliberately a separate root module from
# anything else in this repository: nothing here is imported by the CLI.
terraform {
  required_version = ">= 1.9.0"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "~> 7.0"
    }
  }
}

# Three ways in, all optional, so the same module serves a laptop and CI. Locally, name a profile
# in ~/.oci/config. In CI the harness passes auth = "SecurityToken" with a profile pointing at the
# UPST that `oci-nuke auth export-session` wrote, so the run stays keyless end to end -- see the
# `auth` variable for the config-file path trap that comes with it. The API-key values remain for
# a caller that has a key and wants to use it; `private_key` takes the PEM's own bytes, so no key
# file is written, matching what `oci-nuke --auth api-key` already refuses to do. Unset arguments
# fall back to the provider's own resolution.
provider "oci" {
  auth                = var.auth
  region              = var.region
  tenancy_ocid        = var.tenancy_ocid
  config_file_profile = var.config_file_profile
  user_ocid           = var.user_ocid
  fingerprint         = var.fingerprint
  private_key         = var.private_key
}
