package scope

import (
	"strings"
	"testing"
)

const testExpectedTenancyID = "ocid1.tenancy.oc1..aaaaaaaaexampletenancy"

func TestRefuseTenancyRoot_TargetEqualsTenancyID(t *testing.T) {
	err := RefuseTenancyRoot(testExpectedTenancyID, testExpectedTenancyID)

	if err == nil {
		t.Fatal("expected a non-nil error when target equals the tenancy OCID")
	}
	if !strings.Contains(err.Error(), testExpectedTenancyID) {
		t.Fatalf("expected error to name the offending OCID %q, got: %v", testExpectedTenancyID, err)
	}
}

func TestRefuseTenancyRoot_TargetHasTenancyPrefix(t *testing.T) {
	// A different tenancy OCID than expectedTenancyOCID -- defense-in-depth case.
	otherTenancyID := "ocid1.tenancy.oc1..aaaaaaaaothertenancy"

	err := RefuseTenancyRoot(otherTenancyID, testExpectedTenancyID)

	if err == nil {
		t.Fatal("expected a non-nil error for any ocid1.tenancy. prefixed target, even one not matching expectedTenancyOCID")
	}
	if !strings.Contains(err.Error(), otherTenancyID) {
		t.Fatalf("expected error to name the offending OCID %q, got: %v", otherTenancyID, err)
	}
}

func TestRefuseTenancyRoot_AllowsRealCompartment(t *testing.T) {
	realCompartment := "ocid1.compartment.oc1..aaaaaaaaexamplecompartment"

	if err := RefuseTenancyRoot(realCompartment, testExpectedTenancyID); err != nil {
		t.Fatalf("expected nil error for a normal compartment target, got: %v", err)
	}
}
