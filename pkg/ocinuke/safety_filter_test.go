package ocinuke

import (
	"testing"
	"time"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

const (
	testCompartmentID = "ocid1.compartment.oc1..aaaaaaaaexample"
	testResourceType  = "Instance"
	testResourceID    = "ocid1.instance.oc1..aaaaaaaaexample"
	testTagValueTrue  = "true"
	// testProtectTagKey is the freeform tag key 02-CONTEXT.md locked requirement 7's own example
	// uses ("Persistent=true"), shared across this file and scoped_lister_test.go so the literal
	// is never repeated (goconst, min-occurrences: 3).
	testProtectTagKey = "Persistent"
)

// TestEvaluate_ProtectsOnFreeformTagMatch proves protect-by-tag for the freeform-tag form
// (02-CONTEXT.md locked requirement 7's explicit "Persistent=true" example).
func TestEvaluate_ProtectsOnFreeformTagMatch(t *testing.T) {
	cfg := SafetyFilterConfig{
		ProtectTags: []TagMatch{{Key: testProtectTagKey, Value: testTagValueTrue, Defined: false}},
	}
	freeformTags := map[string]string{testProtectTagKey: testTagValueTrue}

	got := Evaluate(testCompartmentID, testResourceType, testResourceID, freeformTags, nil, time.Now(), cfg)

	if got == nil {
		t.Fatal("Evaluate() = nil, want a non-nil SkipEvent for a matching freeform tag")
	}
	if got.Reason != scope.ReasonProtectedByTag {
		t.Errorf("got.Reason = %q, want %q", got.Reason, scope.ReasonProtectedByTag)
	}
}

// TestEvaluate_ProtectsOnDefinedTagMatch proves protect-by-tag for the defined-tag form --
// locked requirement 7 requires both freeform AND defined tags to work, not just freeform.
func TestEvaluate_ProtectsOnDefinedTagMatch(t *testing.T) {
	cfg := SafetyFilterConfig{
		ProtectTags: []TagMatch{{Key: "Operations.Persistent", Value: testTagValueTrue, Defined: true}},
	}
	definedTags := map[string]string{"Operations.Persistent": testTagValueTrue}

	got := Evaluate(testCompartmentID, testResourceType, testResourceID, nil, definedTags, time.Now(), cfg)

	if got == nil {
		t.Fatal("Evaluate() = nil, want a non-nil SkipEvent for a matching defined tag")
	}
	if got.Reason != scope.ReasonProtectedByTag {
		t.Errorf("got.Reason = %q, want %q", got.Reason, scope.ReasonProtectedByTag)
	}
}

// TestEvaluate_MinAge_YoungerThanThresholdIsProtected pins the dateOlderThan-derived min-age
// polarity with the exact worked example from research: a resource created 1h ago against a
// 24h MinAge is protected (matches, excluded from deletion). This is the single most dangerous
// fact in this plan -- the name "dateOlderThan" reads backwards from this behavior.
func TestEvaluate_MinAge_YoungerThanThresholdIsProtected(t *testing.T) {
	cfg := SafetyFilterConfig{MinAge: 24 * time.Hour}
	createdAt := time.Now().Add(-1 * time.Hour)

	got := Evaluate(testCompartmentID, testResourceType, testResourceID, nil, nil, createdAt, cfg)

	if got == nil {
		t.Fatal("Evaluate() = nil, want a non-nil SkipEvent -- a 1h-old resource against a 24h MinAge must be protected")
	}
	if got.Reason != scope.ReasonTooYoung {
		t.Errorf("got.Reason = %q, want %q", got.Reason, scope.ReasonTooYoung)
	}
}

// TestEvaluate_MinAge_OlderThanThresholdIsNotProtected pins the other half of the same worked
// example: a resource created 30 days ago against a 24h MinAge is NOT protected (eligible for
// deletion). Named explicitly, and distinctly from the "younger" test above, so a future reader
// cannot mistake this for testing the inverse polarity.
func TestEvaluate_MinAge_OlderThanThresholdIsNotProtected(t *testing.T) {
	cfg := SafetyFilterConfig{MinAge: 24 * time.Hour}
	createdAt := time.Now().Add(-30 * 24 * time.Hour)

	got := Evaluate(testCompartmentID, testResourceType, testResourceID, nil, nil, createdAt, cfg)

	if got != nil {
		t.Fatalf("Evaluate() = %+v, want nil -- a 30-day-old resource against a 24h MinAge must NOT be protected", got)
	}
}

// TestEvaluate_NoMatchReturnsNil proves the default-off behavior: no tag match and MinAge
// unconfigured (zero value) means no protection fires.
func TestEvaluate_NoMatchReturnsNil(t *testing.T) {
	cfg := SafetyFilterConfig{}
	freeformTags := map[string]string{"Env": "prod"}

	got := Evaluate(testCompartmentID, testResourceType, testResourceID, freeformTags, nil, time.Now().Add(-time.Hour), cfg)

	if got != nil {
		t.Fatalf("Evaluate() = %+v, want nil -- no configured protections should ever fire", got)
	}
}
