package ocinuke

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// InScope reports whether compartmentID is in the resolved in-scope set for this run. Injected
// once per run (see pipelineRun in pkg/commands/run) so scopedLister needs no global state.
type InScope func(compartmentID string) bool

// CompartmentScoped is the interface every resource struct registered through Register must
// implement so scopedLister can extract the resource's OWN compartment OCID for
// re-verification -- this is distinct from (and a stricter check than) the compartment the
// Lister was asked to search. Concretely, resource authors implement this by reading whatever
// field the OCI SDK response struct calls CompartmentId.
type CompartmentScoped interface {
	resource.Resource
	GetCompartmentID() string
}

// SafetyEvaluated is the interface every resource type must implement so scopedLister can apply
// ocinuke.Evaluate (protect-by-tag/min-age) at SCAN time -- before a resource can ever become a
// queue.Item and appear in the plan artifact as would-remove -- rather than at Remove() time.
// This distinction is not cosmetic: libnuke v1.3.0's Nuke.Run() returns immediately after Scan()
// whenever Parameters.NoDryRun is false (verified against libnuke@v1.3.0's own source), so
// Remove() is never reached at all during a dry run. A protection evaluated only inside Remove()
// is therefore invisible to the plan -- a dry run would report "would-remove" for a resource a
// real, destructive run would actually refuse to delete. The direction of that error is safe
// (under-deletion, never over-deletion), but it defeats the purpose of a plan an operator is
// meant to be able to verify before committing to a destructive run. See 04-13's plan/apply
// divergence fix for the full account.
//
// Mirrors CompartmentScoped exactly: Register (below) enforces this via a registration-time
// panic, so a resource type missing an implementation is a startup failure discovered the moment
// its init() runs, never a silent pass-through discovered later (or never) at Remove() time.
//
// SafetyTags returns this resource's freeform tags, its ALREADY-FLATTENED ("<namespace>.<key>",
// TagMatch's own documented convention -- see safety_filter.go) defined tags, and its creation
// time -- the three values Evaluate needs beyond compartment/resource identity (both already
// available via CompartmentScoped.GetCompartmentID/resource.UniqueKeyGetter.UniqueKey, both
// embedded below so scopedLister can reach every value Evaluate needs off one interface). A
// resource type with no tags of a given kind, or no creation-time field at all, returns a nil map
// / the zero time.Time -- Evaluate treats both as "does not match," never as falsely "protected".
type SafetyEvaluated interface {
	CompartmentScoped
	resource.UniqueKeyGetter
	SafetyTags() (freeform, defined map[string]string, createdAt time.Time)
}

// scopedLister wraps a registry.Lister so every resource it returns is re-verified against the
// resolved in-scope set before it can ever become a queue.Item. This is the ONLY mandatory,
// unconditional, per-resource enforcement point -- unlike resource.Filter() or QueueItemHook
// (both per-resource-type opt-in, see 02-RESEARCH.md Q3), every resource type registered through
// Register gets it for free, because the wrapping happens once here, not per resource struct.
//
// Concrete failure mode this defends against (not hypothetical): a Lister for a service that
// supports compartmentIdInSubtree=true as a call-count optimization will legitimately return
// resources from descendants of the compartment it was asked to search -- including any
// blocklisted descendant the resolver pruned out of the in-scope set. Without this check, such a
// resource would be scanned and, on a --no-dry-run run, deleted.
//
// Fail-closed, at two points, per 02-CONTEXT.md's "the scope seam must fail closed" addendum:
//  1. Register (below) rejects, at registration time, any resource that does not implement
//     CompartmentScoped -- a missing accessor is a startup failure, not a silent pass-through.
//  2. List's own type-assertion failure branch (defense in depth for the case this reaches
//     runtime despite (1) -- e.g. a Lister returning a different concrete type than its own
//     Registration.Resource declared) drops the resource instead of keeping it, matching the
//     project's bias toward under-deletion over over-deletion.
type scopedLister struct {
	inner        registry.Lister
	inScope      InScope
	onSkip       func(*scope.SkipEvent)
	resourceType string // Registration.Name, threaded through for SkipEvent.ResourceType
}

