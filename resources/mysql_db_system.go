// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. MySQLDbSystem wraps the `mysql` package's own DB System
// concept -- resolving RES-07's naming collision against the Oracle `database` package's own DB
// System concept (resources/db_system.go's DbSystem, registered under the distinct name
// "DbSystem"). There is exactly one Go type named `DbSystem` in this package (db_system.go); this
// file's type is `MySQLDbSystem`.
//
// Hand-written rather than scaffolded by cmd/gen-resource: mysql.DbSystemSummary has no
// Summary-suffixed lifecycle-enum alias at all (its own lifecycle type is
// mysql.DbSystemLifecycleStateEnum, verified this session against
// oci-go-sdk/v65@v65.123.0/mysql/db_system.go), and needs the DeletionPolicy.IsDeleteProtected
// nil-checked branch below, which the generator's template cannot express.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/mysql"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// mySQLDbSystemClient is the narrow slice of mysql's DbSystemClient this lister needs -- a
// hand-written interface so MySQLDbSystem is stub-testable with zero network access. Deliberately
// named `mySQLDbSystemClient`, distinct from db_system.go's own `dbSystemClient` -- both are
// unexported, file-scoped interfaces, so their differing names avoid any ambiguity about which
// SDK package a given call site's client narrows down to.
type mySQLDbSystemClient interface {
	ListDbSystems(ctx context.Context, req mysql.ListDbSystemsRequest) (mysql.ListDbSystemsResponse, error)
	DeleteDbSystem(ctx context.Context, req mysql.DeleteDbSystemRequest) (mysql.DeleteDbSystemResponse, error)
}

// MySQLDbSystemResourceType is the registry.Registration.Name for MySQLDbSystem (NOT
// DbSystemResourceType, the Oracle `database` package's own, distinct DB System concept).
const MySQLDbSystemResourceType = "MySQLDbSystem"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     MySQLDbSystemResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &MySQLDbSystem{},
		Lister:   &mySQLDbSystemLister{},
		// DependsOn is intentionally empty -- see resources/autonomous_database.go's own init()
		// comment for why: automatic post-termination backups are read-only residue, never an
		// independently registered, deletable resource type.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type mySQLDbSystemLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// mySQLDbSystemList, kept separate so it is unit-testable against a stub client without ever
// constructing a real MySQLDbSystem client.
func (l *mySQLDbSystemLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("mySQLDbSystemLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.MySQLDbSystem(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing MySQLDbSystem client for %s: %w", o.Region, err)
	}

	items, err := mySQLDbSystemList(ctx, client, o.CompartmentID)
	if err != nil {
		return nil, err
	}

	// Automatic post-termination backups are enumerated as a read-only side effect of this
	// List() call -- see resources/database_backup_support.go's package doc comment.
	// MySQLDbBackups is a DIFFERENT client than MySQLDbSystem above (mysql.ListBackupsRequest is
	// hosted on DbBackupsClient, not DbSystemClient), so a second client fetch is required.
	backupsClient, err := o.Clients.MySQLDbBackups(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing MySQLDbBackups client for %s: %w", o.Region, err)
	}
	if err := reportMySQLDbSystemBackupResidue(ctx, backupsClient, o.CompartmentID); err != nil {
		return nil, err
	}

	return items, nil
}

