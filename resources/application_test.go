package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/functions"
)

// TestApplicationList_List proves applicationList wraps every functions.ApplicationSummary
// returned by ListApplications as an Application, threading compartmentID through the request.
func TestApplicationList_List(t *testing.T) {
	compartmentID := testCompartmentOCID
	id := testApplicationID1
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeFunctionsClient{
		applicationsByCompartment: map[string][]functions.ApplicationSummary{
			compartmentID: {{
				Id:             &id,
				CompartmentId:  &compartmentID,
				LifecycleState: functions.ApplicationLifecycleStateActive,
				TimeCreated:    &timeCreated,
			}},
		},
	}

	got, err := applicationList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("applicationList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("applicationList() returned %d resources, want 1", len(got))
	}
	a, ok := got[0].(*Application)
	if !ok {
		t.Fatalf("applicationList()[0] is %T, want *Application", got[0])
	}
	if a.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", a.GetCompartmentID(), compartmentID)
	}
	if a.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", a.UniqueKey(), id)
	}
	if stub.listApplicationsCalls != 1 {
		t.Errorf("ListApplications called %d times, want 1", stub.listApplicationsCalls)
	}
}

// TestApplication_GetCompartmentID_NilSafe proves a nil ApplicationSummary.CompartmentId (
// mandatory:"false", verified against functions/application_summary.go) returns "" rather than
// panicking, so scopedLister drops it as out-of-scope (fail closed) instead of crashing the scan.
func TestApplication_GetCompartmentID_NilSafe(t *testing.T) {
	r := &Application{}
	r.application.Id = new(string)
	*r.application.Id = testResourceOCID

	if got := r.GetCompartmentID(); got != "" {
		t.Errorf("GetCompartmentID() = %q, want \"\" (nil CompartmentId)", got)
	}
}

// TestApplication_Filter proves the seven-value lifecycle switch: CREATING/ACTIVE/UPDATING
// present, INACTIVE/DELETING/DELETED/FAILED excluded.
func TestApplication_Filter(t *testing.T) {
	tests := []struct {
		state   functions.ApplicationLifecycleStateEnum
		present bool
	}{
		{functions.ApplicationLifecycleStateCreating, true},
		{functions.ApplicationLifecycleStateActive, true},
		{functions.ApplicationLifecycleStateUpdating, true},
		{functions.ApplicationLifecycleStateInactive, false},
		{functions.ApplicationLifecycleStateDeleting, false},
		{functions.ApplicationLifecycleStateDeleted, false},
		{functions.ApplicationLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &Application{}
			r.application.LifecycleState = tc.state

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

// TestApplication_Remove proves the SDK delete call fires with the right ApplicationId, against
// a client that never touches the network.
func TestApplication_Remove(t *testing.T) {
	id := testApplicationID1
	stub := &fakeFunctionsClient{}
	r := &Application{client: stub}
	r.application.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedApplications) != 1 || stub.deletedApplications[0] != id {
		t.Fatalf("stub.deletedApplications = %v, want [%s]", stub.deletedApplications, id)
	}
}

// TestApplicationRegistration_DependsOnFunction proves Application declares
// DependsOn: ["Function"] exactly as 05-CONTEXT.md locks -- Functions must be
// scanned/removed before the Application that hosts them.
func TestApplicationRegistration_DependsOnFunction(t *testing.T) {
	reg := registry.GetRegistration(ApplicationResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", ApplicationResourceType)
	}
	want := []string{"Function"}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("Application DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("Application DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}
