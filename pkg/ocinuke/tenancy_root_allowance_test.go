package ocinuke

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/resource"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// testTenancyOCID is the verified tenancy root every test in this file admits against. Shared so
// the literal is never repeated (goconst, min-occurrences: 3).
const (
	testTenancyOCID      = "ocid1.tenancy.oc1..testroot"
	testDynamicGroupType = "DynamicGroup"
)

// TestTenancyRootAllowance_Allows pins the admission rule from both directions. The zero value
// and every partially-populated shape must refuse, because this is the one seam in the project
// that widens the compartment scope check: a bug that fails OPEN here would admit a resource the
// resolved scope already refused.
func TestTenancyRootAllowance_Allows(t *testing.T) {
	populated := TenancyRootAllowance{
		TenancyOCID: testTenancyOCID,
		Types:       map[string]struct{}{testDynamicGroupType: {}},
	}

	tests := []struct {
		name          string
		allowance     TenancyRootAllowance
		resourceType  string
		compartmentID string
		want          bool
	}{
		{
			name:          "named type at the verified root is admitted",
			allowance:     populated,
			resourceType:  testDynamicGroupType,
			compartmentID: testTenancyOCID,
			want:          true,
		},
		{
			name:          "zero value admits nothing",
			allowance:     TenancyRootAllowance{},
			resourceType:  testDynamicGroupType,
			compartmentID: testTenancyOCID,
			want:          false,
		},
		{
			name:          "type not named in the config is refused at the root",
			allowance:     populated,
			resourceType:  "Bucket",
			compartmentID: testTenancyOCID,
			want:          false,
		},
		{
			name:          "named type in a compartment that is not the root is refused",
			allowance:     populated,
			resourceType:  testDynamicGroupType,
			compartmentID: "ocid1.compartment.oc1..somewhere",
			want:          false,
		},
		{
			name:          "another tenancy's root is refused -- exact match, never a prefix",
			allowance:     populated,
			resourceType:  testDynamicGroupType,
			compartmentID: "ocid1.tenancy.oc1..otherroot",
			want:          false,
		},
		{
			name:          "empty TenancyOCID refuses even an empty compartment id",
			allowance:     TenancyRootAllowance{Types: map[string]struct{}{testDynamicGroupType: {}}},
			resourceType:  testDynamicGroupType,
			compartmentID: "",
			want:          false,
		},
		{
			name:          "nil Types map refuses every type",
			allowance:     TenancyRootAllowance{TenancyOCID: testTenancyOCID},
			resourceType:  testDynamicGroupType,
			compartmentID: testTenancyOCID,
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.allowance.Allows(tt.resourceType, tt.compartmentID); got != tt.want {
				t.Errorf("Allows(%q, %q) = %v, want %v", tt.resourceType, tt.compartmentID, got, tt.want)
			}
		})
	}
}

// TestSetTenancyRootAllowance_RestoreFailsClosed proves the restore func returns the package to
// the fail-closed zero value, so one run's allowance can never leak into the next.
func TestSetTenancyRootAllowance_RestoreFailsClosed(t *testing.T) {
	restore := SetTenancyRootAllowance(TenancyRootAllowance{
		TenancyOCID: testTenancyOCID,
		Types:       map[string]struct{}{testFakeScopedResourceType: {}},
	})
	if !CurrentTenancyRootAllowance().Allows(testFakeScopedResourceType, testTenancyOCID) {
		t.Fatal("installed allowance must admit its own named type at the root")
	}

	restore()

	if CurrentTenancyRootAllowance().Allows(testFakeScopedResourceType, testTenancyOCID) {
		t.Error("after restore the allowance must admit nothing")
	}
}

// TestScopedLister_KeepsAllowedTenancyRootResource is the point of the whole change: a resource
// whose own compartment is the tenancy root -- which the resolved in-scope set never contains --
// survives when, and only when, the config named its type.
func TestScopedLister_KeepsAllowedTenancyRootResource(t *testing.T) {
	restore := SetTenancyRootAllowance(TenancyRootAllowance{
		TenancyOCID: testTenancyOCID,
		Types:       map[string]struct{}{testFakeScopedResourceType: {}},
	})
	defer restore()

	r := &fakeScopedResource{compartmentID: testTenancyOCID, uniqueKey: "ocid1.dynamicgroup.oc1..one"}
	sl := &scopedLister{
		inner:        &fakeLister{resources: []resource.Resource{r}},
		inScope:      func(string) bool { return false }, // the root is never in the resolved set
		onSkip:       func(*scope.SkipEvent) { t.Error("onSkip must not fire for an admitted resource") },
		resourceType: testFakeScopedResourceType,
	}

	got, err := sl.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() returned %d resources, want 1 -- the allowance must admit the root-owned resource", len(got))
	}
}

// TestScopedLister_DropsTenancyRootResourceOfUnnamedType is the companion refusal: an allowance
// for one type must not admit another type that happens to sit at the same root.
func TestScopedLister_DropsTenancyRootResourceOfUnnamedType(t *testing.T) {
	restore := SetTenancyRootAllowance(TenancyRootAllowance{
		TenancyOCID: testTenancyOCID,
		Types:       map[string]struct{}{"SomeOtherType": {}},
	})
	defer restore()

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:        &fakeLister{resources: []resource.Resource{&fakeScopedResource{compartmentID: testTenancyOCID}}},
		inScope:      func(string) bool { return false },
		onSkip:       func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
		resourceType: testFakeScopedResourceType,
	}

	got, err := sl.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- an unnamed type must still be dropped", len(got))
	}
	if len(skipped) != 1 || skipped[0].Reason != scope.ReasonOutOfScope {
		t.Errorf("skip events = %+v, want exactly one %s event", skipped, scope.ReasonOutOfScope)
	}
}

// TestScopedLister_TenancyRootAllowanceStillEvaluatesSafety is the guarantee that keeps this from
// being a hole: the allowance short-circuits the SCOPE check only. Protect-by-tag runs after it,
// on whatever it admitted, exactly as it does for an ordinary in-scope resource.
func TestScopedLister_TenancyRootAllowanceStillEvaluatesSafety(t *testing.T) {
	restore := SetTenancyRootAllowance(TenancyRootAllowance{
		TenancyOCID: testTenancyOCID,
		Types:       map[string]struct{}{testFakeScopedResourceType: {}},
	})
	defer restore()

	protected := &fakeScopedResource{
		compartmentID: testTenancyOCID,
		freeformTags:  map[string]string{testProtectTagKey: testTagValueTrue},
		createdAt:     time.Now().Add(-24 * time.Hour),
		uniqueKey:     "ocid1.dynamicgroup.oc1..protected",
	}

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:        &fakeLister{resources: []resource.Resource{protected}},
		inScope:      func(string) bool { return false },
		onSkip:       func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
		resourceType: testFakeScopedResourceType,
	}

	opts := &ListerOpts{SafetyFilter: SafetyFilterConfig{
		ProtectTags: []TagMatch{{Key: testProtectTagKey, Value: testTagValueTrue}},
	}}

	got, err := sl.List(context.Background(), opts)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- Persistent=true must still protect a root-owned resource", len(got))
	}
	if len(skipped) != 1 || skipped[0].Reason != scope.ReasonProtectedByTag {
		t.Errorf("skip events = %+v, want exactly one %s event", skipped, scope.ReasonProtectedByTag)
	}
}
