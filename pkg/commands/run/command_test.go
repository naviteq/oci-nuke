package run

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/settings"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/spf13/cobra"

	"github.com/naviteq/oci-nuke/pkg/clients"
	naviteqcommon "github.com/naviteq/oci-nuke/pkg/common"
	"github.com/naviteq/oci-nuke/pkg/config"
	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// fakeConfigProvider implements common.ConfigurationProvider directly, backed by a freshly
// generated RSA key the SDK's request signer needs. It is a local duplicate of the identical
// type in pkg/ociauth's own test file -- that one is unexported, so it cannot be imported
// here.
type fakeConfigProvider struct {
	tenancyOCID string
	region      string
	key         *rsa.PrivateKey
}

// testTenancyID/testHomeRegion/testTargetCompartmentID are the fixed fixture values every test
// in this file uses -- named constants rather than repeated literals (goconst). testHomeRegion
// doubles as the fixture "real home region" (the ListRegionSubscriptions IsHomeRegion=true
// value) everywhere except testConnectionRegionDivergence's regression test, which deliberately
// gives the fake provider a DIFFERENT connection region to prove the fix.
const (
	testTenancyID           = "ocid1.tenancy.oc1..test"
	testHomeRegion          = "us-ashburn-1"
	testTargetCompartmentID = "ocid1.compartment.oc1..target"
	// resourceTypeBucket names the type three fixtures now use; goconst's threshold is 3.
	resourceTypeBucket = "Bucket"

	// testConnectionRegionDivergence is a fixture connection region deliberately DIFFERENT
	// from testHomeRegion -- it directly encodes the live-tenancy finding (the credential
	// profile's connection region, eu-frankfurt-1, diverges from the tenancy's real home
	// region, us-ashburn-1). Only TestRunPipeline_HomeRegionDerivedFromSubscriptionsNotProviderRegion
	// uses this; every other test's fake provider returns testHomeRegion so the coincidence of
	// connection region == home region never masks a regression there.
	testConnectionRegionDivergence = "eu-frankfurt-1"

	// minForceSleepSeconds is libnuke's Nuke.Validate() hard minimum for ForceSleep (see
	// nuke.go: "value for --force-sleep cannot be less than 3 seconds"). Nuke.Validate() runs
	// unconditionally on every Nuke.Run() call regardless of NoDryRun/Force, so any test whose
	// runPipeline call reaches buildNukes/Run must set ForceSleep to at least this value.
	minForceSleepSeconds = 3

	// testBlockedCompartmentID is the fixture CompartmentBlocklist entry every test in this
	// file uses that only needs a non-empty blocklist to satisfy SAFE-05, not a specific
	// blocked-ancestor scenario (those tests define their own node OCIDs).
	testBlockedCompartmentID = "ocid1.compartment.oc1..blocked"

	// fakePlanResourceType is the registry.Registration.Name every plan-artifact test in this
	// file registers fakePlanResource under (goconst: referenced 4+ times).
	fakePlanResourceType = "FakePlanResource"

	// filterPropertyName is the Filter.Property value this file's filter fixtures use (goconst:
	// referenced 3+ times, shared with TestBuildNukes_AppliesPerCompartmentFilters below).
	filterPropertyName = "Name"
)

func (f *fakeConfigProvider) TenancyOCID() (string, error)    { return f.tenancyOCID, nil }
func (f *fakeConfigProvider) UserOCID() (string, error)       { return "ocid1.user.oc1..test", nil }
func (f *fakeConfigProvider) KeyFingerprint() (string, error) { return "aa:bb:cc", nil }
func (f *fakeConfigProvider) Region() (string, error)         { return f.region, nil }
func (f *fakeConfigProvider) AuthType() (common.AuthConfig, error) {
	return common.AuthConfig{AuthType: common.UserPrincipal}, nil
}
func (f *fakeConfigProvider) KeyID() (string, error) {
	return f.tenancyOCID + "/ocid1.user.oc1..test/aa:bb:cc", nil
}
func (f *fakeConfigProvider) PrivateRSAKey() (*rsa.PrivateKey, error) { return f.key, nil }

// newFakeProvider always issues a provider for testTenancyID whose Region() (connection
// region) equals testHomeRegion -- every test in this file except the home-region-divergence
// regression test targets the same fixture tenancy with a coincidentally-matching connection
// region, so the OCID is not a parameter (unparam).
func newFakeProvider(t *testing.T) *fakeConfigProvider {
	t.Helper()
	return newFakeProviderWithRegion(t, testHomeRegion)
}

// newFakeProviderWithRegion issues a provider for testTenancyID whose Region() (connection
// region) is the given value -- used by the home-region-divergence regression test to give the
// fake provider a connection region deliberately different from the fixture tenancy's real home
// region.
func newFakeProviderWithRegion(t *testing.T, region string) *fakeConfigProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeConfigProvider{tenancyOCID: testTenancyID, region: region, key: key}
}

func stubVerifyTenancy(
	t *testing.T,
	stub func(ctx context.Context, provider common.ConfigurationProvider, expectedTenancyOCID string) error,
) {
	t.Helper()
	orig := verifyTenancy
	t.Cleanup(func() { verifyTenancy = orig })
	verifyTenancy = stub
}

func testConfig() *config.Config {
	return &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: []string{testBlockedCompartmentID},
	}
}

// compartmentFixture builds a synthetic ACTIVE identity.Compartment with the fields
// resolveScope reads (Id, CompartmentId, LifecycleState), mirroring pkg/scope/tree_test.go's
// own compartment() helper -- that one is unexported to pkg/scope, so it cannot be imported
// here. Lifecycle-state variation (e.g. DELETED) is already covered at the pkg/scope unit-test
// layer; this file's tests exercise the refusal-path wiring, not lifecycle nuances, so the
// state is fixed rather than a parameter (unparam).
func compartmentFixture(id, parent string) identity.Compartment {
	c := identity.Compartment{
		Id:             common.String(id),
		Name:           common.String(id),
		LifecycleState: identity.CompartmentLifecycleStateActive,
	}
	if parent != "" {
		c.CompartmentId = common.String(parent)
	}
	return c
}

// fakeIdentityClient implements scope.IdentityClient purely in-memory: ListCompartments
// returns every fixture compartment in one page regardless of the request, GetCompartment
// looks the requested OCID up in the same slice.
type fakeIdentityClient struct {
	compartments []identity.Compartment
}

// ListCompartments signature must match the real identity.IdentityClient.ListCompartments (by
// value, not a pointer) -- see pkg/scope/tree_test.go's identical fake for the same reasoning.
//
//nolint:gocritic
func (f *fakeIdentityClient) ListCompartments(
	_ context.Context,
	_ identity.ListCompartmentsRequest,
) (identity.ListCompartmentsResponse, error) {
	return identity.ListCompartmentsResponse{Items: f.compartments}, nil
}

func (f *fakeIdentityClient) GetCompartment(
	_ context.Context,
	req identity.GetCompartmentRequest,
) (identity.GetCompartmentResponse, error) {
	if req.CompartmentId != nil {
		for _, c := range f.compartments {
			if c.Id != nil && *c.Id == *req.CompartmentId {
				return identity.GetCompartmentResponse{Compartment: c}, nil
			}
		}
	}
	return identity.GetCompartmentResponse{}, fmt.Errorf("compartment not found")
}

// stubScopeIdentityClient reassigns the newScopeIdentityClient package var (see command.go) to
// return a fakeIdentityClient seeded with compartments, following stubVerifyTenancy's exact
// reassign-and-cleanup pattern.
func stubScopeIdentityClient(t *testing.T, compartments []identity.Compartment) {
	t.Helper()
	orig := newScopeIdentityClient
	t.Cleanup(func() { newScopeIdentityClient = orig })
	newScopeIdentityClient = func(_ *clients.Cache, _ string) (scope.IdentityClient, error) {
		return &fakeIdentityClient{compartments: compartments}, nil
	}
}

// fakeRegionClient implements scope.RegionClient purely in-memory: ListRegionSubscriptions
// returns the fixture subscriptions regardless of the request, mirroring fakeIdentityClient's
// pattern above.
type fakeRegionClient struct {
	subscriptions []identity.RegionSubscription
}

func (f *fakeRegionClient) ListRegionSubscriptions(
	_ context.Context,
	_ identity.ListRegionSubscriptionsRequest,
) (identity.ListRegionSubscriptionsResponse, error) {
	return identity.ListRegionSubscriptionsResponse{Items: f.subscriptions}, nil
}

// regionSubscriptionFixture builds one READY identity.RegionSubscription entry -- the shape
// stubRegionClient/fakeRegionClient use to answer scope.ResolveRegions without a live Identity
// API round-trip.
func regionSubscriptionFixture(name string, isHome bool) identity.RegionSubscription {
	return identity.RegionSubscription{
		RegionName:   common.String(name),
		Status:       identity.RegionSubscriptionStatusReady,
		IsHomeRegion: common.Bool(isHome),
	}
}

// stubRegionClient reassigns the newRegionClient package var (see command.go) to return a
// fakeRegionClient subscribed to testHomeRegion (IsHomeRegion=true, the fixture "real home
// region" every test in this file uses) plus every entry in otherRegions (IsHomeRegion=false,
// all READY), following stubScopeIdentityClient's exact reassign-and-cleanup pattern. Every test
// in this file that reaches runPipeline's scope.ResolveRegions call must stub this first, or the
// call would otherwise attempt a live Identity API round-trip through the real newRegionClient
// default. testHomeRegion is not a parameter here (unparam) -- the one test that needs a
// DIFFERENT home region fixture (the divergence regression test) still asserts against
// testHomeRegion as the expected corrected value, so hardcoding it here keeps that assertion and
// this fixture's home-region value structurally impossible to drift apart.
func stubRegionClient(t *testing.T, otherRegions ...string) {
	t.Helper()
	orig := newRegionClient
	t.Cleanup(func() { newRegionClient = orig })

	subs := make([]identity.RegionSubscription, 0, 1+len(otherRegions))
	subs = append(subs, regionSubscriptionFixture(testHomeRegion, true))
	for _, r := range otherRegions {
		subs = append(subs, regionSubscriptionFixture(r, false))
	}

	newRegionClient = func(_ *clients.Cache, _ string) (scope.RegionClient, error) {
		return &fakeRegionClient{subscriptions: subs}, nil
	}
}

// stubNukeRunSleep lowers the nukeRunSleep package var (see command.go) to a near-zero duration,
// following stubVerifyTenancy's exact reassign-and-cleanup pattern -- required by any test that
// exercises a real ~3-round give-up threshold through the full runPipeline (unlike
// resources_test's direct *libnuke.Nuke construction), or it would otherwise take ~10s against
// libnuke's hardcoded 5s production default.
func stubNukeRunSleep(t *testing.T) {
	t.Helper()
	orig := nukeRunSleep
	nukeRunSleep = time.Millisecond
	t.Cleanup(func() { nukeRunSleep = orig })
}

