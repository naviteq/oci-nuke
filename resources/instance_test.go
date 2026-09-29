package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// testResourceOCID/testCompartmentOCID are shared, package-level test fixture OCIDs -- declared
// once here (goconst, min-occurrences 3) and reused by instance_pool_test.go and
// instance_configuration_test.go, which construct the same single-item stub-fixture shape.
//
// testFreeformTagValue/testDefinedTagFlatValue are shared SafetyTags() fixture values, reused by
// policy_test.go and tag_namespace_test.go (goconst, min-occurrences 3).
//
// testTerminatedOCID is a shared "gone" fixture OCID, reused by autonomous_database_test.go and
// db_system_test.go alongside this file's own terminated-instance fixture (goconst,
// min-occurrences 3).
//
// testNotificationTopicID is a shared ons Topic OCID fixture, reused by notification_topic_test.go
// and subscription_test.go (goconst, min-occurrences 3).
//
// testVaultManagementEndpointA is a shared KMS vault ManagementEndpoint fixture, reused by
// vault_test.go and kms_support_test.go (goconst, min-occurrences 3).
//
// testFreeformTagKey/testDefinedTagNamespace/testDefinedTagKey are the shared map-key literals
// paired with testFreeformTagValue/testDefinedTagFlatValue above, reused by policy_test.go,
// tag_namespace_test.go, and compartment_test.go's own SafetyTags fixtures (goconst,
// min-occurrences 3).
const (
	testResourceOCID    = "ocid1.test.oc1..id"
	testCompartmentOCID = "ocid1.compartment.oc1..id"

	testFreeformTagValue    = "prod"
	testDefinedTagFlatValue = "value"

	testFreeformTagKey      = "env"
	testDefinedTagNamespace = "ns"
	testDefinedTagKey       = "key"

	testTerminatedOCID = "ocid1.test.oc1..terminated"

	testNotificationTopicID = "ocid1.onstopic.oc1..topic"

	testVaultManagementEndpointA = "https://vault-a-kms.management.us-ashburn-1.oraclecloud.com"
)

// stubInstanceClient implements instanceClient against in-memory data -- zero network
// access, mirroring the seam pkg/scope's IdentityClient/RegionClient already establish.
type stubInstanceClient struct {
	items   []core.Instance
	deleted []string
	listErr error
}

// ListInstances/TerminateInstance signatures below must match the real
// core.Instance client's methods exactly (request struct by value, not a
// pointer) to satisfy instanceClient -- see resources_test's fakeIdentityClient for
// this project's established precedent.
func (s *stubInstanceClient) ListInstances(
	_ context.Context,
	_ core.ListInstancesRequest,
) (core.ListInstancesResponse, error) {
	if s.listErr != nil {
		return core.ListInstancesResponse{}, s.listErr
	}
	return core.ListInstancesResponse{Items: s.items}, nil
}

func (s *stubInstanceClient) TerminateInstance(
	_ context.Context,
	req core.TerminateInstanceRequest,
) (core.TerminateInstanceResponse, error) {
	s.deleted = append(s.deleted, *req.InstanceId)
	return core.TerminateInstanceResponse{}, nil
}

// TestInstanceLister_List proves instanceList returns BOTH a RUNNING and a TERMINATED instance
// -- filtering is Filter()'s job, not List()'s -- without ever constructing a real Compute
// client.
func TestInstanceLister_List(t *testing.T) {
	runningID := "ocid1.test.oc1..running"
	terminatedID := testTerminatedOCID
	compartmentID := testCompartmentOCID

	stub := &stubInstanceClient{
		items: []core.Instance{
			{Id: &runningID, CompartmentId: &compartmentID, LifecycleState: core.InstanceLifecycleStateRunning},
			{Id: &terminatedID, CompartmentId: &compartmentID, LifecycleState: core.InstanceLifecycleStateTerminated},
		},
	}

	got, err := instanceList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("instanceList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("instanceList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*Instance).Properties(); props.Get("id") != runningID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), runningID)
	}
	if props := got[1].(*Instance).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestInstance_Filter is table-driven over every core.InstanceLifecycleStateEnum value --
// RUNNING/STOPPED/STARTING/STOPPING/MOVING/CREATING_IMAGE must return nil (present), and
// TERMINATING/TERMINATED must both return non-nil (excluded). This IS the regression test for
// the hang trap: a Filter() that forgets the TERMINATING branch fails this test immediately,
// because a resource stuck in TERMINATING would otherwise never converge in HandleWait.
func TestInstance_Filter(t *testing.T) {
	tests := []struct {
		state   core.InstanceLifecycleStateEnum
		present bool
	}{
		{core.InstanceLifecycleStateRunning, true},
		{core.InstanceLifecycleStateStopped, true},
		{core.InstanceLifecycleStateStarting, true},
		{core.InstanceLifecycleStateStopping, true},
		{core.InstanceLifecycleStateMoving, true},
		{core.InstanceLifecycleStateCreatingImage, true},
		{core.InstanceLifecycleStateProvisioning, false},
		{core.InstanceLifecycleStateTerminating, false},
		{core.InstanceLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &Instance{}
		r.instance.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestInstance_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestInstance_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubInstanceClient{}
	r := &Instance{client: stub}
	r.instance.Id = &id
	r.instance.CompartmentId = &compartmentID
	r.instance.LifecycleState = core.InstanceLifecycleStateRunning
	r.instance.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
