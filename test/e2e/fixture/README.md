# e2e fixture

Seeds a throwaway compartment subtree in OCI so `oci-nuke` can be pointed at it with
`--no-dry-run` and the result checked by something other than `oci-nuke`. The workflow that does
the pointing and the checking lives in [`../harness`](../harness/).

It exists because of one fact: as of 2026-09-01 the tool has **never deleted anything outside a
stubbed client**. 57 registered resource types, a compartment-scope safety model, a hashed plan
artifact, an approve-then-apply gate, months of live dry runs — and no evidence that the delete
path works. That is the same shape as the `scripts/oci-nuke.py` this project replaced: that script
never crashed either, it exited 0 over a compartment that was still full.

## What it seeds, and why each piece is here

| Resource | Why it is in the fixture |
|---|---|
| run compartment + nested child | deepest-first ordering is only observable if something has to be deleted below before the parent can go |
| VCN, internet gateway, NAT gateway | two gateway types, because they are separate registry entries and handling one is not handling both |
| two subnets and two route tables | not for coverage. OKE refuses to place nodes in a subnet named as a service-LB subnet, so the node pool needs its own — routed through the NAT gateway, the way a real private node subnet is |
| one instance, in the **nested** compartment on a subnet from its **parent** | a cross-compartment reference, and the thing that makes emptying the child a precondition for deleting it |
| load balancer | deletion is asynchronous and returns a work request that has to be waited on |
| network security group, with rules | started as a `CKV2_OCI_3` box-tick and turned out to be load-bearing: with no rules, an OKE node cannot reach the cluster's private API endpoint and the pool never registers |
| an OKE service policy | OKE's own service principal cannot build a node pool on the caller's rights alone. Attached to the **parent** compartment, so it is deliberately outside the scanned subtree |
| a second, in-scope policy | so `Policy` is actually inside the subtree the run is pointed at. Its statement duplicates a grant the OKE policy already makes, so it widens nothing: it exists to be deleted |
| versioned bucket, one key written twice | a delete path that removes current objects and stops leaves a bucket that still refuses to be deleted |
| OKE cluster + node pool | the pool must be deleted before the cluster, both asynchronously — the ordering NR-680 claims and has never run |
| KMS vault | **off by default.** See below. |

Nothing carries `Persistent = true`. That tag is what production configs protect on, and a
fixture carrying it would be skipped by the run that is supposed to delete it.

## What is deliberately not here

**The uncommitted multipart upload.** The ticket asks for one, and the OCI Terraform provider has
no resource for it — an upload that is never committed is a transient API state, not a managed
object. The harness creates it after this module applies, with `oci raw-request` rather than
`oci os multipart create`: there is no such command, the CLI offers only `abort` and `list`.
`MultipartUpload` is still in `expected_resource_types`, so the plan assertion covers it.

**The KMS vault, unless asked for.** A vault cannot be deleted, only scheduled for deletion 7–30
days out, so every run that seeded one would leave another billed vault behind. So
`include_kms_vault` defaults to `false` and only a manual dispatch can turn it on — a
label-triggered run never does. Turning it on exercises the vault paths — the expected
`scheduled-deletion` leftover, and NR-679's
relocate-before-scheduling path.

**The OKE service policy, from the run's point of view.** It grants OKE's service principal
rights *on* the run compartment, which means it has to be attached above it — so the run under
test never sees it, correctly, and `tofu destroy` is what removes it. That is why
`oci_identity_policy.in_scope` exists: without it the fixture claimed coverage of a resource type
it never put in scope.

**The parent compartment.** It is created once, by hand, as a sibling of any sandbox a nuke
pipeline targets — never inside one. A fixture seeded under a compartment that some other nightly
scans would be deleted by that nightly instead of by the run under test. The service principal
needs create and delete rights only inside that parent, which is why this module cannot create it.

## It has to run in the home region