// mySQLDbSystemList paginates mysql.ListDbSystems and wraps every returned item as a
// MySQLDbSystem. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func mySQLDbSystemList(
	ctx context.Context,
	client mySQLDbSystemClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := mysql.ListDbSystemsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDbSystems(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing MySQLDbSystem in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &MySQLDbSystem{client: client, dbSystem: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// MySQLDbSystem wraps one mysql.DbSystemSummary -- the `mysql` package's own DB System concept.
// See db_system.go's DbSystem for the distinct Oracle `database` package DB System type this
// wave's naming collision required resolving.
type MySQLDbSystem struct {
	client   mySQLDbSystemClient
	dbSystem mysql.DbSystemSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. mysql.DbSystemSummary.CompartmentId is mandatory:"false" (verified this
// session, a second, independently-verified nil-safety requirement beyond this type's own
// DeletionPolicy nil-check) -- nil-checked here rather than dereferenced directly, so a record
// missing it is dropped fail-closed by scopedLister rather than panicking mid-scan.
func (r *MySQLDbSystem) GetCompartmentID() string {
	if r.dbSystem.CompartmentId == nil {
		return ""
	}
	return *r.dbSystem.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// mysql.DbSystemSummary.Id is mandatory:"true" (verified this session), so a direct dereference
// is safe.
func (r *MySQLDbSystem) UniqueKey() string { return *r.dbSystem.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// FIRST checks DeletionPolicy.IsDeleteProtected, mirroring resources/retention_rule.go's
// pre-emptive check-then-report-then-error shape: DeletionPolicy itself is mandatory:"false"
// (verified this session, per 05-CONTEXT.md's amendment) -- the OUTER pointer is nil-checked
// before reading the nested bool, which is mandatory:"true" on DeletionPolicyDetails once
// DeletionPolicy is non-nil. This is a real OCI-service-enforced refusal
// (scope.ReasonDeleteProtected), deliberately checked BEFORE the lifecycle-state switch below, so
// a delete-protected system is never misreported as merely "not in a present lifecycle state."
// --force/NoDryRun never bypasses this -- see scope.ReasonDeleteProtected's own doc comment for
// why no code path needs an explicit --force check.
//
// THEN switches on LifecycleState using mysql.DbSystemLifecycleState* constants (NOT
// DbSystemSummaryLifecycleState* -- no such alias exists, per 05-RESEARCH.md Pitfall 1).
// Present = CREATING, ACTIVE, UPDATING, INACTIVE (mysql.DbSystemLifecycleStateEnum's full const
// block, verified this session against oci-go-sdk/v65@v65.123.0/mysql/db_system.go).
// DELETING/DELETED/FAILED are excluded (going/gone/errored, the hang-trap defense), as is every
// other value this switch does not recognize (fail-safe exclusion).
func (r *MySQLDbSystem) Filter() error {
	x := r.dbSystem
	if x.DeletionPolicy != nil && x.DeletionPolicy.IsDeleteProtected != nil && *x.DeletionPolicy.IsDeleteProtected {
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonDeleteProtected,
			ResourceType:  MySQLDbSystemResourceType,
			ResourceID:    *x.Id,
			CompartmentID: r.GetCompartmentID(),
			Detail:        "MySQL DB System has DeletionPolicy.IsDeleteProtected=true",
		})
		return fmt.Errorf("MySQLDbSystem %s is delete-protected (DeletionPolicy.IsDeleteProtected=true)", *x.Id)
	}

	switch x.LifecycleState {
	case mysql.DbSystemLifecycleStateCreating,
		mysql.DbSystemLifecycleStateActive,
		mysql.DbSystemLifecycleStateUpdating,
		mysql.DbSystemLifecycleStateInactive:
		return nil
	default:
		return fmt.Errorf("MySQLDbSystem is %s, not available", x.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity. Read by ocinuke's scopedLister at
// SCAN time, before this resource can ever become a queue.Item.
//
// x.TimeCreated.Time is dereferenced directly, NOT via resources/support.go's timeCreatedOrZero:
// mysql.DbSystemSummary.TimeCreated is mandatory:"true" (verified this session), unlike the
// `database`-package types in resources/autonomous_database.go and resources/db_system.go, so no
// nil-safety wrapper is needed here.
func (r *MySQLDbSystem) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.dbSystem
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call, with NO retention-policy override -- 05-CONTEXT.md: "MySQL DB
// System deletion does not override the tenancy's final-backup policy." No field beyond
// DbSystemId is set on the request; any resulting automatic backup shows up as residue via
// resources/database_backup_support.go, never deleted by this tool. Protect-by-tag/min-age
// protection, and the DeletionPolicy.IsDeleteProtected check above, are both applied at SCAN time
// (Filter()/SafetyTags() above), before this resource can ever become a queue.Item, so a
// protected resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *MySQLDbSystem) Remove(ctx context.Context) error {
	x := r.dbSystem
	_, err := r.client.DeleteDbSystem(ctx, mysql.DeleteDbSystemRequest{DbSystemId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper.
func (r *MySQLDbSystem) Properties() types.Properties {
	x := r.dbSystem
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
