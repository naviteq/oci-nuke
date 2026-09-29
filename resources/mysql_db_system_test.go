package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/mysql"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubMySQLDbSystemClient implements mySQLDbSystemClient against in-memory data -- zero network
// access, mirroring resources/instance_test.go's stubInstanceClient shape.
type stubMySQLDbSystemClient struct {
	items   []mysql.DbSystemSummary
	deleted []string
	listErr error
}

func (s *stubMySQLDbSystemClient) ListDbSystems(
	_ context.Context,
	_ mysql.ListDbSystemsRequest,
) (mysql.ListDbSystemsResponse, error) {
	if s.listErr != nil {
		return mysql.ListDbSystemsResponse{}, s.listErr
	}
	return mysql.ListDbSystemsResponse{Items: s.items}, nil
}

func (s *stubMySQLDbSystemClient) DeleteDbSystem(
	_ context.Context,
	req mysql.DeleteDbSystemRequest,
) (mysql.DeleteDbSystemResponse, error) {
	s.deleted = append(s.deleted, *req.DbSystemId)
	return mysql.DeleteDbSystemResponse{}, nil
}

// TestMySQLDbSystemLister_List proves mySQLDbSystemList returns BOTH an ACTIVE and a DELETED
// system -- filtering is Filter()'s job, not List()'s -- without ever constructing a real
// MySQLDbSystem client.
func TestMySQLDbSystemLister_List(t *testing.T) {
	activeID := "ocid1.test.oc1..active"
	deletedID := "ocid1.test.oc1..deleted"
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubMySQLDbSystemClient{
		items: []mysql.DbSystemSummary{
			{
				Id: &activeID, CompartmentId: &compartmentID, TimeCreated: &timeCreated,
				LifecycleState: mysql.DbSystemLifecycleStateActive,
			},
			{
				Id: &deletedID, CompartmentId: &compartmentID, TimeCreated: &timeCreated,
				LifecycleState: mysql.DbSystemLifecycleStateDeleted,
			},
		},
	}

	got, err := mySQLDbSystemList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("mySQLDbSystemList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("mySQLDbSystemList() returned %d resources, want 2", len(got))
	}
	if got[0].(*MySQLDbSystem).GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", got[0].(*MySQLDbSystem).GetCompartmentID(), compartmentID)
	}
}