// fakePlanResource is a single fake resource type used by this file's plan-artifact tests. Unlike
// resources_test/dryrun_test.go's fakeResource (registered via raw registry.Register), this
// implements ocinuke.CompartmentScoped (GetCompartmentID) because it is registered via
// ocinuke.Register -- the real production registration path -- so the scopedLister wrapping
// buildNukes/resolveResourceTypes actually exercises in production is exercised here too.
type fakePlanResource struct {
	compartmentID string
	removed       *bool
	// shouldFail makes every Remove() call return an error -- used by the leftover-report and
	// exit-code tests. When false, Remove() succeeds and flips *removed.
	shouldFail bool
}

func (r *fakePlanResource) Remove(_ context.Context) error {
	if r.shouldFail {
		return fmt.Errorf("simulated remove failure")
	}
	*r.removed = true
	return nil
}
func (r *fakePlanResource) UniqueKey() string        { return "fake-plan-resource-1" }
func (r *fakePlanResource) GetCompartmentID() string { return r.compartmentID }
func (r *fakePlanResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// fakePlanLister returns res until it has actually been removed, mirroring dryrun_test.go's
// fakeLister so a real (non-dry-run) removal loop can converge instead of polling forever.
type fakePlanLister struct {
	res *fakePlanResource
}

func (l *fakePlanLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if l.res.removed != nil && *l.res.removed {
		return nil, nil
	}
	return []resource.Resource{l.res}, nil
}

// registerFakePlanResource registers res as "FakePlanResource" via the real ocinuke.Register
// path (inScope unconditionally true -- scope membership is not what these tests are about,
// mirroring resources_test/equivalence_test.go's identical fixture pattern), against a fresh
// registry cleared before and after this test.
func registerFakePlanResource(t *testing.T, res *fakePlanResource) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)
	inScope := func(string) bool { return true }
	ocinuke.Register(&registry.Registration{
		Name:     fakePlanResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: res,
		Lister:   &fakePlanLister{res: res},
	}, inScope, nil)
}

// findEntry returns a pointer to the first entry in entries matching predicate, or nil.
func findEntry(entries []plan.Entry, predicate func(plan.Entry) bool) *plan.Entry {
	for i := range entries {
		if predicate(entries[i]) {
			return &entries[i]
		}
	}
	return nil
}

// readPlanArtifact reads and JSON-decodes the plan.Artifact written to path, failing the test on
// any error -- shared by every test in this file that asserts against a written artifact's
// contents.
func readPlanArtifact(t *testing.T, path string) plan.Artifact {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected a plan artifact file at %s, got error: %v", path, err)
	}
	var artifact plan.Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatalf("failed to parse written plan artifact at %s as JSON: %v", path, err)
	}
	return artifact
}

// TestRunPipeline_WritesPlanArtifactOnDryRun proves PLAN-01 end to end (not only at the pkg/plan
// unit level): a dry run against a fixture with one registered fake resource type writes a
// versioned, canonical JSON plan artifact to --plan-out's equivalent (pipelineOptions.PlanOut, a
// temp-dir path, since runPipeline is called directly without cobra in tests).
func TestRunPipeline_WritesPlanArtifactOnDryRun(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed})

	planOut := filepath.Join(t.TempDir(), "plan.json")

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       planOut,
	})
	if err != nil {
		t.Fatalf("expected nil error for a dry run with a registered fake resource, got: %v", err)
	}

	artifact := readPlanArtifact(t, planOut)
	if artifact.Body.SchemaVersion != plan.SchemaVersion {
		t.Fatalf("expected schema_version %d, got %d", plan.SchemaVersion, artifact.Body.SchemaVersion)
	}
	if len(artifact.Body.Entries) != 1 {
		t.Fatalf("expected exactly one entry in the artifact, got %d: %+v", len(artifact.Body.Entries), artifact.Body.Entries)
	}

	entry := artifact.Body.Entries[0]
	if entry.ResourceType != fakePlanResourceType {
		t.Errorf("expected ResourceType %q, got %q", fakePlanResourceType, entry.ResourceType)
	}
	if entry.ResourceID != "fake-plan-resource-1" {
		t.Errorf("expected ResourceID %q, got %q", "fake-plan-resource-1", entry.ResourceID)
	}
	if entry.CompartmentID != testTargetCompartmentID {
		t.Errorf("expected CompartmentID %q, got %q", testTargetCompartmentID, entry.CompartmentID)
	}
	if entry.Region != testHomeRegion {
		t.Errorf("expected Region %q, got %q", testHomeRegion, entry.Region)
	}
	if entry.State != plan.StateWouldRemove {
		t.Errorf("expected State %q, got %q", plan.StateWouldRemove, entry.State)
	}
}

// TestRunPipeline_SameArtifactSchemaForLeftoverReport proves PLAN-03's "sharing the plan
// artifact's schema" structurally, via one shared writer, not by convention: the identical
// fixture, run destructively against a resource whose Remove() always fails, writes an artifact
// containing a StateLeftover entry using the exact same plan.Entry/plan.Body schema the dry-run
// test above asserts against.
func TestRunPipeline_SameArtifactSchemaForLeftoverReport(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})
	stubNukeRunSleep(t)

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed, shouldFail: true})

	planOut := filepath.Join(t.TempDir(), "plan.json")

	provider := newFakeProvider(t)
	cfg := testConfig()

	// n.Run(ctx)'s own error (libnuke's ~3-round give-up threshold, an always-failing Remove())
	// is expected here and not asserted on directly -- the written artifact is this test's source
	// of truth, exactly like resources_test/leftover_queue_test.go's equivalent proof.
	_ = runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      true,
		Force:         true,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       planOut,
	})

	artifact := readPlanArtifact(t, planOut)
	if artifact.Body.SchemaVersion != plan.SchemaVersion {
		t.Fatalf("expected schema_version %d, got %d", plan.SchemaVersion, artifact.Body.SchemaVersion)
	}

	entry := findEntry(artifact.Body.Entries, func(e plan.Entry) bool { return e.ResourceType == fakePlanResourceType })
	if entry == nil {
		t.Fatalf("expected a FakePlanResource entry in the leftover artifact, entries: %+v", artifact.Body.Entries)
	}
	if entry.State != plan.StateLeftover {
		t.Fatalf("expected State %q, got %q", plan.StateLeftover, entry.State)
	}
	if entry.Reason != scope.ReasonAPIError {
		t.Errorf("expected Reason %q, got %q", scope.ReasonAPIError, entry.Reason)
	}
}

// TestRunPipeline_BlocklistSkipsMergedIntoArtifact proves resolveScope's blocklist-skip data
// (already flowing through every run today, previously only logged) is merged into the plan
// artifact via plan.MergeSkipEvents: target A has blocklisted child B; the written artifact must
// contain a StateSkipped entry for B with Reason ReasonBlocklisted.
func TestRunPipeline_BlocklistSkipsMergedIntoArtifact(t *testing.T) {
	nodeA := "ocid1.compartment.oc1..planblocka"
	nodeB := "ocid1.compartment.oc1..planblockb"

	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(nodeA, testTenancyID),
		compartmentFixture(nodeB, nodeA),
	})

	planOut := filepath.Join(t.TempDir(), "plan.json")

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: []string{nodeB},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: nodeA,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       planOut,
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	artifact := readPlanArtifact(t, planOut)
	entry := findEntry(artifact.Body.Entries, func(e plan.Entry) bool { return e.CompartmentID == nodeB })
	if entry == nil {
		t.Fatalf("expected an entry for blocklisted compartment %s, entries: %+v", nodeB, artifact.Body.Entries)
	}
	if entry.State != plan.StateSkipped {
		t.Errorf("expected State %q, got %q", plan.StateSkipped, entry.State)
	}
	if entry.Reason != scope.ReasonBlocklisted {
		t.Errorf("expected Reason %q, got %q", scope.ReasonBlocklisted, entry.Reason)
	}
}

// TestRunPipeline_UnmatchedFilterWarningLogged proves SAFE-08's "reported as a warning in run
// output" half: a configured filter that matches none of a fixture's resources emits a Warn-level
// log entry naming the resource type and filter property.
func TestRunPipeline_UnmatchedFilterWarningLogged(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed})

	hook := logrustest.NewLocal(logrus.StandardLogger())
	t.Cleanup(hook.Reset)

	planOut := filepath.Join(t.TempDir(), "plan.json")

	provider := newFakeProvider(t)
	cfg := testConfig()
	cfg.Filters = map[string]config.CompartmentFilters{
		testTargetCompartmentID: {Filters: filter.Filters{
			fakePlanResourceType: {{Type: filter.Exact, Property: filterPropertyName, Value: "does-not-exist"}},
		}},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       planOut,
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	found := false
	for _, e := range hook.AllEntries() {
		if e.Level == logrus.WarnLevel && strings.Contains(e.Message, fakePlanResourceType) && strings.Contains(e.Message, filterPropertyName) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a Warn-level log entry naming the unmatched filter's resource type and property, found none")
	}
}

// TestRunPipeline_TenancyRootTargetRefused proves DoD refusal path 1 ("tenancy root as
// target") at the integration layer: scope.RefuseTenancyRoot must reject the run before
// verifyTenancy or newScopeIdentityClient are ever invoked (SAFE-06).
func TestRunPipeline_TenancyRootTargetRefused(t *testing.T) {
	verifyCalled := false
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		verifyCalled = true
		return nil
	})

	scopeCalled := false
	orig := newScopeIdentityClient
	t.Cleanup(func() { newScopeIdentityClient = orig })
	newScopeIdentityClient = func(_ *clients.Cache, _ string) (scope.IdentityClient, error) {
		scopeCalled = true
		return nil, fmt.Errorf("newScopeIdentityClient must not be called for a tenancy-root target")
	}

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: cfg.TenancyID,
		NoDryRun:      false,
	})
	if err == nil {
		t.Fatal("expected a non-nil error when the target is the tenancy root")
	}
	if verifyCalled {
		t.Fatal("verifyTenancy must not be called when the target is the tenancy root -- zero API calls required")
	}
	if scopeCalled {
		t.Fatal("newScopeIdentityClient must not be called when the target is the tenancy root -- zero API calls required")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
	}
	if exitErr.Code != ExitRefused {
		t.Fatalf("expected ExitError.Code = ExitRefused (%d), got %d", ExitRefused, exitErr.Code)
	}
}

