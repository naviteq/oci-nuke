package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/functions"
)

// TestFunctionList_NestedEnumeration proves functionList walks EVERY Application in the
// compartment (not just the first one) and fully paginates ListFunctions per ApplicationId --
// this plan's headline risk, since functions.ListFunctionsRequest has no CompartmentId field at
// all. Two applications, each with its own function, distinguished by which ApplicationId
// ListFunctions was called with.
func TestFunctionList_NestedEnumeration(t *testing.T) {
	compartmentID := testCompartmentOCID
	app1, app2 := testApplicationID1, testApplicationID2
	fn1, fn2 := "ocid1.fnfunc.oc1..one", "ocid1.fnfunc.oc1..two"

	stub := &fakeFunctionsClient{
		applicationsByCompartment: map[string][]functions.ApplicationSummary{
			compartmentID: {{Id: &app1}, {Id: &app2}},
		},
		functionsByApplication: map[string][]functions.FunctionSummary{
			app1: {{Id: &fn1, ApplicationId: &app1, LifecycleState: functions.FunctionLifecycleStateActive}},
			app2: {{Id: &fn2, ApplicationId: &app2, LifecycleState: functions.FunctionLifecycleStateActive}},
		},
	}

	got, err := functionList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("functionList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("functionList() returned %d resources, want 2 (one per application)", len(got))
	}
	if stub.listApplicationsCalls != 1 {
		t.Errorf("ListApplications called %d times, want 1", stub.listApplicationsCalls)
	}
	if stub.listFunctionsCalls != 2 {
		t.Errorf("ListFunctions called %d times, want 2 (once per application, an application was missed if this is 1)", stub.listFunctionsCalls)
	}

	gotIDs := map[string]bool{}
	for _, r := range got {
		f, ok := r.(*Function)
		if !ok {
			t.Fatalf("functionList()[i] is %T, want *Function", r)
		}
		gotIDs[f.UniqueKey()] = true
		if f.GetCompartmentID() != compartmentID {
			t.Errorf("GetCompartmentID() = %q, want %q", f.GetCompartmentID(), compartmentID)
		}
	}
	for _, want := range []string{fn1, fn2} {
		if !gotIDs[want] {
			t.Errorf("functionList() missing %q -- an application was silently dropped", want)
		}
	}
}

// TestFunctionList_CompartmentIDInheritedEvenWhenApplicationAndFunctionFieldsAreNil proves
// Pitfall 5: a Function discovered under an Application still gets a non-empty
// GetCompartmentID(), even in the fixture where BOTH the owning ApplicationSummary.CompartmentId
// and the FunctionSummary's own CompartmentId come back nil (both mandatory:"false") -- the
// exact case that would otherwise cause scopedLister to silently drop an in-scope Function as
// out-of-scope.
func TestFunctionList_CompartmentIDInheritedEvenWhenApplicationAndFunctionFieldsAreNil(t *testing.T) {
	compartmentID := testCompartmentOCID
	appID := testApplicationID1
	fnID := "ocid1.fnfunc.oc1..nilcompartment"

	stub := &fakeFunctionsClient{
		applicationsByCompartment: map[string][]functions.ApplicationSummary{
			// CompartmentId deliberately nil on the Application fixture.
			compartmentID: {{Id: &appID}},
		},
		functionsByApplication: map[string][]functions.FunctionSummary{
			// CompartmentId deliberately nil on the Function fixture too.
			appID: {{Id: &fnID, ApplicationId: &appID, LifecycleState: functions.FunctionLifecycleStateActive}},
		},
	}

	got, err := functionList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("functionList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("functionList() returned %d resources, want 1", len(got))
	}
	f, ok := got[0].(*Function)
	if !ok {
		t.Fatalf("functionList()[0] is %T, want *Function", got[0])
	}
	if f.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q (inherited from scan scope, not dropped as \"\")",
			f.GetCompartmentID(), compartmentID)
	}
}

// TestFunction_Filter proves the seven-value lifecycle switch: CREATING/ACTIVE/UPDATING present,
// INACTIVE/DELETING/DELETED/FAILED excluded.
func TestFunction_Filter(t *testing.T) {
	tests := []struct {
		state   functions.FunctionLifecycleStateEnum
		present bool
	}{
		{functions.FunctionLifecycleStateCreating, true},
		{functions.FunctionLifecycleStateActive, true},
		{functions.FunctionLifecycleStateUpdating, true},
		{functions.FunctionLifecycleStateInactive, false},
		{functions.FunctionLifecycleStateDeleting, false},
		{functions.FunctionLifecycleStateDeleted, false},
		{functions.FunctionLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &Function{}
			r.function.LifecycleState = tc.state

			err := r.Filter()
			if tc.present && err != nil {
				t.Errorf("Filter() = %v, want nil (present)", err)
			}
			if !tc.present && err == nil {
				t.Errorf("Filter() = nil, want non-nil (excluded, hang-trap regression)")
			}
		})
	}
}

// TestFunction_Remove proves the SDK delete call fires with the right FunctionId, against a
// client that never touches the network.
func TestFunction_Remove(t *testing.T) {
	id := "ocid1.fnfunc.oc1..remove"
	stub := &fakeFunctionsClient{}
	r := &Function{client: stub}
	r.function.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedFunctions) != 1 || stub.deletedFunctions[0] != id {
		t.Fatalf("stub.deletedFunctions = %v, want [%s]", stub.deletedFunctions, id)
	}
}

// TestFunction_Properties_ApplicationIDPresent proves application_id is surfaced in Properties(),
// and that Properties() reports the inherited compartmentID, never a possibly-nil
// function.CompartmentId read directly.
func TestFunction_Properties_ApplicationIDPresent(t *testing.T) {
	id := "ocid1.fnfunc.oc1..props"
	appID := testApplicationID1
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	r := &Function{compartmentID: compartmentID}
	r.function.Id = &id
	r.function.ApplicationId = &appID
	r.function.TimeCreated = &timeCreated

	props := r.Properties()
	if got := props.Get("application_id"); got != appID {
		t.Errorf("Properties()[%q] = %q, want %q", "application_id", got, appID)
	}
	if got := props.Get(propCompartmentID); got != compartmentID {
		t.Errorf("Properties()[%q] = %q, want %q", propCompartmentID, got, compartmentID)
	}
}
