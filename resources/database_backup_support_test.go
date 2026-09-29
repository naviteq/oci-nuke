package resources

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/database"
	"github.com/oracle/oci-go-sdk/v65/mysql"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubAutonomousDatabaseBackupLister implements autonomousDatabaseBackupLister against in-memory
// data -- zero network access.
type stubAutonomousDatabaseBackupLister struct {
	items   []database.AutonomousDatabaseBackupSummary
	listErr error
}

func (s *stubAutonomousDatabaseBackupLister) ListAutonomousDatabaseBackups(
	_ context.Context,
	_ database.ListAutonomousDatabaseBackupsRequest,
) (database.ListAutonomousDatabaseBackupsResponse, error) {
	if s.listErr != nil {
		return database.ListAutonomousDatabaseBackupsResponse{}, s.listErr
	}
	return database.ListAutonomousDatabaseBackupsResponse{Items: s.items}, nil
}

// stubDbSystemBackupLister implements dbSystemBackupLister against in-memory data -- zero network
// access.
type stubDbSystemBackupLister struct {
	items   []database.BackupSummary
	listErr error
}

func (s *stubDbSystemBackupLister) ListBackups(
	_ context.Context,
	_ database.ListBackupsRequest,
) (database.ListBackupsResponse, error) {
	if s.listErr != nil {
		return database.ListBackupsResponse{}, s.listErr
	}
	return database.ListBackupsResponse{Items: s.items}, nil
}

// stubMySQLDbBackupLister implements mysqlDbBackupLister against in-memory data -- zero network
// access.
type stubMySQLDbBackupLister struct {
	items   []mysql.BackupSummary
	listErr error
}

func (s *stubMySQLDbBackupLister) ListBackups(
	_ context.Context,
	_ mysql.ListBackupsRequest,
) (mysql.ListBackupsResponse, error) {
	if s.listErr != nil {
		return mysql.ListBackupsResponse{}, s.listErr
	}
	return mysql.ListBackupsResponse{Items: s.items}, nil
}

// TestDatabaseBackupResidue_Autonomous_ReportsEveryBackup proves
// reportAutonomousDatabaseBackupResidue against a stub returning two backups calls the injected
// reporter exactly twice, with ResourceType == "AutonomousDatabaseBackup" and
// Reason == scope.ReasonBackupResidue on both calls.
func TestDatabaseBackupResidue_Autonomous_ReportsEveryBackup(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	compartmentID := testCompartmentOCID
	id1, id2 := "ocid1.test.oc1..backup1", "ocid1.test.oc1..backup2"
	stub := &stubAutonomousDatabaseBackupLister{
		items: []database.AutonomousDatabaseBackupSummary{
			{Id: &id1, CompartmentId: &compartmentID},
			{Id: &id2, CompartmentId: &compartmentID},
		},
	}

	if err := reportAutonomousDatabaseBackupResidue(context.Background(), stub, compartmentID); err != nil {
		t.Fatalf("reportAutonomousDatabaseBackupResidue() error = %v, want nil", err)
	}

	if len(got) != 2 {
		t.Fatalf("ReportLeftover called %d times, want 2", len(got))
	}
	for i, evt := range got {
		if evt.ResourceType != "AutonomousDatabaseBackup" {
			t.Errorf("event[%d].ResourceType = %q, want %q", i, evt.ResourceType, "AutonomousDatabaseBackup")
		}
		if evt.Reason != scope.ReasonBackupResidue {
			t.Errorf("event[%d].Reason = %q, want %q", i, evt.Reason, scope.ReasonBackupResidue)
		}
	}
}

// TestDatabaseBackupResidue_Autonomous_ListError proves a genuine API failure is surfaced, never
// silently swallowed.
func TestDatabaseBackupResidue_Autonomous_ListError(t *testing.T) {
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	stub := &stubAutonomousDatabaseBackupLister{listErr: context.DeadlineExceeded}
	if err := reportAutonomousDatabaseBackupResidue(context.Background(), stub, testCompartmentOCID); err == nil {
		t.Fatal("reportAutonomousDatabaseBackupResidue() error = nil, want non-nil on list failure")
	}
}

// TestDatabaseBackupResidue_DbSystem_NilIDSkipped proves reportDbSystemBackupResidue against a
// stub returning one backup with a nil Id does not panic and does not call the reporter for that
// entry (skip, not report) -- database.BackupSummary.Id is mandatory:"false", unlike the
// Autonomous case.
func TestDatabaseBackupResidue_DbSystem_NilIDSkipped(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	stub := &stubDbSystemBackupLister{
		items: []database.BackupSummary{{Id: nil}},
	}

	if err := reportDbSystemBackupResidue(context.Background(), stub, testCompartmentOCID); err != nil {
		t.Fatalf("reportDbSystemBackupResidue() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ReportLeftover called %d times, want 0 (nil Id must be skipped, not reported)", len(got))
	}
}

