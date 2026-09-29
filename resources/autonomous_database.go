// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. AutonomousDatabase is hand-written rather than
// scaffolded by cmd/gen-resource: database.AutonomousDatabaseSummary.TimeCreated is
// mandatory:"false" (verified this session against
// oci-go-sdk/v65@v65.123.0/database/autonomous_database_summary.go), which breaks the
// generator template's SafetyTags() body (unconditionally dereferences x.TimeCreated.Time) --
// resources/support.go's timeCreatedOrZero is used instead, mirroring Plan 04-07's
// Drg/DrgAttachment/PrivateIp/PublicIp/Vlan precedent for the same real quirk.
//
// Filter() below deliberately has NO delete-protection branch. 05-CONTEXT.md's amendment (after
// 05-RESEARCH.md's Contradiction 1) states this plainly: every field on
// AutonomousDatabaseSummary was enumerated this session, and the whole database package was
// grepped for Protect/Lock -- none of them is a delete-protection or Locks-shaped field in this
// pinned SDK version (v65.123.0). ocinuke.Evaluate's protect-by-tag/min-age (SafetyTags below,
// applied at scan time by pkg/ocinuke's scopedLister) is the only protection mechanism available
// for this type -- do not invent a substitute: no extra GetAutonomousDatabase call hunting for a
// lock field, and no repurposing of the struct's own backup-retention-lock boolean field (a real
// field on this struct, but it bounds how far the backup retention window can be shortened, not
// whether the database itself can be deleted). This is a user-approved, documented gap
// (05-CONTEXT.md, "Databases and platform services").
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/database"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// autonomousDatabaseClient is the narrow slice of database's client this lister needs -- a
// hand-written interface so AutonomousDatabase is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type autonomousDatabaseClient interface {
	ListAutonomousDatabases(
		ctx context.Context, req database.ListAutonomousDatabasesRequest,
	) (database.ListAutonomousDatabasesResponse, error)
	DeleteAutonomousDatabase(
		ctx context.Context, req database.DeleteAutonomousDatabaseRequest,
	) (database.DeleteAutonomousDatabaseResponse, error)
}

// AutonomousDatabaseResourceType is the registry.Registration.Name for AutonomousDatabase.
const AutonomousDatabaseResourceType = "AutonomousDatabase"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     AutonomousDatabaseResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &AutonomousDatabase{},
		Lister:   &autonomousDatabaseLister{},
		// DependsOn is intentionally empty. Automatic post-termination backups are enumerated as
		// a read-only residue side effect of this type's own List() (resources/
		// database_backup_support.go), never registered as an independent, deletable resource
		// type, so there is nothing this registration needs to declare a dependency edge on.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type autonomousDatabaseLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// autonomousDatabaseList, kept separate so it is unit-testable against a stub client without
// ever constructing a real Database client.
func (l *autonomousDatabaseLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("autonomousDatabaseLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Database(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Database client for %s: %w", o.Region, err)
	}

	items, err := autonomousDatabaseList(ctx, client, o.CompartmentID)
	if err != nil {
		return nil, err
	}

	// Automatic post-termination backups are enumerated as a read-only side effect of this
	// List() call, AFTER the primary resource list succeeds and BEFORE returning -- see
	// resources/database_backup_support.go's package doc comment. A failure here surfaces as
	// this List()'s own error, consistent with the project's fail-visible-not-silent philosophy
	// for every other list call, even though this one is only a reporting side effect.
	if err := reportAutonomousDatabaseBackupResidue(ctx, client, o.CompartmentID); err != nil {
		return nil, err
	}

	return items, nil
}

