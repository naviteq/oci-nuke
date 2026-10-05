// Package run registers the `run` command (alias `nuke`) -- the integration point that wires
// authentication, config loading, the tenancy gate, and libnuke together. It is split into
// execute (cobra glue: flag parsing, provider resolution, config load) and runPipeline (the
// actual auth -> tenancy-gate -> scope-resolve -> libnuke pipeline) so the pipeline can be
// exercised in tests against a fake, non-network provider (see command_test.go) without live
// OCI credentials.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/ekristen/libnuke/pkg/utils"
	ocicommon "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/naviteq/oci-nuke/pkg/clients"
	"github.com/naviteq/oci-nuke/pkg/commands/global"
	"github.com/naviteq/oci-nuke/pkg/common"
	"github.com/naviteq/oci-nuke/pkg/config"
	"github.com/naviteq/oci-nuke/pkg/ociauth"
	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"

	_ "github.com/naviteq/oci-nuke/resources" // empty in Phase 1; populated from Phase 4
)

// verifyTenancy is a package-level seam so tests can swap in a stub without reaching into
// pkg/ociauth's unexported internals, mirroring the newIdentityClient pattern pkg/ociauth
// uses for the same reason.
var verifyTenancy = ociauth.VerifyTenancy

// newScopeIdentityClient is a package-level seam so tests can swap in a fake scope.IdentityClient
// without a live Identity API round-trip, mirroring verifyTenancy's pattern above.
// identity.IdentityClient (the concrete client clients.Cache.Identity returns) structurally
// satisfies scope.IdentityClient's narrow two-method interface, so this is a type-narrowing
// wrapper, not a new client.
var newScopeIdentityClient = func(cache *clients.Cache, region string) (scope.IdentityClient, error) {
	return cache.Identity(region)
}

// newRegionClient is a package-level seam so tests can swap in a fake scope.RegionClient
// without a live Identity API round-trip, mirroring newScopeIdentityClient's pattern above.
// identity.IdentityClient (the concrete client clients.Cache.Identity returns) structurally
// satisfies scope.RegionClient's one-method interface, so this is the same type-narrowing
// wrapper, not a new client.
var newRegionClient = func(cache *clients.Cache, region string) (scope.RegionClient, error) {
	return cache.Identity(region)
}

// nukeRunSleep is a package-level seam for buildNukes' n.SetRunSleep call, mirroring
// verifyTenancy/newScopeIdentityClient/newRegionClient's reassign-and-cleanup pattern above.
// Production always uses the real 5s value. A test exercising a real ~3-round give-up threshold
// through the full runPipeline (unlike resources_test's direct *libnuke.Nuke construction, which
// already lowers SetRunSleep per-instance) would otherwise take ~10s per test against this
// hardcoded production default -- this seam lets such tests lower it the same way
// resources_test/leftover_queue_test.go already does for its own directly-constructed Nuke.
var nukeRunSleep = 5 * time.Second

// pipelineOptions carries the run-specific flag values runPipeline needs, decoupled from
// cobra so runPipeline is directly callable from tests.
type pipelineOptions struct {
	CompartmentID string
	NoDryRun      bool
	Quiet         bool
	Includes      []string
	Excludes      []string
	Force         bool
	ForceSleep    int
	// PlanOut is the path the single, canonical, versioned plan/leftover artifact (PLAN-01/
	// PLAN-03) is written to on every run, dry or destructive.
	PlanOut string
	// MaxWaitRetries bounds libnuke.Parameters.MaxWaitRetries -- the total number of ~runSleep
	// rounds a resource can stay ItemStateWaiting before handleWaiting() gives up. Unset (0)
	// means unlimited (libnuke's own default); production always sets this via the
	// --max-wait-retries flag (default 120), never leaves it at the unbounded default.
	MaxWaitRetries int
	// FailOnLeftover gates ONLY the exit code classifyOutcome produces -- the plan/leftover
	// artifact is written and reported unconditionally regardless of this flag (locked
	// requirement 5: "a leftover-containing run still reports everything but exits 0" when unset).
	FailOnLeftover bool
	// DeleteCompartments opts into scanning/removing the Compartment resource type itself --
	// off by default (06-CONTEXT.md). This field carries ONLY the --delete-compartments CLI
	// flag's own value; runPipeline unions it with settings.compartment.delete
	// (ocinuke.DeleteCompartmentsEnabledFromSettings) immediately on entry, mutating this same
	// field in place, so every downstream reader (resolveResourceTypes, the retry-floor check)
	// sees the resolved union, never the flag alone. The flag can only ever turn this ON: because
	// the safe value is false and flag absence is indistinguishable from an explicit false, there
	// is no way for an absent/false flag to turn OFF a config key that already enabled it.
	DeleteCompartments bool
	// ApprovedPlanPath is --approved-plan's value: when non-empty, this destructive run refuses
	// unless a fresh, forced-dry-run rescan of the identical scope hashes identically to the
	// artifact at this path (07-CONTEXT.md's plan-integrity gate, OPS-05). Empty (the default)
	// means no approval gate applies -- every existing run shape is unaffected.
	ApprovedPlanPath string
	// MaxPlanAge is --max-plan-age's value: the maximum age (from plan.Artifact.GeneratedAt) an
	// ApprovedPlanPath artifact may have before it is refused, checked before any OCI API call.
	// Only consulted when ApprovedPlanPath is non-empty.
	MaxPlanAge time.Duration
}

// runFlags carries every cobra flag execute reads before delegating to runPipeline.
type runFlags struct {
	configPath         string
	compartmentID      string
	noDryRun           bool
	profile            string
	authMethod         string
	quiet              bool
	includes           []string
	excludes           []string
	force              bool
	forceSleep         int
	planOut            string
	maxWaitRetries     int
	failOnLeftover     bool
	deleteCompartments bool
	// oidcRegion/oidcAudience back --auth github-oidc's --oidc-region/--oidc-audience flags.
	// The client secret and the other two OIDC credential values are deliberately NOT cobra
	// flags -- see the OCI_OIDC_* env-var-only comment beside their registration in init()
	// below -- so they are read directly via os.Getenv in execute, never carried here.
	oidcRegion   string
	oidcAudience string
	// approvedPlanPath/maxPlanAge back --approved-plan/--max-plan-age -- see pipelineOptions'
	// ApprovedPlanPath/MaxPlanAge doc comments for the semantics.
	approvedPlanPath string
	maxPlanAge       time.Duration
}

// pipelineRun bundles everything runPipeline's helper functions need, so those helpers stay
// short and few-argument even as later phases add more scope-resolution/region-fan-out state.
type pipelineRun struct {
	ctx        context.Context
	provider   ocicommon.ConfigurationProvider
	cfg        *config.Config
	opts       pipelineOptions
	homeRegion string
	regions    []string
	clients    *clients.Cache
	logger     *logrus.Logger
	// safetyFilter holds this run's parsed settings.protect configuration (Plan 04-02's Task 1),
	// populated once in runPipeline immediately after resolveScope returns and read fresh by
	// newCompartmentScanner for every constructed ocinuke.ListerOpts.
	safetyFilter ocinuke.SafetyFilterConfig
	// vaultDeletionWindowDays holds this run's parsed settings.vault.deletion-window-days value
	// (05-06-PLAN.md Task 2), populated once in runPipeline alongside safetyFilter above and read
	// fresh by newCompartmentScanner for every constructed ocinuke.ListerOpts -- the identical
	// wiring shape safetyFilter already established.
	vaultDeletionWindowDays int
	// secretDeletionWindowDays is settings.vault.secret-deletion-window-days, wired the same way.
	secretDeletionWindowDays int
	// skipEvents reads the run's leftover accumulator. Set once the accumulator exists, after
	// the approved-plan gate, so the gate's own scan sees nil.
	skipEvents func() []scope.SkipEvent
	// tree holds this run's already-fetched *scope.Tree (resolveScope's own return value),
	// populated once in runPipeline immediately after resolveScope returns and read fresh by
	// newCompartmentScanner (for CompartmentHasBlocklistedDescendant) and buildNukes (for
	// deepest-first construction order via tree.DeletionOrder) -- the identical wiring shape
	// safetyFilter/vaultDeletionWindowDays already established above.
	tree *scope.Tree
	// blocklist holds this run's compartment-blocklist as the map[string]struct{} shape
	// scope.Tree's methods expect, populated once in runPipeline alongside tree above via the
	// existing pure blocklistSet helper (resolveScope computes its own internal blocklist set but
	// does not expose it, and recomputing it here is a zero-API-call, cheap, pure operation).
	blocklist map[string]struct{}
}

// leftoverAccumulator is the thread-safe, per-pipeline-run []scope.SkipEvent accumulator
// 03-RESEARCH.md Q4 says production must add, mirroring resources_test/scheduled_deletion_test.go's
// eventRecorder shape exactly -- this project's own established pattern for the identical
// concurrency need (many scanners across many goroutines can report a leftover concurrently
// during a single run). report is installed as the reporter half of ocinuke.SetRunContext, so
// every resource type's ocinuke.CurrentReporter call during this run lands here.
type leftoverAccumulator struct {
	mu     sync.Mutex
	events []scope.SkipEvent
}

// report stores a dereferenced copy of evt -- the accumulator's whole point is to keep every
// reported event independently addressable after the run, not aliased to whatever the caller
// does with evt next. Signature matches ocinuke.LeftoverReporter (by pointer, the project-wide
// convention for scope.SkipEvent).
func (a *leftoverAccumulator) report(evt *scope.SkipEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, *evt)
}

// snapshot returns a fresh copy of every event reported so far, safe to read after the run
// completes without racing any in-flight report call.
func (a *leftoverAccumulator) snapshot() []scope.SkipEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]scope.SkipEvent(nil), a.events...)
}

