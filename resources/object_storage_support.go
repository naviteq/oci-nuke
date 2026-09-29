// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// objectStorageClient is the narrow slice of objectstorage.ObjectStorageClient shared by every
// resource type this plan registers (Bucket, ObjectVersion, MultipartUpload,
// PreauthenticatedRequest, RetentionRule, ReplicationPolicy) -- a deliberate, documented exception
// to "one narrow interface per type" (04-RESEARCH.md Q1's default, already carved out once before
// this wave for VolumeAttachment). All six types operate on the same two path parameters
// (namespace, bucket name); splitting the interface six ways would only fragment a stub every one
// of the six types' tests needs identically. Thirteen methods total (04-RESEARCH.md Q4).
//
// ListObjectVersions/DeleteObject are the ONLY object-enumeration methods declared here --
// ListObjects is deliberately NOT part of this interface at all. This is a compile-time guarantee
// (T-04-24), not just a runtime test: a future edit cannot silently reintroduce the "misses
// superseded versions on a versioned bucket" pitfall this plan closes (04-RESEARCH.md Q4's
// live-verified finding: ListObjects returned 1 object, ListObjectVersions returned 24 versions of
// the same key, on the same real bucket) without adding a NEW method to this interface, which is a
// visible, reviewable diff.
type objectStorageClient interface {
	GetNamespace(ctx context.Context, req objectstorage.GetNamespaceRequest) (objectstorage.GetNamespaceResponse, error)
	ListBuckets(ctx context.Context, req objectstorage.ListBucketsRequest) (objectstorage.ListBucketsResponse, error)
	DeleteBucket(ctx context.Context, req objectstorage.DeleteBucketRequest) (objectstorage.DeleteBucketResponse, error)
	ListObjectVersions(
		ctx context.Context, req objectstorage.ListObjectVersionsRequest,
	) (objectstorage.ListObjectVersionsResponse, error)
	DeleteObject(ctx context.Context, req objectstorage.DeleteObjectRequest) (objectstorage.DeleteObjectResponse, error)
	ListMultipartUploads(
		ctx context.Context, req objectstorage.ListMultipartUploadsRequest,
	) (objectstorage.ListMultipartUploadsResponse, error)
	AbortMultipartUpload(
		ctx context.Context, req objectstorage.AbortMultipartUploadRequest,
	) (objectstorage.AbortMultipartUploadResponse, error)
	ListPreauthenticatedRequests(
		ctx context.Context, req objectstorage.ListPreauthenticatedRequestsRequest,
	) (objectstorage.ListPreauthenticatedRequestsResponse, error)
	DeletePreauthenticatedRequest(
		ctx context.Context, req objectstorage.DeletePreauthenticatedRequestRequest,
	) (objectstorage.DeletePreauthenticatedRequestResponse, error)
	ListRetentionRules(
		ctx context.Context, req objectstorage.ListRetentionRulesRequest,
	) (objectstorage.ListRetentionRulesResponse, error)
	DeleteRetentionRule(
		ctx context.Context, req objectstorage.DeleteRetentionRuleRequest,
	) (objectstorage.DeleteRetentionRuleResponse, error)
	ListReplicationPolicies(
		ctx context.Context, req objectstorage.ListReplicationPoliciesRequest,
	) (objectstorage.ListReplicationPoliciesResponse, error)
	DeleteReplicationPolicy(
		ctx context.Context, req objectstorage.DeleteReplicationPolicyRequest,
	) (objectstorage.DeleteReplicationPolicyResponse, error)
}

// listBucketsInCompartment paginates objectstorage.ListBuckets for namespace+compartmentID and
// returns every objectstorage.BucketSummary across all pages. Shared by every one of this plan's
// six listers -- each one calls client.GetNamespace once, then this helper, then enumerates its
// own resource type per discovered bucket. Object Storage's API is namespace-scoped rather than
// compartment-scoped in places (ListBuckets itself still takes a CompartmentId, but every
// subsequent per-bucket List call this plan's other five types make takes only namespace+bucket
// name -- neither dependent type's own SDK summary struct carries a usable CompartmentId), which
// is why every dependent type's constructed resource struct threads the owning bucket's own
// CompartmentId down explicitly rather than reading one off its own SDK response shape.
func listBucketsInCompartment(
	ctx context.Context,
	client objectStorageClient,
	namespace, compartmentID string,
) ([]objectstorage.BucketSummary, error) {
	var out []objectstorage.BucketSummary
	// Without Fields, ListBuckets omits tags entirely and BucketSummary comes back with nil
	// FreeformTags/DefinedTags -- which reads to Evaluate as "carries no tags", so
	// settings.protect.tags can never match a bucket and a tag-protected bucket is deleted.
	req := objectstorage.ListBucketsRequest{
		NamespaceName: &namespace,
		CompartmentId: &compartmentID,
		Fields:        []objectstorage.ListBucketsFieldsEnum{objectstorage.ListBucketsFieldsTags},
	}
	for {
		resp, err := client.ListBuckets(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing buckets in namespace %s, compartment %s: %w", namespace, compartmentID, err)
		}
		out = append(out, resp.Items...)
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}
