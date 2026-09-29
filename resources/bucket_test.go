package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// TestBucketLister_List proves bucketList wraps every objectstorage.BucketSummary returned by
// listBucketsInCompartment as a Bucket, threading namespace/compartmentID down correctly.
func TestBucketLister_List(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	name := testBucketName
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{
			{Namespace: &namespace, Name: &name, CompartmentId: &compartmentID, TimeCreated: &timeCreated},
		},
	}

	got, err := bucketList(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("bucketList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("bucketList() returned %d resources, want 1", len(got))
	}
	b, ok := got[0].(*Bucket)
	if !ok {
		t.Fatalf("bucketList()[0] is %T, want *Bucket", got[0])
	}
	if b.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", b.GetCompartmentID(), compartmentID)
	}
	if props := b.Properties(); props.Get(propName) != name {
		t.Errorf("Properties()[%q] = %q, want %q", propName, props.Get(propName), name)
	}
}

// TestBucket_Filter_AlwaysNil proves Bucket.Filter() unconditionally returns nil -- Bucket has no
// LifecycleState field at all (verified against objectstorage/bucket.go/bucket_summary.go), so
// there is nothing to switch on.
func TestBucket_Filter_AlwaysNil(t *testing.T) {
	r := &Bucket{namespace: testNamespace, name: testBucketName}
	if err := r.Filter(); err != nil {
		t.Errorf("Filter() = %v, want nil (Bucket has no lifecycle state)", err)
	}
}

// TestBucket_UniqueKey_NamespaceSlashName proves UniqueKey() is the composite namespace+"/"+name,
// never an OCID -- the SAFE-09 exception this plan's objective documents (BucketSummary has no Id
// field at all).
func TestBucket_UniqueKey_NamespaceSlashName(t *testing.T) {
	r := &Bucket{namespace: testNamespace, name: testBucketName}
	want := testNamespace + "/" + testBucketName
	if got := r.UniqueKey(); got != want {
		t.Errorf("UniqueKey() = %q, want %q", got, want)
	}
}

// TestBucket_Remove proves the SDK delete call fires with the right namespace/bucket name, against
// a client that never touches the network.
func TestBucket_Remove(t *testing.T) {
	namespace := testNamespace
	name := testBucketName
	compartmentID := testCompartmentOCID
	timeCreated := &common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{}
	r := &Bucket{client: stub, namespace: namespace, name: name, compartmentID: compartmentID, timeCreated: timeCreated}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	want := namespace + "/" + name
	if len(stub.deletedBuckets) != 1 || stub.deletedBuckets[0] != want {
		t.Fatalf("stub.deletedBuckets = %v, want [%s]", stub.deletedBuckets, want)
	}
}

// TestBucket_DependsOn_AllFiveDependentTypes asserts Bucket's registered DependsOn names all five
// dependent types this plan registers, so none of them is left un-deleted before DeleteBucket
// fires.
func TestBucket_DependsOn_AllFiveDependentTypes(t *testing.T) {
	reg := registry.GetRegistration(BucketResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", BucketResourceType)
	}

	want := []string{"ObjectVersion", "MultipartUpload", "PreauthenticatedRequest", "RetentionRule", "ReplicationPolicy"}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("Bucket DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("Bucket DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}

// TestBucket_Properties proves Properties() surfaces name/compartment_id/namespace, built directly
// rather than via baseProperties (Bucket has no Id/LifecycleState).
func TestBucket_Properties(t *testing.T) {
	r := &Bucket{namespace: testNamespace, name: testBucketName, compartmentID: testCompartmentOCID}

	props := r.Properties()
	if got := props.Get(propName); got != testBucketName {
		t.Errorf("Properties()[%q] = %q, want %q", propName, got, testBucketName)
	}
	if got := props.Get(propCompartmentID); got != testCompartmentOCID {
		t.Errorf("Properties()[%q] = %q, want %q", propCompartmentID, got, testCompartmentOCID)
	}
	if got := props.Get(propNamespace); got != testNamespace {
		t.Errorf("Properties()[%q] = %q, want %q", propNamespace, got, testNamespace)
	}
}