// execute is the cobra RunE for `run`/`nuke`. It resolves credentials (step 1) and loads the
// config (step 2), then delegates the tenancy gate through Run (steps 3-6) to runPipeline.
func execute(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	flags, err := parseRunFlags(cmd)
	if err != nil {
		return err
	}

	apiKeyEnv := readAPIKeyEnv()

	provider, method, err := ociauth.Resolve(ctx, ociauth.ResolveOptions{
		Method:  ociauth.Method(flags.authMethod),
		Profile: flags.profile,
		// The following five fields are no-ops for every auth method except github-oidc.
		// OIDCDomainURL/OIDCClientID/OIDCClientSecret are read directly from the environment,
		// never from a CLI flag -- see the OCI_OIDC_* comment beside --oidc-region/
		// --oidc-audience's registration in init() for why the client secret in particular
		// must never be reachable via a flag.
		OIDCRegion:       flags.oidcRegion,
		OIDCAudience:     flags.oidcAudience,
		OIDCDomainURL:    os.Getenv(ociauth.EnvOIDCDomainURL),
		OIDCClientID:     os.Getenv(ociauth.EnvOIDCClientID),
		OIDCClientSecret: os.Getenv(ociauth.EnvOIDCClientSecret),
		// The six api-key fields are no-ops for every other method, and every one of them is
		// env-only for the same reason the OIDC client secret is -- see the OCI_CLI_* comment
		// beside --auth's registration in init().
		APIKeyTenancyOCID: apiKeyEnv.tenancyOCID,
		APIKeyUserOCID:    apiKeyEnv.userOCID,
		APIKeyFingerprint: apiKeyEnv.fingerprint,
		APIKeyRegion:      apiKeyEnv.region,
		APIKeyPrivateKey:  apiKeyEnv.privateKey,
		APIKeyPassphrase:  apiKeyEnv.passphrase,
	})
	if err != nil {
		return fmt.Errorf("resolving OCI credentials: %w", err)
	}

	// 07-CONTEXT.md: "Run output carries ... the resolved auth method with the principal's
	// OCID" -- audit material, not debug detail. principalOCID falls back to "unknown" rather
	// than failing the run: this is audit-trail logging, not a safety gate, and not every
	// provider shape guarantees a resolvable user OCID.
	principalOCID, ocidErr := provider.UserOCID()
	if ocidErr != nil {
		principalOCID = "unknown"
	}
	logrus.WithFields(logrus.Fields{
		"method":         method,
		"principal_ocid": principalOCID,
	}).Info("authenticated to OCI")

	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return fmt.Errorf("loading config %s: %w", flags.configPath, err)
	}

	return runPipeline(ctx, provider, cfg, &pipelineOptions{
		CompartmentID:      flags.compartmentID,
		NoDryRun:           flags.noDryRun,
		Quiet:              flags.quiet,
		Includes:           flags.includes,
		Excludes:           flags.excludes,
		Force:              flags.force,
		ForceSleep:         flags.forceSleep,
		PlanOut:            flags.planOut,
		MaxWaitRetries:     flags.maxWaitRetries,
		FailOnLeftover:     flags.failOnLeftover,
		DeleteCompartments: flags.deleteCompartments,
		ApprovedPlanPath:   flags.approvedPlanPath,
		MaxPlanAge:         flags.maxPlanAge,
	})
}

// parseRunFlags reads every cobra flag execute needs. Each Flags().GetX call only errors if
// the flag was never registered on cmd, which cannot happen here -- the checks exist so
// errcheck's safety-relevant guarantee holds structurally, not by convention.
func parseRunFlags(cmd *cobra.Command) (runFlags, error) {
	flags := cmd.Flags()
	var f runFlags
	var err error

	// Table-driven rather than one sequential GetX/err-check pair per flag (the pattern every
	// flag here used before this task added --oidc-region/--oidc-audience): the sequential form
	// already sat exactly at golangci-lint's gocyclo threshold, and two more checks pushed it
	// over. Each loop below is a single branch point regardless of how many flags it reads, so
	// this scales to future flags without retripping the threshold.
	for _, sf := range []struct {
		name string
		dst  *string
	}{
		{"config", &f.configPath},
		{"compartment-id", &f.compartmentID},
		{"profile", &f.profile},
		{"auth", &f.authMethod},
		{"plan-out", &f.planOut},
		{"oidc-region", &f.oidcRegion},
		{"oidc-audience", &f.oidcAudience},
		{"approved-plan", &f.approvedPlanPath},
	} {
		if *sf.dst, err = flags.GetString(sf.name); err != nil {
			return runFlags{}, err
		}
	}

	for _, bf := range []struct {
		name string
		dst  *bool
	}{
		{"no-dry-run", &f.noDryRun},
		{"quiet", &f.quiet},
		{"force", &f.force},
		{"fail-on-leftover", &f.failOnLeftover},
		{"delete-compartments", &f.deleteCompartments},
	} {
		if *bf.dst, err = flags.GetBool(bf.name); err != nil {
			return runFlags{}, err
		}
	}

	for _, iflag := range []struct {
		name string
		dst  *int
	}{
		{"force-sleep", &f.forceSleep},
		{"max-wait-retries", &f.maxWaitRetries},
	} {
		if *iflag.dst, err = flags.GetInt(iflag.name); err != nil {
			return runFlags{}, err
		}
	}

	for _, slf := range []struct {
		name string
		dst  *[]string
	}{
		{"include", &f.includes},
		{"exclude", &f.excludes},
	} {
		if *slf.dst, err = flags.GetStringSlice(slf.name); err != nil {
			return runFlags{}, err
		}
	}

	for _, df := range []struct {
		name string
		dst  *time.Duration
	}{
		{"max-plan-age", &f.maxPlanAge},
	} {
		if *df.dst, err = flags.GetDuration(df.name); err != nil {
			return runFlags{}, err
		}
	}

	return f, nil
}

// resolveDeleteCompartmentsOptIn resolves the --delete-compartments flag/settings.compartment.
// delete union precedence (06-CONTEXT.md's amended decision) and, when the resolved result is
// enabled, validates the 15-minute retry-budget floor a compartment delete needs (06-CONTEXT.md,
// COMP-02) -- both as one zero-API-call, pre-scan gate, extracted out of runPipeline purely to
// keep that function under golangci-lint's gocyclo threshold; the ordering/semantics are
// unchanged from inlining this directly.
//
// Precedence: opts.DeleteCompartments (the CLI flag's own value, as passed in) is combined with
// settings.compartment.delete via a pure OR, mutating opts.DeleteCompartments in place so every
// downstream reader (pr.opts, resolveResourceTypes' Excludes composition) sees the resolved
// union, never the flag alone. There is no code path here that lets an absent/false flag
// downgrade a true config key back to false.
//
// Retry floor: validated -- and refused, zero API calls -- ONLY when the resolved
// opts.DeleteCompartments is true (Pitfall C: this must never silently tighten the wait budget
// of a run that never touches Compartment).
func resolveDeleteCompartmentsOptIn(cfg *config.Config, opts *pipelineOptions) error {
	configEnabled, err := ocinuke.DeleteCompartmentsEnabledFromSettings(cfg.Settings)
	if err != nil {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf("parsing settings.compartment: %w", err)}
	}
	opts.DeleteCompartments = opts.DeleteCompartments || configEnabled

	if !opts.DeleteCompartments {
		return nil
	}

	// opts.MaxWaitRetries == 0 is libnuke's own "retry indefinitely" sentinel (verified against
	// the pinned github.com/ekristen/libnuke@v1.3.0 source, pkg/nuke/nuke.go's handleWaiting:
	// "if MaxWaitRetries is set to 0, then we do not need to do anything, we will retry
	// indefinitely"), which trivially satisfies -- exceeds -- any finite floor. Treating it as a
	// zero-length budget here (0 * nukeRunSleep = 0 < floor) would make an operator's deliberate
	// choice of unbounded patience unusable together with --delete-compartments, forcing them to
	// pick an arbitrarily large finite number instead of expressing the intent directly
	// (06-REVIEW.md WR-02). Skip the floor check entirely for the unlimited case; any nonzero
	// value is still checked against the floor exactly as before.
	if opts.MaxWaitRetries != 0 {
		const deleteCompartmentsRetryFloor = 15 * time.Minute
		budget := time.Duration(opts.MaxWaitRetries) * nukeRunSleep
		if budget < deleteCompartmentsRetryFloor {
			return &ExitError{Code: ExitRefused, Err: fmt.Errorf(
				"--delete-compartments requires --max-wait-retries * run-sleep to be at least %v (got %d * %v = %v); "+
					"raise --max-wait-retries",
				deleteCompartmentsRetryFloor, opts.MaxWaitRetries, nukeRunSleep, budget,
			)}
		}
	}
	return nil
}

// loadApprovedPlan reads and parses --approved-plan's file at path as a plan.Artifact, refuses it
// if its own stored Hash does not match plan.Hash(artifact.Body) (WR-02, 07-REVIEW.md -- defense
// in depth against a corrupted or hand-edited approved-plan file, since verifyApprovedPlan's own
// proceed/abort decision only ever compares approved.Hash against a fresh rescan's hash), and
// refuses it if artifact.GeneratedAt is older than maxAge -- all zero-API-call, pure file I/O plus
// local computation, run before anything reaches the network (07-CONTEXT.md: "A plan older than a
// configurable maximum is refused ... before any OCI API call"). Any read, parse, hash-consistency,
// or staleness failure is a hard error -- there is no silent-skip path back to an unverified run
// (threat T-07-05).
func loadApprovedPlan(path string, maxAge time.Duration) (plan.Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return plan.Artifact{}, fmt.Errorf("reading approved plan %s: %w", path, err)
	}

	var artifact plan.Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return plan.Artifact{}, fmt.Errorf("parsing approved plan %s as JSON: %w", path, err)
	}

	// WR-02 (07-REVIEW.md): recompute the artifact's own hash from its own Body and compare
	// against the stored Hash field, rather than trusting Hash at face value. The actual
	// proceed/abort decision in verifyApprovedPlan only ever compares approved.Hash (an opaque
	// string) against a fresh rescan's own hash -- it never uses approved.Body to decide anything
	// except what to print via plan.Diff on a mismatch -- so this check does not change what can
	// ever be deleted. It closes a real defense-in-depth gap: a corrupted or hand-edited approved-
	// plan file whose Hash no longer matches its own Body (e.g. an operator manually adjusts one
	// field of Body for a local invocation and forgets to recompute Hash) is refused outright,
	// rather than silently accepted and later diffed against a Body that was never actually the
	// thing approved.
	if wantHash, err := plan.Hash(artifact.Body); err != nil {
		return plan.Artifact{}, fmt.Errorf("recomputing approved plan %s's own hash: %w", path, err)
	} else if wantHash != artifact.Hash {
		return plan.Artifact{}, fmt.Errorf(
			"approved plan %s is internally inconsistent: stored hash %s does not match its own body's hash %s",
			path, artifact.Hash, wantHash,
		)
	}

	age := time.Since(artifact.GeneratedAt)
	if age > maxAge {
		return plan.Artifact{}, fmt.Errorf(
			"approved plan %s is %v old (generated_at=%s), which exceeds --max-plan-age of %v -- refusing",
			path, age.Round(time.Second), artifact.GeneratedAt.Format(time.RFC3339), maxAge,
		)
	}

	return artifact, nil
}

