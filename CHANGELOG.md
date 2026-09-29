# Changelog

## [2.1.0] (2026-09-29)


### Features

* **ci:** publish each release to the public mirror naviteq/oci-nuke

## [2.0.0] (2026-09-06)


### ⚠ BREAKING CHANGES

* this changes a documented safety property, which PROJECT.md defines as breaking regardless of semver. The tenancy gate itself is not modified and the default admits nothing, but a run can now delete resources at the tenancy root that no configuration could reach before. See docs/adr/0003-tenancy-root-type-allowance.md.

### Features

* admit named resource types at the tenancy root

## [1.5.1] (2026-09-03)


### Bug Fixes

* **ci:** 409 BucketAlreadyExists is how OCI refuses an unauthorized create
* **ci:** clear the errexit the probe steps inherit
* **ci:** give each probe job its own bucket name
* **ci:** stop the nuke version rotting, and make the review gates bite

## [1.5.0] (2026-09-03)


### Features

* **ci:** verify the released image instead of trusting the release run
* **docs:** publishable documentation site, generated where it can drift


### Bug Fixes

* **tools:** stop the identifier scan aborting, and let it gate merges

## [1.4.0] (2026-09-03)


### Features

* **test:** run the delete path against real OCI, on demand
* **tools:** add a UPST token-exchange diagnostic


### Bug Fixes

* let the OKE nodes register, survive an expiring session, and stop attempting a VCN's default DHCP options
* stop a refused delete from deadlocking a compartment, and say what OCI said
* **test:** ask pipx where it put the OCI CLI
* **test:** check the parent compartment before Terraform touches it
* **test:** give the node pool its own subnet, and clean up from state
* **test:** three things OCI insists on that tofu validate accepts

## [1.3.0] (2026-09-01)


### Features

* **auth:** export the exchanged session so other tools can use it
* **test:** keep the e2e fixture inside the compartment it is granted
* **test:** seed an expendable compartment subtree for the e2e harness

## [1.2.1] (2026-09-01)


### Bug Fixes

* **release:** let the tag exist before release-please looks for it

## [1.2.0] (2026-09-01)


### Features

* **auth:** add --auth api-key for classic API-signing-key credentials
* **tools:** add a UPST token-exchange diagnostic


### Bug Fixes

* **ci:** catch this repository's nuke callers up to the shared workflows
* **release:** advance the release PR's label after the tag is cut
* **resources:** stop DynamicGroup listing every compartment it cannot exist in
* **resources:** stop one unreachable vault costing a compartment its KMS keys

## [1.1.2] (2026-08-31)


### Bug Fixes

* **release:** pin create-release at v1.53.0 so skip-github-release works
* **release:** refuse to build into an already-published release

## [1.1.1] (2026-08-31)


### Bug Fixes

* **release:** stop the changelog pipe from discarding --release-notes

## [1.1.0] (2026-08-31)


### Features

* publish release binaries and ship an install action

## [1.0.4] (2026-08-30)


### Bug Fixes

* **release:** stop goreleaser attaching assets to an immutable release

## [1.0.3] (2026-08-30)


### Bug Fixes

* **release:** use cosign v3's renamed github-actions OIDC provider

## [1.0.2] (2026-08-30)


### Bug Fixes

* **release:** stop cosign v3 from rejecting the explicit OIDC issuer

## [1.0.1] (2026-08-30)


### Bug Fixes

* **release:** pin cosign to the legacy bundle format so signing succeeds

## 1.0.0 (2026-08-30)


### Features

* cut tagged releases automatically from main