// TestMySQLDbSystem_Filter_LifecycleState is table-driven over mysql.DbSystemLifecycleStateEnum's
// full, real const block -- CREATING/ACTIVE/UPDATING/INACTIVE present, DELETING/DELETED/FAILED
// excluded (the hang-trap regression). None of these fixtures set DeletionPolicy, so this proves
// the lifecycle switch alone, independent of the delete-protection branch.
func TestMySQLDbSystem_Filter_LifecycleState(t *testing.T) {
	tests := []struct {
		state   mysql.DbSystemLifecycleStateEnum
		present bool
	}{
		{mysql.DbSystemLifecycleStateCreating, true},
		{mysql.DbSystemLifecycleStateActive, true},
		{mysql.DbSystemLifecycleStateUpdating, true},
		{mysql.DbSystemLifecycleStateInactive, true},
		{mysql.DbSystemLifecycleStateDeleting, false},
		{mysql.DbSystemLifecycleStateDeleted, false},
		{mysql.DbSystemLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		r := &MySQLDbSystem{}
		r.dbSystem.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestMySQLDbSystem_Filter_DeleteProtected proves Filter() returns non-nil and reports
// scope.ReasonDeleteProtected -- never scope.ReasonProtectedByTag -- when
// DeletionPolicy.IsDeleteProtected is true, even though LifecycleState is otherwise present
// (ACTIVE): the delete-protection check must run BEFORE the lifecycle switch.
func TestMySQLDbSystem_Filter_DeleteProtected(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	protected := true

	r := &MySQLDbSystem{dbSystem: mysql.DbSystemSummary{
		Id:             &id,
		CompartmentId:  &compartmentID,
		LifecycleState: mysql.DbSystemLifecycleStateActive,
		DeletionPolicy: &mysql.DeletionPolicyDetails{IsDeleteProtected: &protected},
	}}

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with DeletionPolicy.IsDeleteProtected=true = nil, want non-nil")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonDeleteProtected {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonDeleteProtected)
	}
	if got[0].ResourceType != MySQLDbSystemResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, MySQLDbSystemResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestMySQLDbSystem_Filter_NilDeletionPolicy_LifecycleOnly proves Filter() does NOT report
// ReasonDeleteProtected -- and never even inspects the delete-protection branch's nested field --
// when DeletionPolicy itself is nil, only the lifecycle-state check applies. DeletionPolicy is
// mandatory:"false", so this is the common case for a system whose deletion policy was never
// explicitly configured.
func TestMySQLDbSystem_Filter_NilDeletionPolicy_LifecycleOnly(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	r := &MySQLDbSystem{dbSystem: mysql.DbSystemSummary{
		Id:             &id,
		LifecycleState: mysql.DbSystemLifecycleStateActive,
	}}

	if err := r.Filter(); err != nil {
		t.Errorf("Filter() with nil DeletionPolicy and ACTIVE state = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ReportLeftover called %d times, want 0 (nil DeletionPolicy is not delete-protected)", len(got))
	}
}

// TestMySQLDbSystem_GetCompartmentID_NilSafe proves GetCompartmentID() returns "" (not a panic)
// when CompartmentId is nil -- mysql.DbSystemSummary.CompartmentId is mandatory:"false" (verified
// this session).
func TestMySQLDbSystem_GetCompartmentID_NilSafe(t *testing.T) {
	r := &MySQLDbSystem{dbSystem: mysql.DbSystemSummary{}}

	if got := r.GetCompartmentID(); got != "" {
		t.Errorf("GetCompartmentID() with nil CompartmentId = %q, want \"\"", got)
	}
}

// TestMySQLDbSystem_Remove_NoRetentionOverride proves DeleteDbSystem fires with ONLY DbSystemId
// set -- 05-CONTEXT.md: "does not override the tenancy's final-backup policy" -- against a client
// that never touches the network.
func TestMySQLDbSystem_Remove_NoRetentionOverride(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubMySQLDbSystemClient{}
	r := &MySQLDbSystem{client: stub}
	r.dbSystem.Id = &id
	r.dbSystem.CompartmentId = &compartmentID
	r.dbSystem.LifecycleState = mysql.DbSystemLifecycleStateActive
	r.dbSystem.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

const (
	mySQLDeleteProtectedTestType   = "MySQLDbSystemDeleteProtectedTestFixture"
	mySQLDeleteProtectedTestRegion = "us-ashburn-1"
)

// fixtureMySQLDbSystemLister always returns the one fixed *MySQLDbSystem resource it is
// constructed with -- mirroring resources/vault_test.go's fixtureVaultLister shape.
type fixtureMySQLDbSystemLister struct {
	dbSystem *MySQLDbSystem
}

func (l *fixtureMySQLDbSystemLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return []resource.Resource{l.dbSystem}, nil
}

// TestMySQLDbSystem_Remove_NeverReachedWhenDeleteProtected drives a delete-protected
// MySQLDbSystem through the REAL, production Filter() implementation via an actual
// *libnuke.Nuke.Run() pass -- mirroring resources/vault_test.go's
// TestVault_PendingDeletion_EndToEnd_ReportedAsScheduledDeletionNotAPIError shape -- rather than
// asserting a delete counter that nothing in the test body could ever increment (R-WR-02: the
// prior version of this test never called Remove() at all, so it passed even with the protection
// check inverted). Registered under a distinct fixture type name, never "MySQLDbSystem" itself,
// so the package's own real init()-installed registration is untouched.
func TestMySQLDbSystem_Remove_NeverReachedWhenDeleteProtected(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	protected := true
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubMySQLDbSystemClient{}
	fixture := &MySQLDbSystem{
		client: stub,
		dbSystem: mysql.DbSystemSummary{
			Id:             &id,
			CompartmentId:  &compartmentID,
			LifecycleState: mysql.DbSystemLifecycleStateActive,
			DeletionPolicy: &mysql.DeletionPolicyDetails{IsDeleteProtected: &protected},
			TimeCreated:    &timeCreated,
		},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	ocinuke.Register(&registry.Registration{
		Name:     mySQLDeleteProtectedTestType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fixtureMySQLDbSystemLister{dbSystem: fixture},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:   true,
		Force:      true,
		ForceSleep: 3, // Nuke.Validate() rejects anything below 3
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         mySQLDeleteProtectedTestRegion + "/" + compartmentID,
		ResourceTypes: []string{mySQLDeleteProtectedTestType},
		Opts:          &ocinuke.ListerOpts{Region: mySQLDeleteProtectedTestRegion, CompartmentID: compartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run() error = %v, want nil (a Filter()-excluded item is not a run failure)", err)
	}

	if len(stub.deleted) != 0 {
		t.Errorf(
			"DeleteDbSystem called %d times through the real Run() pipeline, want 0 (delete-protected system must never reach Remove())",
			len(stub.deleted),
		)
	}

	unmerged := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	skipped := entriesWithState(unmerged, plan.StateFiltered)
	if len(skipped) != 1 {
		t.Fatalf("got %d StateFiltered entries, want exactly 1 (entries: %+v)", len(skipped), unmerged)
	}
	if skipped[0].ResourceID != id {
		t.Errorf("StateFiltered entry ResourceID = %q, want %q", skipped[0].ResourceID, id)
	}
}