// loadApprovedPlanGate wraps loadApprovedPlan's opts.ApprovedPlanPath != "" gate as one
// self-contained, zero-API-call pre-scan step -- extracted out of runPipeline purely to keep that
// function under golangci-lint's gocyclo threshold, mirroring resolveDeleteCompartmentsOptIn's
// own doc comment and precedent above. *out is left untouched (nil) when opts.ApprovedPlanPath is
// empty -- every existing run shape is unaffected. out-param shape (rather than a second return
// value) sidesteps returning a valid-nil-pair ambiguity for the common "no approved plan" case.
func loadApprovedPlanGate(opts *pipelineOptions, out **plan.Artifact) error {
	if opts.ApprovedPlanPath == "" {
		return nil
	}

	artifact, err := loadApprovedPlan(opts.ApprovedPlanPath, opts.MaxPlanAge)
	if err != nil {
		return &ExitError{Code: ExitRefused, Err: err}
	}
	*out = &artifact
	return nil
}

// verifyApprovedPlanGate wraps verifyApprovedPlan's approvedPlan != nil gate as one
// self-contained step, mirroring loadApprovedPlanGate's own extraction above -- both exist purely
// to keep runPipeline's branch count under golangci-lint's gocyclo threshold. A nil approvedPlan
// (no --approved-plan passed) is a no-op, returning nil immediately.
func verifyApprovedPlanGate(
	pr *pipelineRun,
	params *libnuke.Parameters,
	inScope map[string]struct{},
	tree *scope.Tree,
	blocklistSkipEvents []scope.SkipEvent,
	approvedPlan *plan.Artifact,
) error {
	if approvedPlan == nil {
		return nil
	}
	return verifyApprovedPlan(pr, params, inScope, tree, blocklistSkipEvents, *approvedPlan)
}

// verifyApprovedPlan is the forced-rescan verification pass 07-CONTEXT.md's plan-integrity gate
// requires: libnuke.Nuke.Run exposes no hook between Scan() and Remove() (nuke.go:192-227,
// 07-RESEARCH.md), so the only trustworthy way to prove the tenancy still matches `approved` is a
// second, full, forced-dry-run pass through the exact same buildNukes/runNukesFn/buildArtifact
// path every other run already uses -- never a second, divergently-implemented hashing or
// scanning logic.
//
// forced is a copy of the caller-supplied params with NoDryRun forced false -- a second,
// independent NoDryRun value that exists ONLY for this rescan, never mutating the real params the
// caller-supplied destructive run still uses after this function returns. This is the one place
// in this codebase two different NoDryRun values coexist within a single runPipeline call.
// NoDryRun=false on both buildNukes and runNukesFn means entries classify as would-remove,
// matching what the approved artifact -- itself built by a dry-run "mode: plan" job -- would have
// recorded, so a genuinely unchanged tenancy hashes identically.
//
// verifyApprovedPlan only reads params; it never mutates it. Reusing buildNukes here also
// re-invokes verifyTenancy once per compartment via each Nuke's own RegisterValidateHandler -- a
// harmless, already-budgeted extra cost per 07-CONTEXT.md's "the apply run scans twice" framing,
// not a second, independently-written tenancy check.
//
// CR-03 (07-REVIEW.md): the rescan's own runNukesFn call runs through the exact same
// scopedLister-wrapped Listers the real destructive pass uses, and scopedLister.List reports
// protect-by-tag/min-age/out-of-scope/blocklisted-descendant skip events via CurrentReporter
// unconditionally of NoDryRun -- during List() itself, not Remove() (pkg/ocinuke/scoped_lister.go's
// own doc comment). This function installs its OWN, dedicated leftoverAccumulator/run context for
// the duration of that one runNukesFn call -- never the caller's real-pass accumulator -- for two
// reasons at once:
//
//  1. Isolation: the rescan's skip events must never reach the real pass's own leftover
//     accumulator (installed separately by runPipeline, after this gate returns). Sharing one
//     accumulator across both passes double-reports every such event, and mergeSkipEvent's
//     fallthrough for an already-StateSkipped entry appends a duplicate rather than deduping it --
//     corrupting the real run's own --plan-out artifact for any protected resource in scope.
//  2. Correctness: freshArtifact below must still MERGE those same skip events (via
//     rescanSkipEvents, alongside blocklistSkipEvents) for the hash comparison to mean anything --
//     approved.Hash comes from a plan-mode run whose own finishNukes call merged an identical set
//     of resource-level skip events into ITS artifact. Comparing a freshArtifact that silently
//     dropped them against an approved artifact that included them would hash-mismatch on every
//     apply run touching a protected resource, regardless of whether the tenancy actually drifted.
//
// The inScope half of the installed run context is the real, caller-supplied set -- NOT a stub --
// since scopedLister.List's CurrentScope check fails closed by default (pkg/ocinuke/runcontext.go):
// an unset/wrong inScope during the rescan would misclassify every resource as out-of-scope and
// produce a meaningless rescan, not merely a duplication bug.
//
// On a hash mismatch, this returns a non-nil *ExitError{Code: ExitRefused} whose message names
// both hashes and includes plan.Diff's line-by-line report of what changed (threat T-07-07:
// mismatch aborts are never unexplained). A failure of the rescan itself (not a hash comparison
// at all -- a genuine internal/API failure mid-scan) is returned unwrapped, matching this file's
// existing convention that only safety-model refusals get ExitRefused.
func verifyApprovedPlan(
	pr *pipelineRun,
	params *libnuke.Parameters,
	inScope map[string]struct{},
	tree *scope.Tree,
	blocklistSkipEvents []scope.SkipEvent,
	approved plan.Artifact,
) error {
	forced := *params
	forced.NoDryRun = false

	nukes, err := buildNukes(pr, &forced, inScope, tree)
	if err != nil {
		return err
	}

	rescanAccumulator := &leftoverAccumulator{}
	restoreRescanContext := ocinuke.SetRunContext(func(id string) bool {
		_, ok := inScope[id]
		return ok
	}, rescanAccumulator.report)
	// The rescan must see exactly what the real pass will, allowance included -- a plan verified
	// without it would disagree with the run it is meant to authorize.
	restoreRescanAllowance := ocinuke.SetTenancyRootAllowance(tenancyRootAllowance(pr))
	body, _, _, runErr := runNukesFn(pr.ctx, nukes, false, pr.logger)
	restoreRescanAllowance()
	restoreRescanContext()
	if runErr != nil {
		return fmt.Errorf("approved-plan verification rescan failed: %w", runErr)
	}

	rescanSkipEvents := append(append([]scope.SkipEvent(nil), blocklistSkipEvents...), rescanAccumulator.snapshot()...)
	freshArtifact, err := buildArtifact(body, rescanSkipEvents, tree)
	if err != nil {
		return err
	}

	if freshArtifact.Hash != approved.Hash {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf(
			"approved plan hash mismatch (approved=%s, current=%s): tenancy state has drifted since approval:\n%s",
			approved.Hash, freshArtifact.Hash, strings.Join(plan.Diff(approved.Body, freshArtifact.Body), "\n"),
		)}
	}

	return nil
}

// resolveHomeRegion resolves the tenancy's real, ListRegionSubscriptions-verified home region and
// validated region list -- extracted out of runPipeline purely to keep that function under
// golangci-lint's gocyclo threshold, mirroring resolveDeleteCompartmentsOptIn's own doc comment
// and precedent above; the ordering/semantics are unchanged from inlining this directly.
//
// connectionRegion (provider.Region()) is used for exactly ONE purpose inside this function:
// reaching the Identity API endpoint to issue the ListRegionSubscriptions call. It must NEVER be
// treated as the tenancy's semantic home region -- that was Phase 2's bug (homeRegion derived
// directly from provider.Region()), live-verified wrong against a real tenancy,
// where the credential profile's connection region (eu-frankfurt-1) diverges from the tenancy's
// real home region (us-ashburn-1, the ListRegionSubscriptions IsHomeRegion=true entry). The
// corrected home region returned here is sourced exclusively from scope.ResolveRegions's own
// return value, never from connectionRegion.
func resolveHomeRegion(
	ctx context.Context,
	provider ocicommon.ConfigurationProvider,
	clientCache *clients.Cache,
	cfg *config.Config,
) (regions []string, homeRegion string, err error) {
	connectionRegion, err := provider.Region()
	if err != nil {
		return nil, "", fmt.Errorf("resolving connection region from credentials: %w", err)
	}

	regionClient, err := newRegionClient(clientCache, connectionRegion)
	if err != nil {
		return nil, "", fmt.Errorf("constructing identity client for region resolution: %w", err)
	}

	regions, homeRegion, err = scope.ResolveRegions(ctx, regionClient, cfg.TenancyID, cfg.Regions)
	if err != nil {
		return nil, "", &ExitError{Code: ExitRefused, Err: fmt.Errorf("region validation: %w", err)}
	}

	warnIfHomeRegionOutOfScope(logrus.StandardLogger(), regions, homeRegion)

	return regions, homeRegion, nil
}

