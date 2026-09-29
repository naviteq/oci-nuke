package run

// ExitError pairs a process exit code with the underlying error that caused it. main.go's
// errors.As check unwraps this before its existing, unconditional os.Exit(1) fallback --
// every error path NOT explicitly wrapped in ExitError still exits 1 exactly as before this
// phase (03-RESEARCH.md Q9's "additive, not breaking" property).
type ExitError struct {
	Code int
	Err  error
}

// Error returns the underlying error's message verbatim -- wrapping in ExitError never changes
// what an operator or a test asserting on err.Error() observes.
func (e *ExitError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error to errors.Is/errors.As chains.
func (e *ExitError) Unwrap() error { return e.Err }

// The four exit codes runPipeline can produce, each documented in README.md's "Exit codes"
// section. ExitClean=0 and ExitInternal=1 preserve this project's pre-Phase-3 behavior for
// every uncategorized error path (main.go's existing os.Exit(1) default); ExitRefused and
// ExitLeftovers are net-new, distinct codes a CI pipeline can branch on.
const (
	// ExitClean means the run completed with nothing left unresolved that --fail-on-leftover
	// would care about, or --fail-on-leftover was not set. A leftover-containing run without
	// --fail-on-leftover still exits ExitClean -- it still reports everything (locked
	// requirement 5), only the exit signal differs.
	ExitClean = 0
	// ExitInternal is the uncategorized-error default for every error not explicitly wrapped in
	// ExitError below -- unchanged from this project's exit(1) behavior before this phase, and
	// also the code an unexplained internal failure (a *libnuke.Nuke.Run() error with zero
	// corresponding leftover entries) is explicitly classified as by classifyOutcome.
	ExitInternal = 1
	// ExitRefused means a pre-scan safety-model refusal gate rejected the run before any
	// *libnuke.Nuke was constructed: tenancy root target, empty blocklist, missing
	// --compartment-id, tenancy mismatch, region validation failure, unknown target compartment,
	// or a blocklisted ancestor.
	ExitRefused = 2
	// ExitLeftovers means the run completed, --fail-on-leftover was set, and at least one
	// resource remained unresolved (a leftover) when the run finished.
	ExitLeftovers = 3
)

// classifyOutcome is the ONLY place exit-code precedence is decided -- runPipeline calls this
// function rather than re-implementing the if/else chain inline (03-RESEARCH.md Q9's explicit
// composition note). Precedence is internal > leftovers > clean: an unexplained internal error
// (hasUnexplainedError, computed by runNukes per-Nuke) always outranks a simultaneously-present,
// flag-gated leftover from a DIFFERENT compartment's Nuke in the same run -- it is never
// downgraded to ExitLeftovers merely because --fail-on-leftover happens to be set and another
// compartment also has real leftovers.
func classifyOutcome(hasUnexplainedError bool, leftoverCount int, failOnLeftover bool) (code int, isFailure bool) {
	switch {
	case hasUnexplainedError:
		return ExitInternal, true
	case failOnLeftover && leftoverCount > 0:
		return ExitLeftovers, true
	default:
		return ExitClean, false
	}
}