// autonomousDatabaseList paginates database.ListAutonomousDatabases and wraps every returned item
// as an AutonomousDatabase. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this
// is what the list test below exercises against a stub, with zero network access.
func autonomousDatabaseList(
	ctx context.Context,
	client autonomousDatabaseClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := database.ListAutonomousDatabasesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListAutonomousDatabases(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing AutonomousDatabase in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &AutonomousDatabase{client: client, db: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// AutonomousDatabase wraps one database.AutonomousDatabaseSummary.
type AutonomousDatabase struct {
	client autonomousDatabaseClient
	db     database.AutonomousDatabaseSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// AutonomousDatabaseSummary.CompartmentId is mandatory:"true" (verified this session), so a
// direct dereference is safe.
func (r *AutonomousDatabase) GetCompartmentID() string { return *r.db.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// AutonomousDatabaseSummary.Id is mandatory:"true" (verified this session).
func (r *AutonomousDatabase) UniqueKey() string { return *r.db.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// Present = AVAILABLE, PROVISIONING, UPDATING, STOPPING, STOPPED, STARTING,
// RESTORE_IN_PROGRESS, SCALE_IN_PROGRESS, UNAVAILABLE, BACKUP_IN_PROGRESS,
// AVAILABLE_NEEDS_ATTENTION, MAINTENANCE_IN_PROGRESS, RESTARTING, RECREATING,
// ROLE_CHANGE_IN_PROGRESS, UPGRADING, STANDBY, TRANSPORTING
// (database.AutonomousDatabaseSummaryLifecycleStateEnum's full 22-value const block, reconciled
// against oci-go-sdk/v65@v65.123.0/database/autonomous_database_summary.go: every one of the 22
// constants is deliberately classified below, not just the 9 originally covered). The added 9 are
// all routine, automatic, non-terminal states -- BACKUP_IN_PROGRESS and MAINTENANCE_IN_PROGRESS in
// particular are common defaults (automatic backups, OCI-initiated maintenance windows), and
// silently excluding them from teardown was a real functional gap: a database sitting in one of
// these states would be skipped from every scan with no ReportLeftover and no visible reason,
// directly undermining reliable sandbox/demo/CI teardown. None of the 9 is terminal or "going
// away" in the hang-trap sense, so including them as present carries no risk of returning an
// already-removed resource.
// TERMINATING/TERMINATED are excluded (going/gone, the hang-trap defense), as is every other
// value this switch does not explicitly recognize -- a fail-safe exclusion that deliberately
// also covers RESTORE_FAILED and INACCESSIBLE: neither is a state this tool should attempt
// TerminateDbSystem-equivalent removal against (a restore that failed or a database OCI itself
// cannot reach needs operator attention before deletion is safe), so both fall through to the
// excluded default rather than being assumed present.
//
// This switch has NO delete-protection branch -- see this file's package doc comment for why: no
// such field exists on AutonomousDatabaseSummary in the pinned SDK.
func (r *AutonomousDatabase) Filter() error {
	switch r.db.LifecycleState {
	case database.AutonomousDatabaseSummaryLifecycleStateAvailable,
		database.AutonomousDatabaseSummaryLifecycleStateProvisioning,
		database.AutonomousDatabaseSummaryLifecycleStateUpdating,
		database.AutonomousDatabaseSummaryLifecycleStateStopping,
		database.AutonomousDatabaseSummaryLifecycleStateStopped,
		database.AutonomousDatabaseSummaryLifecycleStateStarting,
		database.AutonomousDatabaseSummaryLifecycleStateRestoreInProgress,
		database.AutonomousDatabaseSummaryLifecycleStateScaleInProgress,
		database.AutonomousDatabaseSummaryLifecycleStateUnavailable,
		database.AutonomousDatabaseSummaryLifecycleStateBackupInProgress,
		database.AutonomousDatabaseSummaryLifecycleStateAvailableNeedsAttention,
		database.AutonomousDatabaseSummaryLifecycleStateMaintenanceInProgress,
		database.AutonomousDatabaseSummaryLifecycleStateRestarting,
		database.AutonomousDatabaseSummaryLifecycleStateRecreating,
		database.AutonomousDatabaseSummaryLifecycleStateRoleChangeInProgress,
		database.AutonomousDatabaseSummaryLifecycleStateUpgrading,
		database.AutonomousDatabaseSummaryLifecycleStateStandby,
		database.AutonomousDatabaseSummaryLifecycleStateTransporting:
		return nil
	default:
		return fmt.Errorf("AutonomousDatabase is %s, not available", r.db.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time, before this resource
// can ever become a queue.Item.
//
// timeCreatedOrZero (resources/support.go), not a direct x.TimeCreated.Time dereference: TimeCreated
// is mandatory:"false" on AutonomousDatabaseSummary (verified this session) -- a direct
// dereference would panic on a real record created without one.
func (r *AutonomousDatabase) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.db
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is applied at SCAN time
// (SafetyTags above), before this resource can ever become a queue.Item, so a protected resource
// never reaches Remove() at all, in either dry-run or --no-dry-run mode. No delete-protection
// check is possible here either -- see this file's package doc comment.
func (r *AutonomousDatabase) Remove(ctx context.Context) error {
	x := r.db
	_, err := r.client.DeleteAutonomousDatabase(ctx, database.DeleteAutonomousDatabaseRequest{
		AutonomousDatabaseId: x.Id,
	})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper.
func (r *AutonomousDatabase) Properties() types.Properties {
	x := r.db
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
