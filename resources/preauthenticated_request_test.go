package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// TestPreauthenticatedRequestLister_List proves preauthenticatedRequestList wraps every
// objectstorage.PreauthenticatedRequestSummary returned per bucket, threading namespace/bucket
// name/compartmentID down correctly.
func TestPreauthenticatedRequestLister_List(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	bucketName := testBucketName
	id := testResourceOCID
	name := "test-par"
	timeCreated := common.SDKTime{Time: time.Now()}
	timeExpires := common.SDKTime{Time: time.Now().Add(24 * time.Hour)}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{
			{Namespace: &namespace, Name: &bucketName, CompartmentId: &compartmentID},
		},
		preauthRequests: map[string][]objectstorage.PreauthenticatedRequestSummary{
			bucketName: {{
				Id: &id, Name: &name, TimeCreated: &timeCreated, TimeExpires: &timeExpires,
				AccessType: objectstorage.PreauthenticatedRequestSummaryAccessTypeObjectread,
			}},
		},
	}

	got, err := preauthenticatedRequestList(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("preauthenticatedRequestList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("preauthenticatedRequestList() returned %d resources, want 1", len(got))
	}
	par, ok := got[0].(*PreauthenticatedRequest)
	if !ok {
		t.Fatalf("preauthenticatedRequestList()[0] is %T, want *PreauthenticatedRequest", got[0])
	}
	if par.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", par.GetCompartmentID(), compartmentID)
	}
	if par.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", par.UniqueKey(), id)
	}
}

// TestPreauthenticatedRequest_UniqueKey_IsID proves UniqueKey() returns the real Id field
// directly, no composite needed -- PreauthenticatedRequestSummary HAS a real Id (verified this
// session).
func TestPreauthenticatedRequest_UniqueKey_IsID(t *testing.T) {
	id := testResourceOCID
	r := &PreauthenticatedRequest{par: objectstorage.PreauthenticatedRequestSummary{Id: &id}}
	if got := r.UniqueKey(); got != id {
		t.Errorf("UniqueKey() = %q, want %q", got, id)
	}
}

// TestPreauthenticatedRequest_Filter_AlwaysNil proves Filter() unconditionally returns nil --
// PreauthenticatedRequestSummary has no LifecycleState field at all.
func TestPreauthenticatedRequest_Filter_AlwaysNil(t *testing.T) {
	r := &PreauthenticatedRequest{}
	if err := r.Filter(); err != nil {
		t.Errorf("Filter() = %v, want nil (PreauthenticatedRequest has no lifecycle state)", err)
	}
}

// TestPreauthenticatedRequest_Remove proves DeletePreauthenticatedRequest fires with the exact
// ParId/namespace/bucket, against a client that never touches the network.
func TestPreauthenticatedRequest_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{}
	r := &PreauthenticatedRequest{
		client:        stub,
		namespace:     testNamespace,
		bucketName:    testBucketName,
		compartmentID: compartmentID,
		par:           objectstorage.PreauthenticatedRequestSummary{Id: &id, TimeCreated: &timeCreated},
	}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedPARs) != 1 {
		t.Fatalf("stub.deletedPARs has %d entries, want 1", len(stub.deletedPARs))
	}
	got := stub.deletedPARs[0]
	if got.ParId == nil || *got.ParId != id {
		t.Errorf("DeletePreauthenticatedRequestRequest.ParId = %v, want %q", got.ParId, id)
	}
	if got.BucketName == nil || *got.BucketName != testBucketName {
		t.Errorf("DeletePreauthenticatedRequestRequest.BucketName = %v, want %q", got.BucketName, testBucketName)
	}
}

// TestPreauthenticatedRequest_Properties proves Properties() surfaces id/name/bucket/namespace/
// access_type.
func TestPreauthenticatedRequest_Properties(t *testing.T) {
	id := testResourceOCID
	name := "test-par"

	r := &PreauthenticatedRequest{
		namespace:  testNamespace,
		bucketName: testBucketName,
		par: objectstorage.PreauthenticatedRequestSummary{
			Id: &id, Name: &name, AccessType: objectstorage.PreauthenticatedRequestSummaryAccessTypeObjectread,
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
	if got := props.Get("access_type"); got != "ObjectRead" {
		t.Errorf("Properties()[%q] = %q, want %q", "access_type", got, "ObjectRead")
	}
}
