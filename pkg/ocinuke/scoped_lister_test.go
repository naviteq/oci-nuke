package ocinuke

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// fakeScopedResource implements CompartmentScoped AND SafetyEvaluated -- the control case for
// these tests. freeformTags/createdAt are zero-valued unless a test sets them, so a
// fakeScopedResource is never protected by default (mirrors a real resource with no matching
// tags/a very old creation time).
type fakeScopedResource struct {
	compartmentID string
	freeformTags  map[string]string
	definedTags   map[string]string
	createdAt     time.Time
	uniqueKey     string
}

func (r *fakeScopedResource) Remove(_ context.Context) error { return nil }
func (r *fakeScopedResource) GetCompartmentID() string       { return r.compartmentID }
func (r *fakeScopedResource) UniqueKey() string              { return r.uniqueKey }
func (r *fakeScopedResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return r.freeformTags, r.definedTags, r.createdAt
}

// testFakeScopedResourceType is the registry.Registration.Name/scopedLister.resourceType this
// file's tests share, so the literal "FakeScoped" is never repeated (goconst, min-occurrences: 3).
const testFakeScopedResourceType = "FakeScoped"

// fakeUnscopedResource implements only resource.Resource -- it deliberately does NOT implement
// CompartmentScoped or SafetyEvaluated, exercising both fail-closed points from 02-CONTEXT.md's
// addendum plus SafetyEvaluated's own mirrored fail-closed point (04-13).
type fakeUnscopedResource struct{}

func (r *fakeUnscopedResource) Remove(_ context.Context) error { return nil }

// fakeScopedNoSafetyResource implements CompartmentScoped but deliberately NOT SafetyEvaluated --
// isolates List's own SafetyEvaluated fail-closed branch (defense in depth for the case this
// reaches runtime despite Register's registration-time guard) from the CompartmentScoped branch
// fakeUnscopedResource above already covers.
type fakeScopedNoSafetyResource struct {
	compartmentID string
}

func (r *fakeScopedNoSafetyResource) Remove(_ context.Context) error { return nil }
func (r *fakeScopedNoSafetyResource) GetCompartmentID() string       { return r.compartmentID }

// fakeLister returns whatever resources it is constructed with, mirroring
// resources_test/dryrun_test.go's fakeLister pattern (package ocinuke_test) but kept internal
// here (package ocinuke) so these tests can reach the unexported scopedLister type directly.
type fakeLister struct {
	resources []resource.Resource
}

func (l *fakeLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return l.resources, nil
}

// TestScopedLister_DropsResourceOutsideResolvedScope is DoD refusal path 4: a resource whose own
// compartmentId falls outside the resolved in-scope set must never reach the kept slice.
func TestScopedLister_DropsResourceOutsideResolvedScope(t *testing.T) {
	const outOfScopeID = "ocid1.compartment.oc1..outofscope"
	inner := &fakeLister{resources: []resource.Resource{&fakeScopedResource{compartmentID: outOfScopeID}}}

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:   inner,
		inScope: func(id string) bool { return id != outOfScopeID },
		onSkip:  func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
	}

	got, err := sl.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- out-of-scope resource must be dropped", len(got))
	}
	if len(skipped) != 1 {
		t.Fatalf("onSkip called %d times, want exactly 1", len(skipped))
	}
	want := scope.SkipEvent{Reason: scope.ReasonOutOfScope, CompartmentID: outOfScopeID}
	if skipped[0] != want {
		t.Errorf("onSkip event = %+v, want %+v", skipped[0], want)
	}
}

// TestScopedLister_KeepsInScopeResource is the non-vacuous control case (mirroring
// dryrun_test.go's own TestNoDryRunCallsRemove control case): proves List doesn't drop
// everything unconditionally.
func TestScopedLister_KeepsInScopeResource(t *testing.T) {
	const inScopeID = "ocid1.compartment.oc1..inscope"
	r := &fakeScopedResource{compartmentID: inScopeID}
	inner := &fakeLister{resources: []resource.Resource{r}}

	var skipCalled bool
	sl := &scopedLister{
		inner:   inner,
		inScope: func(id string) bool { return id == inScopeID },
		onSkip:  func(*scope.SkipEvent) { skipCalled = true },
	}

	got, err := sl.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != resource.Resource(r) {
		t.Fatalf("List() = %+v, want the single in-scope resource unchanged", got)
	}
	if skipCalled {
		t.Error("onSkip must not be called for an in-scope resource")
	}
}

