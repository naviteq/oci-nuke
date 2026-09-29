package run

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// skipDedupResourceType is the registry.Registration.Name TestRunPipeline_ApprovedPlanApplyDoesNotDuplicateSkipEvents
// registers skipDedupResource under.
const skipDedupResourceType = "SkipDedupResource"

// skipDedupOutOfScopeCompartmentID is a fixture OCID deliberately absent from every compartment
// TestRunPipeline_ApprovedPlanApplyDoesNotDuplicateSkipEvents' fixture tree resolves in scope --
// its sole purpose is to make scopedLister.List's CurrentScope check fail and report a
// scope.ReasonOutOfScope SkipEvent via CurrentReporter, at scan time, on every List() call (both
// the --approved-plan gate's forced-dry-run rescan and the real destructive pass).
const skipDedupOutOfScopeCompartmentID = "ocid1.compartment.oc1..skipdedupoutofscope"

// skipDedupResource is a minimal ocinuke.CompartmentScoped fixture whose own compartment is
// permanently out of scope -- unlike fakePlanResource/scopeProbeResource elsewhere in this
// package, it is registered with the REAL ocinuke.CurrentScope/ocinuke.CurrentReporter
// indirection (not a hardcoded-true closure or a nil reporter), so it is actually filtered and
// reported through whatever run context runPipeline/verifyApprovedPlan currently has installed --
// exactly the production wiring 07-REVIEW.md's CR-03 finding is about.
type skipDedupResource struct{}

func (r *skipDedupResource) Remove(_ context.Context) error { return nil }
func (r *skipDedupResource) UniqueKey() string              { return "skip-dedup-resource-1" }
func (r *skipDedupResource) GetCompartmentID() string       { return skipDedupOutOfScopeCompartmentID }
func (r *skipDedupResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// skipDedupLister always returns the single fixture resource, regardless of which compartment it
// was asked to search -- scopedLister's own inScope re-verification (not the Lister) is what is
// supposed to drop it, exactly mirroring the real-world "compartmentIdInSubtree over-fetch"
// scenario CR-03's own scoped_lister.go doc comment describes.
type skipDedupLister struct{}

func (l *skipDedupLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{&skipDedupResource{}}, nil
}

// registerSkipDedupResource registers skipDedupResource via the real ocinuke.Register path with
// ocinuke.CurrentScope/ocinuke.CurrentReporter as its inScope/onSkip arguments -- the same wiring
// every real resources/*.go init() uses (see pkg/ocinuke/runcontext.go's doc comment) -- against a
// fresh registry cleared before and after the test.
func registerSkipDedupResource(t *testing.T) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)
	ocinuke.Register(&registry.Registration{
		Name:     skipDedupResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &skipDedupResource{},
		Lister:   &skipDedupLister{},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

// countSkippedFixtureEntries counts entries in entries whose ResourceType matches
// skipDedupResourceType and whose State is plan.StateSkipped -- the assertion
// TestRunPipeline_ApprovedPlanApplyDoesNotDuplicateSkipEvents' both scenarios below are built
// around.
func countSkippedFixtureEntries(entries []plan.Entry) int {
	count := 0
	for _, e := range entries {
		if e.ResourceType == skipDedupResourceType && e.State == plan.StateSkipped {
			count++
		}
	}
	return count
}

// TestRunPipeline_ApprovedPlanApplyDoesNotDuplicateSkipEvents is the CR-03 (07-REVIEW.md)
// regression test: a resource whose own compartment is out of scope is reported as a
// scope.ReasonOutOfScope SkipEvent at scan time (scopedLister.List, unconditionally of NoDryRun)
// by BOTH the --approved-plan gate's forced-dry-run rescan and the real destructive pass that
// follows it. Before the fix, both passes reported into the SAME leftoverAccumulator, so the
// apply run's own final --plan-out artifact ended up with two StateSkipped entries for the one
// fixture resource. This test fails (2 entries) against the pre-fix code and passes (1 entry)
// against the fix.
func TestRunPipeline_ApprovedPlanApplyDoesNotDuplicateSkipEvents(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})
	stubNukeRunSleep(t)

	registerSkipDedupResource(t)

	provider := newFakeProvider(t)
	cfg := testConfig()

	// Step 1: a plan-mode (dry-run) run, mirroring nuke.yml's "mode: plan" job -- its own
	// finishNukes call merges the fixture's single out-of-scope SkipEvent into the artifact it
	// writes, via its own freshly-installed accumulator.
	planPath := filepath.Join(t.TempDir(), "plan.json")
	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       planPath,
	})
	if err != nil {
		t.Fatalf("expected nil error producing the plan-mode artifact, got: %v", err)
	}

	planArtifact := readPlanArtifact(t, planPath)
	if got := countSkippedFixtureEntries(planArtifact.Body.Entries); got != 1 {
		t.Fatalf("expected exactly 1 StateSkipped %s entry in the plan-mode artifact, got %d: %+v",
			skipDedupResourceType, got, planArtifact.Body.Entries)
	}

	// Step 2: a later, separate apply-mode run against --approved-plan, mirroring nuke.yml's
	// "mode: apply" job -- this is the run whose own --plan-out artifact CR-03 corrupted.
	applyPath := filepath.Join(t.TempDir(), "apply-plan.json")
	err = runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:    testTargetCompartmentID,
		NoDryRun:         true,
		Force:            true,
		ForceSleep:       minForceSleepSeconds,
		ApprovedPlanPath: planPath,
		MaxPlanAge:       time.Hour,
		PlanOut:          applyPath,
	})
	if err != nil {
		t.Fatalf("expected nil error for an apply run whose forced rescan matches the approved plan, got: %v", err)
	}

	applyArtifact := readPlanArtifact(t, applyPath)
	if got := countSkippedFixtureEntries(applyArtifact.Body.Entries); got != 1 {
		t.Fatalf("expected exactly 1 StateSkipped %s entry in the apply run's own --plan-out artifact "+
			"(the gate's forced-dry-run rescan must not double-report into the real pass's accumulator), got %d: %+v",
			skipDedupResourceType, got, applyArtifact.Body.Entries)
	}

	// The out-of-scope SkipEvent carries no ResourceID (scoped_lister.go: CompartmentID-only),
	// so also assert the single surviving entry's Reason/CompartmentID are what scopedLister.List
	// actually reports -- a passing entry count alone would not distinguish "correctly deduped"
	// from "the fixture was silently dropped for an unrelated reason."
	entry := findEntry(applyArtifact.Body.Entries, func(e plan.Entry) bool {
		return e.ResourceType == skipDedupResourceType && e.State == plan.StateSkipped
	})
	if entry == nil {
		t.Fatal("expected to find the deduped StateSkipped entry in the apply artifact")
	}
	if entry.Reason != scope.ReasonOutOfScope {
		t.Errorf("expected Reason %q, got %q", scope.ReasonOutOfScope, entry.Reason)
	}
	if entry.CompartmentID != skipDedupOutOfScopeCompartmentID {
		t.Errorf("expected CompartmentID %q, got %q", skipDedupOutOfScopeCompartmentID, entry.CompartmentID)
	}
}