// TestRunPipeline_EmptyBlocklistRefused proves DoD refusal path 5's runtime half (distinct
// from Plan 02-03's schema-level `config validate` test): an empty compartment-blocklist is
// refused by runPipeline itself, with zero API calls, since execute() calls config.Load
// directly and never calls config.Validate (SAFE-05).
func TestRunPipeline_EmptyBlocklistRefused(t *testing.T) {
	verifyCalled := false
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		verifyCalled = true
		return nil
	})

	scopeCalled := false
	orig := newScopeIdentityClient
	t.Cleanup(func() { newScopeIdentityClient = orig })
	newScopeIdentityClient = func(_ *clients.Cache, _ string) (scope.IdentityClient, error) {
		scopeCalled = true
		return nil, fmt.Errorf("newScopeIdentityClient must not be called for an empty blocklist")
	}

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: nil,
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
	})
	if err == nil {
		t.Fatal("expected a non-nil error for an empty compartment-blocklist")
	}
	if !strings.Contains(err.Error(), "compartment-blocklist") {
		t.Fatalf("expected error to name compartment-blocklist, got: %v", err)
	}
	if verifyCalled {
		t.Fatal("verifyTenancy must not be called for an empty compartment-blocklist -- zero API calls required")
	}
	if scopeCalled {
		t.Fatal("newScopeIdentityClient must not be called for an empty compartment-blocklist -- zero API calls required")
	}
}

// TestRunPipeline_UnknownTargetCompartmentErrors proves an operator-supplied --compartment-id
// absent from the fetched tenancy tree is refused with a clear error, rather than silently
// resolving to a single-node in-scope set (the gap Tree.Exists closes, per 02-CONTEXT.md).
func TestRunPipeline_UnknownTargetCompartmentErrors(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture("ocid1.compartment.oc1..known", testTenancyID),
	})

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: "ocid1.compartment.oc1..unknown",
		NoDryRun:      false,
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a target compartment absent from the fetched tree")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error to indicate the target compartment was not found, got: %v", err)
	}
}

// TestRunPipeline_EmptyPlanSucceeds proves the full auth(fake) -> tenancy-gate(stubbed) ->
// config -> scope-resolve -> libnuke.New -> RegisterScanner -> Run pipeline completes cleanly
// with zero registered resource types -- the "empty plan, exit success" property (CLI-01,
// reinforcing SAFE-01 end to end). No real network call is made: verifyTenancy and the Phase 2
// newScopeIdentityClient seam are both stubbed before any Identity code is reached.
func TestRunPipeline_EmptyPlanSucceeds(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error for an empty-plan run, got: %v", err)
	}
}

// TestRunPipeline_TenancyMismatchBlocksBeforeListing proves a tenancy mismatch short-circuits
// runPipeline before any scanner is built or registered: verifyTenancy is called as the very
// first statement in runPipeline (see command.go), before homeRegion/clients.Cache/buildNukes
// are ever touched, so a non-nil return from the stub here structurally proves nothing past
// the tenancy gate ran (SAFE-02 reinforced at the CLI-integration layer, complementing plan
// 01-03's unit test of VerifyTenancy itself).
func TestRunPipeline_TenancyMismatchBlocksBeforeListing(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return fmt.Errorf("tenancy mismatch: credentials resolve to a different tenancy")
	})

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
	})
	if err == nil {
		t.Fatal("expected a tenancy mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "tenancy mismatch") {
		t.Fatalf("expected error to contain 'tenancy mismatch', got: %v", err)
	}
}

// TestRunPipeline_BlocklistedAncestorRefused proves DoD refusal path 2 ("target inside a
// blocklisted subtree"): the operator targets C, a compartment nested UNDER blocklisted
// ancestor B (root -> A -> B(blocklisted) -> C), not B itself. Tree.Resolve's downward-only walk
// from C cannot see this; Tree.AncestorBlocklisted is the independent check that catches it.
func TestRunPipeline_BlocklistedAncestorRefused(t *testing.T) {
	nodeA := "ocid1.compartment.oc1..a"
	nodeB := "ocid1.compartment.oc1..b"
	nodeC := "ocid1.compartment.oc1..c"

	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(nodeA, testTenancyID),
		compartmentFixture(nodeB, nodeA),
		compartmentFixture(nodeC, nodeB),
	})

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: []string{nodeB},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: nodeC,
		NoDryRun:      false,
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a target nested under a blocklisted ancestor")
	}
	if !strings.Contains(err.Error(), nodeB) {
		t.Fatalf("expected error to name the blocking ancestor %q, got: %v", nodeB, err)
	}
}

// TestRunPipeline_ResolvesSubtreeAndSkipsBlocklistedDescendant proves DoD refusal path 3
// ("blocklisted OCID nested below the target") through the full pipeline: target A has children
// B (blocklisted, with its own child C) and D (not blocklisted). The run must complete
// successfully with an empty plan, and the resolved scanner set must be exactly {A, D} -- never
// B or C, whose entire subtree is pruned during traversal, not filtered afterward.
func TestRunPipeline_ResolvesSubtreeAndSkipsBlocklistedDescendant(t *testing.T) {
	nodeA := "ocid1.compartment.oc1..a"
	nodeB := "ocid1.compartment.oc1..b"
	nodeC := "ocid1.compartment.oc1..c"
	nodeD := "ocid1.compartment.oc1..d"

	compartments := []identity.Compartment{
		compartmentFixture(nodeA, testTenancyID),
		compartmentFixture(nodeB, nodeA),
		compartmentFixture(nodeC, nodeB),
		compartmentFixture(nodeD, nodeA),
	}

	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, compartments)

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: []string{nodeB},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: nodeA,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error for a successful subtree-resolution run, got: %v", err)
	}

	// runPipeline's own return value only proves "the run succeeded," not which compartments
	// were represented by a scanner. buildScanners is independently callable from this test
	// file (same package), so exercise it directly against the same tree/blocklist inputs
	// runPipeline used, asserting the resolved scanner set is exactly {A, D}.
	tree := scope.BuildTree(compartments)
	blocklist := blocklistSet(cfg.CompartmentBlocklist)
	inScope, skippedBlocklisted, _ := tree.Resolve(nodeA, blocklist)

	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        cfg,
		opts:       pipelineOptions{CompartmentID: nodeA},
		homeRegion: testHomeRegion,
		regions:    []string{testHomeRegion},
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logrus.StandardLogger(),
	}
	scanners, err := buildScanners(pr, buildParameters(&pr.opts), inScope)
	if err != nil {
		t.Fatalf("unexpected error from buildScanners: %v", err)
	}

	gotOwners := make(map[string]struct{}, len(scanners))
	for _, s := range scanners {
		gotOwners[s.Owner] = struct{}{}
	}

	wantOwnerA := fmt.Sprintf("%s/%s", pr.homeRegion, nodeA)
	wantOwnerD := fmt.Sprintf("%s/%s", pr.homeRegion, nodeD)
	if _, ok := gotOwners[wantOwnerA]; !ok {
		t.Fatalf("expected a scanner for A (%s), got owners: %v", wantOwnerA, gotOwners)
	}
	if _, ok := gotOwners[wantOwnerD]; !ok {
		t.Fatalf("expected a scanner for D (%s), got owners: %v", wantOwnerD, gotOwners)
	}
	if len(scanners) != 2 {
		t.Fatalf("expected exactly 2 scanners ({A, D}), got %d: %v", len(scanners), gotOwners)
	}
	if len(skippedBlocklisted) != 1 || skippedBlocklisted[0] != nodeB {
		t.Fatalf("expected skippedBlocklisted to contain exactly [%s], got %v", nodeB, skippedBlocklisted)
	}
}

// TestBuildNukes_AppliesPerCompartmentFilters proves T-02-16: buildNukes constructs one
// *libnuke.Nuke per in-scope compartment, each with its OWN cfg.ResolveFilters(compartmentID)
// result as its Filters -- not one shared Filters value silently applied to every compartment.
// Two compartment OCIDs in cfg.Filters, each with distinct own filters (no presets involved, so
// this exercises buildNukes' direct ResolveFilters call independently of Plan 02-03's
// preset-merge tests).
func TestBuildNukes_AppliesPerCompartmentFilters(t *testing.T) {
	ocidA := "ocid1.compartment.oc1..filtersa"
	ocidB := "ocid1.compartment.oc1..filtersb"

	cfg := testConfig()
	cfg.Filters = map[string]config.CompartmentFilters{
		ocidA: {Filters: filter.Filters{
			resourceTypeBucket: {{Type: filter.Exact, Property: filterPropertyName, Value: "keep-a"}},
		}},
		ocidB: {Filters: filter.Filters{
			"Instance": {{Type: filter.Exact, Property: filterPropertyName, Value: "keep-b"}},
		}},
	}

	wantA, err := cfg.ResolveFilters(ocidA)
	if err != nil {
		t.Fatalf("ResolveFilters(ocidA) returned error: %v", err)
	}
	wantB, err := cfg.ResolveFilters(ocidB)
	if err != nil {
		t.Fatalf("ResolveFilters(ocidB) returned error: %v", err)
	}
	if reflect.DeepEqual(wantA, wantB) {
		t.Fatalf("fixture bug: ocidA and ocidB must resolve to different filter sets, both got %+v", wantA)
	}

	provider := newFakeProvider(t)
	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        cfg,
		opts:       pipelineOptions{CompartmentID: ocidA},
		homeRegion: testHomeRegion,
		regions:    []string{testHomeRegion},
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logrus.StandardLogger(),
	}

	filtersRootOCID := "ocid1.compartment.oc1..filtersroot"
	tree := scope.BuildTree([]identity.Compartment{
		compartmentFixture(ocidA, filtersRootOCID),
		compartmentFixture(ocidB, filtersRootOCID),
	})

	nukes, err := buildNukes(pr, buildParameters(&pr.opts), map[string]struct{}{ocidA: {}, ocidB: {}}, tree)
	if err != nil {
		t.Fatalf("unexpected error from buildNukes: %v", err)
	}
	if len(nukes) != 2 {
		t.Fatalf("expected 2 nukes (one per in-scope compartment), got %d", len(nukes))
	}

	gotFilters := make([]filter.Filters, 0, len(nukes))
	for _, n := range nukes {
		gotFilters = append(gotFilters, n.Filters)
	}
	if reflect.DeepEqual(gotFilters[0], gotFilters[1]) {
		t.Fatalf("expected the two Nukes' Filters to differ -- both were %+v", gotFilters[0])
	}

	foundA, foundB := false, false
	for _, got := range gotFilters {
		if reflect.DeepEqual(got, wantA) {
			foundA = true
		}
		if reflect.DeepEqual(got, wantB) {
			foundB = true
		}
	}
	if !foundA {
		t.Errorf("no returned Nuke had Filters matching ResolveFilters(ocidA) = %+v; got %+v", wantA, gotFilters)
	}
	if !foundB {
		t.Errorf("no returned Nuke had Filters matching ResolveFilters(ocidB) = %+v; got %+v", wantB, gotFilters)
	}
}

