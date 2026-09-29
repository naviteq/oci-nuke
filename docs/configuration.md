# Configuration

`oci-nuke` reads a single YAML file. Validate it before the first run — `config validate`
issues **zero OCI API calls**, so it is safe to run against anything:

```bash
oci-nuke config validate --config config.yaml
```

`config.example.yaml` in the repository is a commented starting point; this page is the
reference.

## The three required keys are the safety model

Two of the three required keys exist only to stop a run going somewhere it was not meant to,
and neither can be omitted.

### `tenancy-id`

Before listing a single resource, `oci-nuke` calls `GetTenancy` and compares the answer against
this value. A mismatch exits non-zero immediately. This is what stops a config written for one
tenancy from ever running against another — the credentials do not get a say.

### `compartment-blocklist`

Compartments that must never be touched. Matching is **always by OCID and never by display
name**: compartment names are unique only within their parent, so a name match would be a real
bypass vector. The blocklist prunes during traversal, so blocklisting a compartment protects
everything beneath it too.

!!! warning "Do not list the tenancy root"
    Every compartment descends from the tenancy root, so a root entry makes every possible
    target refuse with `target compartment ... is blocklisted`. It buys no protection either:
    the tenancy root is already rejected as a target by a separate refusal that no config key
    can switch off. List the compartments you actually want to protect — production, shared
    services, anything holding state you cannot rebuild.

### `regions`

Resources are enumerated once per region. Include the tenancy's **home** region if you want IAM
coverage: `Policy`, `DynamicGroup`, `TagNamespace`, `TagDefault` and `Compartment` are
home-region-only and are enumerated once, in the home region, rather than once per region. Omit
it and those five types are scanned nowhere — the run warns, but the plan simply has nothing to
say about them. The home region is often not the region your credentials connect through.

## Two polarities worth reading twice

Both of these have bitten someone. Neither is a bug.

**`settings.protect.min-age` skips what is *younger*.** It guards against deleting a resource
somebody is creating right now, in a compartment you were told was idle.

**A `dateOlderThan` filter with `value: "24h"` protects what is *younger* than 24 hours.** The
filter engine computes `fieldTime + duration` and compares against now, so a resource created an
hour ago lands in the future and matches the filter; one created a month ago does not.

## Filters are per-compartment, deliberately

Each compartment resolves its own filter set — a filter written for one compartment never
silently applies to another. Within a compartment, the reserved `__global__` key applies to
every resource type.

`presets` are named, reusable filter sets; a compartment opts into one by listing its name under
its own `presets` key. A preset is not applied to any compartment that does not name it.

```yaml
filters:
  ocid1.compartment.oc1..aaaaaaaaexamplesandboxcompartmentexampleexampleexamplex:
    filters:
      __global__:
        - property: "tag:Persistent"
          type: exact
          value: "true"
```

A freeform or defined tag is addressed as `tag:<key>` — `libnuke`'s own default tag prefix, which
is what the resource types here build their properties with. Earlier revisions of this page showed
`freeform_tags.<key>`, which never matches anything.

## Scanning the tenancy root for the types that can only live there

A few OCI types exist nowhere but the tenancy root — `DynamicGroup` is the one implemented today,
because OCI refuses to create a dynamic group in any other compartment. Their own compartment is
therefore always the tenancy OCID, which the resolved in-scope set never contains, so by default
they are listed, dropped as out-of-scope, and no configuration can delete them.

`tenancy-root-types` names the types a run may scan and remove there. Absent or empty — the
default — admits nothing:

```yaml
tenancy-root-types:
  - DynamicGroup
```

This does **not** make the tenancy root a target: a `--compartment-id` naming the tenancy is still
refused before any API call, and the extra root scan is restricted to exactly the types named
here. `resource-types.excludes` still wins over an entry, protect-by-tag and min-age are still
evaluated, and the compartment blocklist is untouched.

Filters for that scan are keyed by the tenancy OCID, like any other compartment. To delete only
the groups one tool created, invert a filter so everything else is protected:

```yaml
filters:
  ocid1.tenancy.oc1..aaaaaaaaexampletenancyexampleexampleexampleexamplexxx:
    filters:
      DynamicGroup:
        - property: "tag:module"
          type: exact
          value: "terraform-oci-oke"
          invert: true
```

The reasoning, and the alternatives it rejects, are in
[ADR-0003](adr/0003-tenancy-root-type-allowance.md).

## What a config cannot do

- It cannot make the tenancy root a valid target. `tenancy-root-types` admits named types at the
  root; it does not let a run target it.
- It cannot shorten OCI's 7–30 day vault deletion window. Vaults are scheduled for deletion and
  then reported as residue, not as failures.
- It cannot turn compartment deletion back off once enabled: `--delete-compartments` can only
  turn `settings.compartment.delete` on, never override it back off.

See [What cannot be deleted](limitations.md) for the platform limits that no configuration
changes.

## Reference

Generated from `pkg/config/schema/config.schema.json` — the same schema `config validate` uses,
so this table cannot disagree with what the tool accepts. Regenerate with
`go run ./tools/generate-config-docs`; CI runs it with `-check`.

A constraint cell reading `—` means the schema constrains nothing beyond the type.

<!-- generated: config-schema -->

### Top-level keys

| Key | Type | Required | Constraints |
|---|---|---|---|
| `compartment-blocklist` | list of string | **yes** | at least 1 entry; each entry matches `^ocid1\.(compartment|tenancy)\.[a-z0-9.]+$` |
| `filters` | map of `compartmentFilters` | no | — |
| `presets` | map of `preset` | no | — |
| `regions` | list of string | **yes** | at least 1 entry |
| `resource-types` | object (see below) | no | no other keys allowed |
| `settings` | object (see below) | no | — |
| `tenancy-id` | string | **yes** | matches `^ocid1\.tenancy\.[a-z0-9.]+$` |
| `tenancy-root-types` | list of string | no | — |

### Nested objects

#### `resource-types`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `excludes` | list of string | no | — |
| `includes` | list of string | no | — |

#### `settings`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `compartment` | `compartmentSettings` | no | — |
| `protect` | `protectSettings` | no | — |
| `vault` | `vaultSettings` | no | — |

#### `compartmentFilters`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `filters` | `filterMap` | no | — |
| `presets` | list of string | no | — |

#### `compartmentSettings`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `delete` | boolean | no | — |

#### `filter`

Accepts 2 shapes.

**As a string.**

**As an object.**

| Key | Type | Required | Constraints |
|---|---|---|---|
| `group` | string | no | — |
| `invert` | boolean | no | — |
| `property` | string | **yes** | — |
| `type` | string | **yes** | one of `exact`, `glob`, `regex`, `contains`, `dateOlderThan`, `dateOlderThanNow`, `suffix`, `prefix`, `In`, `NotIn` |
| `value` | string | no | — |
| `values` | list of string | no | — |

#### `filterMap`

map of list of `filter`.

#### `preset`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `filters` | `filterMap` | no | — |

#### `protectSettings`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `min-age` | string | no | matches `^([0-9]+(ns|us|µs|ms|s|m|h))+$` |
| `tags` | list of object (see below) | no | each entry no other keys allowed |

#### `vaultSettings`

| Key | Type | Required | Constraints |
|---|---|---|---|
| `deletion-window-days` | integer | no | between 7 and 30 |

<!-- /generated: config-schema -->
