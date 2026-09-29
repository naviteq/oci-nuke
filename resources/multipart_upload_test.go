package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// testUploadObjectName/testUploadID are shared multipart-upload fixture literals reused across
// this file's tests -- declared once so the package stays goconst-clean (min-occurrences: 3).
const (
	testUploadObjectName = "in-progress-upload.zip"
	testUploadID         = "upload-1"
)

// TestMultipartUploadLister_List proves multipartUploadList wraps every objectstorage.MultipartUpload
// returned per bucket, threading namespace/bucket name/compartmentID down correctly.
func TestMultipartUploadLister_List(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	bucketName := testBucketName
	object := testUploadObjectName
	uploadID := testUploadID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{
			{Namespace: &namespace, Name: &bucketName, CompartmentId: &compartmentID},
		},
		multipartUploads: map[string][]objectstorage.MultipartUpload{
			bucketName: {{Object: &object, UploadId: &uploadID, TimeCreated: &timeCreated}},
		},
	}

	got, err := multipartUploadList(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("multipartUploadList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("multipartUploadList() returned %d resources, want 1", len(got))
	}
	mu, ok := got[0].(*MultipartUpload)
	if !ok {
		t.Fatalf("multipartUploadList()[0] is %T, want *MultipartUpload", got[0])
	}
	if mu.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", mu.GetCompartmentID(), compartmentID)
	}
}

// TestMultipartUpload_Filter_AlwaysNil proves Filter() unconditionally returns nil --
// MultipartUpload has no LifecycleState field at all.
func TestMultipartUpload_Filter_AlwaysNil(t *testing.T) {
	r := &MultipartUpload{namespace: testNamespace, bucketName: testBucketName}
	if err := r.Filter(); err != nil {
		t.Errorf("Filter() = %v, want nil (MultipartUpload has no lifecycle state)", err)
	}
}

// TestMultipartUpload_UniqueKey_Composite proves UniqueKey() is the composite
// namespace/bucket/object/uploadId, never an OCID -- an in-progress multipart upload has no OCID.
func TestMultipartUpload_UniqueKey_Composite(t *testing.T) {
	object := testUploadObjectName
	uploadID := testUploadID
	r := &MultipartUpload{
		namespace:  testNamespace,
		bucketName: testBucketName,
		mu:         objectstorage.MultipartUpload{Object: &object, UploadId: &uploadID},
	}
	want := testNamespace + "/" + testBucketName + "/" + object + "/" + uploadID
	if got := r.UniqueKey(); got != want {
		t.Errorf("UniqueKey() = %q, want %q", got, want)
	}
}

// TestMultipartUpload_Remove_TargetsObjectAndUploadId proves AbortMultipartUpload is called with
// BOTH ObjectName and UploadId set, targeting the exact in-progress upload this resource wraps.
func TestMultipartUpload_Remove_TargetsObjectAndUploadId(t *testing.T) {
	object := testUploadObjectName
	uploadID := testUploadID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{}
	r := &MultipartUpload{
		client:        stub,
		namespace:     testNamespace,
		bucketName:    testBucketName,
		compartmentID: testCompartmentOCID,
		mu:            objectstorage.MultipartUpload{Object: &object, UploadId: &uploadID, TimeCreated: &timeCreated},
	}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.abortedUploads) != 1 {
		t.Fatalf("stub.abortedUploads has %d entries, want 1", len(stub.abortedUploads))
	}
	got := stub.abortedUploads[0]
	if got.ObjectName == nil || *got.ObjectName != object {
		t.Errorf("AbortMultipartUploadRequest.ObjectName = %v, want %q", got.ObjectName, object)
	}
	if got.UploadId == nil || *got.UploadId != uploadID {
		t.Errorf("AbortMultipartUploadRequest.UploadId = %v, want %q", got.UploadId, uploadID)
	}
	if got.BucketName == nil || *got.BucketName != testBucketName {
		t.Errorf("AbortMultipartUploadRequest.BucketName = %v, want %q", got.BucketName, testBucketName)
	}
}

// TestMultipartUpload_Properties proves Properties() surfaces bucket/namespace/object/upload_id.
func TestMultipartUpload_Properties(t *testing.T) {
	object := testUploadObjectName
	uploadID := testUploadID

	r := &MultipartUpload{
		namespace:  testNamespace,
		bucketName: testBucketName,
		mu:         objectstorage.MultipartUpload{Object: &object, UploadId: &uploadID},
	}

	props := r.Properties()
	if got := props.Get(propBucket); got != testBucketName {
		t.Errorf("Properties()[%q] = %q, want %q", propBucket, got, testBucketName)
	}
	if got := props.Get(propNamespace); got != testNamespace {
		t.Errorf("Properties()[%q] = %q, want %q", propNamespace, got, testNamespace)
	}
	if got := props.Get("object"); got != object {
		t.Errorf("Properties()[%q] = %q, want %q", "object", got, object)
	}
	if got := props.Get("upload_id"); got != uploadID {
		t.Errorf("Properties()[%q] = %q, want %q", "upload_id", got, uploadID)
	}
}