// TestRunPipeline_ForceIsLoggedWhenUsed proves T-02-14 (SAFE-07, locked requirement 6): a
// --force run that bypasses the interactive confirmation must leave a record in the run output.
// ForceSleep is set to the Nuke.Validate() minimum (3s) so the test's own confirmTarget-driven
// sleep does not run unreasonably long.
func TestRunPipeline_ForceIsLoggedWhenUsed(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	hook := logrustest.NewLocal(logrus.StandardLogger())
	t.Cleanup(hook.Reset)

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      true,
		Force:         true,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error for a --force destructive run with an empty resolved plan, got: %v", err)
	}

	forceLogged := false
	for _, entry := range hook.AllEntries() {
		if v, ok := entry.Data["force"].(bool); ok && v {
			forceLogged = true
			break
		}
	}
	if !forceLogged {
		t.Fatal("expected a log entry with field force=true when --force is used, found none")
	}
}

// TestRunPipeline_HomeRegionDerivedFromSubscriptionsNotProviderRegion is the load-bearing
// regression guard for 03-RESEARCH.md's Q1 live finding: the fake provider's connection region
// (testConnectionRegionDivergence, eu-frankfurt-1) deliberately differs from the fixture
// tenancy's real home region (testHomeRegion, us-ashburn-1, the RegionClient's IsHomeRegion=true
// entry). cfg.Regions names the provider's connection region as a valid, subscribed, non-home
// entry -- proving the fix does not merely coincide with an unused connection region. runNukesFn
// is stubbed to capture the constructed *libnuke.Nuke slice (while still exercising the real
// runNukes underneath) so every scanner's ListerOpts.HomeRegion can be asserted directly.
func TestRunPipeline_HomeRegionDerivedFromSubscriptionsNotProviderRegion(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t, testConnectionRegionDivergence)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	var capturedNukes []*libnuke.Nuke
	origRunNukesFn := runNukesFn
	t.Cleanup(func() { runNukesFn = origRunNukesFn })
	runNukesFn = func(
		ctx context.Context, nukes []*libnuke.Nuke, noDryRun bool, logger logrus.FieldLogger,
	) (plan.Body, []ocinuke.FilterWarning, bool, error) {
		capturedNukes = nukes
		return runNukes(ctx, nukes, noDryRun, logger)
	}

	provider := newFakeProviderWithRegion(t, testConnectionRegionDivergence)
	cfg := &config.Config{
		TenancyID: testTenancyID,
		// The provider's own connection region is a valid, subscribed, non-home config
		// region -- not the coincidence of an unused value.
		Regions:              []string{testConnectionRegionDivergence},
		CompartmentBlocklist: []string{testBlockedCompartmentID},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if len(capturedNukes) == 0 {
		t.Fatal("expected at least one constructed *libnuke.Nuke")
	}

	inspected := 0
	for _, n := range capturedNukes {
		for _, s := range n.Scanners[ocinuke.CompartmentScope] {
			opts, ok := s.Options.(*ocinuke.ListerOpts)
			if !ok {
				t.Fatalf("scanner Options is not *ocinuke.ListerOpts: %T", s.Options)
			}
			inspected++
			if opts.HomeRegion != testHomeRegion {
				t.Fatalf(
					"expected HomeRegion=%q (ListRegionSubscriptions IsHomeRegion=true value), got %q",
					testHomeRegion, opts.HomeRegion,
				)
			}
			if opts.HomeRegion == testConnectionRegionDivergence {
				t.Fatalf(
					"HomeRegion must never equal the provider's connection region %q",
					testConnectionRegionDivergence,
				)
			}
		}
	}
	if inspected == 0 {
		t.Fatal("expected at least one scanner to inspect")
	}
}

// TestNewCompartmentScanner_SetsVaultDeletionWindowDaysFromSettings proves 05-06-PLAN.md Task 2's
// wiring end to end: cfg.Settings' settings.vault.deletion-window-days value flows through
// runPipeline's VaultDeletionWindowDaysFromSettings parse-once-and-store step into
// pr.vaultDeletionWindowDays, and every scanner's constructed ocinuke.ListerOpts carries it --
// mirroring TestRunPipeline_HomeRegionDerivedFromSubscriptionsNotProviderRegion's own
// runNukesFn-capture shape so every scanner's Options can be inspected directly.
func TestNewCompartmentScanner_SetsVaultDeletionWindowDaysFromSettings(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	var capturedNukes []*libnuke.Nuke
	origRunNukesFn := runNukesFn
	t.Cleanup(func() { runNukesFn = origRunNukesFn })
	runNukesFn = func(
		ctx context.Context, nukes []*libnuke.Nuke, noDryRun bool, logger logrus.FieldLogger,
	) (plan.Body, []ocinuke.FilterWarning, bool, error) {
		capturedNukes = nukes
		return runNukes(ctx, nukes, noDryRun, logger)
	}

	const wantDeletionWindowDays = 14

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: []string{testBlockedCompartmentID},
		Settings: &settings.Settings{
			"vault": &settings.Setting{"deletion-window-days": wantDeletionWindowDays},
		},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if len(capturedNukes) == 0 {
		t.Fatal("expected at least one constructed *libnuke.Nuke")
	}

	inspected := 0
	for _, n := range capturedNukes {
		for _, s := range n.Scanners[ocinuke.CompartmentScope] {
			opts, ok := s.Options.(*ocinuke.ListerOpts)
			if !ok {
				t.Fatalf("scanner Options is not *ocinuke.ListerOpts: %T", s.Options)
			}
			inspected++
			if opts.VaultDeletionWindowDays != wantDeletionWindowDays {
				t.Fatalf(
					"ListerOpts.VaultDeletionWindowDays = %d, want %d (from settings.vault.deletion-window-days)",
					opts.VaultDeletionWindowDays, wantDeletionWindowDays,
				)
			}
		}
	}
	if inspected == 0 {
		t.Fatal("expected at least one scanner to inspect")
	}
}

// TestNewCompartmentScanner_VaultDeletionWindowDaysDefaultsToSeven proves the companion default
// case: a run with no settings.vault block at all still threads the locked default (7) into every
// scanner's ListerOpts, never a zero value.
func TestNewCompartmentScanner_VaultDeletionWindowDaysDefaultsToSeven(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	var capturedNukes []*libnuke.Nuke
	origRunNukesFn := runNukesFn
	t.Cleanup(func() { runNukesFn = origRunNukesFn })
	runNukesFn = func(
		ctx context.Context, nukes []*libnuke.Nuke, noDryRun bool, logger logrus.FieldLogger,
	) (plan.Body, []ocinuke.FilterWarning, bool, error) {
		capturedNukes = nukes
		return runNukes(ctx, nukes, noDryRun, logger)
	}

	const wantDefaultDeletionWindowDays = 7

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{testHomeRegion},
		CompartmentBlocklist: []string{testBlockedCompartmentID},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if len(capturedNukes) == 0 {
		t.Fatal("expected at least one constructed *libnuke.Nuke")
	}

	inspected := 0
	for _, n := range capturedNukes {
		for _, s := range n.Scanners[ocinuke.CompartmentScope] {
			opts, ok := s.Options.(*ocinuke.ListerOpts)
			if !ok {
				t.Fatalf("scanner Options is not *ocinuke.ListerOpts: %T", s.Options)
			}
			inspected++
			if opts.VaultDeletionWindowDays != wantDefaultDeletionWindowDays {
				t.Fatalf(
					"ListerOpts.VaultDeletionWindowDays = %d, want the locked default %d",
					opts.VaultDeletionWindowDays, wantDefaultDeletionWindowDays,
				)
			}
		}
	}
	if inspected == 0 {
		t.Fatal("expected at least one scanner to inspect")
	}
}

// TestRunPipeline_UnsubscribedConfigRegionFailsValidation proves T-03-14: a config region
// absent from the tenancy's live subscriptions fails runPipeline before scope resolution or any
// scanner is constructed. newScopeIdentityClient is stubbed to fail the test if it is ever
// called, proving region validation runs strictly before scope resolution.
func TestRunPipeline_UnsubscribedConfigRegionFailsValidation(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)

	scopeCalled := false
	orig := newScopeIdentityClient
	t.Cleanup(func() { newScopeIdentityClient = orig })
	newScopeIdentityClient = func(_ *clients.Cache, _ string) (scope.IdentityClient, error) {
		scopeCalled = true
		return nil, fmt.Errorf("newScopeIdentityClient must not be called when region validation fails")
	}

	const unsubscribedRegion = "ap-tokyo-1"

	provider := newFakeProvider(t)
	cfg := &config.Config{
		TenancyID:            testTenancyID,
		Regions:              []string{unsubscribedRegion},
		CompartmentBlocklist: []string{testBlockedCompartmentID},
	}

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
	})
	if err == nil {
		t.Fatal("expected a non-nil error for an unsubscribed config region")
	}
	if !strings.Contains(err.Error(), unsubscribedRegion) {
		t.Fatalf("expected error to name the offending region %q, got: %v", unsubscribedRegion, err)
	}
	if scopeCalled {
		t.Fatal("newScopeIdentityClient must not be called when region validation fails -- zero scope-resolution calls required")
	}
}

// TestBuildNukes_FansOutOneScannerPerRegion proves Q2/Q3's answer end to end at the
// buildNukes layer: exactly one *libnuke.Nuke per in-scope compartment (unchanged from Phase
// 2), each gaining exactly len(pr.regions) scanners -- never a 6-Nuke, one-per-(region,
// compartment) shape.
func TestBuildNukes_FansOutOneScannerPerRegion(t *testing.T) {
	compA := "ocid1.compartment.oc1..compa"
	compB := "ocid1.compartment.oc1..compb"
	regions := []string{"r1", "r2", "r3"}

	provider := newFakeProvider(t)
	cfg := testConfig()
	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        cfg,
		opts:       pipelineOptions{CompartmentID: compA, ForceSleep: minForceSleepSeconds},
		homeRegion: testHomeRegion,
		regions:    regions,
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logrus.StandardLogger(),
	}

	fanOutRootOCID := "ocid1.compartment.oc1..fanoutroot"
	tree := scope.BuildTree([]identity.Compartment{
		compartmentFixture(compA, fanOutRootOCID),
		compartmentFixture(compB, fanOutRootOCID),
	})

	nukes, err := buildNukes(pr, buildParameters(&pr.opts), map[string]struct{}{compA: {}, compB: {}}, tree)
	if err != nil {
		t.Fatalf("unexpected error from buildNukes: %v", err)
	}
	if len(nukes) != 2 {
		t.Fatalf("expected exactly 2 nukes (one per compartment, unchanged from Phase 2), got %d", len(nukes))
	}

	wantOwners := map[string][]string{
		compA: {"r1/" + compA, "r2/" + compA, "r3/" + compA},
		compB: {"r1/" + compB, "r2/" + compB, "r3/" + compB},
	}

	seenCompartments := make(map[string]struct{}, 2)
	for _, n := range nukes {
		scanners := n.Scanners[ocinuke.CompartmentScope]
		if len(scanners) != len(regions) {
			t.Fatalf("expected exactly %d scanners on this Nuke (one per resolved region), got %d", len(regions), len(scanners))
		}

		gotOwners := make([]string, 0, len(scanners))
		var compartmentID string
		for _, s := range scanners {
			gotOwners = append(gotOwners, s.Owner)
			opts, ok := s.Options.(*ocinuke.ListerOpts)
			if !ok {
				t.Fatalf("scanner Options is not *ocinuke.ListerOpts: %T", s.Options)
			}
			compartmentID = opts.CompartmentID
		}

		want, ok := wantOwners[compartmentID]
		if !ok {
			t.Fatalf("unexpected compartment %q among constructed nukes", compartmentID)
		}
		sort.Strings(gotOwners)
		wantSorted := append([]string(nil), want...)
		sort.Strings(wantSorted)
		if !reflect.DeepEqual(gotOwners, wantSorted) {
			t.Fatalf("compartment %q: expected owners %v, got %v", compartmentID, wantSorted, gotOwners)
		}
		seenCompartments[compartmentID] = struct{}{}
	}
	if len(seenCompartments) != 2 {
		t.Fatalf("expected exactly 2 distinct compartments represented across all nukes, got %d: %v", len(seenCompartments), seenCompartments)
	}
}

