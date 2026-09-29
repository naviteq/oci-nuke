package resources

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// testObjectName/testVersionID are shared object-version fixture literals reused across this
// file's tests -- declared once so the package stays goconst-clean (min-occurrences: 3).
const (
	testObjectName = "object.log"
	testVersionID  = "v1"
)

// TestObjectVersionLister_List_EnumeratesAllVersionsViaListObjectVersions is the named test this
// plan's success criteria require: proof that ObjectVersion enumeration goes through
// ListObjectVersions, never ListObjects, against a stub configured with 04-RESEARCH.md Q4's
// live-verified real-bucket shape -- 1 current object, 24 total versions, none of them delete
// markers. objectStorageClient does not declare a ListObjects method at all (a compile-time
// guarantee, T-04-24) -- the fakeObjectStorageClient stub this test uses could not satisfy a
// ListObjects-based enumeration even if objectVersionList tried to call one.
func TestObjectVersionLister_List_EnumeratesAllVersionsViaListObjectVersions(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	bucketName := testBucketName
	name := "naviteq-onboarding-v1.zip"

	versions := make([]objectstorage.ObjectVersionSummary, 0, 24)
	for i := 0; i < 24; i++ {
		versionID := fmt.Sprintf("version-%d", i)
		isDeleteMarker := false
		versions = append(versions, objectstorage.ObjectVersionSummary{
			Name: &name, VersionId: &versionID, IsDeleteMarker: &isDeleteMarker,
		})
	}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{
			{Namespace: &namespace, Name: &bucketName, CompartmentId: &compartmentID},
		},
		objectVersions: map[string][]objectstorage.ObjectVersionSummary{bucketName: versions},
	}

	got, err := objectVersionList(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("objectVersionList() error = %v, want nil", err)
	}
	if len(got) != 24 {
		t.Fatalf(
			"objectVersionList() returned %d resources, want 24 (live-verified real bucket shape, "+
				"04-RESEARCH.md Q4) -- enumeration must go through ListObjectVersions, never ListObjects",
			len(got),
		)
	}
	if stub.listObjectVersionsCalls != 1 {
		t.Errorf("ListObjectVersions called %d times, want 1", stub.listObjectVersionsCalls)
	}
}

// TestObjectVersion_Filter_AlwaysNil proves Filter() unconditionally returns nil -- ObjectVersion
// has no LifecycleState field at all.
func TestObjectVersion_Filter_AlwaysNil(t *testing.T) {
	r := &ObjectVersion{namespace: testNamespace, bucketName: testBucketName}
	if err := r.Filter(); err != nil {
		t.Errorf("Filter() = %v, want nil (ObjectVersion has no lifecycle state)", err)
	}
}

// TestObjectVersion_UniqueKey_Composite proves UniqueKey() is the composite
// namespace/bucket/name/versionId, never an OCID -- an object version has no OCID of its own.
func TestObjectVersion_UniqueKey_Composite(t *testing.T) {
	name := testObjectName
	versionID := testVersionID
	r := &ObjectVersion{
		namespace:  testNamespace,
		bucketName: testBucketName,
		ov:         objectstorage.ObjectVersionSummary{Name: &name, VersionId: &versionID},
	}
	want := testNamespace + "/" + testBucketName + "/" + name + "/" + versionID
	if got := r.UniqueKey(); got != want {
		t.Errorf("UniqueKey() = %q, want %q", got, want)
	}
}

// TestObjectVersion_Remove_TargetsExactVersionId proves DeleteObject is called with BOTH
// ObjectName and the EXACT VersionId this resource wraps -- not merely that some delete call
// fired, proving a specific superseded version is targeted, never accidentally the current one or
// a different one.
func TestObjectVersion_Remove_TargetsExactVersionId(t *testing.T) {
	name := testObjectName
	versionID := "superseded-version-id"
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{}
	r := &ObjectVersion{
		client:        stub,
		namespace:     testNamespace,
		bucketName:    testBucketName,
		compartmentID: testCompartmentOCID,
		ov:            objectstorage.ObjectVersionSummary{Name: &name, VersionId: &versionID, TimeCreated: &timeCreated},
	}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedObjects) != 1 {
		t.Fatalf("stub.deletedObjects has %d entries, want 1", len(stub.deletedObjects))
	}
	got := stub.deletedObjects[0]
	if got.VersionId == nil || *got.VersionId != versionID {
		t.Errorf("DeleteObjectRequest.VersionId = %v, want %q", got.VersionId, versionID)
	}
	if got.ObjectName == nil || *got.ObjectName != name {
		t.Errorf("DeleteObjectRequest.ObjectName = %v, want %q", got.ObjectName, name)
	}
	if got.BucketName == nil || *got.BucketName != testBucketName {
		t.Errorf("DeleteObjectRequest.BucketName = %v, want %q", got.BucketName, testBucketName)
	}
}

// TestObjectVersion_Remove_NilTimeCreated proves Remove() does not panic when TimeCreated is nil
// -- objectstorage.ObjectVersionSummary declares TimeCreated `mandatory:"false"`.
func TestObjectVersion_Remove_NilTimeCreated(t *testing.T) {
	name := testObjectName
	versionID := testVersionID
	stub := &fakeObjectStorageClient{}
	r := &ObjectVersion{
		client:     stub,
		namespace:  testNamespace,
		bucketName: testBucketName,
		ov:         objectstorage.ObjectVersionSummary{Name: &name, VersionId: &versionID},
	}
	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
}

// TestObjectVersion_Properties proves Properties() surfaces name/bucket/namespace/version_id/
// is_delete_marker/size.
func TestObjectVersion_Properties(t *testing.T) {
	name := testObjectName
	versionID := testVersionID
	isDeleteMarker := true
	size := int64(1024)

	r := &ObjectVersion{
		namespace:  testNamespace,
		bucketName: testBucketName,
		ov: objectstorage.ObjectVersionSummary{
			Name: &name, VersionId: &versionID, IsDeleteMarker: &isDeleteMarker, Size: &size,
		},
	}

	props := r.Properties()
	if got := props.Get(propName); got != name {
		t.Errorf("Properties()[%q] = %q, want %q", propName, got, name)
	}
	if got := props.Get(propBucket); got != testBucketName {
		t.Errorf("Properties()[%q] = %q, want %q", propBucket, got, testBucketName)
	}
	if got := props.Get(propNamespace); got != testNamespace {
		t.Errorf("Properties()[%q] = %q, want %q", propNamespace, got, testNamespace)
	}
	if got := props.Get("version_id"); got != versionID {
		t.Errorf("Properties()[%q] = %q, want %q", "version_id", got, versionID)
	}
	if got, want := props.Get("is_delete_marker"), fmt.Sprint(isDeleteMarker); got != want {
		t.Errorf("Properties()[%q] = %q, want %q", "is_delete_marker", got, want)
	}
	if got := props.Get("size"); got != "1024" {
		t.Errorf("Properties()[%q] = %q, want %q", "size", got, "1024")
	}
}
