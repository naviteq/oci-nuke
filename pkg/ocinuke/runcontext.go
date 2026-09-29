package ocinuke

import (
	"sync"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// CurrentScope and CurrentReporter are the indirection every real resources/*.go init() function
// needs. ocinuke.Register's inScope/onSkip parameters are captured once, at Register() call
// time, into the scopedLister wrapper. Every existing test call site (resources_test/*.go,
// command_test.go) calls Register from inside a live test function with a real closure already
// in hand. A real resources/*.go file calls Register from its own package init(), which runs at
// process startup -- before any run, any resolved compartment tree, any leftover accumulator
// exists. Passing ocinuke.CurrentScope and ocinuke.CurrentReporter (function values, not
// locally-constructed closures) as Register's inScope/onSkip arguments defers the lookup to
// whenever the run actually invokes them, by which point SetRunContext has installed the real,
// run-scoped functions.
//
// This is the ONE place both wiring points are documented:
//   - Every real resources/*.go init() must pass ocinuke.CurrentScope, ocinuke.CurrentReporter
//     (not a locally-constructed closure) as Register's inScope/onSkip arguments -- this is also
//     what scopedLister.List calls onSkip with for a protect-by-tag/min-age match it finds via
//     SafetyEvaluated (04-13), the same channel it already uses for an out-of-scope drop.
//   - A resource type's Filter()/Remove() implementation that needs to report a leftover reason
//     (scheduled-deletion, retention-locked, a FAULTY-equivalent api-error, ...) passes
//     ocinuke.CurrentReporter (the function value, not a per-struct stored field) as
//     ocinuke.ReportLeftover's reporter argument.
//
// Plan 04-03's scaffold template and every domain plan's task actions point here instead of
// re-deriving this wiring.
var (
	runContextMu    sync.RWMutex
	currentInScope  InScope          = func(string) bool { return false }
	currentReporter LeftoverReporter = nil
	// Fail-closed zero value: admits no type at no compartment until a run installs one.
	currentTenancyRootAllowance TenancyRootAllowance
)

// SetRunContext installs inScope and reporter as the functions CurrentScope/CurrentReporter
// delegate to, for the duration of one run. It returns a restore func that resets both back to
// the fail-closed defaults (CurrentScope reporting nothing in scope, CurrentReporter a no-op) --
// call restore once the run finishes, so a later, unrelated call (e.g. a test that forgets its
// own setup) never inherits a previous run's scope.
func SetRunContext(inScope InScope, reporter LeftoverReporter) (restore func()) {
	runContextMu.Lock()
	currentInScope = inScope
	currentReporter = reporter
	runContextMu.Unlock()

	return func() {
		runContextMu.Lock()
		currentInScope = func(string) bool { return false }
		currentReporter = nil
		runContextMu.Unlock()
	}
}

// CurrentScope reports whether compartmentID is in the resolved in-scope set for the currently
// installed run context. Before any SetRunContext call, it fails closed: it returns false for
// every compartment ID, never defaulting to passing resources through.
func CurrentScope(compartmentID string) bool {
	runContextMu.RLock()
	defer runContextMu.RUnlock()
	return currentInScope(compartmentID)
}

// CurrentReporter reports evt through the currently installed run context's reporter. Before any
// SetRunContext call (or after a nil reporter was installed), it is a safe no-op. The lock is
// never held while invoking the caller-supplied reporter function, avoiding a re-entrant deadlock
// if the reporter itself ever calls back into this package.
//
// evt is taken by pointer: *scope.SkipEvent is the project-wide convention for this type, shared
// by LeftoverReporter, scopedLister's onSkip field, and Evaluate's return, so no seam in this
// path ever copies the struct by value.
func CurrentReporter(evt *scope.SkipEvent) {
	runContextMu.RLock()
	reporter := currentReporter
	runContextMu.RUnlock()

	if reporter != nil {
		reporter(evt)
	}
}

// TenancyRootAllowance is the run-scoped, type-restricted admission of resources that live at
// the tenancy root. It exists for one structural reason: OCI permits a handful of types --
// DynamicGroup today -- nowhere else, so their own CompartmentId is always the tenancy OCID, a
// value the resolved in-scope set never contains because SAFE-06 refuses the tenancy root as a
// target outright. Without an allowance those types are registered, listed and then dropped, and
// no configuration can ever delete one. See docs/adr/0003-tenancy-root-type-allowance.md.
//
// What this does NOT do, and the distinction is the whole safety argument: it does not make the
// tenancy root a target. scope.RefuseTenancyRoot still rejects a --compartment-id that names the
// tenancy, before any API call, exactly as before. The allowance widens nothing except the
// scope check, for the named types, at the one compartment those types can structurally occupy.
// Every other guard still applies downstream -- protect-by-tag and min-age are evaluated after
// this check in scopedLister.List, the compartment blocklist is untouched, and dry-run is still
// the default.
//
// The zero value admits nothing: an empty TenancyOCID or a nil Types map makes Allows report
// false for every input, so a run that never installs an allowance behaves exactly as it did
// before this type existed.
type TenancyRootAllowance struct {
	// TenancyOCID is the verified tenancy root OCID this run authenticated against -- never a
	// value derived from the operator's target, and never a prefix match.
	TenancyOCID string
	// Types holds the registry.Registration.Name of every resource type admitted at the root.
	Types map[string]struct{}
}

// Allows reports whether resourceType may be kept when the resource's own compartment is
// compartmentID. Both halves must hold: the compartment must be exactly this run's verified
// tenancy root, and the type must have been named explicitly in the config. A type not named,
// or a compartment that merely looks like a root, is refused.
func (a TenancyRootAllowance) Allows(resourceType, compartmentID string) bool {
	if a.TenancyOCID == "" || compartmentID != a.TenancyOCID || a.Types == nil {
		return false
	}
	_, ok := a.Types[resourceType]
	return ok
}

// SetTenancyRootAllowance installs a for the duration of one run and returns a restore func that
// resets it to the fail-closed zero value. Deliberately a separate setter rather than a third
// parameter on SetRunContext: this is the one seam in the project that widens the compartment
// scope check, and a distinctly named call makes every site that does so greppable, rather than
// hiding the widening in an argument list 43 existing call sites already pass through.
func SetTenancyRootAllowance(a TenancyRootAllowance) (restore func()) {
	runContextMu.Lock()
	currentTenancyRootAllowance = a
	runContextMu.Unlock()

	return func() {
		runContextMu.Lock()
		currentTenancyRootAllowance = TenancyRootAllowance{}
		runContextMu.Unlock()
	}
}

// CurrentTenancyRootAllowance returns the allowance installed for the current run, or the
// fail-closed zero value when none was installed.
func CurrentTenancyRootAllowance() TenancyRootAllowance {
	runContextMu.RLock()
	defer runContextMu.RUnlock()
	return currentTenancyRootAllowance
}