// fakeGlobalResource is the single resource fakeGlobalLister returns when not skipped by the
// Global-geography guard.
type fakeGlobalResource struct{}

func (r *fakeGlobalResource) Remove(_ context.Context) error { return nil }
func (r *fakeGlobalResource) UniqueKey() string              { return "fake-global-1" }

// fakeGlobalLister calls ocinuke.ListerOpts.BeforeList(ocinuke.Global) as its first statement,
// mirroring the contract every Global-geography resource Lister must follow (pkg/ocinuke's own
// doc comment on BeforeList) -- proving the guard actually gates enqueueing end to end through a
// real multi-scanner Nuke.Scan()/Run(), not only at the BeforeList unit level.
type fakeGlobalLister struct{}

func (l *fakeGlobalLister) List(_ context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	return []resource.Resource{&fakeGlobalResource{}}, nil
}

// TestBuildNukes_GlobalGeographyTypeEnqueuesOnlyInHomeRegion proves SCOPE-04/SCOPE-05/T-03-15
// end to end: a Global-geography resource type is attempted from every one of a compartment's
// region scanners but enqueues a queue.Item in exactly one of them (the home region), regardless
// of how many regions are configured -- proven through a real multi-region, multi-scanner
// *libnuke.Nuke.Run(), not only at the ListerOpts.BeforeList unit level.
func TestBuildNukes_GlobalGeographyTypeEnqueuesOnlyInHomeRegion(t *testing.T) {
	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)
	registry.Register(&registry.Registration{
		Name:   "FakeGlobalResource",
		Scope:  ocinuke.CompartmentScope,
		Lister: &fakeGlobalLister{},
	})

	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})

	regions := []string{testHomeRegion, "eu-frankfurt-1", "ap-tokyo-1"}

	provider := newFakeProvider(t)
	cfg := testConfig()
	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        cfg,
		opts:       pipelineOptions{CompartmentID: testTargetCompartmentID, ForceSleep: minForceSleepSeconds},
		homeRegion: testHomeRegion,
		regions:    regions,
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logrus.StandardLogger(),
	}

	globalGeoRootOCID := "ocid1.compartment.oc1..globalgeoroot"
	tree := scope.BuildTree([]identity.Compartment{
		compartmentFixture(testTargetCompartmentID, globalGeoRootOCID),
	})

	params := buildParameters(&pr.opts)
	nukes, err := buildNukes(pr, params, map[string]struct{}{testTargetCompartmentID: {}}, tree)
	if err != nil {
		t.Fatalf("unexpected error from buildNukes: %v", err)
	}
	if len(nukes) != 1 {
		t.Fatalf("expected exactly 1 Nuke, got %d", len(nukes))
	}

	n := nukes[0]
	if len(n.Scanners[ocinuke.CompartmentScope]) != len(regions) {
		t.Fatalf("expected %d scanners (one per resolved region), got %d", len(regions), len(n.Scanners[ocinuke.CompartmentScope]))
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	wantOwner := fmt.Sprintf("%s/%s", testHomeRegion, testTargetCompartmentID)
	globalItems := 0
	for _, item := range n.Queue.GetItems() {
		if item.Type != "FakeGlobalResource" {
			continue
		}
		globalItems++
		if item.Owner != wantOwner {
			t.Fatalf("expected the enqueued item's Owner to be the home-region scanner's owner %q, got %q", wantOwner, item.Owner)
		}
	}
	if globalItems != 1 {
		t.Fatalf("expected exactly 1 FakeGlobalResource queue.Item across all %d region scanners, got %d", len(regions), globalItems)
	}
}

// TestBuildNukes_DeepestFirstOrder proves buildNukes' Task 1 fix end to end: given a two-level
// tree (parent -> child, both in inScope), the returned []*libnuke.Nuke places the deeper
// compartment's own Nuke strictly before its shallower ancestor's -- the property runNukes'
// unchanged sequential Run loop depends on to guarantee bottom-up completion (06-RESEARCH.md
// "The runNukes structural question"). Each Nuke's owning compartment is identified via its own
// registered scanner's Owner string ("<region>/<compartmentID>"), an externally observable
// property distinguishing which compartment each Nuke belongs to.
func TestBuildNukes_DeepestFirstOrder(t *testing.T) {
	rootID := "ocid1.compartment.oc1..deepestfirstroot"
	parentID := "ocid1.compartment.oc1..deepestfirstparent"
	childID := "ocid1.compartment.oc1..deepestfirstchild"

	tree := scope.BuildTree([]identity.Compartment{
		compartmentFixture(parentID, rootID),
		compartmentFixture(childID, parentID),
	})

	provider := newFakeProvider(t)
	cfg := testConfig()
	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        cfg,
		opts:       pipelineOptions{CompartmentID: parentID, ForceSleep: minForceSleepSeconds},
		homeRegion: testHomeRegion,
		regions:    []string{testHomeRegion},
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logrus.StandardLogger(),
	}

	nukes, err := buildNukes(pr, buildParameters(&pr.opts), map[string]struct{}{parentID: {}, childID: {}}, tree)
	if err != nil {
		t.Fatalf("unexpected error from buildNukes: %v", err)
	}
	if len(nukes) != 2 {
		t.Fatalf("expected exactly 2 nukes, got %d", len(nukes))
	}

	ownerOf := func(n *libnuke.Nuke) string {
		scanners := n.Scanners[ocinuke.CompartmentScope]
		if len(scanners) == 0 {
			t.Fatalf("Nuke has no registered scanners")
		}
		return scanners[0].Owner
	}

	wantChildOwner := fmt.Sprintf("%s/%s", testHomeRegion, childID)
	wantParentOwner := fmt.Sprintf("%s/%s", testHomeRegion, parentID)

	if got := ownerOf(nukes[0]); got != wantChildOwner {
		t.Fatalf("expected the FIRST constructed Nuke to belong to the deeper compartment %q, got owner %q", wantChildOwner, got)
	}
	if got := ownerOf(nukes[1]); got != wantParentOwner {
		t.Fatalf("expected the SECOND constructed Nuke to belong to the shallower compartment %q, got owner %q", wantParentOwner, got)
	}
}

// TestClassifyOutcome_TableDriven covers every meaningful (hasUnexplainedError, leftoverCount,
// failOnLeftover) combination classifyOutcome can be called with, including the exact
// combination Task 2 exists to fix: an unexplained internal error takes precedence over a
// simultaneously-present, flag-gated leftover, never the reverse.
func TestClassifyOutcome_TableDriven(t *testing.T) {
	tests := []struct {
		name                string
		hasUnexplainedError bool
		leftoverCount       int
		failOnLeftover      bool
		wantCode            int
		wantIsFailure       bool
	}{
		{
			name:          "clean run, no leftovers, flag unset",
			wantCode:      ExitClean,
			wantIsFailure: false,
		},
		{
			name:          "leftovers present but flag unset -- reported, not fatal",
			leftoverCount: 2,
			wantCode:      ExitClean,
			wantIsFailure: false,
		},
		{
			name:           "leftovers present and flag set -- fatal",
			leftoverCount:  2,
			failOnLeftover: true,
			wantCode:       ExitLeftovers,
			wantIsFailure:  true,
		},
		{
			name:                "unexplained internal error, no leftovers, flag unset -- fatal",
			hasUnexplainedError: true,
			wantCode:            ExitInternal,
			wantIsFailure:       true,
		},
		{
			name:                "unexplained internal error takes precedence over a flag-gated leftover",
			hasUnexplainedError: true,
			leftoverCount:       3,
			failOnLeftover:      true,
			wantCode:            ExitInternal,
			wantIsFailure:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, isFailure := classifyOutcome(tt.hasUnexplainedError, tt.leftoverCount, tt.failOnLeftover)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if isFailure != tt.wantIsFailure {
				t.Errorf("isFailure = %v, want %v", isFailure, tt.wantIsFailure)
			}
		})
	}
}

// TestBuildParameters_SetsMaxWaitRetriesAndWaitOnDependencies proves buildParameters threads
// opts.MaxWaitRetries through to libnuke.Parameters.MaxWaitRetries and sets WaitOnDependencies
// unconditionally true, regardless of input (03-RESEARCH.md's own recommendation, not gated by
// any flag).
func TestBuildParameters_SetsMaxWaitRetriesAndWaitOnDependencies(t *testing.T) {
	params := buildParameters(&pipelineOptions{MaxWaitRetries: 120})
	if params.MaxWaitRetries != 120 {
		t.Errorf("expected MaxWaitRetries=120, got %d", params.MaxWaitRetries)
	}
	if !params.WaitOnDependencies {
		t.Error("expected WaitOnDependencies=true unconditionally")
	}
}

// TestRunPipeline_FailOnLeftoverExitsNonZeroWithMatchingCode proves the --fail-on-leftover half
// of PLAN-04: a resource whose Remove() always fails, run destructively with FailOnLeftover:
// true, produces a non-nil error satisfying errors.As(err, &exitErr) with exitErr.Code ==
// ExitLeftovers.
func TestRunPipeline_FailOnLeftoverExitsNonZeroWithMatchingCode(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})
	stubNukeRunSleep(t)

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed, shouldFail: true})

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:  testTargetCompartmentID,
		NoDryRun:       true,
		Force:          true,
		ForceSleep:     minForceSleepSeconds,
		FailOnLeftover: true,
		PlanOut:        filepath.Join(t.TempDir(), "plan.json"),
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a leftover-containing run with --fail-on-leftover")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
	}
	if exitErr.Code != ExitLeftovers {
		t.Fatalf("expected ExitError.Code = ExitLeftovers (%d), got %d", ExitLeftovers, exitErr.Code)
	}
}

