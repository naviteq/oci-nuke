package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// TestBucketChildren_InheritBucketTags proves every type listed per bucket returns its bucket's
// tags from SafetyTags, so a protect-by-tag match on the bucket keeps what is inside it.
func TestBucketChildren_InheritBucketTags(t *testing.T) {
	ctx := context.Background()
	namespace, compartmentID, bucketName := testNamespace, testCompartmentOCID, testBucketName
	name, id := testObjectName, testVersionID
	created := &common.SDKTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{{
			Namespace:     &namespace,
			Name:          &bucketName,
			CompartmentId: &compartmentID,
			FreeformTags:  map[string]string{testFreeformTagKey: testFreeformTagValue},
			DefinedTags: map[string]map[string]interface{}{
				testDefinedTagNamespace: {testDefinedTagKey: testDefinedTagFlatValue},
			},
		}},
		objectVersions: map[string][]objectstorage.ObjectVersionSummary{
			bucketName: {{Name: &name, VersionId: &id}},
		},
		multipartUploads: map[string][]objectstorage.MultipartUpload{
			bucketName: {{Object: &name, UploadId: &id, TimeCreated: created}},
		},
		preauthRequests: map[string][]objectstorage.PreauthenticatedRequestSummary{
			bucketName: {{Id: &id, Name: &name, TimeCreated: created}},
		},
		retentionRules: map[string][]objectstorage.RetentionRuleSummary{
			bucketName: {{Id: &id, DisplayName: &name, TimeCreated: created}},
		},
		replicationPolicies: map[string][]objectstorage.ReplicationPolicySummary{
			bucketName: {{Id: &id, Name: &name, TimeCreated: created}},
		},
	}

	listers := map[string]func() ([]resource.Resource, error){
		ObjectVersionResourceType: func() ([]resource.Resource, error) {
			return objectVersionList(ctx, stub, namespace, compartmentID)
		},
		MultipartUploadResourceType: func() ([]resource.Resource, error) {
			return multipartUploadList(ctx, stub, namespace, compartmentID)
		},
		PreauthenticatedRequestResourceType: func() ([]resource.Resource, error) {
			return preauthenticatedRequestList(ctx, stub, namespace, compartmentID)
		},
		RetentionRuleResourceType: func() ([]resource.Resource, error) {
			return retentionRuleList(ctx, stub, namespace, compartmentID)
		},
		ReplicationPolicyResourceType: func() ([]resource.Resource, error) {
			return replicationPolicyList(ctx, stub, namespace, compartmentID)
		},
	}

	protect := ocinuke.SafetyFilterConfig{
		ProtectTags: []ocinuke.TagMatch{{Key: testFreeformTagKey, Value: testFreeformTagValue}},
	}
	definedKey := testDefinedTagNamespace + "." + testDefinedTagKey

	for resourceType, list := range listers {
		t.Run(resourceType, func(t *testing.T) {
			got, err := list()
			if err != nil {
				t.Fatalf("list error = %v, want nil", err)
			}
			if len(got) != 1 {
				t.Fatalf("list returned %d resources, want 1", len(got))
			}
			safe, ok := got[0].(ocinuke.SafetyEvaluated)
			if !ok {
				t.Fatalf("%T does not implement ocinuke.SafetyEvaluated", got[0])
			}
			freeform, defined, _ := safe.SafetyTags()
			if freeform[testFreeformTagKey] != testFreeformTagValue {
				t.Errorf("SafetyTags() freeform = %v, want the bucket's %s=%s", freeform, testFreeformTagKey, testFreeformTagValue)
			}
			if defined[definedKey] != testDefinedTagFlatValue {
				t.Errorf("SafetyTags() defined = %v, want the bucket's %s=%s", defined, definedKey, testDefinedTagFlatValue)
			}
			skip := ocinuke.Evaluate(compartmentID, resourceType, id, freeform, defined, time.Time{}, protect)
			if skip == nil || skip.Reason != scope.ReasonProtectedByTag {
				t.Errorf("Evaluate() = %+v, want %s from the bucket's tag", skip, scope.ReasonProtectedByTag)
			}
		})
	}
}

// TestBucketChildren_UntaggedBucketProtectsNothing proves an untagged bucket hands its contents
// no tags, so nothing inside it is protected by inheritance.
func TestBucketChildren_UntaggedBucketProtectsNothing(t *testing.T) {
	namespace, compartmentID, bucketName := testNamespace, testCompartmentOCID, testBucketName
	name, id := testObjectName, testVersionID
	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{{Namespace: &namespace, Name: &bucketName, CompartmentId: &compartmentID}},
		objectVersions: map[string][]objectstorage.ObjectVersionSummary{
			bucketName: {{Name: &name, VersionId: &id}},
		},
	}
	got, err := objectVersionList(context.Background(), stub, namespace, compartmentID)
	if err != nil || len(got) != 1 {
		t.Fatalf("objectVersionList() = %d resources, %v; want 1, nil", len(got), err)
	}
	freeform, defined, _ := got[0].(*ObjectVersion).SafetyTags()
	if len(freeform) != 0 || len(defined) != 0 {
		t.Errorf("SafetyTags() = %v, %v; want no tags from an untagged bucket", freeform, defined)
	}
}
