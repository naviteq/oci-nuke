package resources

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/artifacts"
	"github.com/oracle/oci-go-sdk/v65/common"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubServiceError implements common.ServiceError with only the fields R-WR-03's Remove()
// classification actually reads (GetHTTPStatusCode) -- zero network access, no real OCI response
// ever constructed.
type stubServiceError struct {
	statusCode int
}

func (e *stubServiceError) Error() string           { return "stub service error" }
func (e *stubServiceError) GetHTTPStatusCode() int  { return e.statusCode }
func (e *stubServiceError) GetMessage() string      { return "stub service error" }
func (e *stubServiceError) GetCode() string         { return "StubError" }
func (e *stubServiceError) GetOpcRequestID() string { return "" }

// stubContainerRepositoryClient implements containerRepositoryClient against in-memory data --
// zero network access, mirroring stubInstanceClient's established precedent
// (resources/instance_test.go).
type stubContainerRepositoryClient struct {
	items     []artifacts.ContainerRepositorySummary
	deleted   []string
	listErr   error
	deleteErr error
}

func (s *stubContainerRepositoryClient) ListContainerRepositories(
	_ context.Context,
	_ artifacts.ListContainerRepositoriesRequest,
) (artifacts.ListContainerRepositoriesResponse, error) {
	if s.listErr != nil {
		return artifacts.ListContainerRepositoriesResponse{}, s.listErr
	}
	return artifacts.ListContainerRepositoriesResponse{
		ContainerRepositoryCollection: artifacts.ContainerRepositoryCollection{Items: s.items},
	}, nil
}

func (s *stubContainerRepositoryClient) DeleteContainerRepository(
	_ context.Context,
	req artifacts.DeleteContainerRepositoryRequest,
) (artifacts.DeleteContainerRepositoryResponse, error) {
	if s.deleteErr != nil {
		return artifacts.DeleteContainerRepositoryResponse{}, s.deleteErr
	}
	s.deleted = append(s.deleted, *req.RepositoryId)
	return artifacts.DeleteContainerRepositoryResponse{}, nil
}

// TestContainerRepositoryLister_List proves containerRepositoryList returns every item
// ListContainerRepositories gives back, wrapped, without ever constructing a real Artifacts
// client.
func TestContainerRepositoryLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	displayName := "test-repo"
	namespace := testNamespace
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubContainerRepositoryClient{
		items: []artifacts.ContainerRepositorySummary{
			{
				Id: &id, CompartmentId: &compartmentID, DisplayName: &displayName,
				Namespace: &namespace, TimeCreated: &timeCreated,
				LifecycleState: artifacts.ContainerRepositoryLifecycleStateAvailable,
			},
		},
	}

	got, err := containerRepositoryList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("containerRepositoryList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("containerRepositoryList() returned %d resources, want 1", len(got))
	}
	cr, ok := got[0].(*ContainerRepository)
	if !ok {
		t.Fatalf("containerRepositoryList()[0] is %T, want *ContainerRepository", got[0])
	}
	if cr.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", cr.GetCompartmentID(), compartmentID)
	}
	if cr.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", cr.UniqueKey(), id)
	}
}

// TestContainerRepository_Filter is table-driven over every
// artifacts.ContainerRepositoryLifecycleStateEnum value -- AVAILABLE must return nil (present),
// and DELETING/DELETED must both return non-nil (excluded). This IS the regression test for the
// hang trap (T-05-01-02): a Filter() that forgets the DELETING branch fails this test
// immediately.
func TestContainerRepository_Filter(t *testing.T) {
	tests := []struct {
		state   artifacts.ContainerRepositoryLifecycleStateEnum
		present bool
	}{
		{artifacts.ContainerRepositoryLifecycleStateAvailable, true},
		{artifacts.ContainerRepositoryLifecycleStateDeleting, false},
		{artifacts.ContainerRepositoryLifecycleStateDeleted, false},
		{artifacts.ContainerRepositoryLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &ContainerRepository{}
		r.repository.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestContainerRepository_Remove proves the SDK delete call fires with the right parameter,
// against a client that never touches the network.
func TestContainerRepository_Remove(t *testing.T) {
	id := testResourceOCID

	stub := &stubContainerRepositoryClient{}
	r := &ContainerRepository{client: stub}
	r.repository.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestContainerRepository_Remove_NonEmptyReportsDependencyNotSatisfied proves R-WR-03's fix: a
// 409 Conflict from DeleteContainerRepository (the non-empty-repository case) is reported via
// ocinuke.ReportLeftover with scope.ReasonDependencyNotSatisfied -- a labeled leftover, not a
// bare, unclassified API error -- and Remove() still returns the original error so the item
// genuinely fails this round (no cascading hang-trap risk: it is never treated as removed).
func TestContainerRepository_Remove_NonEmptyReportsDependencyNotSatisfied(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubContainerRepositoryClient{deleteErr: &stubServiceError{statusCode: http.StatusConflict}}
	r := &ContainerRepository{client: stub}
	r.repository.Id = &id
	r.repository.CompartmentId = &compartmentID

	if err := r.Remove(context.Background()); err == nil {
		t.Fatal("Remove() with a 409 Conflict delete error = nil, want non-nil")
	}
	if len(stub.deleted) != 0 {
		t.Errorf("stub.deleted = %v, want empty (delete failed)", stub.deleted)
	}
	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonDependencyNotSatisfied {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonDependencyNotSatisfied)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestContainerRepository_Remove_OtherErrorNotReclassified proves a non-409 delete failure is
// NOT reported via ReportLeftover -- the 409-only check must never swallow or relabel an
// unrelated error (permissions, network, ...) as "waiting on images."
func TestContainerRepository_Remove_OtherErrorNotReclassified(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubContainerRepositoryClient{deleteErr: &stubServiceError{statusCode: http.StatusForbidden}}
	r := &ContainerRepository{client: stub}
	r.repository.Id = &id
	r.repository.CompartmentId = &compartmentID

	if err := r.Remove(context.Background()); err == nil {
		t.Fatal("Remove() with a 403 delete error = nil, want non-nil")
	}
	if len(got) != 0 {
		t.Errorf("ReportLeftover called %d times for a non-409 error, want 0", len(got))
	}
}