// globalGeographyTypes names the resource types whose List() calls BeforeList(ocinuke.Global) and
// which therefore enumerate only in the tenancy's home region. Kept as a literal list because
// geography is declared inside each type's own List(), not in its registry.Registration -- there
// is no runtime-queryable field to derive this from. resources_test's
// TestIAMTypesRegisteredAsGlobal_NotRegional proves the four IAM entries really are Global by
// scanning source; resources/compartment.go's own List() carries the fifth.
var globalGeographyTypes = []string{"Compartment", "DynamicGroup", "Policy", "TagDefault", "TagNamespace"}

// warnIfHomeRegionOutOfScope reports, at Warn level so it is visible at the default log level,
// that every Global-geography resource type will go unscanned because the tenancy's home region
// is not among the regions this run covers.
//
// Without this the omission is completely silent: BeforeList(Global) returns
// liberrors.ErrSkipRequest, which libnuke logs at Debug and drops. It never becomes a
// scope.SkipEvent, so it never reaches plan.MergeSkipEvents and never appears in the plan
// artifact -- a run against a config listing only eu-frankfurt-1 in a tenancy homed in
// us-ashburn-1 reports a clean plan that quietly covers no IAM at all (05-UAT.md gap 2).
//
// Deliberately a log line rather than a plan entry: plan.Entry is keyed on a concrete resource
// (type, id, compartment, region) and a never-scanned type has no id to carry, and plan entries
// are hashed -- adding non-resource rows would change the artifact hash that --approved-plan
// compares, invalidating stored approved plans for a reason unrelated to tenancy drift.
//
// Extracted as its own function rather than inlined at the call site because runPipeline and
// resolveHomeRegion both sit at golangci-lint's gocyclo threshold; a bare function call adds no
// branch to either.
func warnIfHomeRegionOutOfScope(logger *logrus.Logger, regions []string, homeRegion string) {
	if slices.Contains(regions, homeRegion) {
		return
	}

	logger.WithFields(logrus.Fields{
		"home_region":     homeRegion,
		"scanned_regions": strings.Join(regions, ","),
		"uncovered_types": strings.Join(globalGeographyTypes, ","),
	}).Warn("home region is not in this run's regions -- global resource types will not be scanned " +
		"anywhere; add the home region to the config's regions list to cover them")
}

// runPipeline executes the run command's locked ordering (tenancy-root refusal -> blocklist
// refusal -> tenancy gate -> scope-resolve -> confirmTarget (once, only for a destructive run) ->
// buildNukes (one nuke.New + RegisterValidateHandler + RegisterScanner per in-scope compartment)
// -> runNukes): both zero-API-call safety refusals (SAFE-05/SAFE-06), the tenancy gate, real
// subtree resolution via pkg/scope (replacing Phase 1's single-compartment stub), the single
// upfront confirmation (SAFE-07, locked requirement 6), and one *libnuke.Nuke per compartment so
// each compartment's own resolved filters actually apply (CONF-04) rather than one shared
// Filters value silently applying to every compartment (see buildNukes' doc comment). auth and
// config loading are execute's job, not runPipeline's -- this split is what lets the pipeline run
// against a fake, non-network provider in command_test.go without live OCI credentials.
//
// The live sandbox run (ROADMAP.md Phase 1 success criterion 1) is a single command:
//
//	oci-nuke run --config <file> --compartment-id <ocid> --profile <profile>
//
// (dry-run by default; add --no-dry-run to actually remove resources). This was executed
// against a live tenancy on 2026-08-07 -- empty plan, exit 0, nothing deleted -- and the
// tenancy-mismatch path was exercised live in the same session. The fake-provider tests in
// command_test.go keep both paths, plus the Phase 2 refusal paths below, covered in CI, where no
// credentials exist.
func runPipeline(ctx context.Context, provider ocicommon.ConfigurationProvider, cfg *config.Config, opts *pipelineOptions) error {
	// Every pre-scan safety-model refusal gate below (SAFE-05/SAFE-06 and the tenancy/region
	// gates that follow) is wrapped in &ExitError{Code: ExitRefused, ...} so main.go's errors.As
	// check reports a distinct exit code (03-RESEARCH.md Q9) -- the error's own message text is
	// never changed by this wrapping. Every OTHER pre-scan error in this function (region-client
	// construction, FetchTenancyTree's own API-call failure inside resolveScope, filter
	// resolution, artifact marshaling) is deliberately left unwrapped, defaulting to ExitInternal
	// via main.go's existing os.Exit(1) fallback, since those are genuine internal/API failures,
	// not safety-model refusals.

	// --approved-plan staleness check: the cheapest possible refusal, zero OCI API calls, so it
	// runs before even SAFE-06's tenancy-root refusal below (07-CONTEXT.md: "A plan older than a
	// configurable maximum is refused ... before any OCI API call"). approvedPlan stays nil for
	// every run that doesn't pass --approved-plan -- every existing run shape is unaffected.
	var approvedPlan *plan.Artifact
	if err := loadApprovedPlanGate(opts, &approvedPlan); err != nil {
		return err
	}

	// SAFE-06: refuse the tenancy root before anything else that touches the network -- zero API
	// calls, unconditional, checked before even the tenancy gate's own auth round-trip. The only
	// check that runs earlier is --approved-plan's staleness gate above, itself zero-API-call and
	// cheaper still (a duration comparison versus a compartment-ID string comparison).
	if err := scope.RefuseTenancyRoot(opts.CompartmentID, cfg.TenancyID); err != nil {
		return &ExitError{Code: ExitRefused, Err: err}
	}

	// SAFE-05 / 02-CONTEXT.md locked requirement 3: there is no safe default for an empty
	// blocklist. config.Validate covers config-file-only entry points (`config validate`), but
	// execute() calls config.Load directly and never calls Validate -- an operator who never
	// runs `config validate` before `run` would otherwise get zero enforcement of this
	// requirement. Zero API calls, unconditional, checked before verifyTenancy.
	if len(cfg.CompartmentBlocklist) == 0 {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf(
			"compartment-blocklist must not be empty: refusing to run with no blocklist configured",
		)}
	}

	if opts.CompartmentID == "" {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf("--compartment-id is required")}
	}

	// --delete-compartments union precedence + retry-budget floor (06-CONTEXT.md, COMP-02):
	// resolved and validated as one pre-scan, zero-API-call gate, before verifyTenancy -- see
	// resolveDeleteCompartmentsOptIn's own doc comment for the exact precedence/floor rules.
	if err := resolveDeleteCompartmentsOptIn(cfg, opts); err != nil {
		return err
	}

	// Tenancy gate: the live Identity round-trip proving credentials resolve to the expected
	// tenancy, still before anything lists or scans.
	if err := verifyTenancy(ctx, provider, cfg.TenancyID); err != nil {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf("tenancy gate: %w", err)}
	}

	retryPolicy := ocicommon.DefaultRetryPolicy()
	clientCache := clients.New(provider, retryPolicy)

	validatedRegions, homeRegion, err := resolveHomeRegion(ctx, provider, clientCache, cfg)
	if err != nil {
		return err
	}

	pr := &pipelineRun{
		ctx:        ctx,
		provider:   provider,
		cfg:        cfg,
		opts:       *opts,
		homeRegion: homeRegion,
		regions:    validatedRegions,
		clients:    clientCache,
		logger:     logrus.StandardLogger(),
	}

	inScope, tree, blocklistSkipEvents, err := resolveScope(pr)
	if err != nil {
		return err
	}
	pr.tree = tree
	pr.blocklist = blocklistSet(cfg.CompartmentBlocklist)

	// Plan 04-02 Task 3: parse this run's safety filter once, before any resource type's
	// Remove()/Filter() can possibly run, and install the run-scoped CurrentScope/CurrentReporter
	// indirection (Plan 04-01) for the ENTIRE remainder of this function -- confirmTarget,
	// buildNukes, and finishNukes all sit downstream of this defer, since Remove()/Filter() calls
	// happen deep inside finishNukes' runNukesFn call. restore() resets both back to their
	// fail-closed defaults the moment runPipeline returns, by any path.
	safetyFilter, err := ocinuke.SafetyFilterConfigFromSettings(pr.cfg.Settings)
	if err != nil {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf("parsing settings.protect: %w", err)}
	}
	pr.safetyFilter = safetyFilter

	if err := resolveDeletionWindows(pr); err != nil {
		return &ExitError{Code: ExitRefused, Err: fmt.Errorf("parsing settings.vault: %w", err)}
	}

	params := buildParameters(&pr.opts)

	// --approved-plan verification (07-CONTEXT.md's plan-integrity gate, OPS-05): only when the
	// operator passed --approved-plan, after resolveScope (both this rescan and the real pass
	// need the identical in-scope set, so scope resolution happens once, not twice) and strictly
	// before any Remove() call -- confirmTarget/buildNukes/finishNukes below are all downstream of
	// this check. A mismatch or a genuine rescan failure aborts here, before the operator's own
	// confirmation prompt and before the real destructive pass is ever constructed.
	//
	// CR-03 (07-REVIEW.md): this call -- and the real run's own CurrentScope/CurrentReporter
	// installation immediately below -- must NOT be nested inside a single outer
	// ocinuke.SetRunContext. verifyApprovedPlan installs its own, separate, isolated run context
	// for the rescan's own runNukesFn call (see its doc comment): ocinuke.SetRunContext's returned
	// restore() always resets CurrentScope/CurrentReporter to their fail-closed package defaults,
	// never back to a caller's previously-installed context, so an outer SetRunContext installed
	// here before the gate would be silently clobbered the moment the gate's own inner restore()
	// fires. Installing the real run's context only AFTER the gate returns avoids that nesting
	// hazard entirely, and also keeps the real pass's accumulator from ever seeing the rescan's
	// own skip events (which is the actual CR-03 duplication bug).
	if err := verifyApprovedPlanGate(pr, params, inScope, tree, blocklistSkipEvents, approvedPlan); err != nil {
		return err
	}

	// Real destructive pass's own run context/accumulator: installed only now, after the gate's
	// own isolated rescan (if any) has already run and its context been fully restored above --
	// this is the ONLY reporter whose events reach the real run's final --plan-out artifact
	// (finishNukes' accumulator.snapshot() call).
	accumulator := &leftoverAccumulator{}
	pr.skipEvents = accumulator.snapshot
	restore := ocinuke.SetRunContext(func(id string) bool {
		_, ok := inScope[id]
		return ok
	}, accumulator.report)
	defer restore()
	defer ocinuke.SetTenancyRootAllowance(tenancyRootAllowance(pr))()

	// The confirmation is asked ONCE, upfront, for the operator's original target -- not once per
	// discovered descendant compartment (see buildNukes: RegisterPrompt is deliberately never
	// called on any per-compartment Nuke this pipeline constructs). Skipped entirely on a
	// dry-run, mirroring Phase 1's existing dry-run-skips-prompt reasoning: a preview that can
	// never reach Remove() must not block on stdin.
	if pr.opts.NoDryRun {
		if err := confirmTarget(pr, tree, params); err != nil {
			return err
		}
	}

	nukes, err := buildNukes(pr, params, inScope, tree)
	if err != nil {
		return err
	}

	return finishNukes(pr, nukes, blocklistSkipEvents, accumulator)
}