`Compartment` and `Policy` are home-region-only types in `oci-nuke` — the scanner enqueues them
in the tenancy's home region and nowhere else. Seed the fixture in another region and the run
will delete everything regional and leave the compartments and the policy behind, reported as
leftovers by a scan that never looked for them.

The development tenancy's home region is **`us-ashburn-1`**, read from `ListRegionSubscriptions` rather than
assumed. One region, and it is that one.

## A wrong `parent_compartment_id` does not always fail

Worth knowing before debugging it. With a malformed or non-existent OCID:

| data source | what OCI did |
|---|---|
| `oci_identity_availability_domains` | returned an **empty body, no error** |
| `oci_core_images` | returned an **empty body, no error** |
| `oci_objectstorage_namespace` | `400-InvalidCompartmentId` |

So the apply fails with `Attempt to index null value` on `compute.tf` line 4, which points at the
instance resource when the problem is one variable. The harness runs
`oci iam compartment get` before Terraform for exactly this reason; check the OCID by hand before a
local run.
## Three things OCI insists on that are not obvious

All three cost a live run to find; `tofu validate` is happy without any of them.

**`launch_options` drags in `network_type`.** The block exists only because `CKV_OCI_4` looks for
the in-transit-encryption flag inside it, and the moment it exists at all OCI answers
`400-InvalidParameter, If LaunchOptions is provided, NetworkType must be specified`. It is set to
`PARAVIRTUALIZED`, which is what a flexible VM shape gets by default anyway.

**An empty NSG is not a permissive NSG, but it is not a restrictive one either.** NSGs and security
lists are unioned and both are allow-only, so an NSG with no rules contributes nothing and the
effective policy was the VCN's default security list: all egress, ingress on TCP 22 only. Nothing
let a node reach the API endpoint on 6443, so after twenty-two minutes:

    Work Request error … entity: nodepool, action: CREATED.
    Message: 1 nodes(s) register timeout. First, confirm that network prerequisites have been met.

The cluster endpoint and the node pool are both members of one NSG, so the rules say "the members
of this group may talk to each other" with the group itself as source — no subnet CIDRs to keep in
step, and it survives re-addressing.

**An OKE node pool cannot go in a service subnet.** A subnet named in a cluster's
`options.service_lb_subnet_ids` is a service subnet, and `CreateNodePool` answers
`Invalid nodeConfigDetails.placementConfigs[].subnetId: The service subnets cannot be used by node
pools.` for one. "One subnet keeps the fixture small" was the original reasoning and it is simply
not available. The nodes get `oci_core_subnet.nodes`, routed through the NAT gateway.

**An OKE node pool will not boot a platform image.** `data.oci_core_images.ol` is right for the
standalone instance and wrong for the pool: OKE answers `Invalid nodeSourceDetails.imageId: Node
image not supported.` for anything outside its own build-numbered list, e.g.
`Oracle-Linux-9.8-2026.08.14-0-OKE-1.36.1-1699`. The pool reads
`data.oci_containerengine_node_pool_option`, and `locals.tf` picks the newest non-GPU,
non-aarch64 image for the cluster's Kubernetes version.

**A brand-new compartment is not immediately usable by Object Storage.** The bucket was created
fine and the very next call, `PutObject`, returned `404-BucketNotFound … or you are not authorized
to access it` — over a compartment forty seconds old, with `manage all-resources` granted on its
parent (checked against the live policy, not assumed). Object Storage's data plane authorizes
against cached compartment and policy state, and a grant on an ancestor is not effective for a
fresh descendant the instant `CreateCompartment` returns. `oci_objectstorage_object.first`
therefore waits on the OKE cluster: it takes thirteen minutes, runs in parallel with everything
else, and so buys a propagation window for free. `oke.tf` already ordered around the same fact for
OKE's service principal.

## Credentials

Three ways in, all optional, so the same module serves a laptop and CI.

