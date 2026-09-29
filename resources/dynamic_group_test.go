package resources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	liberrors "github.com/ekristen/libnuke/pkg/errors"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubDynamicGroupClient implements dynamicGroupClient against in-memory data -- zero network
// access.
type stubDynamicGroupClient struct {
	items   []identity.DynamicGroup
	deleted []string
	listErr error

	// listCalls counts ListDynamicGroups invocations, so NR-775's guard can be proven by the
	// absence of a call rather than by the absence of an error -- a lister that swallowed the
	// 404 would look identical from the outside if only the error were checked.
	listCalls int
}

func (s *stubDynamicGroupClient) ListDynamicGroups(
	_ context.Context,
	_ identity.ListDynamicGroupsRequest,
) (identity.ListDynamicGroupsResponse, error) {
	s.listCalls++
	if s.listErr != nil {
		return identity.ListDynamicGroupsResponse{}, s.listErr
	}
	return identity.ListDynamicGroupsResponse{Items: s.items}, nil
}

func (s *stubDynamicGroupClient) DeleteDynamicGroup(
	_ context.Context,
	req identity.DeleteDynamicGroupRequest,
) (identity.DeleteDynamicGroupResponse, error) {
	s.deleted = append(s.deleted, *req.DynamicGroupId)
	return identity.DeleteDynamicGroupResponse{}, nil
}

// TestDynamicGroupLister_List proves dynamicGroupList wraps every returned item, without ever
// constructing a real Identity client.
func TestDynamicGroupLister_List(t *testing.T) {
	id := testResourceOCID
	tenancyID := "ocid1.tenancy.oc1..faketenancy"
	name := "test-dynamic-group"
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubDynamicGroupClient{
		items: []identity.DynamicGroup{
			{
				Id: &id, CompartmentId: &tenancyID, Name: &name,
				TimeCreated:    &timeCreated,
				LifecycleState: identity.DynamicGroupLifecycleStateActive,
			},
		},
	}

	got, err := dynamicGroupList(context.Background(), stub, tenancyID)
	if err != nil {
		t.Fatalf("dynamicGroupList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("dynamicGroupList() returned %d resources, want 1", len(got))
	}
	dg, ok := got[0].(*DynamicGroup)
	if !ok {
		t.Fatalf("dynamicGroupList()[0] is %T, want *DynamicGroup", got[0])
	}
	if dg.GetCompartmentID() != tenancyID {
		t.Errorf("GetCompartmentID() = %q, want %q", dg.GetCompartmentID(), tenancyID)
	}
	if dg.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", dg.UniqueKey(), id)
	}
}

// TestDynamicGroup_Filter is table-driven over every identity.DynamicGroupLifecycleStateEnum
// value -- only ACTIVE must return nil (present); every other state must return non-nil
// (excluded).
func TestDynamicGroup_Filter(t *testing.T) {
	tests := []struct {
		state   identity.DynamicGroupLifecycleStateEnum
		present bool
	}{
		{identity.DynamicGroupLifecycleStateActive, true},
		{identity.DynamicGroupLifecycleStateCreating, false},
		{identity.DynamicGroupLifecycleStateInactive, false},
		{identity.DynamicGroupLifecycleStateDeleting, false},
		{identity.DynamicGroupLifecycleStateDeleted, false},
		{identity.DynamicGroupLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &DynamicGroup{}
		r.dynamicGroup.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil", tc.state)
		}
	}
}

// TestDynamicGroup_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network -- exercised independently of the scope-drop test below,
// since Remove() and the scope-drop are two separately testable concerns (05-05-PLAN.md).
func TestDynamicGroup_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubDynamicGroupClient{}
	r := &DynamicGroup{client: stub}
	r.dynamicGroup.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// fakeDynamicGroupScopeDropLister returns exactly the fixtures it is constructed with, mirroring
// resources_test/equivalence_test.go's recordingLister shape -- used only by
// TestDynamicGroup_TenancyRootDroppedByScope below, to exercise the real, production
// ocinuke.Register wrapping (scopedLister) without ever constructing a real Identity client or
// touching the network.
type fakeDynamicGroupScopeDropLister struct {
	resources []resource.Resource
}

func (l *fakeDynamicGroupScopeDropLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	return l.resources, nil
}

// dynamicGroupScopeDropTestType is a distinct registry.Registration.Name, deliberately NOT
// "DynamicGroup" -- this avoids colliding with (or clearing/restoring) the real registration this
// package's own init() already installed, and avoids touching registry.ClearRegistry() at all
// (unlike resources_test/equivalence_test.go's pattern, this package's own test binary has no
// TestMain-based registry snapshot/restore machinery, so clearing the global registry here would
// silently break every other test in this package that runs afterward in the same process).
const dynamicGroupScopeDropTestType = "DynamicGroupScopeDropTestFixture"