// TestScopedLister_DropsResourceNotImplementingCompartmentScoped proves the list-time fail-closed
// point from 02-CONTEXT.md's addendum directly (constructing scopedLister without going through
// Register, to isolate List's own behavior): the !ok branch drops, never keeps, even though the
// injected InScope closure would keep everything if it were (wrongly) reached.
func TestScopedLister_DropsResourceNotImplementingCompartmentScoped(t *testing.T) {
	inner := &fakeLister{resources: []resource.Resource{&fakeUnscopedResource{}}}

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:   inner,
		inScope: func(string) bool { return true }, // would keep if reachable -- must never be reached
		onSkip:  func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
	}

	got, err := sl.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- non-CompartmentScoped resource must be dropped, not kept", len(got))
	}
	if len(skipped) != 1 || skipped[0].Reason != scope.ReasonOutOfScope {
		t.Fatalf("onSkip = %+v, want exactly one ReasonOutOfScope event", skipped)
	}
}

// TestRegister_PanicsWhenResourceDoesNotImplementCompartmentScoped proves the registration-time
// fail-closed point from 02-CONTEXT.md's addendum: a missing accessor is a startup failure, not a
// silent pass-through, and the panic happens before registry.Register ever mutates the registry.
func TestRegister_PanicsWhenResourceDoesNotImplementCompartmentScoped(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	reg := &registry.Registration{
		Name:     "FakeUnscoped",
		Scope:    CompartmentScope,
		Resource: &fakeUnscopedResource{},
		Lister:   &fakeLister{},
	}

	defer func() {
		if recover() == nil {
			t.Fatal("Register() did not panic for a Resource that does not implement CompartmentScoped")
		}
		if l := registry.GetLister(reg.Name); l != nil {
			t.Fatalf("registry.GetLister(%q) = %v, want nil -- panic must happen before registry mutation", reg.Name, l)
		}
	}()

	Register(reg, func(string) bool { return true }, nil)
}

// TestRegister_WrapsListerSoRegistryGetListerAppliesScoping proves registry.GetLister after
// ocinuke.Register returns a Lister that actually applies scope-filtering, not merely that
// Register was called. scopedLister is unexported, so this asserts indirectly: drives the
// registered Lister end-to-end and confirms it drops an out-of-scope resource the raw inner
// fakeLister would otherwise have returned unfiltered -- the more direct proof available given
// the unexported wrapper type.
func TestRegister_WrapsListerSoRegistryGetListerAppliesScoping(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	const outOfScopeID = "ocid1.compartment.oc1..outofscope"
	inner := &fakeLister{resources: []resource.Resource{&fakeScopedResource{compartmentID: outOfScopeID}}}

	reg := &registry.Registration{
		Name:     testFakeScopedResourceType,
		Scope:    CompartmentScope,
		Resource: &fakeScopedResource{},
		Lister:   inner,
	}

	Register(reg, func(string) bool { return false }, nil)

	got, err := registry.GetLister(reg.Name).List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf(
			"registry.GetLister(%q).List() returned %d resources, want 0 -- registry must store "+
				"the scope-filtering wrapper, not the raw inner Lister",
			reg.Name, len(got),
		)
	}
}

// TestScopedLister_DropsProtectedByTagResource is the scan-time proof for the 04-13 plan/apply
// divergence fix's core property, at the scopedLister unit level: a resource matching a
// protect-by-tag rule is dropped by List itself -- before it can ever become a queue.Item -- with
// a ReasonProtectedByTag onSkip event, exactly mirroring the already-established out-of-scope
// drop above. Unlike the pre-fix design (ocinuke.EvaluateAndRemove, called from inside Remove()),
// this drop happens during Scan(), so it applies identically whether or not the run is a dry run.
func TestScopedLister_DropsProtectedByTagResource(t *testing.T) {
	const compartmentID = "ocid1.compartment.oc1..inscope"
	r := &fakeScopedResource{
		compartmentID: compartmentID,
		uniqueKey:     "protected-1",
		freeformTags:  map[string]string{testProtectTagKey: "true"},
	}
	inner := &fakeLister{resources: []resource.Resource{r}}

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:        inner,
		inScope:      func(string) bool { return true },
		onSkip:       func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
		resourceType: testFakeScopedResourceType,
	}

	opts := &ListerOpts{SafetyFilter: SafetyFilterConfig{
		ProtectTags: []TagMatch{{Key: testProtectTagKey, Value: "true"}},
	}}

	got, err := sl.List(context.Background(), opts)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- protected-by-tag resource must be dropped", len(got))
	}
	if len(skipped) != 1 {
		t.Fatalf("onSkip called %d times, want exactly 1", len(skipped))
	}
	if skipped[0].Reason != scope.ReasonProtectedByTag {
		t.Errorf("onSkip event Reason = %q, want %q", skipped[0].Reason, scope.ReasonProtectedByTag)
	}
	if skipped[0].ResourceID != "protected-1" {
		t.Errorf("onSkip event ResourceID = %q, want %q", skipped[0].ResourceID, "protected-1")
	}
}

