package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// TestReplicationPolicyLister_List proves replicationPolicyList wraps every
// objectstorage.ReplicationPolicySummary returned per bucket, threading namespace/bucket name/
// compartmentID down correctly.
func TestReplicationPolicyLister_List(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	bucketName := testBucketName
	id := testResourceOCID
	name := "test-replication-policy"
	destBucket := "destination-bucket"
	timeCreated := common.SDKTime{Time: time.Now()}
	timeLastSync := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{
			{Namespace: &namespace, Name: &bucketName, CompartmentId: &compartmentID},
		},
		replicationPolicies: map[string][]objectstorage.ReplicationPolicySummary{
			bucketName: {{
				Id: &id, Name: &name, DestinationBucketName: &destBucket,
				TimeCreated: &timeCreated, TimeLastSync: &timeLastSync,
				Status: objectstorage.ReplicationPolicySummaryStatusActive,
			}},
		},
	}

	got, err := replicationPolicyList(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("replicationPolicyList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("replicationPolicyList() returned %d resources, want 1", len(got))
	}
	rp, ok := got[0].(*ReplicationPolicy)
	if !ok {
		t.Fatalf("replicationPolicyList()[0] is %T, want *ReplicationPolicy", got[0])
	}
	if rp.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", rp.GetCompartmentID(), compartmentID)
	}
	if rp.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", rp.UniqueKey(), id)
	}
}

// TestReplicationPolicy_Filter_AlwaysNil is table-driven over both ReplicationPolicySummaryStatusEnum
// values -- ACTIVE and CLIENT_ERROR both mean "present, attempt removal." Unlike every other
// lifecycle-driven Filter() in this wave, this type has no going/gone transitional state at all,
// so Filter() returns nil unconditionally regardless of Status.
func TestReplicationPolicy_Filter_AlwaysNil(t *testing.T) {
	tests := []objectstorage.ReplicationPolicySummaryStatusEnum{
		objectstorage.ReplicationPolicySummaryStatusActive,
		objectstorage.ReplicationPolicySummaryStatusClientError,
	}
	for _, status := range tests {
		r := &ReplicationPolicy{policy: objectstorage.ReplicationPolicySummary{Status: status}}
		if err := r.Filter(); err != nil {
			t.Errorf("Filter() with status %s = %v, want nil (delete-and-see is the only signal)", status, err)
		}
	}
}

// TestReplicationPolicy_Remove proves DeleteReplicationPolicy fires with the exact
// ReplicationId/namespace/bucket, against a client that never touches the network -- this fires
// BEFORE Bucket.Remove() is ever attempted, per Bucket.DependsOn's ordering (T-04-27).
func TestReplicationPolicy_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{}
	r := &ReplicationPolicy{
		client:        stub,
		namespace:     testNamespace,
		bucketName:    testBucketName,
		compartmentID: compartmentID,
		policy:        objectstorage.ReplicationPolicySummary{Id: &id, TimeCreated: &timeCreated},
	}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedReplicationPolicies) != 1 {
		t.Fatalf("stub.deletedReplicationPolicies has %d entries, want 1", len(stub.deletedReplicationPolicies))
	}
	got := stub.deletedReplicationPolicies[0]
	if got.ReplicationId == nil || *got.ReplicationId != id {
		t.Errorf("DeleteReplicationPolicyRequest.ReplicationId = %v, want %q", got.ReplicationId, id)
	}
	if got.BucketName == nil || *got.BucketName != testBucketName {
		t.Errorf("DeleteReplicationPolicyRequest.BucketName = %v, want %q", got.BucketName, testBucketName)
	}
}

// TestReplicationPolicy_Properties proves Properties() surfaces id/name/bucket/namespace/status/
// destination_bucket_name.
func TestReplicationPolicy_Properties(t *testing.T) {
	id := testResourceOCID
	name := "test-replication-policy"
	destBucket := "destination-bucket"

	r := &ReplicationPolicy{
		namespace:  testNamespace,
		bucketName: testBucketName,
		policy: objectstorage.ReplicationPolicySummary{
			Id: &id, Name: &name, DestinationBucketName: &destBucket,
			Status: objectstorage.ReplicationPolicySummaryStatusActive,
		},
	}

	props := r.Properties()
	if got := props.Get(propID); got != id {
		t.Errorf("Properties()[%q] = %q, want %q", propID, got, id)
	}
	if got := props.Get(propName); got != name {
		t.Errorf("Properties()[%q] = %q, want %q", propName, got, name)
	}
	if got := props.Get(propBucket); got != testBucketName {
		t.Errorf("Properties()[%q] = %q, want %q", propBucket, got, testBucketName)
	}
	if got := props.Get(propNamespace); got != testNamespace {
		t.Errorf("Properties()[%q] = %q, want %q", propNamespace, got, testNamespace)
	}
	if got := props.Get("status"); got != "ACTIVE" {
		t.Errorf("Properties()[%q] = %q, want %q", "status", got, "ACTIVE")
	}
	if got := props.Get("destination_bucket_name"); got != destBucket {
		t.Errorf("Properties()[%q] = %q, want %q", "destination_bucket_name", got, destBucket)
	}
}