// resolveScope performs step 4 of runPipeline's locked ordering: one root-scoped Identity call
// (scope.FetchTenancyTree, always against the tenancy root -- never the operator's target, per
// the live-verified HTTP-400 constraint on CompartmentIdInSubtree), a pure in-memory tree build,
// an existence check on the target (closing the gap where an unknown/mistyped target would
// otherwise silently resolve to a single-node in-scope set instead of erroring), an
// ancestor-blocklist check (catching a target nested under a blocklisted ancestor, which
// Tree.Resolve's downward-only walk cannot see), and finally Tree.Resolve for the in-scope set.
// The built *scope.Tree is also returned: confirmTarget reads the target's live display name off
// it directly (tree.Name), so runPipeline never issues a second GetCompartment call for data this
// one root-scoped ListCompartments response already carried.
//
// The returned []scope.SkipEvent structures the blocklist-skip data this function already
// computed and logged (skippedBlocklisted) -- Plan 03-06's finishNukes merges it into the plan
// artifact via plan.MergeSkipEvents, so a blocklisted subtree's absence from the plan has a
// machine-readable reason, not only a log line. This is real, already-flowing production data,
// not the separate per-resource ocinuke.ReportLeftover/onSkip side channel (03-RESEARCH.md Q4's
// scope note), which still has zero production call sites as of this phase.
func resolveScope(pr *pipelineRun) (map[string]struct{}, *scope.Tree, []scope.SkipEvent, error) {
	identityClient, err := newScopeIdentityClient(pr.clients, pr.homeRegion)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("constructing identity client for scope resolution: %w", err)
	}

	compartments, err := scope.FetchTenancyTree(pr.ctx, identityClient, pr.cfg.TenancyID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetching tenancy compartment tree: %w", err)
	}
	tree := scope.BuildTree(compartments)

	if !tree.Exists(pr.opts.CompartmentID) {
		return nil, nil, nil, &ExitError{Code: ExitRefused, Err: fmt.Errorf(
			"target compartment %q not found in tenancy %q", pr.opts.CompartmentID, pr.cfg.TenancyID,
		)}
	}

	blocklist := blocklistSet(pr.cfg.CompartmentBlocklist)

	if blockedOCID, blocked := tree.AncestorBlocklisted(pr.opts.CompartmentID, blocklist); blocked {
		return nil, nil, nil, &ExitError{Code: ExitRefused, Err: fmt.Errorf(
			"target compartment %q is blocklisted (blocked by %q)", pr.opts.CompartmentID, blockedOCID,
		)}
	}

	// Before Resolve, because Resolve prunes a non-ACTIVE target to an empty in-scope set while
	// tree.Exists stays true -- so a DELETING or CREATING target produced a zero-entry plan and
	// exit 0, indistinguishable from a compartment that was scanned and found clean, silently and
	// at --log-level debug too (NR-787). CREATING is refused as well as DELETING: a compartment
	// seconds old also scans empty, and "I did not look" is the same answer either way.
	if state, ok := tree.LifecycleState(pr.opts.CompartmentID); ok &&
		state != scope.LifecycleStateActive {
		return nil, nil, nil, &ExitError{Code: ExitRefused, Err: fmt.Errorf(
			"target compartment %q is %s, not ACTIVE: nothing in it can be examined or deleted "+
				"while it is in that state", pr.opts.CompartmentID, state,
		)}
	}

	inScope, skippedBlocklisted, skippedNonActive := tree.Resolve(pr.opts.CompartmentID, blocklist)
	skipEvents := make([]scope.SkipEvent, 0, len(skippedBlocklisted)+len(skippedNonActive))
	for _, skipped := range skippedBlocklisted {
		pr.logger.WithFields(logrus.Fields{
			logFieldCompartmentID: skipped,
			logFieldReason:        scope.ReasonBlocklisted,
		}).Warn("skipping blocklisted compartment subtree")
		skipEvents = append(skipEvents, scope.SkipEvent{
			Reason:        scope.ReasonBlocklisted,
			CompartmentID: skipped,
		})
	}
	// A non-ACTIVE descendant keeps being pruned -- that is the ACTIVE-only default and it is
	// correct. What it now also does is say so, in the log and in the artifact.
	for _, skipped := range skippedNonActive {
		pr.logger.WithFields(logrus.Fields{
			logFieldCompartmentID: skipped.ID,
			"lifecycle_state":     skipped.LifecycleState,
			logFieldReason:        scope.ReasonCompartmentNotActive,
		}).Warn("skipping non-ACTIVE compartment subtree: it cannot be examined in this state")
		skipEvents = append(skipEvents, scope.SkipEvent{
			Reason:        scope.ReasonCompartmentNotActive,
			CompartmentID: skipped.ID,
			Detail:        "lifecycle_state " + skipped.LifecycleState,
		})
	}

	return inScope, tree, skipEvents, nil
}

// blocklistSet converts the config's CompartmentBlocklist slice into the map[string]struct{}
// shape pkg/scope's Tree methods expect, built fresh each call so it never aliases cfg's own
// backing slice.
func blocklistSet(blocklist []string) map[string]struct{} {
	set := make(map[string]struct{}, len(blocklist))
	for _, id := range blocklist {
		set[id] = struct{}{}
	}
	return set
}

// buildParameters constructs libnuke.Parameters from opts. Dry-run is the default because
// NoDryRun defaults false (SAFE-01). Force/ForceSleep thread the --force/--force-sleep flags
// (locked requirement 6) through to Nuke.Validate(), which independently rejects any ForceSleep
// below 3 regardless of what this pipeline does with it -- libnuke v1.3.0 has no field for tuning
// failure retries on Parameters itself; MaxWaitRetries/SetRunSleep are the real knobs, tuned
// starting Phase 3. MaxWaitRetries defaults to 0 (unlimited) in libnuke unless opts.MaxWaitRetries
// is set -- production always sets it via --max-wait-retries (default 120), per 03-RESEARCH.md
// Q6/Q7's finding that an unbounded default risks a run that never terminates.
// WaitOnDependencies is unconditionally true, not a flag -- 03-RESEARCH.md's own recommendation
// (it is off by default even in aws-nuke), independent of anything opts carries.
func buildParameters(opts *pipelineOptions) *libnuke.Parameters {
	return &libnuke.Parameters{
		NoDryRun:           opts.NoDryRun,
		Force:              opts.Force,
		ForceSleep:         opts.ForceSleep,
		Quiet:              opts.Quiet,
		Includes:           opts.Includes,
		Excludes:           opts.Excludes,
		MaxWaitRetries:     opts.MaxWaitRetries,
		WaitOnDependencies: true,
	}
}

// confirmTarget performs the pipeline's single destructive-run confirmation (locked requirement
// 6), mirroring aws-nuke's own Prompt struct (pkg/nuke/prompt.go) and using
// libnuke/pkg/utils.Prompt -- which reads a full line via bufio.Reader, unlike Phase 1's
// single-token stdin scan, which truncated at the first whitespace character and made correct
// confirmation of any multi-word compartment display name impossible (T-02-15). Called exactly
// once, before any per-compartment *libnuke.Nuke is constructed -- never via
// Nuke.RegisterPrompt on each of them, which would reprompt once per compartment in the resolved
// in-scope set (aws-nuke has one account per run; oci-nuke has one target subtree per run, and
// the prompt is about the subtree). The live display name is read from tree (Plan 02-04's
// already-fetched Identity response, held by resolveScope), never a second GetCompartment call
// for data already in memory.
func confirmTarget(pr *pipelineRun, tree *scope.Tree, params *libnuke.Parameters) error {
	targetName, ok := tree.Name(pr.opts.CompartmentID)
	if !ok {
		return fmt.Errorf("target compartment %q has no recorded display name in the fetched tree", pr.opts.CompartmentID)
	}

	if params.Force {
		sleep := time.Duration(params.ForceSleep) * time.Second
		// T-02-14: a --force run's only record of having bypassed confirmation is this log
		// line -- it must fire on every Force branch taken, unconditionally.
		pr.logger.WithField("force", true).Warnf("--force set, continuing without confirmation after %v", sleep)
		time.Sleep(sleep)
		return nil
	}

	fmt.Printf("Do you really want to nuke compartment %q (%s) in tenancy %q?\n", targetName, pr.opts.CompartmentID, pr.cfg.TenancyID)
	fmt.Println("Enter the compartment name to continue.")
	return utils.Prompt(targetName)
}