// TestScopedLister_DropsTooYoungResource is TestScopedLister_DropsProtectedByTagResource's
// min-age sibling, exercising the OTHER named protection (02-CONTEXT.md locked requirement 7) at
// scan time, including the deliberately inverted dateOlderThan polarity Evaluate documents:
// createdAt one hour ago against MinAge 24h protects (too young), not excludes.
func TestScopedLister_DropsTooYoungResource(t *testing.T) {
	const compartmentID = "ocid1.compartment.oc1..inscope"
	r := &fakeScopedResource{
		compartmentID: compartmentID,
		uniqueKey:     "too-young-1",
		createdAt:     time.Now().Add(-1 * time.Hour),
	}
	inner := &fakeLister{resources: []resource.Resource{r}}

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:        inner,
		inScope:      func(string) bool { return true },
		onSkip:       func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
		resourceType: testFakeScopedResourceType,
	}

	opts := &ListerOpts{SafetyFilter: SafetyFilterConfig{MinAge: 24 * time.Hour}}

	got, err := sl.List(context.Background(), opts)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- too-young resource must be dropped", len(got))
	}
	if len(skipped) != 1 || skipped[0].Reason != scope.ReasonTooYoung {
		t.Fatalf("onSkip = %+v, want exactly one ReasonTooYoung event", skipped)
	}
}

// TestScopedLister_KeepsResourceNotMatchingSafetyFilter is the non-vacuous control case for both
// tests above (mirroring TestScopedLister_KeepsInScopeResource's own role for the scope check):
// proves a configured SafetyFilter does not drop a resource that does not match it.
func TestScopedLister_KeepsResourceNotMatchingSafetyFilter(t *testing.T) {
	const compartmentID = "ocid1.compartment.oc1..inscope"
	r := &fakeScopedResource{
		compartmentID: compartmentID,
		uniqueKey:     "unprotected-1",
		freeformTags:  map[string]string{"env": "dev"},
		createdAt:     time.Now().Add(-30 * 24 * time.Hour),
	}
	inner := &fakeLister{resources: []resource.Resource{r}}

	var skipCalled bool
	sl := &scopedLister{
		inner:   inner,
		inScope: func(string) bool { return true },
		onSkip:  func(*scope.SkipEvent) { skipCalled = true },
	}

	opts := &ListerOpts{SafetyFilter: SafetyFilterConfig{
		ProtectTags: []TagMatch{{Key: "env", Value: "prod"}},
		MinAge:      24 * time.Hour,
	}}

	got, err := sl.List(context.Background(), opts)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != resource.Resource(r) {
		t.Fatalf("List() = %+v, want the single non-matching resource unchanged", got)
	}
	if skipCalled {
		t.Error("onSkip must not be called for a resource matching no configured safety filter")
	}
}

// TestScopedLister_DropsResourceNotImplementingSafetyEvaluated proves List's own SafetyEvaluated
// fail-closed branch directly (constructing scopedLister without going through Register): the
// !ok branch drops, never keeps, even though the resource DOES implement CompartmentScoped and
// would otherwise pass the scope check.
func TestScopedLister_DropsResourceNotImplementingSafetyEvaluated(t *testing.T) {
	const compartmentID = "ocid1.compartment.oc1..inscope"
	inner := &fakeLister{resources: []resource.Resource{&fakeScopedNoSafetyResource{compartmentID: compartmentID}}}

	var skipped []scope.SkipEvent
	sl := &scopedLister{
		inner:        inner,
		inScope:      func(string) bool { return true },
		onSkip:       func(e *scope.SkipEvent) { skipped = append(skipped, *e) },
		resourceType: "FakeScopedNoSafety",
	}

	got, err := sl.List(context.Background(), &ListerOpts{})
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- non-SafetyEvaluated resource must be dropped, not kept", len(got))
	}
	if len(skipped) != 1 || skipped[0].Reason != scope.ReasonOutOfScope {
		t.Fatalf("onSkip = %+v, want exactly one ReasonOutOfScope event", skipped)
	}
}

// TestRegister_PanicsWhenResourceDoesNotImplementSafetyEvaluated is
// TestRegister_PanicsWhenResourceDoesNotImplementCompartmentScoped's SafetyEvaluated sibling: a
// resource that implements CompartmentScoped but not SafetyEvaluated must still fail closed at
// registration time, not merely at List time.
func TestRegister_PanicsWhenResourceDoesNotImplementSafetyEvaluated(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	reg := &registry.Registration{
		Name:     "FakeScopedNoSafety",
		Scope:    CompartmentScope,
		Resource: &fakeScopedNoSafetyResource{},
		Lister:   &fakeLister{},
	}

	defer func() {
		if recover() == nil {
			t.Fatal("Register() did not panic for a Resource that does not implement SafetyEvaluated")
		}
		if l := registry.GetLister(reg.Name); l != nil {
			t.Fatalf("registry.GetLister(%q) = %v, want nil -- panic must happen before registry mutation", reg.Name, l)
		}
	}()

	Register(reg, func(string) bool { return true }, nil)
}