// TestRunPipeline_LeftoverWithoutFailOnLeftoverExitsClean proves locked requirement 5's "still
// reports everything but exits 0" half: the identical fixture and run configuration as the test
// above, with FailOnLeftover: false, must return nil -- the leftover is still recorded in the
// written artifact (Task 1's tests already prove that structurally), only the exit signal
// differs.
func TestRunPipeline_LeftoverWithoutFailOnLeftoverExitsClean(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})
	stubNukeRunSleep(t)

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed, shouldFail: true})

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:  testTargetCompartmentID,
		NoDryRun:       true,
		Force:          true,
		ForceSleep:     minForceSleepSeconds,
		FailOnLeftover: false,
		PlanOut:        filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error (leftover reported but not fatal without --fail-on-leftover), got: %v", err)
	}
}

// scopeProbeOutOfScopeCompartmentID is a fixture OCID deliberately absent from every fixture
// tree TestRunPipeline_InstallsAndRestoresRunContext constructs -- never a member of the
// resolved in-scope set, so ocinuke.CurrentScope must report false for it during the run.
const scopeProbeOutOfScopeCompartmentID = "ocid1.compartment.oc1..scopeprobeoutofscope"

// scopeProbeResourceType is the registry.Registration.Name TestRunPipeline_InstallsAndRestoresRunContext
// registers scopeProbeResource under.
const scopeProbeResourceType = "ScopeProbeResource"

// scopeProbeResource is a minimal ocinuke.CompartmentScoped fixture -- its own Remove()/Filter()
// are never exercised by this test; only its Lister's List() call matters, since that is what
// runs (via a real *libnuke.Nuke.Scan()) while ocinuke.SetRunContext's installed context is live.
type scopeProbeResource struct {
	compartmentID string
}

func (r *scopeProbeResource) Remove(_ context.Context) error { return nil }
func (r *scopeProbeResource) UniqueKey() string              { return "scope-probe-1" }
func (r *scopeProbeResource) GetCompartmentID() string       { return r.compartmentID }
func (r *scopeProbeResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

// scopeProbeLister records what ocinuke.CurrentScope reports for a known in-scope OCID and a
// known out-of-scope OCID at List() time -- the moment a real, production
// scopedLister-wrapped Lister.List() call runs inside runPipeline's installed run context.
type scopeProbeLister struct {
	res                *scopeProbeResource
	observedInScope    *bool
	observedOutOfScope *bool
}

func (l *scopeProbeLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	*l.observedInScope = ocinuke.CurrentScope(testTargetCompartmentID)
	*l.observedOutOfScope = ocinuke.CurrentScope(scopeProbeOutOfScopeCompartmentID)
	return []resource.Resource{l.res}, nil
}

// TestRunPipeline_InstallsAndRestoresRunContext proves Task 3's core wiring claim end to end:
// (a) runPipeline installs an ocinuke.CurrentScope that returns true for a known in-scope OCID
// and false for a known out-of-scope OCID while a real Lister.List() call runs inside it, and
// (b) after runPipeline returns, ocinuke.CurrentScope is back to its fail-closed default (proving
// restore() fired) -- registered via the real ocinuke.Register path (not raw registry.Register),
// with inScope unconditionally true, since scope membership at the scopedLister layer is not
// what this test is about; it is ocinuke.CurrentScope's own installed value that matters here.
func TestRunPipeline_InstallsAndRestoresRunContext(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	registry.ClearRegistry()
	t.Cleanup(registry.ClearRegistry)

	var observedInScope, observedOutOfScope bool
	ocinuke.Register(&registry.Registration{
		Name:     scopeProbeResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &scopeProbeResource{compartmentID: testTargetCompartmentID},
		Lister: &scopeProbeLister{
			res:                &scopeProbeResource{compartmentID: testTargetCompartmentID},
			observedInScope:    &observedInScope,
			observedOutOfScope: &observedOutOfScope,
		},
	}, func(string) bool { return true }, nil)

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !observedInScope {
		t.Error("expected ocinuke.CurrentScope(testTargetCompartmentID) to return true while runPipeline's context was installed")
	}
	if observedOutOfScope {
		t.Error("expected ocinuke.CurrentScope(scopeProbeOutOfScopeCompartmentID) to return false while runPipeline's context was installed")
	}

	if ocinuke.CurrentScope(testTargetCompartmentID) {
		t.Fatal("expected ocinuke.CurrentScope to be restored to its fail-closed default after runPipeline returns -- restore() did not fire")
	}
}

// fakeToggleLister backs the raw registry.Register fixtures
// TestRunPipeline_DeleteCompartmentsFlagAndConfigKeyUnion uses to prove the
// --delete-compartments/settings.compartment.delete Excludes-composition wiring without
// depending on the real "resources" package's registration state -- this file's own tests
// upstream of this one (e.g. TestBuildNukes_GlobalGeographyTypeEnqueuesOnlyInHomeRegion)
// intentionally clear the global registry and never restore it, so a test relying on the real
// "Compartment" registration here would be order-dependent on where it sits in this file. No
// registry.Registration.Resource value is needed -- this test never runs a real scan/removal, it
// only inspects each constructed scanner's own resolved ResourceTypes slice.
type fakeToggleLister struct{}

func (l *fakeToggleLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return nil, nil
}

// TestRunPipeline_DeleteCompartmentsFlagAndConfigKeyUnion proves T-06-03-02: --delete-compartments
// and settings.compartment.delete combine via a pure union (never exclusive-or), and the flag can
// never turn OFF a config key that already enabled the feature. Three subtests -- flag-only,
// config-key-only, neither -- each assert, via the runNukesFn seam this file already uses
// elsewhere (e.g. TestRunPipeline_HomeRegionDerivedFromSubscriptionsNotProviderRegion) to observe
// constructed *libnuke.Nuke state, whether the fixture "Compartment"-named resource type appears
// in the resolved, in-scan resource-type set each Nuke's own scanners carry (present when either
// source enables it, absent when neither does). runNukesFn is stubbed to capture and return
// WITHOUT calling the real runNukes -- this test only needs the resolved resource-type set each
// scanner was configured with, never an actual n.Run(ctx).
func TestRunPipeline_DeleteCompartmentsFlagAndConfigKeyUnion(t *testing.T) {
	testCases := []struct {
		name          string
		flag          bool
		configEnabled bool
		wantIncluded  bool
	}{
		{name: "flag-only", flag: true, configEnabled: false, wantIncluded: true},
		{name: "config-key-only", flag: false, configEnabled: true, wantIncluded: true},
		{name: "neither", flag: false, configEnabled: false, wantIncluded: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			registry.ClearRegistry()
			t.Cleanup(registry.ClearRegistry)
			registry.Register(&registry.Registration{
				Name:   compartmentResourceTypeName,
				Scope:  ocinuke.CompartmentScope,
				Lister: &fakeToggleLister{},
			})
			registry.Register(&registry.Registration{
				Name:   "OtherFakeResource",
				Scope:  ocinuke.CompartmentScope,
				Lister: &fakeToggleLister{},
			})

			stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
				return nil
			})
			stubRegionClient(t)
			stubScopeIdentityClient(t, []identity.Compartment{
				compartmentFixture(testTargetCompartmentID, testTenancyID),
			})

			var capturedNukes []*libnuke.Nuke
			origRunNukesFn := runNukesFn
			t.Cleanup(func() { runNukesFn = origRunNukesFn })
			runNukesFn = func(
				_ context.Context, nukes []*libnuke.Nuke, _ bool, _ logrus.FieldLogger,
			) (plan.Body, []ocinuke.FilterWarning, bool, error) {
				capturedNukes = nukes
				return plan.Body{SchemaVersion: plan.SchemaVersion}, nil, false, nil
			}

			provider := newFakeProvider(t)
			cfg := &config.Config{
				TenancyID:            testTenancyID,
				Regions:              []string{testHomeRegion},
				CompartmentBlocklist: []string{testBlockedCompartmentID},
			}
			if tc.configEnabled {
				cfg.Settings = &settings.Settings{
					"compartment": &settings.Setting{"delete": true},
				}
			}

			err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
				CompartmentID: testTargetCompartmentID,
				NoDryRun:      false,
				ForceSleep:    minForceSleepSeconds,
				PlanOut:       filepath.Join(t.TempDir(), "plan.json"),
				// MaxWaitRetries=180 satisfies the 15-minute retry floor in the flag-only/
				// config-key-only subtests (where the resolved union is enabled); it is simply
				// unused in the "neither" subtest, where the floor is never checked at all.
				MaxWaitRetries:     180,
				DeleteCompartments: tc.flag,
			})
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}

			if len(capturedNukes) == 0 {
				t.Fatal("expected at least one constructed *libnuke.Nuke")
			}

			gotIncluded := false
			for _, n := range capturedNukes {
				for _, s := range n.Scanners[ocinuke.CompartmentScope] {
					for _, rt := range s.ResourceTypes {
						if rt == compartmentResourceTypeName {
							gotIncluded = true
						}
					}
				}
			}
			if gotIncluded != tc.wantIncluded {
				t.Fatalf("Compartment in resolved resource-type set = %v, want %v", gotIncluded, tc.wantIncluded)
			}
		})
	}
}

// TestRunPipeline_RetryFloorRefusedBelowMinimum proves T-06-03-03: when --delete-compartments is
// enabled and MaxWaitRetries * nukeRunSleep is under the 15-minute floor, runPipeline refuses
// before any API call, mirroring TestRunPipeline_EmptyBlocklistRefused's exact zero-API-call
// refusal assertion style, naming --max-wait-retries in the returned error.
func TestRunPipeline_RetryFloorRefusedBelowMinimum(t *testing.T) {
	verifyCalled := false
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		verifyCalled = true
		return nil
	})

	scopeCalled := false
	orig := newScopeIdentityClient
	t.Cleanup(func() { newScopeIdentityClient = orig })
	newScopeIdentityClient = func(_ *clients.Cache, _ string) (scope.IdentityClient, error) {
		scopeCalled = true
		return nil, fmt.Errorf("newScopeIdentityClient must not be called when the retry floor refuses the run")
	}

	provider := newFakeProvider(t)
	cfg := testConfig()

	// 10 rounds * 5s (nukeRunSleep's production default, unstubbed here) = 50s, well under the
	// 15-minute floor --delete-compartments requires.
	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:      testTargetCompartmentID,
		NoDryRun:           false,
		MaxWaitRetries:     10,
		DeleteCompartments: true,
	})
	if err == nil {
		t.Fatal("expected a non-nil error when --delete-compartments is enabled below the retry floor")
	}
	if !strings.Contains(err.Error(), "--max-wait-retries") {
		t.Fatalf("expected error to name --max-wait-retries, got: %v", err)
	}
	if verifyCalled {
		t.Fatal("verifyTenancy must not be called when the retry floor refuses the run -- zero API calls required")
	}
	if scopeCalled {
		t.Fatal("newScopeIdentityClient must not be called when the retry floor refuses the run -- zero API calls required")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
	}
	if exitErr.Code != ExitRefused {
		t.Fatalf("expected ExitError.Code = ExitRefused (%d), got %d", ExitRefused, exitErr.Code)
	}
}