// compartmentResourceTypeName MUST match resources.CompartmentResourceType's value exactly
// ("Compartment") -- it is deliberately a bare string literal here, not an import of the
// resources package's own constant, since pkg/commands/run only ever blank-imports resources
// (registration side effects only, never referencing its exported symbols directly). This is
// the sanctioned Excludes-composition opt-in seam (06-RESEARCH.md "Pattern: opt-in via
// Excludes, not conditional registration") -- Compartment always registers unconditionally at
// init() like every other resource type; there is no compile-time seam to skip that.
const compartmentResourceTypeName = "Compartment"

// resolveResourceTypes computes the include/exclude-resolved set of resource types registered
// under ocinuke.CompartmentScope. Shared by buildScanners (Plan 02-04's multi-compartment
// helper, still exercised directly by command_test.go's subtree-resolution regression test) and
// buildNukes (this plan's one-Nuke-per-compartment construction), so the two call sites cannot
// drift on how resource types are resolved.
//
// When compartment deletion is not enabled (pr.opts.DeleteCompartments, already resolved to the
// flag/config-key union by runPipeline), "Compartment" is appended to the excludes collection --
// Excludes always wins over Includes in types.ResolveResourceTypes (verified against the pinned
// libnuke source), so Compartment is never even scanned (zero GetCompartment calls) when the
// feature is off, and this composes for free with the existing --include/--exclude flags.
func resolveResourceTypes(pr *pipelineRun, params *libnuke.Parameters) types.Collection {
	excludes := []types.Collection{params.Excludes, pr.cfg.ResourceTypes.Excludes}
	if !pr.opts.DeleteCompartments {
		excludes = append(excludes, types.Collection{compartmentResourceTypeName})
	}

	return types.ResolveResourceTypes(
		registry.GetNamesForScope(ocinuke.CompartmentScope),
		[]types.Collection{params.Includes, pr.cfg.ResourceTypes.Includes},
		excludes,
		nil, nil,
	)
}

// newCompartmentScanner constructs the single scanner.Scanner for (region, compartmentID) --
// the shared body both buildScanners and buildNukes call, so per-compartment scanner
// construction cannot drift between the two call sites. Owner/ListerOpts.Region vary per call
// (region is now an explicit parameter, no longer read directly off pr.homeRegion); HomeRegion
// stays pr.homeRegion -- the tenancy-wide, scope.ResolveRegions-corrected value constant across
// every scanner of every compartment's Nuke -- so ocinuke.ListerOpts.BeforeList's Global-
// geography guard compares each scanner's own Region against the one true home region,
// regardless of which region this particular scanner was built for.
func newCompartmentScanner(
	pr *pipelineRun, resourceTypes types.Collection, region, compartmentID string, occupancy func() ocinuke.Occupancy,
) (*scanner.Scanner, error) {
	hasBlocklistedDescendant := false
	if pr.tree != nil {
		hasBlocklistedDescendant = pr.tree.HasBlocklistedDescendant(compartmentID, pr.blocklist)
	}

	return scanner.New(&scanner.Config{
		Owner:         fmt.Sprintf("%s/%s", region, compartmentID),
		ResourceTypes: resourceTypes,
		Opts: &ocinuke.ListerOpts{
			ConfigProvider:                      pr.provider,
			Region:                              region,
			CompartmentID:                       compartmentID,
			TenancyID:                           pr.cfg.TenancyID,
			HomeRegion:                          pr.homeRegion,
			Clients:                             pr.clients,
			SafetyFilter:                        pr.safetyFilter,
			VaultDeletionWindowDays:             pr.vaultDeletionWindowDays,
			SecretDeletionWindowDays:            pr.secretDeletionWindowDays,
			CompartmentHasBlocklistedDescendant: hasBlocklistedDescendant,
			CompartmentOccupancy:                occupancy,
		},
		Logger: pr.logger,
	})
}

// buildScanners performs the resource-type resolution and one scanner.New call per
// (region, compartment) pair in the resolved in-scope set -- replacing Phase 1's
// single-compartment stub with real subtree coverage (SCOPE-01/SCOPE-02 reinforced at the
// integration layer). Production code now builds scanners through buildNukes (below), since a
// single shared Nuke cannot apply per-compartment Filters (see buildNukes' doc comment); this
// function remains for command_test.go's
// TestRunPipeline_ResolvesSubtreeAndSkipsBlocklistedDescendant, which asserts the resolved
// scanner set directly against a synthetic tree. Kept in sync with buildNukes' region loop below
// so the two call sites never drift on how scanners are constructed.
func buildScanners(pr *pipelineRun, params *libnuke.Parameters, inScope map[string]struct{}) ([]*scanner.Scanner, error) {
	resourceTypes := resolveResourceTypes(pr, params)

	scanners := make([]*scanner.Scanner, 0, len(inScope)*len(pr.regions))
	for compartmentID := range inScope {
		for _, region := range pr.regions {
			s, err := newCompartmentScanner(pr, resourceTypes, region, compartmentID, nil)
			if err != nil {
				return nil, fmt.Errorf("constructing scanner for %s/%s: %w", region, compartmentID, err)
			}
			scanners = append(scanners, s)
		}
	}

	return scanners, nil
}

// buildNukes constructs one *libnuke.Nuke per in-scope compartment (unchanged from Phase 2),
// each with its own cfg.ResolveFilters(compartmentID) result as its Filters, and now registers
// one *scanner.Scanner per resolved region (pr.regions, scope.ResolveRegions' validated output)
// onto that same compartment's Nuke -- never one Nuke per (region, compartment) pair.
//
// A single shared Nuke cannot make per-compartment filters (CONF-04) actually take effect: a
// *libnuke.Nuke's Filters field is a flat filter.Filters keyed ONLY by resource type
// (Filters.Get(item.Type), confirmed by reading nuke.go's filterWithoutGroups/filterWithGroups
// directly) -- never by compartment. One shared Nuke registering every compartment's scanner
// against one Filters value would silently apply whichever compartment's filters were resolved
// last to every other compartment in the run (T-02-16). This mirrors aws-nuke's own
// one-Nuke-per-account model -- the same precedent this project already follows for
// tenancy-gate ordering. Region has no equivalent collision risk with Filters (resource-type-
// keyed, not region-keyed): libnuke's own Nuke.RegisterScanner already supports many Scanners on
// one Nuke, de-duplicated only by "<scope>-<owner>" (satisfied automatically here since region
// varies per scanner while compartmentID is fixed per Nuke), so region fan-out is a loop inside
// the existing per-compartment construction, not a second dimension of Nuke construction.
//
// RegisterPrompt is deliberately never called on any Nuke built here: the destructive-run
// confirmation happens exactly once, in runPipeline, before this function runs -- not once per
// compartment. Nuke.Run() still calls Prompt() twice internally regardless, but with no
// registered prompt function it is a no-op (Nuke.Prompt() returns nil when n.prompt is nil).
//
// Construction order is deepest-first (Phase 6, COMP-01/COMP-02): tree.DeletionOrder(inScope)
// replaces the prior `for compartmentID := range inScope` map iteration, whose order was
// non-deterministic. Because runNukes' own execution loop is already sequential (`for _, n :=
// range nukes { n.Run(ctx) }`), fixing this function's construction order is sufficient on its
// own to guarantee bottom-up completion: a parent compartment's *libnuke.Nuke is never even
// constructed, let alone run, until every compartment nested beneath it (and also a member of
// inScope) has already had its own Nuke run to completion -- converged or exhausted its own
// retry budget. runNukes itself needs no change to become bottom-up-correct as a consequence.
func buildNukes(pr *pipelineRun, params *libnuke.Parameters, inScope map[string]struct{}, tree *scope.Tree) ([]*libnuke.Nuke, error) {
	resourceTypes := resolveResourceTypes(pr, params)

	nukes := make([]*libnuke.Nuke, 0, len(inScope))
	for _, compartmentID := range tree.DeletionOrder(inScope) {
		resolvedFilters, err := pr.cfg.ResolveFilters(compartmentID)
		if err != nil {
			return nil, fmt.Errorf("resolving filters for compartment %q: %w", compartmentID, err)
		}

		n := libnuke.New(params, resolvedFilters, pr.cfg.Settings)
		n.SetLogger(pr.logger.WithField("component", "libnuke"))
		n.SetRunSleep(nukeRunSleep)
		n.RegisterVersion(common.VersionString())
		n.RegisterValidateHandler(func() error {
			return verifyTenancy(pr.ctx, pr.provider, pr.cfg.TenancyID)
		})
		occupancy := compartmentOccupancy(pr, n, compartmentID)

		for _, region := range pr.regions {
			s, err := newCompartmentScanner(pr, resourceTypes, region, compartmentID, occupancy)
			if err != nil {
				return nil, fmt.Errorf("constructing scanner for %s/%s: %w", region, compartmentID, err)
			}
			if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
				return nil, fmt.Errorf("registering scanner for %s/%s: %w", region, compartmentID, err)
			}
		}

		nukes = append(nukes, n)
	}

	// The tenancy root is deliberately absent from inScope -- SAFE-06 refuses it as a target --
	// so a type OCI permits only there is listed nowhere and dropped everywhere. When the
	// operator named such types explicitly, add one final Nuke for the root restricted to exactly
	// those types: the only compartment they can occupy, and the only types it may touch.
	rootNukes, err := buildTenancyRootNukes(pr, params)
	if err != nil {
		return nil, err
	}
	nukes = append(nukes, rootNukes...)

	return nukes, nil
}

// resolveDeletionWindows reads settings.vault's two scheduled-deletion windows into pr.
func resolveDeletionWindows(pr *pipelineRun) error {
	vaultDays, err := ocinuke.VaultDeletionWindowDaysFromSettings(pr.cfg.Settings)
	if err != nil {
		return err
	}
	secretDays, err := ocinuke.SecretDeletionWindowDaysFromSettings(pr.cfg.Settings)
	if err != nil {
		return err
	}
	pr.vaultDeletionWindowDays, pr.secretDeletionWindowDays = vaultDays, secretDays
	return nil
}