// TestDatabaseBackupResidue_DbSystem_ReportsWithKnownCompartment proves a backup WITH a non-nil
// Id is reported using the caller-supplied compartmentID (not the summary's own possibly-nil
// CompartmentId field).
func TestDatabaseBackupResidue_DbSystem_ReportsWithKnownCompartment(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	compartmentID := testCompartmentOCID
	id := "ocid1.test.oc1..backup"
	stub := &stubDbSystemBackupLister{items: []database.BackupSummary{{Id: &id}}}

	if err := reportDbSystemBackupResidue(context.Background(), stub, compartmentID); err != nil {
		t.Fatalf("reportDbSystemBackupResidue() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].ResourceType != "DbSystemBackup" {
		t.Errorf("ResourceType = %q, want %q", got[0].ResourceType, "DbSystemBackup")
	}
	if got[0].Reason != scope.ReasonBackupResidue {
		t.Errorf("Reason = %q, want %q", got[0].Reason, scope.ReasonBackupResidue)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestDatabaseBackupResidue_MySQL_ReportsEveryBackup proves reportMySQLDbSystemBackupResidue
// against a stub returning one backup reports it with ResourceType == "MySQLDbSystemBackup" and
// Reason == scope.ReasonBackupResidue.
func TestDatabaseBackupResidue_MySQL_ReportsEveryBackup(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	compartmentID := testCompartmentOCID
	id := "ocid1.test.oc1..mysqlbackup"
	stub := &stubMySQLDbBackupLister{items: []mysql.BackupSummary{{Id: &id, CompartmentId: &compartmentID}}}

	if err := reportMySQLDbSystemBackupResidue(context.Background(), stub, compartmentID); err != nil {
		t.Fatalf("reportMySQLDbSystemBackupResidue() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].ResourceType != "MySQLDbSystemBackup" {
		t.Errorf("ResourceType = %q, want %q", got[0].ResourceType, "MySQLDbSystemBackup")
	}
	if got[0].Reason != scope.ReasonBackupResidue {
		t.Errorf("Reason = %q, want %q", got[0].Reason, scope.ReasonBackupResidue)
	}
}

// TestMySQLDbSystemLister_List_CallsBothMySQLClients is a structural (go/ast) proof that
// mySQLDbSystemLister.List() (resources/mysql_db_system.go) calls BOTH o.Clients.MySQLDbSystem
// and o.Clients.MySQLDbBackups -- the two distinct clients this type's List() needs, per
// 05-02-PLAN.md Task 4's acceptance criteria. Parses the real source file directly (self-referential,
// same package directory) rather than exercising List() against a real *clients.Cache, since the
// latter would attempt genuine outbound network calls in a unit test.
func TestMySQLDbSystemLister_List_CallsBothMySQLClients(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "mysql_db_system.go"))
	if err != nil {
		t.Fatalf("reading mysql_db_system.go: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mysql_db_system.go", src, 0)
	if err != nil {
		t.Fatalf("parsing mysql_db_system.go: %v", err)
	}

	var listFn *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv != nil && fn.Name.Name == "List" {
			listFn = fn
			break
		}
	}
	if listFn == nil {
		t.Fatal("no List method found in mysql_db_system.go")
	}

	var callsMySQLDbSystem, callsMySQLDbBackups bool
	ast.Inspect(listFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "MySQLDbSystem":
			callsMySQLDbSystem = true
		case "MySQLDbBackups":
			callsMySQLDbBackups = true
		}
		return true
	})

	if !callsMySQLDbSystem {
		t.Error("List() does not call o.Clients.MySQLDbSystem")
	}
	if !callsMySQLDbBackups {
		t.Error("List() does not call o.Clients.MySQLDbBackups -- backup residue reporting requires this client")
	}
}

// TestDatabaseBackupSupport_NeverRegistersAResourceType proves database_backup_support.go never
// calls ocinuke.Register/registry.Register by parsing its own source and asserting no such call
// expression exists -- a build-level guard against a future contributor accidentally wiring a
// Remove() onto what must remain a residue-only enumeration.
func TestDatabaseBackupSupport_NeverRegistersAResourceType(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "database_backup_support.go"))
	if err != nil {
		t.Fatalf("reading database_backup_support.go: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "database_backup_support.go", src, 0)
	if err != nil {
		t.Fatalf("parsing database_backup_support.go: %v", err)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Register" {
			t.Errorf("database_backup_support.go calls %v.Register -- backups must never be registered as a deletable resource type", sel.X)
		}
		return true
	})
}
