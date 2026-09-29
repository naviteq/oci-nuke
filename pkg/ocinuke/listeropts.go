package ocinuke

import (
	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"

	"github.com/naviteq/oci-nuke/pkg/clients"
)

// CompartmentScope is the single registry.Scope used for every OCI resource type. OCI's
// resource model does not have Azure-style multi-level administrative scoping -- every
// resource type is compartment-agnostic in its type signature; only instances vary by
// compartment.
const CompartmentScope registry.Scope = "compartment"

// Geography distinguishes resource types that must only be scanned in the tenancy's home
// region (IAM, tag namespaces, ...) from those scanned per-subscribed-region.
type Geography string

const (
	Global   Geography = "global"
	Regional Geography = "regional"
)

// ListerOpts is the concrete options struct threaded through every registered
// Lister.List(ctx, opts) call via scanner.Config.Opts. libnuke's Lister interface takes a
// bare interface{} -- every tool built on libnuke defines its own opts struct; this is
// oci-nuke's.
type ListerOpts struct {
	ConfigProvider common.ConfigurationProvider
	Region         string
	CompartmentID  string
	TenancyID      string
	HomeRegion     string
	Clients        *clients.Cache

	// SafetyFilter holds the protect-by-tag/min-age configuration for this run, populated once
	// per run (Plan 04-02's Task 3). scopedLister.List reads it straight off this same opts value
	// -- the one every Lister.List call already receives -- and applies it at SCAN time (04-13
	// plan/apply divergence fix), so no per-resource-type storage or plumbing is needed at all:
	// unlike CurrentScope/CurrentReporter, SafetyFilter is never threaded through Register(), so
	// it has no init()-time-capture problem.
	SafetyFilter SafetyFilterConfig

	// VaultDeletionWindowDays holds this run's parsed settings.vault.deletion-window-days value
	// (05-06-PLAN.md Task 2), populated once per run in runPipeline immediately after
	// SafetyFilter above, and read fresh by newCompartmentScanner for every constructed
	// ocinuke.ListerOpts -- the exact same wiring shape SafetyFilter already established. Vault
	// and KmsKey both read this field directly in their Remove() implementations to compute an
	// explicit TimeOfDeletion, rather than relying on the OCI SDK's own 30-day default.
	VaultDeletionWindowDays int

	// CompartmentHasBlocklistedDescendant holds, for the ONE compartment this ListerOpts value's
	// CompartmentID identifies, whether scope.Tree.HasBlocklistedDescendant found a blocklisted
	// compartment anywhere beneath it. Populated once per constructed ListerOpts by a later plan's
	// newCompartmentScanner (pkg/commands/run/command.go) -- the exact same threading shape
	// VaultDeletionWindowDays above already established (a run-scoped value computed once,
	// captured into the one resource struct that needs it at list time, read fresh per compartment
	// rather than recomputed inside the resource itself). The zero value (false) is safe: every
	// OTHER resource type's Lister.List ignores this field entirely, and Compartment's own lister
	// (resources/compartment.go's compartmentLister) is the only reader.
	CompartmentHasBlocklistedDescendant bool
}

// BeforeListTenancyRoot is the companion guard for a resource type that OCI only permits at
// the tenancy root -- today, DynamicGroup alone. It returns ErrSkipRequest for every other
// compartment, so the lister never issues the call at all.
//
// The alternative was to issue the call anyway and swallow its 404. That is worse, and the
// reason is the whole point of this guard. OCI answers ListDynamicGroups against a non-root
// compartment with 404 NotAuthorizedOrNotFound -- the identical code it returns when the
// principal genuinely may not read dynamic groups. Suppressing that response by its status code
// would silence a real permission failure alongside the expected one; skipping the call by the
// structural fact that the resource cannot exist there suppresses nothing, because no request is
// made. When the compartment IS the root, the call runs and any error surfaces untouched.
//
// This changes nothing about what the tool deletes. A dynamic group's own CompartmentId is
// always the tenancy root, the resolved in-scope set never contains the tenancy root (SAFE-06),
// and scoped_lister.go's fail-closed check therefore drops every instance anyway -- see
// resources/dynamic_group.go's GetCompartmentID. That drop was always the mechanism; it just ran
// one layer too late to stop the doomed list call, which is what NR-775 reported: one
// level=error line per compartment, on every run, for a type that structurally cannot be there.
// An operator who learns to skim past those stops reading the genuine listing failures next to
// them.
func (o *ListerOpts) BeforeListTenancyRoot() error {
	// The empty-TenancyID case is checked, not assumed. Both fields are populated once per run
	// today, but a bare equality test reports "this IS the root" when both are empty, which is
	// the one way this guard could fail open and let the doomed call through again.
	if o.TenancyID == "" || o.CompartmentID != o.TenancyID {
		return liberrors.ErrSkipRequest("resource can only exist at the tenancy root")
	}
	return nil
}

// BeforeList is the geography guard every resource Lister should call first. A lister for a
// Global-geography resource type (e.g. IAM policies) invoked from a scanner registered for a
// non-home region returns ErrSkipRequest, which libnuke's scanner.list() treats as "not
// applicable to this scope instance," logged at Debug, not counted as a failure.
func (o *ListerOpts) BeforeList(geo Geography) error {
	if geo == Global && o.Region != o.HomeRegion {
		return liberrors.ErrSkipRequest("resource is home-region-only")
	}
	return nil
}