// List delegates to the wrapped Lister, then drops any returned resource whose own compartmentId
// falls outside the resolved in-scope set -- or that cannot be placed in a compartment at all --
// or that ocinuke.Evaluate (protect-by-tag/min-age) matches -- before it is ever returned to the
// caller. A dropped resource never becomes a queue.Item: it never appears in a scan summary and
// never prints "would remove", in either a dry run or a destructive run (04-13 plan/apply
// divergence fix -- see SafetyEvaluated's doc comment for why this must happen here, at scan
// time, rather than inside Remove()).
//
// opts is asserted to *ListerOpts to reach this run's SafetyFilter -- the same type assertion
// every real Lister.List already performs on its own copy of opts before this wrapper ever runs,
// so in production this assertion always succeeds (opts already reached here via a successful
// s.inner.List(ctx, opts) call above). A test that constructs a scopedLister directly and passes
// opts=nil (or a different type) simply skips the safety-evaluation branch below -- scope
// re-verification, the other, independently mandatory check, is unaffected either way.
func (s *scopedLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	resources, err := s.inner.List(ctx, opts)
	if err != nil {
		return nil, err
	}

	lo, _ := opts.(*ListerOpts)

	kept := make([]resource.Resource, 0, len(resources))
	for _, r := range resources {
		scoped, ok := r.(CompartmentScoped)
		if !ok {
			// Fail closed: a resource that cannot be re-verified is dropped, never kept. This
			// branch should be unreachable in normal operation once Register's registration-time
			// guard is in place, but must still fail closed if it is ever hit.
			if s.onSkip != nil {
				s.onSkip(&scope.SkipEvent{
					Reason:       scope.ReasonOutOfScope,
					ResourceType: s.resourceType,
					Detail:       "resource does not implement CompartmentScoped; scope membership could not be verified",
				})
			}
			continue
		}
		// The tenancy-root allowance is consulted ONLY after the resolved in-scope set has
		// already refused this compartment, and it can only ever admit a type the config named
		// explicitly at this run's own verified tenancy root -- see TenancyRootAllowance for why
		// that is not a weakening of the tenancy gate. Safety evaluation below still runs on
		// whatever it admits, so protect-by-tag and min-age are not bypassed.
		if !s.inScope(scoped.GetCompartmentID()) &&
			!CurrentTenancyRootAllowance().Allows(s.resourceType, scoped.GetCompartmentID()) {
			if s.onSkip != nil {
				s.onSkip(&scope.SkipEvent{
					Reason:        scope.ReasonOutOfScope,
					ResourceType:  s.resourceType,
					CompartmentID: scoped.GetCompartmentID(),
				})
			}
			continue // dropped before it ever becomes a queue.Item -- never printed as "would remove"
		}

		if lo != nil {
			if evt := s.evaluateSafety(r, scoped, lo.SafetyFilter); evt != nil {
				if s.onSkip != nil {
					s.onSkip(evt)
				}
				continue // dropped before it ever becomes a queue.Item -- never printed as "would remove"
			}
		}

		kept = append(kept, r)
	}
	return kept, nil
}

// evaluateSafety applies ocinuke.Evaluate (protect-by-tag/min-age) to r using cfg, returning the
// matching *scope.SkipEvent if r is protected, or nil if it is not. scoped is threaded through
// (rather than re-asserted here) since List's caller already has it from the scope check directly
// above -- CompartmentScoped.GetCompartmentID is the same accessor SafetyEvaluated embeds.
func (s *scopedLister) evaluateSafety(r resource.Resource, scoped CompartmentScoped, cfg SafetyFilterConfig) *scope.SkipEvent {
	safe, ok := r.(SafetyEvaluated)
	if !ok {
		// Fail closed, mirroring the CompartmentScoped branch above: unreachable in normal
		// operation once Register's registration-time guard is in place (every registered
		// resource type implements SafetyEvaluated or Register panics at init()), but must still
		// fail closed -- drop, never keep -- if it is ever hit.
		return &scope.SkipEvent{
			Reason:        scope.ReasonOutOfScope,
			ResourceType:  s.resourceType,
			CompartmentID: scoped.GetCompartmentID(),
			Detail:        "resource does not implement SafetyEvaluated; protect-by-tag/min-age could not be verified",
		}
	}

	freeform, defined, createdAt := safe.SafetyTags()
	return Evaluate(scoped.GetCompartmentID(), s.resourceType, safe.UniqueKey(), freeform, defined, createdAt, cfg)
}

// Register is the ONLY sanctioned way to register a resource type with libnuke's registry from
// this project. Every resources/*.go file must call ocinuke.Register, never registry.Register
// directly -- this is a structural guarantee, not a convention: TestNoDirectRegistryRegisterInResources
// (registry_integrity_test.go) fails the build the moment any resources/*.go file bypasses it,
// mirroring Phase 1's TestNoOCISDKImport structural guarantee for pkg/config.
//
// Register asserts, before doing anything else, that reg.Resource implements CompartmentScoped
// AND SafetyEvaluated, panicking if either does not hold -- mirroring registry.Register's own
// panic on a duplicate registration name, so a missing accessor is a startup failure discovered
// the moment the resource file's init() runs, not a silent runtime pass-through discovered later
// or never. Only once both assertions hold does Register wrap reg.Lister in a scopedLister and
// delegate to registry.Register.
func Register(reg *registry.Registration, inScope InScope, onSkip func(*scope.SkipEvent)) {
	if _, ok := reg.Resource.(CompartmentScoped); !ok {
		panic(fmt.Sprintf(
			"ocinuke.Register: resource type %q does not implement ocinuke.CompartmentScoped "+
				"(missing GetCompartmentID() string) -- every resource type must be re-verifiable "+
				"against the resolved compartment scope; this is a startup failure, not a "+
				"silent pass-through (see 02-CONTEXT.md, \"the scope seam must fail closed\")",
			reg.Name,
		))
	}
	if _, ok := reg.Resource.(SafetyEvaluated); !ok {
		panic(fmt.Sprintf(
			"ocinuke.Register: resource type %q does not implement ocinuke.SafetyEvaluated "+
				"(missing UniqueKey() string and/or SafetyTags() (freeform, defined map[string]string, "+
				"createdAt time.Time)) -- every resource type must expose its tags/creation time so "+
				"protect-by-tag/min-age can be evaluated at scan time, before this resource can ever "+
				"appear in the plan as would-remove; this is a startup failure, not a silent "+
				"pass-through (04-13 plan/apply divergence fix)",
			reg.Name,
		))
	}

	reg.Lister = &scopedLister{
		inner:        reg.Lister,
		inScope:      inScope,
		onSkip:       onSkip,
		resourceType: reg.Name,
	}
	registry.Register(reg)
}