// TestRunPipeline_RetryFloorNotEnforcedWhenDeleteCompartmentsOff is Pitfall C's non-vacuousness
// control: the SAME low MaxWaitRetries value that TestRunPipeline_RetryFloorRefusedBelowMinimum
// proved gets refused does NOT trigger a refusal here, because --delete-compartments is off --
// proving the floor is genuinely gated to the opt-in, never a silent global tightening of every
// run's wait budget.
func TestRunPipeline_RetryFloorNotEnforcedWhenDeleteCompartmentsOff(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	provider := newFakeProvider(t)
	cfg := testConfig()

	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:  testTargetCompartmentID,
		NoDryRun:       false,
		ForceSleep:     minForceSleepSeconds,
		MaxWaitRetries: 10,
		PlanOut:        filepath.Join(t.TempDir(), "plan.json"),
		// DeleteCompartments left at its zero value (false) -- the opt-in is OFF.
	})
	if err != nil {
		t.Fatalf("expected nil error: the retry floor must not be enforced when --delete-compartments is off, got: %v", err)
	}
}

// TestResolveDeleteCompartmentsOptIn_ZeroMaxWaitRetriesSatisfiesFloor proves 06-REVIEW.md WR-02's
// fix directly against resolveDeleteCompartmentsOptIn (zero API calls, no runPipeline plumbing
// needed): MaxWaitRetries == 0 is libnuke's own "retry indefinitely" sentinel (verified against
// the pinned github.com/ekristen/libnuke@v1.3.0 source, pkg/nuke/nuke.go's handleWaiting), which
// trivially exceeds the 15-minute floor and must be ACCEPTED, not refused as a zero-length
// budget.
func TestResolveDeleteCompartmentsOptIn_ZeroMaxWaitRetriesSatisfiesFloor(t *testing.T) {
	cfg := testConfig()
	opts := &pipelineOptions{
		DeleteCompartments: true,
		MaxWaitRetries:     0,
	}

	if err := resolveDeleteCompartmentsOptIn(cfg, opts); err != nil {
		t.Fatalf("expected nil error for MaxWaitRetries=0 (libnuke's unlimited-retry sentinel), got: %v", err)
	}
}

// TestResolveDeleteCompartmentsOptIn_NonzeroBelowFloorStillRefused is the non-vacuousness control
// for the fix above: a small NONZERO MaxWaitRetries must still be refused exactly as before --
// proving the != 0 guard only carves out the unlimited sentinel, not the floor check itself.
func TestResolveDeleteCompartmentsOptIn_NonzeroBelowFloorStillRefused(t *testing.T) {
	cfg := testConfig()
	opts := &pipelineOptions{
		DeleteCompartments: true,
		MaxWaitRetries:     10, // 10 * 5s (nukeRunSleep's production default) = 50s, under the 15m floor.
	}

	err := resolveDeleteCompartmentsOptIn(cfg, opts)
	if err == nil {
		t.Fatal("expected a non-nil error for a small nonzero MaxWaitRetries below the retry floor")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
	}
	if exitErr.Code != ExitRefused {
		t.Fatalf("expected ExitError.Code = ExitRefused (%d), got %d", ExitRefused, exitErr.Code)
	}
}

// findRunCommand locates the "run" command registered by this package's own init() via
// naviteqcommon.RegisterCommand -- the same live *cobra.Command execute()'s RunE is wired to,
// so Set()/GetString() round-trip through the exact flag definitions init() registers, without
// this test file duplicating that registration.
func findRunCommand(t *testing.T) *cobra.Command {
	t.Helper()
	for _, cmd := range naviteqcommon.GetCommands() {
		if cmd.Name() == "run" {
			return cmd
		}
	}
	t.Fatal("run command not registered via naviteqcommon.RegisterCommand")
	return nil
}

// TestParseRunFlags_ReadsOIDCFlags proves --oidc-region/--oidc-audience round-trip through
// parseRunFlags into runFlags.oidcRegion/oidcAudience correctly.
func TestParseRunFlags_ReadsOIDCFlags(t *testing.T) {
	cmd := findRunCommand(t)

	const wantRegion = "us-ashburn-1"
	const wantAudience = "https://idcs-test.identity.oraclecloud.com"

	if err := cmd.Flags().Set("oidc-region", wantRegion); err != nil {
		t.Fatalf("setting --oidc-region: %v", err)
	}
	if err := cmd.Flags().Set("oidc-audience", wantAudience); err != nil {
		t.Fatalf("setting --oidc-audience: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("oidc-region", "")
		_ = cmd.Flags().Set("oidc-audience", "")
	})

	flags, err := parseRunFlags(cmd)
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if flags.oidcRegion != wantRegion {
		t.Fatalf("oidcRegion = %q, want %q", flags.oidcRegion, wantRegion)
	}
	if flags.oidcAudience != wantAudience {
		t.Fatalf("oidcAudience = %q, want %q", flags.oidcAudience, wantAudience)
	}
}

// TestRunCommand_HasNoOIDCClientSecretFlag proves the client secret is env-var-only by
// construction: no --oidc-client-secret (or --oidc-client-id/--oidc-domain-url) flag exists on
// the run command at all, so it can never appear in a process listing or an echoed CI command
// line.
func TestRunCommand_HasNoOIDCClientSecretFlag(t *testing.T) {
	cmd := findRunCommand(t)

	for _, name := range []string{"oidc-client-secret", "oidc-client-id", "oidc-domain-url"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Fatalf("--%s flag exists; this value must be env-var-only (OCI_OIDC_*)", name)
		}
	}
}

// TestRunPipeline_ApprovedPlanMatchProceedsToRealApply proves 07-CONTEXT.md's plan-integrity
// gate's match path end to end: a plan-mode run against a fixture writes a real artifact; a
// later apply-mode run against the IDENTICAL, unchanged fixture, with --approved-plan pointing at
// that artifact, hashes identically on its forced rescan and proceeds to the real removal pass.
func TestRunPipeline_ApprovedPlanMatchProceedsToRealApply(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})
	stubNukeRunSleep(t)

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed})

	provider := newFakeProvider(t)
	cfg := testConfig()

	approvedPlanPath := filepath.Join(t.TempDir(), "plan.json")
	err := runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID: testTargetCompartmentID,
		NoDryRun:      false,
		ForceSleep:    minForceSleepSeconds,
		PlanOut:       approvedPlanPath,
	})
	if err != nil {
		t.Fatalf("expected nil error producing the plan-mode artifact, got: %v", err)
	}

	err = runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:    testTargetCompartmentID,
		NoDryRun:         true,
		Force:            true,
		ForceSleep:       minForceSleepSeconds,
		ApprovedPlanPath: approvedPlanPath,
		MaxPlanAge:       time.Hour,
		PlanOut:          filepath.Join(t.TempDir(), "apply-plan.json"),
	})
	if err != nil {
		t.Fatalf("expected nil error for an apply run whose forced rescan matches the approved plan, got: %v", err)
	}
	if !removed {
		t.Fatal("expected the real removal pass to have run and removed the fixture resource")
	}
}

// TestRunPipeline_ApprovedPlanMismatchRefusesBeforeRemoval proves the mismatch path: an approved
// artifact hand-constructed with a resource ID that does not match the live fixture's real scan
// result causes the apply run to abort with ExitRefused, and -- the ordering constraint this test
// exists to prove, not just the outcome -- the fixture resource is never removed.
func TestRunPipeline_ApprovedPlanMismatchRefusesBeforeRemoval(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		return nil
	})
	stubRegionClient(t)
	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
	})

	var removed bool
	registerFakePlanResource(t, &fakePlanResource{compartmentID: testTargetCompartmentID, removed: &removed})

	fabricatedBody := plan.Body{
		SchemaVersion: plan.SchemaVersion,
		Entries: []plan.Entry{{
			ResourceType:  fakePlanResourceType,
			ResourceID:    "fake-plan-resource-DOES-NOT-EXIST",
			Region:        testHomeRegion,
			CompartmentID: testTargetCompartmentID,
			State:         plan.StateWouldRemove,
		}},
	}
	fabricatedArtifact, err := plan.NewArtifact(fabricatedBody)
	if err != nil {
		t.Fatalf("building fabricated approved artifact: %v", err)
	}
	approvedPlanPath := writeApprovedPlanFixture(t, fabricatedArtifact)

	provider := newFakeProvider(t)
	cfg := testConfig()

	err = runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:    testTargetCompartmentID,
		NoDryRun:         true,
		Force:            true,
		ForceSleep:       minForceSleepSeconds,
		ApprovedPlanPath: approvedPlanPath,
		MaxPlanAge:       time.Hour,
		PlanOut:          filepath.Join(t.TempDir(), "apply-plan.json"),
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a mismatching approved plan")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
	}
	if exitErr.Code != ExitRefused {
		t.Fatalf("expected ExitError.Code = ExitRefused (%d), got %d", ExitRefused, exitErr.Code)
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected error message to contain %q, got: %v", "mismatch", err)
	}
	if removed {
		t.Fatal("expected the fixture resource to NOT be removed -- the mismatch must abort before any Remove() call")
	}
}