// compartmentOccupancy is what Compartment.Remove() asks before deleting compartmentID: this
// Nuke's own queue, which holds every resource of the compartment across regions, and the skip
// events the run has reported so far.
func compartmentOccupancy(pr *pipelineRun, n *libnuke.Nuke, compartmentID string) func() ocinuke.Occupancy {
	return func() ocinuke.Occupancy {
		var skips []scope.SkipEvent
		if pr.skipEvents != nil {
			skips = pr.skipEvents()
		}
		return ocinuke.CompartmentOccupancy(compartmentID, n.Queue.GetItems(), skips)
	}
}

// tenancyRootAllowance builds this run's allowance from config. An absent or empty
// tenancy-root-types list yields the fail-closed zero value, so a config written before the key
// existed behaves exactly as it did then.
func tenancyRootAllowance(pr *pipelineRun) ocinuke.TenancyRootAllowance {
	if len(pr.cfg.TenancyRootTypes) == 0 {
		return ocinuke.TenancyRootAllowance{}
	}
	allowed := make(map[string]struct{}, len(pr.cfg.TenancyRootTypes))
	for _, name := range pr.cfg.TenancyRootTypes {
		allowed[name] = struct{}{}
	}
	return ocinuke.TenancyRootAllowance{TenancyOCID: pr.cfg.TenancyID, Types: allowed}
}

// buildTenancyRootNukes returns the one extra Nuke that scans the tenancy root, as a slice so
// "nothing to scan there" is an empty result rather than a nil value paired with a nil error. It
// is empty whenever the config named no tenancy-root types, or named only types the run's own
// includes/excludes already removed. The Nuke's resource-type set is the INTERSECTION of the
// allowance and the run's normally-resolved types, so resource-types.excludes and --exclude still
// win: opting a type in here can never re-admit one the operator also excluded.
func buildTenancyRootNukes(pr *pipelineRun, params *libnuke.Parameters) ([]*libnuke.Nuke, error) {
	allowance := tenancyRootAllowance(pr)
	if len(allowance.Types) == 0 {
		return nil, nil
	}

	rootTypes := make(types.Collection, 0, len(allowance.Types))
	for _, name := range resolveResourceTypes(pr, params) {
		if _, ok := allowance.Types[name]; ok {
			rootTypes = append(rootTypes, name)
		}
	}
	if len(rootTypes) == 0 {
		return nil, nil
	}

	resolvedFilters, err := pr.cfg.ResolveFilters(pr.cfg.TenancyID)
	if err != nil {
		return nil, fmt.Errorf("resolving filters for tenancy root %q: %w", pr.cfg.TenancyID, err)
	}

	n := libnuke.New(params, resolvedFilters, pr.cfg.Settings)
	n.SetLogger(pr.logger.WithField("component", "libnuke"))
	n.SetRunSleep(nukeRunSleep)
	n.RegisterVersion(common.VersionString())
	n.RegisterValidateHandler(func() error {
		return verifyTenancy(pr.ctx, pr.provider, pr.cfg.TenancyID)
	})

	for _, region := range pr.regions {
		s, err := newCompartmentScanner(pr, rootTypes, region, pr.cfg.TenancyID, nil)
		if err != nil {
			return nil, fmt.Errorf("constructing tenancy-root scanner for %s: %w", region, err)
		}
		if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
			return nil, fmt.Errorf("registering tenancy-root scanner for %s: %w", region, err)
		}
	}

	return []*libnuke.Nuke{n}, nil
}

// runNukes runs every constructed *libnuke.Nuke in turn. One compartment's failure does not
// abort the remaining compartments -- every Nuke gets a chance to scan/report/remove within its
// own compartment regardless of another compartment's outcome -- and every error encountered is
// aggregated via errors.Join rather than only the first one being surfaced, so a transient
// failure in one compartment never silently hides a real failure in another.
//
// Immediately after each Nuke's n.Run(ctx) call returns -- regardless of its own error, per
// 03-RESEARCH.md Q4's exact call-site instruction -- this builds that Nuke's own plan.Entry set
// via plan.BuildFromQueue (the single writer both the dry-run plan and the destructive-run
// leftover report share), enriches that same Nuke's own Compartment leftover entry (if any) via
// plan.EnrichCompartmentLeftover (Phase 6, COMP-04) using the OTHER entries already built from
// this SAME Nuke's own queue -- no second scan -- and accumulates its own
// ocinuke.DetectUnmatchedFilters warnings, folding both into one Body/[]FilterWarning across
// every compartment's Nuke. hasUnexplainedError is computed
// per-Nuke: if this Nuke's OWN n.Run(ctx) error is non-nil AND none of this Nuke's own entries
// are StateLeftover, the failure cannot be explained by anything the leftover walk found (the
// concrete, realistic case: a Nuke.Validate() rejection before Scan() ever ran, leaving an empty
// queue) -- this is the exact signal Task 2's classifyOutcome needs to never downgrade an
// unrelated internal failure to a mere leftover.
func runNukes(
	ctx context.Context, nukes []*libnuke.Nuke, noDryRun bool, logger logrus.FieldLogger,
) (plan.Body, []ocinuke.FilterWarning, bool, error) {
	var errs []error
	var allEntries []plan.Entry
	var allWarnings []ocinuke.FilterWarning
	hasUnexplainedError := false

	for _, n := range nukes {
		nukeErr := n.Run(ctx)
		if nukeErr != nil {
			errs = append(errs, nukeErr)
		}

		items := n.Queue.GetItems()
		entries := plan.BuildFromQueue(items, noDryRun, plan.ClassifyLeftover)
		if len(entries) > 0 {
			// entries[0].CompartmentID is safe to read as "this Nuke's own compartment": every
			// entry built from one Nuke's queue shares the same compartment (one Nuke per
			// compartment, unchanged architecture since Phase 2) -- guarded by len(entries) > 0
			// since an empty-queue Nuke (nothing scanned) has no CompartmentID to read.
			entries = plan.EnrichCompartmentLeftover(entries, entries[0].CompartmentID)
		}
		allWarnings = append(allWarnings, ocinuke.DetectUnmatchedFilters(n.Filters, items)...)
		logLeftovers(logger, entries)

		if nukeErr != nil && !hasLeftoverEntry(entries) {
			hasUnexplainedError = true
		}

		allEntries = append(allEntries, entries...)
	}

	body := plan.Body{SchemaVersion: plan.SchemaVersion, Entries: allEntries}
	return body, allWarnings, hasUnexplainedError, errors.Join(errs...)
}

// hasLeftoverEntry reports whether entries contains at least one StateLeftover entry -- the
// per-Nuke check runNukes uses to decide whether that Nuke's own n.Run(ctx) error is already
// explained by a leftover the walk found, or is genuinely unexplained.
func hasLeftoverEntry(entries []plan.Entry) bool {
	for _, e := range entries {
		if e.State == plan.StateLeftover {
			return true
		}
	}
	return false
}

// Log-field keys shared by the three skip/leftover log sites below -- goconst's threshold is 3.
const (
	logFieldCompartmentID = "compartment_id"
	logFieldReason        = "reason"
)

// logLeftovers names every leftover, with the API's own words, as soon as the compartment it
// belongs to finishes -- not only in the table at the end of the whole run.
//
// Per-compartment rather than per-round because libnuke exposes no per-round hook: HandleQueue
// calls item.Print() itself (pkg/queue/item.go), which prints the bare word "failed" and never
// item.Reason, and neither SetLogger nor RegisterValidateHandler can reach in front of it. This
// is the closest seam this tool owns, and it is the difference between an operator seeing "409
// BucketNotEmpty" while the run is still going and reading "api-error" twenty minutes later.
func logLeftovers(logger logrus.FieldLogger, entries []plan.Entry) {
	if logger == nil {
		return
	}
	for _, e := range entries {
		if e.State != plan.StateLeftover {
			continue
		}
		logger.WithFields(logrus.Fields{
			"resource_type":       e.ResourceType,
			"resource_id":         e.ResourceID,
			logFieldCompartmentID: e.CompartmentID,
			"region":              e.Region,
			logFieldReason:        string(e.Reason),
			"detail":              e.Detail,
		}).Warn("leftover: this resource was not removed")
	}
}

// runNukesFn is a package-level seam wrapping runNukes, mirroring verifyTenancy/
// newScopeIdentityClient/newRegionClient's stub pattern above -- it lets tests observe the
// *libnuke.Nuke slice buildNukes actually constructed (e.g. each scanner's ListerOpts) before
// (or while) it runs, without runPipeline needing to return internal state.
var runNukesFn = runNukes

// finishNukes runs every constructed *libnuke.Nuke (via runNukesFn), merges blocklistSkipEvents
// and accumulator's resource-level leftover events into the resulting plan.Body, and
// writes/prints the single canonical artifact both PLAN-01 and PLAN-03 share via
// buildAndReportArtifact -- the last step of runPipeline's locked ordering.
// classifyOutcome's internal > leftovers > clean precedence (exitcode.go) is the ONLY place this
// function's final return value is decided -- when !isFailure, this returns nil even if runErr is
// non-nil, because a leftover-explained n.Run() error without --fail-on-leftover must still exit
// ExitClean (locked requirement 5): the artifact already recorded the leftover unconditionally,
// only the exit signal is gated by the flag.
func finishNukes(pr *pipelineRun, nukes []*libnuke.Nuke, blocklistSkipEvents []scope.SkipEvent, accumulator *leftoverAccumulator) error {
	body, warnings, hasUnexplainedError, runErr := runNukesFn(pr.ctx, nukes, pr.opts.NoDryRun, pr.logger)

	// One combined skip-event slice, one plan.MergeSkipEvents call -- blocklist skips
	// (resolveScope, already flowing since Plan 03-06) plus this run's resource-level leftover
	// events (Plan 04-01's ocinuke.CurrentReporter, accumulated above via SetRunContext), never
	// two separate merge passes over the same body.Entries list (T-04-06).
	skipEvents := append(append([]scope.SkipEvent(nil), blocklistSkipEvents...), accumulator.snapshot()...)

	artifact, err := buildAndReportArtifact(&pr.opts, pr.logger, body, skipEvents, warnings, pr.tree)
	if err != nil {
		return err
	}

	leftoverCount := countLeftovers(artifact.Body.Entries)
	code, isFailure := classifyOutcome(hasUnexplainedError, leftoverCount, pr.opts.FailOnLeftover)
	if !isFailure {
		return nil
	}

	finalErr := runErr
	if finalErr == nil {
		finalErr = fmt.Errorf("run finished with %d unresolved leftover(s) and no explaining error", leftoverCount)
	}
	return &ExitError{Code: code, Err: finalErr}
}

