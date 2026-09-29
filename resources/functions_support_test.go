package resources

import (
	"context"
	"errors"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/functions"
)

// fakeFunctionsClient implements applicationIDLister, applicationClient, and functionClient
// against in-memory data -- zero network access. Shared by resources/application_test.go and
// resources/function_test.go, mirroring resources/file_storage_support_test.go's
// fakeFileStorageClient precedent (one stub client per plan, reused across every type's tests).
//
// applicationsByCompartment is keyed by CompartmentId, proving Application's own list scopes by
// compartment. functionsByApplication is keyed by ApplicationId, proving Function's nested walk
// really does call ListFunctions once per discovered Application -- not once for the whole
// compartment (which would be impossible: ListFunctionsRequest has no CompartmentId field at
// all).
type fakeFunctionsClient struct {
	applicationsByCompartment map[string][]functions.ApplicationSummary
	listApplicationsCalls     int
	listApplicationsErr       error

	functionsByApplication map[string][]functions.FunctionSummary
	listFunctionsCalls     int
	listFunctionsErr       error

	deletedApplications []string
	deletedFunctions    []string
}

func (s *fakeFunctionsClient) ListApplications(
	_ context.Context, req functions.ListApplicationsRequest,
) (functions.ListApplicationsResponse, error) {
	s.listApplicationsCalls++
	if s.listApplicationsErr != nil {
		return functions.ListApplicationsResponse{}, s.listApplicationsErr
	}
	return functions.ListApplicationsResponse{Items: s.applicationsByCompartment[safeDeref(req.CompartmentId)]}, nil
}

func (s *fakeFunctionsClient) DeleteApplication(
	_ context.Context, req functions.DeleteApplicationRequest,
) (functions.DeleteApplicationResponse, error) {
	s.deletedApplications = append(s.deletedApplications, safeDeref(req.ApplicationId))
	return functions.DeleteApplicationResponse{}, nil
}

func (s *fakeFunctionsClient) ListFunctions(
	_ context.Context, req functions.ListFunctionsRequest,
) (functions.ListFunctionsResponse, error) {
	s.listFunctionsCalls++
	if s.listFunctionsErr != nil {
		return functions.ListFunctionsResponse{}, s.listFunctionsErr
	}
	return functions.ListFunctionsResponse{Items: s.functionsByApplication[safeDeref(req.ApplicationId)]}, nil
}

func (s *fakeFunctionsClient) DeleteFunction(
	_ context.Context, req functions.DeleteFunctionRequest,
) (functions.DeleteFunctionResponse, error) {
	s.deletedFunctions = append(s.deletedFunctions, safeDeref(req.FunctionId))
	return functions.DeleteFunctionResponse{}, nil
}

// testApplicationID1/testApplicationID2 are the shared two-application fixture OCIDs
// resources/application_test.go and resources/function_test.go both reuse to prove Function's
// nested walk covers every Application, not just the first one found (goconst,
// min-occurrences: 3 -- this file, application_test.go, and function_test.go all reference them).
const (
	testApplicationID1 = "ocid1.fnapp.oc1..one"
	testApplicationID2 = "ocid1.fnapp.oc1..two"
)

// TestListApplicationIDs proves listApplicationIDs returns every ApplicationSummary.Id for the
// scoped compartment, against a stub returning two applications.
func TestListApplicationIDs(t *testing.T) {
	id1, id2 := testApplicationID1, testApplicationID2
	compartmentID := testCompartmentOCID
	stub := &fakeFunctionsClient{
		applicationsByCompartment: map[string][]functions.ApplicationSummary{
			compartmentID: {{Id: &id1}, {Id: &id2}},
		},
	}

	got, err := listApplicationIDs(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("listApplicationIDs() error = %v, want nil", err)
	}
	want := []string{id1, id2}
	if len(got) != len(want) {
		t.Fatalf("listApplicationIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listApplicationIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestListApplicationIDs_NilIDSkipped proves an ApplicationSummary with a nil Id is skipped
// rather than appended as an empty string (which would corrupt the next ListFunctions call's
// ApplicationId) or causing a nil-pointer panic.
func TestListApplicationIDs_NilIDSkipped(t *testing.T) {
	id1 := testApplicationID1
	compartmentID := testCompartmentOCID
	stub := &fakeFunctionsClient{
		applicationsByCompartment: map[string][]functions.ApplicationSummary{
			compartmentID: {{Id: &id1}, {Id: nil}},
		},
	}

	got, err := listApplicationIDs(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("listApplicationIDs() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != id1 {
		t.Fatalf("listApplicationIDs() = %v, want [%q]", got, id1)
	}
}

// TestListApplicationIDs_Error proves a ListApplications error propagates rather than being
// silently swallowed.
func TestListApplicationIDs_Error(t *testing.T) {
	stub := &fakeFunctionsClient{listApplicationsErr: errors.New("boom")}

	_, err := listApplicationIDs(context.Background(), stub, testCompartmentOCID)
	if err == nil {
		t.Fatal("listApplicationIDs() error = nil, want non-nil")
	}
}