```sh
# Local: name a profile in ~/.oci/config
tofu plan \
  -var tenancy_ocid=ocid1.tenancy.oc1..… \
  -var parent_compartment_id=ocid1.compartment.oc1..… \
  -var run_id=localcheck \
  -var region=eu-frankfurt-1 \
  -var config_file_profile=MY-PROFILE
```

In CI the harness passes `auth = "SecurityToken"` and a `config_file_profile` naming the session
that `oci-nuke auth export-session` wrote, so the run stays keyless end to end — the OCI provider
has no GitHub-OIDC mode of its own. See the `auth` variable for the config-file-path trap that
comes with it, and [`../harness`](../harness/) for how the session is refreshed.

The API-key values remain for a caller that has a key and wants to use it: pass `user_ocid`,
`fingerprint` and `private_key` instead of a profile. `private_key` takes the PEM's own bytes
rather than a path, so no key file is written to the runner — the same stance
`oci-nuke --auth api-key` takes.

No OCID has a default. This repository is going public, and a committed tenancy OCID is exactly
the mistake `.github/workflows/nuke-plan.yml` already records having made once.

### Nothing here reads the tenancy root

Availability domains, platform images and the Object Storage namespace are tenancy-wide facts,
and it is conventional to look them up against the tenancy — but their APIs accept any
compartment the caller can read in, so every lookup here goes to `parent_compartment_id` instead.

