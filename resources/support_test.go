package resources

import (
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
)

func TestBaseProperties_FullSet(t *testing.T) {
	id := "ocid1.natgateway.oc1..test"
	name := "test-nat-gateway"
	compartmentID := "ocid1.compartment.oc1..test"
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	timeCreated := &common.SDKTime{Time: createdAt}

	props := baseProperties(&id, &name, &compartmentID, "AVAILABLE", timeCreated, nil, nil)

	want := map[string]string{
		propID:             id,
		propName:           name,
		propCompartmentID:  compartmentID,
		propLifecycleState: "AVAILABLE",
		propTimeCreated:    createdAt.Format(time.RFC3339),
	}
	for k, v := range want {
		if got := props.Get(k); got != v {
			t.Errorf("props[%q] = %q, want %q", k, got, v)
		}
	}
}

func TestBaseProperties_EmptyLifecycleStateOmitted(t *testing.T) {
	id := "ocid1.privateip.oc1..test"

	props := baseProperties(&id, nil, nil, "", nil, nil, nil)

	if _, ok := props[propLifecycleState]; ok {
		t.Errorf("expected propLifecycleState to be entirely absent for lifecycleState==\"\", got %q", props[propLifecycleState])
	}
}

func TestBaseProperties_NilTimeCreatedOmitted(t *testing.T) {
	id := "ocid1.privateip.oc1..test"

	props := baseProperties(&id, nil, nil, "", nil, nil, nil)

	if _, ok := props[propTimeCreated]; ok {
		t.Errorf("expected propTimeCreated to be entirely absent for a nil timeCreated, got %q", props[propTimeCreated])
	}
}

func TestFlattenDefinedTags_DotDelimited(t *testing.T) {
	definedTags := map[string]map[string]interface{}{
		"Operations": {"Persistent": "true"},
	}

	got := flattenDefinedTags(definedTags)

	want := map[string]string{"Operations.Persistent": "true"}
	if len(got) != len(want) {
		t.Fatalf("flattenDefinedTags() = %+v, want %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("flattenDefinedTags()[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestFlattenDefinedTags_NonStringValueDropped(t *testing.T) {
	definedTags := map[string]map[string]interface{}{
		"Operations": {"Retries": 3},
	}

	got := flattenDefinedTags(definedTags)

	if len(got) != 0 {
		t.Errorf("flattenDefinedTags() with a non-string value = %+v, want empty (dropped, not panicked)", got)
	}
}