// TestDynamicGroup_TenancyRootDroppedByScope is the DoD proof for 05-CONTEXT.md's "DynamicGroup is
// registered honestly, then dropped by scope" decision: a DynamicGroup fixture whose
// CompartmentId equals a fake tenancy OCID is dropped by the real ocinuke.Register-installed
// scopedLister wrapper (scopedLister itself is unexported in pkg/ocinuke and cannot be
// constructed directly from this package -- ocinuke.Register is the sanctioned, exported seam
// that installs it, mirroring resources_test/equivalence_test.go's own fixture-registration
// shape) when constructed with an inScope func that returns false for that OCID, and that the
// resulting SkipEvent.Reason is scope.ReasonOutOfScope.
func TestDynamicGroup_TenancyRootDroppedByScope(t *testing.T) {
	fakeTenancyOCID := "ocid1.tenancy.oc1..faketenancy"
	id := testResourceOCID
	fixture := &DynamicGroup{dynamicGroup: identity.DynamicGroup{
		Id:             &id,
		CompartmentId:  &fakeTenancyOCID,
		LifecycleState: identity.DynamicGroupLifecycleStateActive,
	}}

	var skipped []*scope.SkipEvent
	inScope := func(compartmentID string) bool { return compartmentID != fakeTenancyOCID }
	onSkip := func(evt *scope.SkipEvent) { skipped = append(skipped, evt) }

	ocinuke.Register(&registry.Registration{
		Name:     dynamicGroupScopeDropTestType,
		Scope:    ocinuke.CompartmentScope,
		Resource: fixture,
		Lister:   &fakeDynamicGroupScopeDropLister{resources: []resource.Resource{fixture}},
	}, inScope, onSkip)

	reg := registry.GetRegistration(dynamicGroupScopeDropTestType)
	if reg == nil {
		t.Fatal("registry.GetRegistration returned nil for the freshly registered fixture type")
	}

	got, err := reg.Lister.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned %d resources, want 0 -- a tenancy-root DynamicGroup must be dropped by scope", len(got))
	}
	if len(skipped) != 1 {
		t.Fatalf("onSkip called %d times, want exactly 1", len(skipped))
	}
	if skipped[0].Reason != scope.ReasonOutOfScope {
		t.Errorf("SkipEvent.Reason = %q, want %q", skipped[0].Reason, scope.ReasonOutOfScope)
	}
	if skipped[0].CompartmentID != fakeTenancyOCID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", skipped[0].CompartmentID, fakeTenancyOCID)
	}
}

// listerOptsForDynamicGroup builds the ListerOpts dynamicGroupLister.List receives. Clients is
// deliberately nil: every test below that expects a skip expects it to happen before the
// Identity client is ever constructed, so a nil dereference would be a real failure rather than
// a test-harness gap.
func listerOptsForDynamicGroup(compartmentID, tenancyID string) *ocinuke.ListerOpts {
	return &ocinuke.ListerOpts{
		Region:        dynamicGroupTestRegion,
		HomeRegion:    dynamicGroupTestRegion,
		CompartmentID: compartmentID,
		TenancyID:     tenancyID,
	}
}

const (
	dynamicGroupTestRegion  = "us-ashburn-1"
	dynamicGroupTestTenancy = "ocid1.tenancy.oc1..nr775"
)

// TestDynamicGroupLister_SkipsNonRootCompartmentWithoutCallingOCI is NR-775's fix. OCI answers
// ListDynamicGroups against any non-root compartment with 404 NotAuthorizedOrNotFound, so the
// run used to log one level=error line per compartment scanned for a type that structurally
// cannot be there. The lister must now not make the call at all, and must say so with
// ErrSkipRequest -- which libnuke logs at Debug and does not count as a listing failure.
func TestDynamicGroupLister_SkipsNonRootCompartmentWithoutCallingOCI(t *testing.T) {
	opts := listerOptsForDynamicGroup("ocid1.compartment.oc1..nr775", dynamicGroupTestTenancy)

	got, err := (&dynamicGroupLister{}).List(context.Background(), opts)
	if err == nil {
		t.Fatal("expected ErrSkipRequest for a non-root compartment, got nil")
	}
	var skip liberrors.ErrSkipRequest
	if !errors.As(err, &skip) {
		t.Fatalf("expected a libnuke ErrSkipRequest, got %T: %v", err, err)
	}
	if got != nil {
		t.Fatalf("expected no resources alongside the skip, got %d", len(got))
	}
}

// TestDynamicGroupList_GenuinePermissionErrorStillSurfaces is the other half of NR-775's DoD,
// and the reason the fix skips the call rather than suppressing its status code. OCI returns
// 404 NotAuthorizedOrNotFound both for "a dynamic group cannot live here" and for "you may not
// read dynamic groups at all". Nothing in this package inspects that code, so the second case
// still reaches the operator.
func TestDynamicGroupList_GenuinePermissionErrorStillSurfaces(t *testing.T) {
	// oci-go-sdk's own service-failure type is unexported (common/errors.go's servicefailure,
	// reachable only through newServiceFailureFromResponse), so the error the Identity client
	// would hand back is reproduced by its text rather than by its type. What is under test is
	// propagation, and nothing in this package branches on the type.
	stub := &stubDynamicGroupClient{
		listErr: errors.New("Error returned by Identity Service. Http Status Code: 404. " +
			"Error Code: NotAuthorizedOrNotFound. Operation Name: ListDynamicGroups"),
	}

	got, err := dynamicGroupList(context.Background(), stub, dynamicGroupTestTenancy)
	if err == nil {
		t.Fatal("a 404 NotAuthorizedOrNotFound from ListDynamicGroups was swallowed")
	}
	if got != nil {
		t.Fatalf("expected no resources alongside the error, got %d", len(got))
	}
	if !strings.Contains(err.Error(), "NotAuthorizedOrNotFound") {
		t.Fatalf("error does not carry the service failure: %v", err)
	}
	if stub.listCalls != 1 {
		t.Fatalf("listCalls = %d, want 1 -- the call must actually be made at the tenancy root", stub.listCalls)
	}
}