// buildArtifact merges skipEvents into body via plan.MergeSkipEvents and computes the versioned,
// hashed Artifact -- the single hash-construction path both buildAndReportArtifact (the real
// run's artifact) and verifyApprovedPlan (the forced-rescan verification pass) share. There must
// be no second, independently-written hashing/artifact-construction logic anywhere in this file
// -- both callers reuse this function unmodified, and it is the only call site in this file for
// pkg/plan's own artifact constructor.
func buildArtifact(body plan.Body, skipEvents []scope.SkipEvent, tree *scope.Tree) (plan.Artifact, error) {
	body.Entries = plan.MergeSkipEvents(body.Entries, skipEvents)
	if tree != nil {
		body.Entries = plan.DeferNonEmptyCompartments(body.Entries, tree.Children, tree.LifecycleState)
	}

	artifact, err := plan.NewArtifact(body)
	if err != nil {
		return plan.Artifact{}, fmt.Errorf("building plan artifact: %w", err)
	}
	return artifact, nil
}

// buildAndReportArtifact builds the versioned Artifact via buildArtifact, writes it to
// opts.PlanOut, and prints the human-readable table (plan.RenderTable) plus any SAFE-08
// unmatched-filter warnings (ocinuke.FormatWarnings) via logger at Warn level -- the single place
// this run's shared plan/leftover writer's output actually reaches disk and the terminal. A
// marshaling failure inside buildArtifact's own hash construction is itself a hard error: this
// never silently skips writing.
func buildAndReportArtifact(
	opts *pipelineOptions,
	logger *logrus.Logger,
	body plan.Body,
	skipEvents []scope.SkipEvent,
	warnings []ocinuke.FilterWarning,
	tree *scope.Tree,
) (plan.Artifact, error) {
	artifact, err := buildArtifact(body, skipEvents, tree)
	if err != nil {
		return plan.Artifact{}, err
	}

	if err := writePlanArtifact(opts.PlanOut, artifact); err != nil {
		return plan.Artifact{}, fmt.Errorf("writing plan artifact to %s: %w", opts.PlanOut, err)
	}

	logger.Infof("plan artifact written to %s (schema_version=%d, hash=%s, entries=%d)",
		opts.PlanOut, artifact.Body.SchemaVersion, artifact.Hash, len(artifact.Body.Entries))
	fmt.Print(plan.RenderTable(artifact.Body.Entries))

	for _, line := range ocinuke.FormatWarnings(warnings) {
		logger.Warn(line)
	}

	return artifact, nil
}

// writePlanArtifact marshals artifact as indented JSON (for on-disk human/tool readability only
// -- determinism is guaranteed for artifact.Hash, computed over Body alone inside buildArtifact,
// never for this outer indented wrapper's exact bytes) and writes it to path. 0o600 (owner
// read/write only), not the more permissive 0o644: 03-RESEARCH.md's Security Domain findings
// flag the artifact as sensitive tenancy metadata (OCIDs, resource/compartment identifiers) that
// deserves the same access-control expectations as the credentials that produced it.
func writePlanArtifact(path string, artifact plan.Artifact) error {
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling plan artifact: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// countLeftovers counts entries whose State is StateLeftover -- Task 2's classifyOutcome input,
// computed once here from the already-built artifact rather than re-walking body.Entries a
// second time.
func countLeftovers(entries []plan.Entry) int {
	count := 0
	for _, e := range entries {
		if e.State == plan.StateLeftover {
			count++
		}
	}
	return count
}

func init() {
	cmd := &cobra.Command{
		Use:     "run",
		Aliases: []string{"nuke"},
		Short:   "scan and (optionally) remove every resource in a compartment",
		RunE:    execute,
	}
	cmd.Flags().String("config", "config.yaml", "path to config file")
	cmd.Flags().String("compartment-id", "", "OCID of the compartment to target")
	cmd.Flags().Bool("no-dry-run", false, "actually remove resources (default: dry-run)")
	cmd.Flags().String("profile", "", "OCI config file profile (config-file auth only)")
	cmd.Flags().String("auth", "",
		"auth method: config-file|instance-principal|workload-identity|github-oidc|api-key "+
			"(default: auto-detect; github-oidc and api-key are never auto-detected, only reachable "+
			"via this explicit flag)")
	cmd.Flags().Bool("quiet", false, "hide filtered resources from output")
	cmd.Flags().StringSlice("include", nil, "only include these resource types (overrides config)")
	cmd.Flags().StringSlice("exclude", nil, "exclude these resource types (overrides config, always wins)")
	cmd.Flags().Bool("force", false,
		"skip the destructive-run confirmation prompt (for CI; recorded in the run output)")
	cmd.Flags().Int("force-sleep", 3,
		"seconds to wait after --force before continuing (minimum 3, enforced by libnuke)")
	cmd.Flags().String("plan-out", "oci-nuke-plan.json",
		"path to write the canonical, versioned plan/leftover artifact to -- written on every run, dry or destructive")
	cmd.Flags().Int("max-wait-retries", 180,
		"max rounds (at the 5s run-sleep interval, ~15 minutes by default, the floor a compartment delete "+
			"needs for real OKE/LB/NAT async cleanup lag) a resource may stay in a waiting state before it is "+
			"reported as a leftover; raise this for genuinely slower services")
	cmd.Flags().Bool("fail-on-leftover", false,
		"exit non-zero if any resource remains unresolved when the run finishes (the leftover is always "+
			"reported regardless of this flag)")
	cmd.Flags().Bool("delete-compartments", false,
		"delete the target compartment itself after everything inside it is gone (also enabled by "+
			"settings.compartment.delete: true in config; the flag can only turn this ON, never override a "+
			"config key that already enabled it)")
	cmd.Flags().String("oidc-region", "", "OCI region for --auth github-oidc (required; not auto-detected)")
	cmd.Flags().String("oidc-audience", "",
		"audience value requested from GitHub's OIDC provider for --auth github-oidc "+
			"(recommended: the identity domain's own URL)")
	cmd.Flags().String("approved-plan", "",
		"path to a previously-approved plan artifact (pkg/plan.Artifact JSON); when set, a destructive run "+
			"refuses unless a fresh forced-dry-run rescan hashes identically (doubles this run's OCI listing "+
			"calls: it re-scans the entire scope once to verify, once to apply)")
	cmd.Flags().Duration("max-plan-age", 24*time.Hour,
		"maximum age of --approved-plan's generated_at before it is refused, checked before any OCI API call")
	// The confidential application's domain URL, client ID, and client secret are deliberately
	// NOT cobra flags -- no such --oidc-... flags exist for any of the three. They are read
	// directly from OCI_OIDC_DOMAIN_URL/OCI_OIDC_CLIENT_ID/OCI_OIDC_CLIENT_SECRET via
	// os.Getenv in execute() below, so the client secret in particular can never appear in a
	// process listing (`ps`) or an echoed CI workflow command line -- env-var-only by design
	// (07-CONTEXT.md, AUTH-06's 2026-08-09 restatement).
	//
	// --auth api-key's six values follow the same rule and for the sharper version of the same
	// reason: OCI_CLI_KEY_CONTENT holds a private key's own PEM bytes. There is no
	// --api-key-... flag of any kind, not even for the four values that are merely identifiers
	// rather than secrets (tenancy, user, fingerprint, region) -- splitting one credential
	// across two input channels would invite exactly the copy-paste that puts the fifth on a
	// command line too. The set is OCI_CLI_TENANCY, OCI_CLI_USER, OCI_CLI_FINGERPRINT,
	// OCI_CLI_REGION, OCI_CLI_KEY_CONTENT, and the optional OCI_CLI_KEY_PASSPHRASE.

	global.AddFlags(cmd)
	common.RegisterCommand(cmd)
}

// apiKeyCredentials carries --auth api-key's six values between the environment and
// ociauth.ResolveOptions. It exists so readAPIKeyEnv can be tested against the documented
// variable names: a typo in one of the six os.Getenv keys is otherwise silent -- the run just
// reports that variable as missing, and the operator goes looking at their secret rather than
// at this file.
type apiKeyCredentials struct {
	tenancyOCID string
	userOCID    string
	fingerprint string
	region      string
	privateKey  string
	passphrase  string
}

// readAPIKeyEnv reads --auth api-key's credentials from the environment. The names are the
// names are single-sourced in pkg/ociauth so the set an error message tells an operator to set
// cannot drift from the set actually read. No validation happens here
// -- ociauth.Resolve names every missing variable at once, and validating in two places would
// let the two lists drift apart.
func readAPIKeyEnv() apiKeyCredentials {
	return apiKeyCredentials{
		tenancyOCID: os.Getenv(ociauth.EnvAPIKeyTenancy),
		userOCID:    os.Getenv(ociauth.EnvAPIKeyUser),
		fingerprint: os.Getenv(ociauth.EnvAPIKeyFingerprint),
		region:      os.Getenv(ociauth.EnvAPIKeyRegion),
		privateKey:  os.Getenv(ociauth.EnvAPIKeyKeyContent),
		passphrase:  os.Getenv(ociauth.EnvAPIKeyPassphrase),
	}
}
