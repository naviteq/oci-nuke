package ocinuke_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// This file closes 04-CONTEXT.md's environment-note substitution for two of Phase 4's DoD items,
// stated openly: a Terraform-seeded sandbox and a genuinely destructive run against a live
// tenancy are the DoD's literal wording, but the live policy is dry-run-only (04-CONTEXT.md's
// environment_note). Both properties below are instead proven against stubbed SDK clients through
// the REAL libnuke.Nuke.Run() control flow, reusing resources_test/equivalence_test.go's own
// registerXFixtures/newXNuke/real-Run() pattern -- the same substitution
// resources_test/scheduled_deletion_test.go already made for its own Phase 3 DoD item. The
// genuinely live, seeded-sandbox destructive run remains outstanding, deferred to NR-682's e2e
// harness.

const (
	waveConvergenceRegion        = "us-ashburn-1"
	waveConvergenceCompartmentID = "ocid1.compartment.oc1..wave-convergence-test"
	waveConvergenceOwner         = waveConvergenceRegion + "/" + waveConvergenceCompartmentID

	// Five fixture types, each modeling one representative lifecycle pattern from this wave's
	// real domains (04-RESEARCH.md Q1/Q2/Q4/Q6):
	// - FixtureLeaf: standard present/terminal Filter(), list-disappearance convergence
	//   (mirrors NatGateway).
	// - FixtureDependent: a real DependsOn edge onto FixtureLeaf (mirrors
	//   VolumeAttachment->Instance / Vcn->periphery).
	// - FixtureNoLifecycle: unconditional-nil Filter() (mirrors
	//   InstanceConfiguration/Bucket/PrivateIp).
	// - FixtureTerminatingRegression: the hang-trap regression -- stays listed, Filter()
	//   alone converges it.
	// - FixtureSafetyRouted: scan-time protect-by-tag evaluation (ocinuke.SafetyEvaluated,
	//   applied by scopedLister.List, not Remove()) against a run-wide SafetyFilterConfig this
	//   fixture's own tags do not match.
	fixtureLeafType        = "FixtureLeaf"
	fixtureDependentType   = "FixtureDependent"
	fixtureNoLifecycleType = "FixtureNoLifecycle"
	fixtureTerminatingType = "FixtureTerminatingRegression"
	fixtureSafetyType      = "FixtureSafetyRouted"

	// wantWaveFixtureCount is this file's non-vacuousness guard, mirroring equivalence_test.go's
	// wantResourceCount: exactly one instance per fixture type, five total.
	wantWaveFixtureCount = 5

	fixtureStateProvisioning = "PROVISIONING"
	fixtureStateAvailable    = "AVAILABLE"
	fixtureStateTerminating  = "TERMINATING"
	fixtureStateTerminated   = "TERMINATED"

	// fixtureSafetyTagKey is the freeform tag key FixtureSafetyRouted's own protect-by-tag
	// scenario matches on, shared across every use site below so the literal is never repeated
	// (goconst, min-occurrences: 3).
	fixtureSafetyTagKey = "env"
)

// waveLifecycleFilter is the exact allow-list-of-"present" shape every real resources/*.go
// Filter() in this wave follows (04-RESEARCH.md Q2): PROVISIONING/AVAILABLE are present and never
// excluded; anything else (including TERMINATING, which is still returned by a real OCI List()
// call) is excluded -- both at scan time ("never attempt removal") and at HandleWait's post-
// Remove() polling ("already handled, converge now").
func waveLifecycleFilter(state string) error {
	switch state {
	case fixtureStateProvisioning, fixtureStateAvailable:
		return nil
	default:
		return fmt.Errorf("fixture resource is %s, not available", state)
	}
}

// fixtureLeaf models the standard, synchronous-from-the-lister's-perspective delete pattern: once
// Remove() succeeds, the resource is excluded from every subsequent List() call, mirroring
// recordingResource in equivalence_test.go. Its Filter() still carries the same lifecycle switch
// every real type has, so scan-time exclusion is exercised too.
type fixtureLeaf struct {
	uid           string
	compartmentID string
	state         string
	removed       *bool
}