// TestRunPipeline_ApprovedPlanStaleRefusedWithZeroAPICalls proves the staleness path structurally,
// not by inference: an approved artifact older than --max-plan-age is refused before verifyTenancy
// -- the stub below calls t.Fatal if invoked at all, so a regression that lets a stale artifact
// reach the network fails this test immediately rather than merely returning a wrong-but-silent
// result.
func TestRunPipeline_ApprovedPlanStaleRefusedWithZeroAPICalls(t *testing.T) {
	stubVerifyTenancy(t, func(_ context.Context, _ common.ConfigurationProvider, _ string) error {
		t.Fatal("verifyTenancy must not run for a stale approved plan")
		return nil
	})

	body := plan.Body{SchemaVersion: plan.SchemaVersion}
	hash, err := plan.Hash(body)
	if err != nil {
		t.Fatalf("computing fixture hash: %v", err)
	}
	staleArtifact := plan.Artifact{Body: body, Hash: hash, GeneratedAt: time.Now().UTC().Add(-48 * time.Hour)}
	approvedPlanPath := writeApprovedPlanFixture(t, staleArtifact)

	provider := newFakeProvider(t)
	cfg := testConfig()

	err = runPipeline(context.Background(), provider, cfg, &pipelineOptions{
		CompartmentID:    testTargetCompartmentID,
		NoDryRun:         true,
		Force:            true,
		ForceSleep:       minForceSleepSeconds,
		ApprovedPlanPath: approvedPlanPath,
		MaxPlanAge:       24 * time.Hour,
		PlanOut:          filepath.Join(t.TempDir(), "apply-plan.json"),
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a 48-hour-old approved plan against a 24h max age")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
	}
	if exitErr.Code != ExitRefused {
		t.Fatalf("expected ExitError.Code = ExitRefused (%d), got %d", ExitRefused, exitErr.Code)
	}
}

// TestBuildArtifact_HashIndependentOfCallOrderAndGeneratedAt reconfirms 07-CONTEXT.md's "Claude's
// Discretion" instruction: the canonical hash property Phase 3 built (two artifacts from the same
// logical Body hash identically, independent of GeneratedAt) must still hold before this plan
// builds the approval gate on top of it. buildArtifact is called twice with logically identical
// body/skipEvents inputs, entries in different slice order between the two calls.
func TestBuildArtifact_HashIndependentOfCallOrderAndGeneratedAt(t *testing.T) {
	entryA := plan.Entry{
		ResourceType: "Vcn", ResourceID: "vcn-1", Region: testHomeRegion, CompartmentID: testTargetCompartmentID,
		State: plan.StateWouldRemove,
	}
	entryB := plan.Entry{
		ResourceType: resourceTypeBucket, ResourceID: "bucket-1", Region: testHomeRegion, CompartmentID: testTargetCompartmentID,
		State: plan.StateWouldRemove,
	}

	bodyA := plan.Body{SchemaVersion: plan.SchemaVersion, Entries: []plan.Entry{entryA, entryB}}
	bodyB := plan.Body{SchemaVersion: plan.SchemaVersion, Entries: []plan.Entry{entryB, entryA}}

	first, err := buildArtifact(bodyA, nil)
	if err != nil {
		t.Fatalf("buildArtifact(bodyA, nil): %v", err)
	}
	second, err := buildArtifact(bodyB, nil)
	if err != nil {
		t.Fatalf("buildArtifact(bodyB, nil): %v", err)
	}

	if first.Hash != second.Hash {
		t.Errorf("expected identical hashes for the same logical body regardless of call order/GeneratedAt, got %q vs %q",
			first.Hash, second.Hash)
	}
}

// TestWarnIfHomeRegionOutOfScope covers 05-UAT.md's second gap from both directions: the warning
// fires when the tenancy's home region is absent from the run's regions, and stays silent when it
// is present. The silent case is the one that matters -- a warning on every ordinary multi-region
// run would be noise an operator learns to ignore, which is how the original defect (a Debug-level
// skip nobody reads) came about in the first place.
func TestWarnIfHomeRegionOutOfScope(t *testing.T) {
	tests := []struct {
		name     string
		regions  []string
		home     string
		wantWarn bool
	}{
		{
			name:     "home region absent -- global types covered nowhere",
			regions:  []string{testConnectionRegionDivergence},
			home:     testHomeRegion,
			wantWarn: true,
		},
		{
			name:     "home region present alongside others",
			regions:  []string{testConnectionRegionDivergence, testHomeRegion},
			home:     testHomeRegion,
			wantWarn: false,
		},
		{
			name:     "home region is the only region",
			regions:  []string{testHomeRegion},
			home:     testHomeRegion,
			wantWarn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, hook := logrustest.NewNullLogger()
			logger.SetLevel(logrus.WarnLevel)

			warnIfHomeRegionOutOfScope(logger, tt.regions, tt.home)

			entries := hook.AllEntries()
			if !tt.wantWarn {
				if len(entries) != 0 {
					t.Fatalf("expected no log entry for regions=%v home=%q, got %d: %v",
						tt.regions, tt.home, len(entries), entries[0].Message)
				}
				return
			}

			if len(entries) != 1 {
				t.Fatalf("expected exactly one log entry for regions=%v home=%q, got %d",
					tt.regions, tt.home, len(entries))
			}
			entry := entries[0]
			if entry.Level != logrus.WarnLevel {
				t.Errorf("log level = %v, want Warn -- the default log level is info, so anything "+
					"below Warn reproduces the silence this warning exists to fix", entry.Level)
			}
			if got := entry.Data["home_region"]; got != tt.home {
				t.Errorf("home_region field = %v, want %q", got, tt.home)
			}
			for _, typ := range globalGeographyTypes {
				if !strings.Contains(fmt.Sprint(entry.Data["uncovered_types"]), typ) {
					t.Errorf("uncovered_types = %v, want it to name %q -- an operator needs to know "+
						"which types went unscanned, not merely that some did",
						entry.Data["uncovered_types"], typ)
				}
			}
		})
	}
}

// compartmentFixtureInState is compartmentFixture with the lifecycle state as a parameter --
// NR-787's refusal path is entirely about which non-ACTIVE state the target is in, so the state
// stops being a fixed detail here.
func compartmentFixtureInState(id, parent string, state identity.CompartmentLifecycleStateEnum) identity.Compartment {
	c := compartmentFixture(id, parent)
	c.LifecycleState = state
	return c
}

// TestResolveScope_NonActiveTargetRefused is NR-787's third defect at the layer it happens on. A
// non-ACTIVE target is pruned by Tree.Resolve while tree.Exists stays true, so resolveScope used
// to raise nothing, the run wrote a zero-entry plan and exited 0 -- silently, at --log-level
// debug too, and indistinguishable from a compartment that was scanned and found clean. Observed
// live against a DELETING compartment on 2026-09-01.
//
// CREATING is in the table on purpose: a compartment seconds old also scans empty, and in a
// seeded CI fixture that would report a clean teardown of something never examined.
func TestResolveScope_NonActiveTargetRefused(t *testing.T) {
	for _, state := range []identity.CompartmentLifecycleStateEnum{
		identity.CompartmentLifecycleStateDeleting,
		identity.CompartmentLifecycleStateDeleted,
		identity.CompartmentLifecycleStateCreating,
		identity.CompartmentLifecycleStateInactive,
	} {
		t.Run(string(state), func(t *testing.T) {
			stubScopeIdentityClient(t, []identity.Compartment{
				compartmentFixtureInState(testTargetCompartmentID, testTenancyID, state),
			})

			provider := newFakeProvider(t)
			cfg := testConfig()
			pr := &pipelineRun{
				ctx:        context.Background(),
				provider:   provider,
				cfg:        cfg,
				opts:       pipelineOptions{CompartmentID: testTargetCompartmentID},
				homeRegion: testHomeRegion,
				regions:    []string{testHomeRegion},
				clients:    clients.New(provider, common.DefaultRetryPolicy()),
				logger:     logrus.StandardLogger(),
			}

			inScope, _, _, err := resolveScope(pr)
			if err == nil {
				t.Fatalf("a %s target resolved to %d in-scope compartments and no error; an empty "+
					"in-scope set must never be indistinguishable from a clean scan", state, len(inScope))
			}

			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("expected err to satisfy errors.As(err, &exitErr), got %T: %v", err, err)
			}
			if exitErr.Code != ExitRefused {
				t.Fatalf("expected ExitError.Code = ExitRefused (%d), got %d", ExitRefused, exitErr.Code)
			}
			if !strings.Contains(err.Error(), string(state)) {
				t.Errorf("the refusal must name the lifecycle state it found; %q does not contain %q",
					err.Error(), state)
			}
		})
	}
}

// TestResolveScope_NonActiveDescendantEarnsASkipEvent is the other half: pruning a non-ACTIVE
// descendant stays correct (ACTIVE-only default), but the prune now records itself. Blocklisted
// subtrees already did; non-ACTIVE ones were the only silent prune in the walk.
func TestResolveScope_NonActiveDescendantEarnsASkipEvent(t *testing.T) {
	const deletingChild = "ocid1.compartment.oc1..deleting-child"

	stubScopeIdentityClient(t, []identity.Compartment{
		compartmentFixture(testTargetCompartmentID, testTenancyID),
		compartmentFixtureInState(deletingChild, testTargetCompartmentID,
			identity.CompartmentLifecycleStateDeleting),
	})

	provider := newFakeProvider(t)
	cfg := testConfig()
	pr := &pipelineRun{
		ctx:        context.Background(),
		provider:   provider,
		cfg:        cfg,
		opts:       pipelineOptions{CompartmentID: testTargetCompartmentID},
		homeRegion: testHomeRegion,
		regions:    []string{testHomeRegion},
		clients:    clients.New(provider, common.DefaultRetryPolicy()),
		logger:     logrus.StandardLogger(),
	}

	inScope, _, skipEvents, err := resolveScope(pr)
	if err != nil {
		t.Fatalf("an ACTIVE target with a DELETING child must resolve, got: %v", err)
	}
	if _, ok := inScope[deletingChild]; ok {
		t.Error("a DELETING descendant must stay out of the in-scope set")
	}

	var found *scope.SkipEvent
	for i := range skipEvents {
		if skipEvents[i].CompartmentID == deletingChild {
			found = &skipEvents[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no skip event for the pruned DELETING descendant; the artifact would not record "+
			"that a subtree went unexamined. Events: %+v", skipEvents)
	}
	if found.Reason != scope.ReasonCompartmentNotActive {
		t.Errorf("skip event reason = %q, want %q -- compartment-not-empty means a delete was "+
			"attempted, which this is not", found.Reason, scope.ReasonCompartmentNotActive)
	}
	if !strings.Contains(found.Detail, string(identity.CompartmentLifecycleStateDeleting)) {
		t.Errorf("skip event detail = %q, want it to name the DELETING state", found.Detail)
	}
}

// TestLogLeftovers_NamesTheAPIsOwnWords proves the per-compartment half of NR-787's third defect.
// libnuke's own per-round line prints the bare word "failed" and never item.Reason, and there is
// no hook in front of it -- so this is where an operator finds out what OCI actually said, while
// the run is still going rather than twenty minutes later in the final table.
func TestLogLeftovers_NamesTheAPIsOwnWords(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)

	const detail = "HTTP 409 BucketNotEmpty: The bucket is not empty."
	logLeftovers(logger, []plan.Entry{
		{
			ResourceType: resourceTypeBucket, ResourceID: "b", CompartmentID: testTargetCompartmentID,
			Region: testHomeRegion, State: plan.StateLeftover,
			Reason: scope.ReasonAPIError, Detail: detail,
		},
		{
			ResourceType: "Vcn", ResourceID: "v", CompartmentID: testTargetCompartmentID,
			Region: testHomeRegion, State: plan.StateRemoved,
		},
	})

	var found *logrus.Entry
	for _, e := range hook.AllEntries() {
		if e.Level == logrus.WarnLevel && e.Data["resource_id"] == "b" {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a Warn entry for the leftover bucket, got %d entries", len(hook.AllEntries()))
	}
	if found.Data["detail"] != detail {
		t.Errorf("log detail = %v, want %q -- the reason alone reads as %q and says nothing",
			found.Data["detail"], detail, scope.ReasonAPIError)
	}
	if found.Data["reason"] != string(scope.ReasonAPIError) {
		t.Errorf("log reason = %v, want %q", found.Data["reason"], scope.ReasonAPIError)
	}

	// A removed resource is not a leftover and must not be logged as one.
	for _, e := range hook.AllEntries() {
		if e.Data["resource_id"] == "v" {
			t.Errorf("a StateRemoved entry was logged as a leftover: %+v", e.Data)
		}
	}
}
