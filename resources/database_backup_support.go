// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
//
// database_backup_support.go closes 05-CONTEXT.md's locked backup-residue-reporting requirement:
// "Automatic post-termination backups are never deleted, but they are enumerated in the report as
// a residue category alongside ReasonScheduledDeletion." All three helpers below are READ-ONLY --
// none registers with libnuke's registry (via ocinuke's own registration helper or otherwise),
// none has a Remove(), and none ever becomes a deletable resource type or a queue.Item. The ONLY
// effect is a scope.SkipEvent flowing through
// the existing ocinuke.ReportLeftover -> accumulator -> plan.MergeSkipEvents pipeline, landing as
// a StateSkipped entry in the merged plan/leftover report. Each is wired as a side effect inside
// its owning type's own List() method (resources/autonomous_database.go, resources/db_system.go,
// resources/mysql_db_system.go), called AFTER the primary resource list succeeds and BEFORE
// returning, so a backup-list failure surfaces as that type's own List() error -- consistent with
// the project's fail-visible-not-silent philosophy for every other list call, even though this
// one is only a reporting side effect.
//
// Deliberately three small, independent functions rather than one generic abstraction: the three
// request/response shapes differ enough (AutonomousDatabaseBackupSummary's Id/CompartmentId are
// both mandatory:"true"; database.BackupSummary's are BOTH mandatory:"false"; mysql.BackupSummary's
// are both mandatory:"true") that a shared abstraction would need its own escape hatches
// immediately.
package resources

import (
	"context"
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/database"
	"github.com/oracle/oci-go-sdk/v65/mysql"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// backupResidueDetail is the shared SkipEvent.Detail text all three helpers below report --
// factored into one constant (goconst, min-occurrences 3) rather than three identical literals.
const backupResidueDetail = "automatic post-termination backup, not deleted by this tool"

// autonomousDatabaseBackupLister is the narrow slice of database's client
// reportAutonomousDatabaseBackupResidue needs -- a hand-written interface so this helper is
// stub-testable with zero network access, satisfied in production by the same
// database.DatabaseClient resources/autonomous_database.go's List() already constructs for the
// primary list (no second client fetch).
type autonomousDatabaseBackupLister interface {
	ListAutonomousDatabaseBackups(
		ctx context.Context, req database.ListAutonomousDatabaseBackupsRequest,
	) (database.ListAutonomousDatabaseBackupsResponse, error)
}

// dbSystemBackupLister is the narrow slice of database's client reportDbSystemBackupResidue
// needs, satisfied in production by the same database.DatabaseClient
// resources/db_system.go's List() already constructs for the primary list.
type dbSystemBackupLister interface {
	ListBackups(ctx context.Context, req database.ListBackupsRequest) (database.ListBackupsResponse, error)
}

// mysqlDbBackupLister is the narrow slice of mysql's DbBackupsClient
// reportMySQLDbSystemBackupResidue needs, satisfied in production by
// pkg/clients.Cache.MySQLDbBackups -- a DIFFERENT client than MySQLDbSystem's own DbSystemClient
// (resources/mysql_db_system.go), since mysql.ListBackupsRequest is hosted on DbBackupsClient.
type mysqlDbBackupLister interface {
	ListBackups(ctx context.Context, req mysql.ListBackupsRequest) (mysql.ListBackupsResponse, error)
}

// reportAutonomousDatabaseBackupResidue paginates database.ListAutonomousDatabaseBackups scoped
// to compartmentID and reports EVERY returned backup via ocinuke.ReportLeftover(ReasonBackupResidue),
// regardless of lifecycle state -- this is pure enumeration, never a Filter()-gated deletion
// candidate. AutonomousDatabaseBackupSummary.Id/.CompartmentId are BOTH mandatory:"true"
// (verified this session), so no nil-check is needed before dereferencing either.
func reportAutonomousDatabaseBackupResidue(
	ctx context.Context,
	client autonomousDatabaseBackupLister,
	compartmentID string,
) error {
	req := database.ListAutonomousDatabaseBackupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListAutonomousDatabaseBackups(ctx, req)
		if err != nil {
			return fmt.Errorf("listing AutonomousDatabaseBackup residue in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			b := resp.Items[i]
			ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
				Reason:        scope.ReasonBackupResidue,
				ResourceType:  "AutonomousDatabaseBackup",
				ResourceID:    *b.Id,
				CompartmentID: *b.CompartmentId,
				Detail:        backupResidueDetail,
			})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return nil
}

// reportDbSystemBackupResidue paginates database.ListBackups scoped to compartmentID and reports
// every returned backup via ocinuke.ReportLeftover(ReasonBackupResidue). Unlike the Autonomous
// case above, database.BackupSummary.Id/.CompartmentId are BOTH mandatory:"false" (verified this
// session, a real difference from AutonomousDatabaseBackupSummary) -- a backup whose Id is nil is
// skipped (not reported, not panicked on), since a nil ID cannot be reported as a ResourceID; the
// known scan-scope compartmentID parameter is used for SkipEvent.CompartmentID rather than the
// summary's own possibly-nil field.
func reportDbSystemBackupResidue(
	ctx context.Context,
	client dbSystemBackupLister,
	compartmentID string,
) error {
	req := database.ListBackupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListBackups(ctx, req)
		if err != nil {
			return fmt.Errorf("listing DbSystemBackup residue in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			b := resp.Items[i]
			if b.Id == nil {
				continue
			}
			ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
				Reason:        scope.ReasonBackupResidue,
				ResourceType:  "DbSystemBackup",
				ResourceID:    *b.Id,
				CompartmentID: compartmentID,
				Detail:        backupResidueDetail,
			})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return nil
}

// reportMySQLDbSystemBackupResidue paginates mysql.ListBackups scoped to compartmentID (a
// mandatory:"true" field on this request, unlike the two database-package requests above -- verified
// this session) and reports every returned backup via ocinuke.ReportLeftover(ReasonBackupResidue).
// mysql.BackupSummary.Id/.CompartmentId are both mandatory:"true" (verified this session), so no
// nil-check is needed before dereferencing either.
func reportMySQLDbSystemBackupResidue(
	ctx context.Context,
	client mysqlDbBackupLister,
	compartmentID string,
) error {
	req := mysql.ListBackupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListBackups(ctx, req)
		if err != nil {
			return fmt.Errorf("listing MySQLDbSystemBackup residue in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			b := resp.Items[i]
			ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
				Reason:        scope.ReasonBackupResidue,
				ResourceType:  "MySQLDbSystemBackup",
				ResourceID:    *b.Id,
				CompartmentID: *b.CompartmentId,
				Detail:        backupResidueDetail,
			})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return nil
}