func (r *fixtureLeaf) Remove(_ context.Context) error { *r.removed = true; return nil }
func (r *fixtureLeaf) UniqueKey() string              { return r.uid }
func (r *fixtureLeaf) GetCompartmentID() string       { return r.compartmentID }
func (r *fixtureLeaf) Filter() error                  { return waveLifecycleFilter(r.state) }
func (r *fixtureLeaf) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

type fixtureLeafLister struct{ instances []*fixtureLeaf }

func (l *fixtureLeafLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// fixtureDependent is identical in shape to fixtureLeaf, except its own registry.Registration
// declares DependsOn: []string{fixtureLeafType} -- proving this wave's real DependsOn convention
// (declared by the dependent, never the dependency) converges cleanly end to end alongside the
// other four fixtures in the SAME run.
type fixtureDependent struct {
	uid           string
	compartmentID string
	state         string
	removed       *bool
}

func (r *fixtureDependent) Remove(_ context.Context) error { *r.removed = true; return nil }
func (r *fixtureDependent) UniqueKey() string              { return r.uid }
func (r *fixtureDependent) GetCompartmentID() string       { return r.compartmentID }
func (r *fixtureDependent) Filter() error                  { return waveLifecycleFilter(r.state) }
func (r *fixtureDependent) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

type fixtureDependentLister struct{ instances []*fixtureDependent }

func (l *fixtureDependentLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// fixtureNoLifecycle mirrors InstanceConfiguration/Bucket/PrivateIp: the underlying (fictitious)
// SDK type has no lifecycle-state-shaped field at all, so Filter() unconditionally returns nil --
// "gone" is entirely "absent from List()", exactly like the six real allow-listed types in
// resources_test/filter_contract_test.go's noLifecycleFieldTypes.
type fixtureNoLifecycle struct {
	uid           string
	compartmentID string
	removed       *bool
}

func (r *fixtureNoLifecycle) Remove(_ context.Context) error { *r.removed = true; return nil }
func (r *fixtureNoLifecycle) UniqueKey() string              { return r.uid }
func (r *fixtureNoLifecycle) GetCompartmentID() string       { return r.compartmentID }
func (r *fixtureNoLifecycle) Filter() error                  { return nil }
func (r *fixtureNoLifecycle) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

type fixtureNoLifecycleLister struct{ instances []*fixtureNoLifecycle }

func (l *fixtureNoLifecycleLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// fixtureTerminatingRegression is THE regression fixture for Phase 3's hang trap (03-CONTEXT.md,
// 04-CONTEXT.md "The trap found in Phase 3 -- read this twice"): its Remove() call transitions
// the resource to TERMINATING and returns nil (success, from libnuke's perspective -- the SDK
// delete call itself did not error), but the fixture's own lister ALWAYS keeps returning it,
// exactly mirroring a real OCI async delete where the resource is still visible via List() while
// TERMINATING. Convergence depends entirely on Filter() correctly excluding TERMINATING during
// HandleWait's post-Remove() re-list (libnuke/pkg/nuke.Nuke.HandleWait: a Filter() error on the
// re-listed match breaks out of the wait loop and falls through to ItemStateFinished). If Filter()
// were buggy -- e.g. an unconditional `return nil` that never excludes TERMINATING -- HandleWait
// would instead `return` early every round, leaving the item stuck in ItemStateWaiting forever:
// n.Run(ctx)'s round loop has no ctx-cancellation exit, so a real bug here hangs indefinitely, only
// bounded by this test file's own `-timeout 30s` (see T-04-36).
type fixtureTerminatingRegression struct {
	uid           string
	compartmentID string
	state         string
}

func (r *fixtureTerminatingRegression) Remove(_ context.Context) error {
	r.state = fixtureStateTerminating
	return nil
}
func (r *fixtureTerminatingRegression) UniqueKey() string        { return r.uid }
func (r *fixtureTerminatingRegression) GetCompartmentID() string { return r.compartmentID }
func (r *fixtureTerminatingRegression) Filter() error            { return waveLifecycleFilter(r.state) }
func (r *fixtureTerminatingRegression) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

type fixtureTerminatingLister struct{ instance *fixtureTerminatingRegression }

// List ALWAYS returns the one fixed instance, regardless of its current state -- this is the
// defining property of this fixture (see the type's own doc comment above): a real async OCI
// delete leaves the resource visible via List() throughout TERMINATING, and only Filter()'s
// lifecycle check -- never list-disappearance -- can converge it.
func (l *fixtureTerminatingLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{l.instance}, nil
}

// fixtureSafetyRouted proves ocinuke.SafetyEvaluated (scan-time protect-by-tag, applied by
// scopedLister.List against this run's whole ListerOpts.SafetyFilter -- see
// newWaveConvergenceNuke below) is reachable end to end WITHOUT altering this test's convergence
// outcome: the run-wide SafetyFilter protects env=prod, but this resource's own freeform tag is
// env=dev, so ocinuke.Evaluate returns nil (not protected), List keeps it, and Remove() actually
// runs. TestWaveConvergence_ProtectedResourceNeverReachesRemove below is this fixture's own
// negative control: the SAME fixture type, tagged env=prod, dropped before Remove() ever fires.
type fixtureSafetyRouted struct {
	uid           string
	compartmentID string
	state         string
	freeformTags  map[string]string
	removed       *bool
}

func (r *fixtureSafetyRouted) Remove(_ context.Context) error {
	*r.removed = true
	return nil
}
func (r *fixtureSafetyRouted) UniqueKey() string        { return r.uid }
func (r *fixtureSafetyRouted) GetCompartmentID() string { return r.compartmentID }
func (r *fixtureSafetyRouted) Filter() error            { return waveLifecycleFilter(r.state) }
func (r *fixtureSafetyRouted) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return r.freeformTags, nil, time.Now().Add(-48 * time.Hour)
}

type fixtureSafetyRoutedLister struct{ instances []*fixtureSafetyRouted }

func (l *fixtureSafetyRoutedLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// registerWaveConvergenceFixtures registers all five fixtures against a fresh registry.
// ocinuke.SetRunContext installs inScope/reporter for the duration of the call (an always-true
// inScope, since scope membership is not what this test is about, and rec.report -- reusing
// scheduled_deletion_test.go's own eventRecorder type in this same package -- as the reporter),
// then every ocinuke.Register call passes ocinuke.CurrentScope/ocinuke.CurrentReporter (the
// function-value indirection, NOT locally-constructed closures) exactly as every real
// resources/*.go init() does -- proving this test exercises the SAME run-context wiring
// production uses, per pkg/ocinuke/runcontext.go's own doc comment, not a bypass.
func registerWaveConvergenceFixtures(t *testing.T, rec *eventRecorder) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	restore := ocinuke.SetRunContext(func(string) bool { return true }, rec.report)
	t.Cleanup(restore)

	leaf := &fixtureLeaf{uid: "FixtureLeaf-1", compartmentID: waveConvergenceCompartmentID, state: fixtureStateAvailable, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureLeafType,
		Scope:    ocinuke.CompartmentScope,
		Resource: leaf,
		Lister:   &fixtureLeafLister{instances: []*fixtureLeaf{leaf}},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	dependent := &fixtureDependent{
		uid:           "FixtureDependent-1",
		compartmentID: waveConvergenceCompartmentID,
		state:         fixtureStateAvailable,
		removed:       new(bool),
	}
	ocinuke.Register(&registry.Registration{
		Name:      fixtureDependentType,
		Scope:     ocinuke.CompartmentScope,
		Resource:  dependent,
		Lister:    &fixtureDependentLister{instances: []*fixtureDependent{dependent}},
		DependsOn: []string{fixtureLeafType},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	noLifecycle := &fixtureNoLifecycle{uid: "FixtureNoLifecycle-1", compartmentID: waveConvergenceCompartmentID, removed: new(bool)}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureNoLifecycleType,
		Scope:    ocinuke.CompartmentScope,
		Resource: noLifecycle,
		Lister:   &fixtureNoLifecycleLister{instances: []*fixtureNoLifecycle{noLifecycle}},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	terminating := &fixtureTerminatingRegression{
		uid:           "FixtureTerminatingRegression-1",
		compartmentID: waveConvergenceCompartmentID,
		state:         fixtureStateAvailable,
	}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureTerminatingType,
		Scope:    ocinuke.CompartmentScope,
		Resource: terminating,
		Lister:   &fixtureTerminatingLister{instance: terminating},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	safetyRouted := &fixtureSafetyRouted{
		uid:           "FixtureSafetyRouted-1",
		compartmentID: waveConvergenceCompartmentID,
		state:         fixtureStateAvailable,
		freeformTags:  map[string]string{fixtureSafetyTagKey: "dev"},
		removed:       new(bool),
	}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureSafetyType,
		Scope:    ocinuke.CompartmentScope,
		Resource: safetyRouted,
		Lister:   &fixtureSafetyRoutedLister{instances: []*fixtureSafetyRouted{safetyRouted}},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

// waveConvergenceSafetyFilter is this run-wide SafetyFilter every fixture's ocinuke.Evaluate call
// (applied at scan time by scopedLister.List, via the ListerOpts this file's ONE
// newWaveConvergenceNuke passes to every scanner) is checked against: protects env=prod.
// FixtureSafetyRouted-1's own freeform tag is env=dev, so it is never protected -- see that
// fixture's own doc comment. Every other fixture's SafetyTags() returns nil freeform/defined
// maps, so this ProtectTags rule can never match them either (Evaluate's tag loop `continue`s on
// a nil map, per safety_filter.go's own doc comment).
var waveConvergenceSafetyFilter = ocinuke.SafetyFilterConfig{
	ProtectTags: []ocinuke.TagMatch{{Key: fixtureSafetyTagKey, Value: "prod"}},
}

// newWaveConvergenceNuke mirrors equivalence_test.go's newEquivalenceNuke shape, adapted for five
// registered fixture types with WaitOnDependencies: true (fixtureDependentType's real DependsOn
// edge needs it) and NoDryRun: true throughout -- this file only ever proves the destructive-run
// convergence property, never the dry-run plan (already covered by equivalence_test.go).
func newWaveConvergenceNuke(t *testing.T) *libnuke.Nuke {
	t.Helper()

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:           true,
		Force:              true,
		ForceSleep:         3, // Nuke.Validate() rejects anything below 3
		WaitOnDependencies: true,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond) // libnuke's runSleep defaults to 5s; keep the test fast

	s, err := scanner.New(&scanner.Config{
		Owner: waveConvergenceOwner,
		ResourceTypes: []string{
			fixtureLeafType, fixtureDependentType, fixtureNoLifecycleType,
			fixtureTerminatingType, fixtureSafetyType,
		},
		Opts: &ocinuke.ListerOpts{
			Region:        waveConvergenceRegion,
			CompartmentID: waveConvergenceCompartmentID,
			SafetyFilter:  waveConvergenceSafetyFilter,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestWaveConvergence_NoLeftovers is the direct proof for 04-CONTEXT.md's environment-note
// substitution of "a single --no-dry-run run leaves a stubbed compartment empty ... with no
// manual second pass and no leftovers": exactly one n.Run(ctx) call, then the plan artifact
// (built the same way pkg/plan/build.go's BuildFromQueue always is) must show zero StateLeftover
// entries and all five fixtures StateRemoved -- no second n.Run(ctx) call is made anywhere in
// this test, proving "no manual second pass" structurally, not merely by assertion.
func TestWaveConvergence_NoLeftovers(t *testing.T) {
	rec := &eventRecorder{}
	registerWaveConvergenceFixtures(t, rec)

	n := newWaveConvergenceNuke(t)
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) returned error: %v", err)
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)

	if len(entries) != wantWaveFixtureCount {
		t.Fatalf("got %d plan entries, want %d (non-vacuousness guard): %+v", len(entries), wantWaveFixtureCount, entries)
	}

	leftovers := entriesWithState(entries, plan.StateLeftover)
	if len(leftovers) != 0 {
		t.Fatalf("got %d leftover entries after a single --no-dry-run run, want 0 -- a manual second "+
			"pass should never be required (entries: %+v)", len(leftovers), leftovers)
	}

	removed := entriesWithState(entries, plan.StateRemoved)
	if len(removed) != wantWaveFixtureCount {
		t.Fatalf("got %d removed entries, want all %d fixtures removed (entries: %+v)", len(removed), wantWaveFixtureCount, entries)
	}
}

// TestWaveConvergence_TerminatingDoesNotBlock isolates fixtureTerminatingType's own plan entry
// and asserts it specifically reached StateRemoved (ItemStateFinished) -- not merely that the
// whole run "did not time out" -- proving the exact regression Phase 3's hang trap describes:
// a resource already in TERMINATING at scan/wait time neither fails the run nor blocks
// convergence, now proven against this wave's real Filter() allow-list convention.
func TestWaveConvergence_TerminatingDoesNotBlock(t *testing.T) {
	rec := &eventRecorder{}
	registerWaveConvergenceFixtures(t, rec)

	n := newWaveConvergenceNuke(t)
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) returned error: %v", err)
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)

	var found bool
	for _, e := range entries {
		if e.ResourceType != fixtureTerminatingType {
			continue
		}
		found = true
		if e.State != plan.StateRemoved {
			t.Fatalf("FixtureTerminatingRegression plan entry State = %s, want %s -- a resource stuck "+
				"in TERMINATING must converge, not leftover/block the run (Phase 3's hang trap regression)",
				e.State, plan.StateRemoved)
		}
	}
	if !found {
		t.Fatal("no plan entry found for FixtureTerminatingRegression -- non-vacuous check failed")
	}
}

// TestWaveConvergence_ProtectedResourceNeverReachesRemove is FixtureSafetyRouted's own negative
// control (see that fixture's doc comment): the SAME fixture type, this time tagged env=prod --
// matching waveConvergenceSafetyFilter -- must be dropped by scopedLister.List at scan time,
// never becoming a queue.Item, so Remove() (which would flip *removed to true) is never called at
// all, even on a real --no-dry-run run. This is the 04-13 fix's headline property proven against
// a destructive run, not merely a dry run: protection is not a Remove()-time-only check that a
// dry run happens to miss -- it never lets the resource reach Remove() in EITHER mode.
func TestWaveConvergence_ProtectedResourceNeverReachesRemove(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	rec := &eventRecorder{}
	restore := ocinuke.SetRunContext(func(string) bool { return true }, rec.report)
	t.Cleanup(restore)

	protected := &fixtureSafetyRouted{
		uid:           "FixtureSafetyRouted-protected",
		compartmentID: waveConvergenceCompartmentID,
		state:         fixtureStateAvailable,
		freeformTags:  map[string]string{fixtureSafetyTagKey: "prod"},
		removed:       new(bool),
	}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureSafetyType,
		Scope:    ocinuke.CompartmentScope,
		Resource: protected,
		Lister:   &fixtureSafetyRoutedLister{instances: []*fixtureSafetyRouted{protected}},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         waveConvergenceOwner,
		ResourceTypes: []string{fixtureSafetyType},
		Opts: &ocinuke.ListerOpts{
			Region:        waveConvergenceRegion,
			CompartmentID: waveConvergenceCompartmentID,
			SafetyFilter:  waveConvergenceSafetyFilter,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) returned error: %v", err)
	}

	if *protected.removed {
		t.Fatal("Remove() was called for a protected resource -- protection must apply at scan " +
			"time, before this resource can ever become a queue.Item, in a destructive run just " +
			"as much as in a dry run")
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	entries = plan.MergeSkipEvents(entries, rec.snapshot())

	var found bool
	for _, e := range entries {
		if e.ResourceID != protected.uid {
			continue
		}
		found = true
		if e.State != plan.StateSkipped {
			t.Errorf("protected resource's plan entry State = %s, want %s", e.State, plan.StateSkipped)
		}
		if e.Reason != scope.ReasonProtectedByTag {
			t.Errorf("protected resource's plan entry Reason = %q, want %q", e.Reason, scope.ReasonProtectedByTag)
		}
	}
	if !found {
		t.Fatal("no plan entry found for the protected FixtureSafetyRouted instance -- non-vacuous check failed")
	}
}