That is not tidiness. The harness principal is confined to this subtree
(naviteq/internal-infrastructure#100), and a fixture that needed `read` on the tenancy root would
have widened it straight back out just to fetch an image list. `tenancy_ocid` is now optional and
serves only the provider's API-key path.

## `expected_resource_types`

The output the harness reads to assert the plan covers everything seeded — a fixture that grew a
resource type the tool does not know about would otherwise read as a clean run.

Every name in it is checked against the live registry by
`resources_test/e2e_fixture_types_test.go`. That test is not ceremony: the first draft of the list
said `Object`, `OkeCluster`, `OkeNodePool` and `KmsVault`, and the registry has `Bucket`'s
`ObjectVersion` child, `Cluster`, `NodePool` and `Vault`. Four wrong names out of fifteen, and
`tofu validate` is happy with every one of them.

## Cost

Real, billed infrastructure: one instance, one OKE node, one load balancer, one cluster control
plane, for as long as a run takes. That is why the harness runs on demand rather than on a timer,
and why
`tofu destroy` runs as the fallback whenever the nuke does not converge — the tenancy has to be
left as it was found even when the code under test fails.

## Checkov

`checkov 3.3.16 --framework all` over `test/e2e/fixture`: **13 passed, 0 failed, 5 skipped, 0
parsing errors.** It runs in CI on changed files, so a change to any of these is scanned on its own
pull request.

Skipped in place, with the reason on the resource:

| Skipped | Why |
|---|---|
| `CKV_OCI_9` on the bucket | a customer-managed key needs a KMS vault, and the vault is off by default on purpose |
| `CKV2_OCI_6` on the cluster | PodSecurityPolicy was removed in Kubernetes 1.25, so enabling it on a current OKE version is at best ignored and at worst rejected at apply |
| `CKV2_OCI_2` on all three NSG rules | read the check's own definition before treating this as a judgement call: its only passing branch requires `direction = INGRESS`, so **every egress rule in every NSG fails it by construction**, and it also requires `source != 0.0.0.0/0`, which an ICMP path-MTU rule cannot satisfy since those messages arrive from wherever the constricted hop is. The one substantive finding is on `intra_group`, where `protocol = "all"` does permit RDP between the two members — neither of which runs RDP, from a source that is the group itself rather than the internet, on VNICs that exist for about forty minutes |

`CKV_OCI_21` (stateless ingress rules) was **satisfied rather than skipped**. The recommendation is
sound — a stateless rule keeps the connection out of the VNIC's tracking table — and it is safe
here because the intra-group rule is symmetric: both ends of every conversation are members of the
group, so a reply matches the same rule as ingress at the other VNIC. The egress-to-anywhere rule
stays stateful on purpose, because return traffic from the internet is allowed by connection state
and by nothing else.

`CKV_OCI_4` wants `is_pv_encryption_in_transit_enabled` inside `launch_options`, while the API takes
it as a top-level argument on create. It is set in both places on purpose, and the comment in
`compute.tf` says why, so nobody deletes one half as a duplicate.

## Validation status

- `tofu validate` — passes
- `tofu fmt` — clean
- `checkov --framework all` — 9 passed, 0 failed, 2 skipped
- `tofu plan` against the live development tenancy in `us-ashburn-1`, with `tenancy_ocid`
  unset — **22 resources, no errors**, every data source resolved from the parent compartment
  (namespace, availability domains, image lookup, OKE version and node-pool option lists),
  provider `oracle/oci` 7.32.0, lock file committed. The plan resolves the node image to
  `Oracle-Linux-9.8-2026.08.14-0-OKE-1.36.1-1699`, matching what `GetNodePoolOptions` returns for
  the cluster's Kubernetes version — so the selection logic is checked, not just accepted
- `resources_test/e2e_fixture_types_test.go` — all 17 declared resource-type names exist in the
  registry

Re-validated after `oci_identity_policy.in_scope` and the `auth` variable were added:
`tofu validate` passes, `tofu fmt` is clean, and the resource-type test still passes. `checkov`
has **not** been re-run against the new policy resource — it is an IAM policy with no data plane,
and the two standing skips are unrelated to it, but that is a claim from reading the rule set
rather than from running it.

What the plan does **not** prove is that the narrow harness principal has enough permission: it
was run under a broad local profile, so it shows the API accepts a non-root compartment on those
lookups, not that the confined grant covers them. The first real run is that proof.

It **has** been applied, three times, and each run found something the plan could not:

| run | got as far as | and found |
|---|---|---|
| [33518975159](https://github.com/naviteq/oci-nuke/actions/runs/33518975159) | nothing created | the parent compartment OCID was wrong, and two of three data sources returned null instead of erroring |
| [33520724929](https://github.com/naviteq/oci-nuke/actions/runs/33520724929) | 16 of 17 | `launch_options` needs `network_type`; OKE rejects platform images; Object Storage cannot see a forty-second-old compartment |
| [33529587176](https://github.com/naviteq/oci-nuke/actions/runs/33529587176) | 16 of 17, cluster up in 19m19s | node pools are refused in a service subnet |
| [33542156311](https://github.com/naviteq/oci-nuke/actions/runs/33542156311) | 18 of 19, cluster up in 20m24s | an NSG with no rules leaves nodes unable to register; and a UPST does not outlive a 59-minute `tofu destroy` |
| [33553085792](https://github.com/naviteq/oci-nuke/actions/runs/33553085792) | **all 22** — cluster 17m39s, node pool 5m20s — and `Destroy complete! Resources: 20 destroyed` | the OCI CLI has no `os multipart create` |

| [33561290413](https://github.com/naviteq/oci-nuke/actions/runs/33561290413) | **every phase** — plan coverage 15/15, negative case exit 3, subtree emptied, both compartments removed | a deleted compartment stops being readable, and the verifier piped that into `jq` |
| [33568693773](https://github.com/naviteq/oci-nuke/actions/runs/33568693773) | every phase again | the session refresh was applied to the OpenTofu phases only, so the CLI checks were still running on the token the job opened with — nearly two hours earlier |
| [33574874273](https://github.com/naviteq/oci-nuke/actions/runs/33574874273) | **green, end to end**, 79 minutes | — |
| [33593204111](https://github.com/naviteq/oci-nuke/actions/runs/33593204111) | **green again**, 1h23m, with the NSG ingress rules made stateless for `CKV_OCI_21` | —. The node pool registered in 2m21s, so stateless intra-group rules do not break OKE |

So the fixture applies and tears down cleanly, and the run it exists for now passes end to end.
Seeding takes about 23 minutes and a full `tofu destroy` about 53.
