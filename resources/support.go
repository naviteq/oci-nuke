// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"time"

	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/common"
)

// Property key constants for every field this wave's domain plans are already known to need
// repeatedly (3+ occurrences each, across ~35 resource files) -- declared once here so no
// resource file repeats the raw string literal, keeping the package goconst-clean by
// construction (min-occurrences: 3, enforced by this phase's final lint gate, Plan 04-12). A
// domain plan introducing a NEW property key reused 3+ times within its own files MUST add it
// here too -- this is a lint-driven authoring rule, not a style preference.
const (
	propID                 = "id"
	propName               = "name"
	propCompartmentID      = "compartment_id"
	propLifecycleState     = "lifecycle_state"
	propTimeCreated        = "time_created"
	propVcnID              = "vcn_id"
	propSubnetID           = "subnet_id"
	propInstanceID         = "instance_id"
	propVolumeID           = "volume_id"
	propFileSystemID       = "file_system_id"
	propAvailabilityDomain = "availability_domain"
	propSizeInGBs          = "size_in_gbs"
	propNamespace          = "namespace"
	propBucket             = "bucket"
	propExportSetID        = "export_set_id"
)

// baseProperties builds the types.Properties every resource type in this wave shares: id,
// compartment_id (always set -- types.Properties.Set is nil-safe on a nil *string, matching
// libnuke's own Set(key, *string) contract, so id/compartmentID never need a nil check here),
// name and lifecycle_state (each set only when the type actually has one -- PrivateIp/Bucket/
// InstanceConfiguration have no lifecycle field at all, per 04-RESEARCH.md Q2/Q4's tables, so an
// empty lifecycleState omits the key entirely rather than setting it to an empty string),
// time_created (set only when timeCreated is non-nil, never panicking on the nil
// *common.SDKTime a type without a creation-time field would otherwise pass), and every
// freeform/defined tag via SetTag/SetTagWithPrefix -- libnuke's own colon-delimited tag
// convention (04-RESEARCH.md Q6), distinct from flattenDefinedTags' dot-delimited convention
// below, which serves an unrelated purpose (ocinuke.Evaluate's internal parameter shape).
func baseProperties(
	id, name, compartmentID *string,
	lifecycleState string,
	timeCreated *common.SDKTime,
	freeformTags map[string]string,
	definedTags map[string]map[string]interface{},
) types.Properties {
	props := types.NewProperties().
		Set(propID, id).
		Set(propCompartmentID, compartmentID)

	if name != nil {
		props.Set(propName, name)
	}
	if lifecycleState != "" {
		props.Set(propLifecycleState, lifecycleState)
	}
	if timeCreated != nil {
		props.Set(propTimeCreated, timeCreated.Time)
	}

	for k, v := range freeformTags {
		k := k
		props.SetTag(&k, v)
	}
	for ns, kv := range definedTags {
		for k, v := range kv {
			k := k
			props.SetTagWithPrefix(ns, &k, v)
		}
	}

	return props
}

// flattenDefinedTags builds the "<namespace>.<key>" -> value dot-delimited map every resource
// type's SafetyTags() method returns as its defined-tags value (documented on ocinuke.TagMatch),
// from OCI SDK's own nested map[string]map[string]interface{} DefinedTags shape. This is a
// deliberately different convention from baseProperties' colon-delimited SetTagWithPrefix calls
// above -- the two serve unrelated purposes and must not be confused or unified (04-RESEARCH.md
// Q6).
//
// A value that is not a string is silently dropped from the returned map rather than causing a
// panic or a flattening error -- a deliberate, fail-open-on-type-mismatch-but-fail-closed-on-
// safety choice: a tag that can't be flattened simply can't match a protect-by-tag rule, so it
// never blocks removal, but it also never falsely protects a resource by being coerced into a
// string it was never meant to be.
func flattenDefinedTags(definedTags map[string]map[string]interface{}) map[string]string {
	flat := make(map[string]string)
	for ns, kv := range definedTags {
		for k, v := range kv {
			s, ok := v.(string)
			if !ok {
				continue
			}
			flat[ns+"."+k] = s
		}
	}
	return flat
}

// timeCreatedOrZero returns tc.Time, or the zero time.Time if tc is nil. Every resource type
// registered before Plan 04-07 has a `mandatory:"true"` TimeCreated field on its SDK struct, so
// dereferencing x.TimeCreated.Time directly (NatGateway, Vcn, ...) is safe there. Plan 04-07's
// Drg, DrgAttachment, PrivateIp, ReservedPublicIp (core.PublicIp), and Vlan all declare
// TimeCreated as `mandatory:"false"` on the pinned SDK (verified this session against
// core/drg.go, core/drg_attachment.go, core/private_ip.go, core/public_ip.go, core/vlan.go), so
// the same direct dereference would panic on a real record with no creation timestamp -- this
// helper is the nil-safe replacement those five types' SafetyTags() methods use instead. A zero
// time.Time returned to ocinuke.Evaluate's min-age check (applied at scan time by
// ocinuke.scopedLister) is correctly treated as "very old" (never protected), never as a false
// "protected" or "too young" positive -- see pkg/ocinuke/safety_filter.go's Evaluate doc comment
// for the exact dateOlderThan polarity this relies on.
func timeCreatedOrZero(tc *common.SDKTime) time.Time {
	if tc == nil {
		return time.Time{}
	}
	return tc.Time
}
