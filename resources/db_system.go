// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. DbSystem wraps the Oracle `database` package's own DB
// System concept -- resolving RES-07's naming collision against MySQL's own DB System concept
// (resources/mysql_db_system.go's MySQLDbSystem, registered under the distinct name
// "MySQLDbSystem"). "DbSystem" is the "primary" DB System concept, matching OCI console
// terminology (05-RESEARCH.md Pitfall 6's resolution).
//
// Hand-written rather than scaffolded by cmd/gen-resource for the same TimeCreated reason as
// resources/autonomous_database.go: database.DbSystemSummary.TimeCreated is mandatory:"false"
// (verified this session against oci-go-sdk/v65@v65.123.0/database/db_system_summary.go).
//
// Filter() below deliberately has NO delete-protection branch, for the same reason as
// AutonomousDatabase: 05-CONTEXT.md's amendment (after 05-RESEARCH.md's Contradiction 1) states
// every field on DbSystemSummary was enumerated this session and none of them is a
// delete-protection or Locks-shaped field in this pinned SDK version. ocinuke.Evaluate's
// protect-by-tag/min-age (SafetyTags below) is the only protection mechanism available for this
// type -- a user-approved, documented gap (05-CONTEXT.md, "Databases and platform services").
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

// dbSystemClient is the narrow slice of database's client this lister needs -- a hand-written
// interface so DbSystem is stub-testable with zero network access. Deliberately named
// `dbSystemClient`, distinct from mysql_db_system.go's own `mySQLDbSystemClient` -- both are
// unexported, file-scoped interfaces, so their differing names avoid any ambiguity about which
// SDK package a given call site's client narrows down to.
type dbSystemClient interface {
	ListDbSystems(ctx context.Context, req database.ListDbSystemsRequest) (database.ListDbSystemsResponse, error)
	TerminateDbSystem(ctx context.Context, req database.TerminateDbSystemRequest) (database.TerminateDbSystemResponse, error)
}

// DbSystemResourceType is the registry.Registration.Name for DbSystem (the Oracle `database`
// package's own DB System concept -- NOT MySQLDbSystemResourceType).
const DbSystemResourceType = "DbSystem"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     DbSystemResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &DbSystem{},
		Lister:   &dbSystemLister{},
		// DependsOn is intentionally empty -- see resources/autonomous_database.go's own init()
		// comment for why: automatic post-termination backups are read-only residue, never an
		// independently registered, deletable resource type.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type dbSystemLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in dbSystemList,
// kept separate so it is unit-testable against a stub client without ever constructing a real
// Database client.
func (l *dbSystemLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("dbSystemLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Database(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Database client for %s: %w", o.Region, err)
	}

	items, err := dbSystemList(ctx, client, o.CompartmentID)
	if err != nil {
		return nil, err
	}

	// Automatic post-termination backups are enumerated as a read-only side effect of this
	// List() call -- see resources/database_backup_support.go's package doc comment. Same
	// database.DatabaseClient already constructed above for the primary list; no second client
	// fetch.
	if err := reportDbSystemBackupResidue(ctx, client, o.CompartmentID); err != nil {
		return nil, err
	}

	return items, nil
}

// dbSystemList paginates database.ListDbSystems and wraps every returned item as a DbSystem.
// Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test
// below exercises against a stub, with zero network access.
func dbSystemList(
	ctx context.Context,
	client dbSystemClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := database.ListDbSystemsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDbSystems(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing DbSystem in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &DbSystem{client: client, dbSystem: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// DbSystem wraps one database.DbSystemSummary -- the Oracle `database` package's own DB System
// concept. See mysql_db_system.go's MySQLDbSystem for the distinct MySQL DB System type this
// wave's naming collision required resolving.
type DbSystem struct {
	client   dbSystemClient
	dbSystem database.DbSystemSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. DbSystemSummary.CompartmentId is mandatory:"true" (verified this session), so a
// direct dereference is safe.
func (r *DbSystem) GetCompartmentID() string { return *r.dbSystem.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// DbSystemSummary.Id is mandatory:"true" (verified this session).
func (r *DbSystem) UniqueKey() string { return *r.dbSystem.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// Present = PROVISIONING, AVAILABLE, UPDATING, MAINTENANCE_IN_PROGRESS, NEEDS_ATTENTION,
// UPGRADING (database.DbSystemSummaryLifecycleStateEnum's full const block, verified this session
// against oci-go-sdk/v65@v65.123.0/database/db_system_summary.go).
//
// [Rule 1 deviation from 05-02-PLAN.md's literal wording, verified directly against the pinned
// SDK, not assumed]: the plan's task action text names "Stopping, Stopped, Starting,
// MigrationInProgress" as present states, but DbSystemSummaryLifecycleStateEnum has NO such
// values at all -- its full const block is exactly PROVISIONING, AVAILABLE, UPDATING,
// TERMINATING, TERMINATED, FAILED, MIGRATED, MAINTENANCE_IN_PROGRESS, NEEDS_ATTENTION, UPGRADING.
// TERMINATING/TERMINATED/FAILED are excluded (going/gone/errored, the hang-trap defense). MIGRATED
// is also excluded rather than assumed present: DbSystemSummary's own doc comment states this API
// is deprecated for Exadata Cloud Service instances using the new resource model once migrated
// ("To provision and manage new Exadata Cloud Service systems, use CloudExadataInfrastructure and
// CloudVmCluster") -- nothing confirms TerminateDbSystem still succeeds against a MIGRATED system
// via this API, so it fails safe into the excluded default rather than being assumed removable.
//
// This switch has NO delete-protection branch -- see this file's package doc comment for why: no
// such field exists on DbSystemSummary in the pinned SDK.
func (r *DbSystem) Filter() error {
	switch r.dbSystem.LifecycleState {
	case database.DbSystemSummaryLifecycleStateProvisioning,
		database.DbSystemSummaryLifecycleStateAvailable,
		database.DbSystemSummaryLifecycleStateUpdating,
		database.DbSystemSummaryLifecycleStateMaintenanceInProgress,
		database.DbSystemSummaryLifecycleStateNeedsAttention,
		database.DbSystemSummaryLifecycleStateUpgrading:
		return nil
	default:
		return fmt.Errorf("DbSystem is %s, not available", r.dbSystem.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity. Read by ocinuke's scopedLister at
// SCAN time, before this resource can ever become a queue.Item.
//
// timeCreatedOrZero (resources/support.go), not a direct x.TimeCreated.Time dereference:
// TimeCreated is mandatory:"false" on DbSystemSummary (verified this session) -- a direct
// dereference would panic on a real record created without one.
func (r *DbSystem) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.dbSystem
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is applied at SCAN time
// (SafetyTags above), before this resource can ever become a queue.Item, so a protected resource
// never reaches Remove() at all, in either dry-run or --no-dry-run mode. No delete-protection
// check is possible here either -- see this file's package doc comment.
func (r *DbSystem) Remove(ctx context.Context) error {
	x := r.dbSystem
	_, err := r.client.TerminateDbSystem(ctx, database.TerminateDbSystemRequest{DbSystemId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper.
func (r *DbSystem) Properties() types.Properties {
	x := r.dbSystem
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
