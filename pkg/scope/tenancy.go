package scope

import (
	"fmt"
	"strings"
)

// RefuseTenancyRoot returns a non-nil error if target is the tenancy root -- checked BEFORE any
// auth/Identity call, so the refusal is provably zero-API-call (SAFE-06, "having issued zero
// list or delete calls"). This file constructs no client and imports no OCI SDK package
// anywhere: that is the entire point of the contract.
//
// Two checks, both cheap, both string-only:
//  1. target == expectedTenancyOCID: catches the common case where the operator's declared
//     tenancy-id (already OCID-shape-validated by config schema, zero API calls) and their
//     --compartment-id happen to be the same string.
//  2. target has the "ocid1.tenancy." prefix: catches an operator passing ANY tenancy OCID as
//     --compartment-id, even one that (by misconfiguration) doesn't match expectedTenancyOCID --
//     defense in depth, since real compartment OCIDs are always "ocid1.compartment.", never
//     "ocid1.tenancy.".
func RefuseTenancyRoot(target, expectedTenancyOCID string) error {
	if target == expectedTenancyOCID {
		return fmt.Errorf("refusing to target the tenancy root compartment (%s)", target)
	}
	if strings.HasPrefix(target, "ocid1.tenancy.") {
		return fmt.Errorf("refusing to target a tenancy OCID as a compartment (%s)", target)
	}
	return nil
}
