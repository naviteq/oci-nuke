package resources

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/objectstorage"
)

// testNamespace/testBucketName are the shared Object Storage namespace/bucket name literals every
// test file in this plan (Bucket, ObjectVersion, MultipartUpload, PreauthenticatedRequest,
// RetentionRule, ReplicationPolicy) reuses -- declared once here so the package stays goconst-clean
// (min-occurrences: 3), mirroring resources/instance_test.go's testResourceOCID/testCompartmentOCID
// precedent.
const (
	testNamespace  = "test-namespace"
	testBucketName = "test-bucket"
)

// fakeObjectStorageClient implements objectStorageClient against in-memory data -- zero network
// access. Shared by every Object Storage resource type's tests in this plan (Bucket,
// ObjectVersion, MultipartUpload, PreauthenticatedRequest, RetentionRule, ReplicationPolicy),
// mirroring the one shared objectStorageClient interface these six types use in production
// (resources/object_storage_support.go). Per-bucket collections are keyed by bucket name (every
// real per-bucket List* request in this domain is scoped by BucketName, never an independent
// listing call).
type fakeObjectStorageClient struct {
	namespace    string
	namespaceErr error

	// buckets is used directly when bucketPages is empty (the common case: every type besides
	// the dedicated pagination test only needs ListBuckets to return one page).
	buckets []objectstorage.BucketSummary
	// bucketPages, when non-empty, makes ListBuckets page through it via req.Page as a stringified
	// index -- this is what TestListBucketsInCompartment_PaginatesAllPages exercises directly.
	bucketPages    [][]objectstorage.BucketSummary
	listBucketsErr error

	objectVersions      map[string][]objectstorage.ObjectVersionSummary
	multipartUploads    map[string][]objectstorage.MultipartUpload
	preauthRequests     map[string][]objectstorage.PreauthenticatedRequestSummary
	retentionRules      map[string][]objectstorage.RetentionRuleSummary
	replicationPolicies map[string][]objectstorage.ReplicationPolicySummary

	deletedBuckets             []string
	deletedObjects             []objectstorage.DeleteObjectRequest
	abortedUploads             []objectstorage.AbortMultipartUploadRequest
	deletedPARs                []objectstorage.DeletePreauthenticatedRequestRequest
	deletedRetentionRules      []objectstorage.DeleteRetentionRuleRequest
	deletedReplicationPolicies []objectstorage.DeleteReplicationPolicyRequest

	// listBucketsFields records the Fields the last ListBuckets request carried, which is what
	// decides whether OCI returns bucket tags at all.
	listBucketsFields []objectstorage.ListBucketsFieldsEnum

	getNamespaceCalls            int
	listBucketsCalls             int
	listObjectVersionsCalls      int
	listMultipartUploadsCalls    int
	listPreauthRequestsCalls     int
	listRetentionRulesCalls      int
	listReplicationPoliciesCalls int
}

func (s *fakeObjectStorageClient) GetNamespace(
	_ context.Context, _ objectstorage.GetNamespaceRequest,
) (objectstorage.GetNamespaceResponse, error) {
	s.getNamespaceCalls++
	if s.namespaceErr != nil {
		return objectstorage.GetNamespaceResponse{}, s.namespaceErr
	}
	ns := s.namespace
	return objectstorage.GetNamespaceResponse{Value: &ns}, nil
}

func (s *fakeObjectStorageClient) ListBuckets(
	_ context.Context, req objectstorage.ListBucketsRequest,
) (objectstorage.ListBucketsResponse, error) {
	s.listBucketsCalls++
	s.listBucketsFields = req.Fields
	if s.listBucketsErr != nil {
		return objectstorage.ListBucketsResponse{}, s.listBucketsErr
	}
	if len(s.bucketPages) > 0 {
		page := 0
		if req.Page != nil {
			p, err := strconv.Atoi(*req.Page)
			if err == nil {
				page = p
			}
		}
		resp := objectstorage.ListBucketsResponse{Items: s.bucketPages[page]}
		if page+1 < len(s.bucketPages) {
			next := strconv.Itoa(page + 1)
			resp.OpcNextPage = &next
		}
		return resp, nil
	}
	return objectstorage.ListBucketsResponse{Items: s.buckets}, nil
}

func (s *fakeObjectStorageClient) DeleteBucket(
	_ context.Context, req objectstorage.DeleteBucketRequest,
) (objectstorage.DeleteBucketResponse, error) {
	s.deletedBuckets = append(s.deletedBuckets, safeDeref(req.NamespaceName)+"/"+safeDeref(req.BucketName))
	return objectstorage.DeleteBucketResponse{}, nil
}

func (s *fakeObjectStorageClient) ListObjectVersions(
	_ context.Context, req objectstorage.ListObjectVersionsRequest,
) (objectstorage.ListObjectVersionsResponse, error) {
	s.listObjectVersionsCalls++
	items := s.objectVersions[safeDeref(req.BucketName)]
	return objectstorage.ListObjectVersionsResponse{
		ObjectVersionCollection: objectstorage.ObjectVersionCollection{Items: items},
	}, nil
}

func (s *fakeObjectStorageClient) DeleteObject(
	_ context.Context, req objectstorage.DeleteObjectRequest,
) (objectstorage.DeleteObjectResponse, error) {
	s.deletedObjects = append(s.deletedObjects, req)
	return objectstorage.DeleteObjectResponse{}, nil
}

func (s *fakeObjectStorageClient) ListMultipartUploads(
	_ context.Context, req objectstorage.ListMultipartUploadsRequest,
) (objectstorage.ListMultipartUploadsResponse, error) {
	s.listMultipartUploadsCalls++
	return objectstorage.ListMultipartUploadsResponse{Items: s.multipartUploads[safeDeref(req.BucketName)]}, nil
}

func (s *fakeObjectStorageClient) AbortMultipartUpload(
	_ context.Context, req objectstorage.AbortMultipartUploadRequest,
) (objectstorage.AbortMultipartUploadResponse, error) {
	s.abortedUploads = append(s.abortedUploads, req)
	return objectstorage.AbortMultipartUploadResponse{}, nil
}

func (s *fakeObjectStorageClient) ListPreauthenticatedRequests(
	_ context.Context, req objectstorage.ListPreauthenticatedRequestsRequest,
) (objectstorage.ListPreauthenticatedRequestsResponse, error) {
	s.listPreauthRequestsCalls++
	return objectstorage.ListPreauthenticatedRequestsResponse{Items: s.preauthRequests[safeDeref(req.BucketName)]}, nil
}

func (s *fakeObjectStorageClient) DeletePreauthenticatedRequest(
	_ context.Context, req objectstorage.DeletePreauthenticatedRequestRequest,
) (objectstorage.DeletePreauthenticatedRequestResponse, error) {
	s.deletedPARs = append(s.deletedPARs, req)
	return objectstorage.DeletePreauthenticatedRequestResponse{}, nil
}

func (s *fakeObjectStorageClient) ListRetentionRules(
	_ context.Context, req objectstorage.ListRetentionRulesRequest,
) (objectstorage.ListRetentionRulesResponse, error) {
	s.listRetentionRulesCalls++
	return objectstorage.ListRetentionRulesResponse{
		RetentionRuleCollection: objectstorage.RetentionRuleCollection{Items: s.retentionRules[safeDeref(req.BucketName)]},
	}, nil
}

func (s *fakeObjectStorageClient) DeleteRetentionRule(
	_ context.Context, req objectstorage.DeleteRetentionRuleRequest,
) (objectstorage.DeleteRetentionRuleResponse, error) {
	s.deletedRetentionRules = append(s.deletedRetentionRules, req)
	return objectstorage.DeleteRetentionRuleResponse{}, nil
}

func (s *fakeObjectStorageClient) ListReplicationPolicies(
	_ context.Context, req objectstorage.ListReplicationPoliciesRequest,
) (objectstorage.ListReplicationPoliciesResponse, error) {
	s.listReplicationPoliciesCalls++
	return objectstorage.ListReplicationPoliciesResponse{Items: s.replicationPolicies[safeDeref(req.BucketName)]}, nil
}

func (s *fakeObjectStorageClient) DeleteReplicationPolicy(
	_ context.Context, req objectstorage.DeleteReplicationPolicyRequest,
) (objectstorage.DeleteReplicationPolicyResponse, error) {
	s.deletedReplicationPolicies = append(s.deletedReplicationPolicies, req)
	return objectstorage.DeleteReplicationPolicyResponse{}, nil
}

// TestListBucketsInCompartment_PaginatesAllPages proves listBucketsInCompartment follows
// OpcNextPage through every page rather than stopping after the first, against a stub returning
// two pages -- a single-page test would not catch a lister that silently drops every bucket past
// page 1.
func TestListBucketsInCompartment_PaginatesAllPages(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	name1, name2 := "bucket-page-1", "bucket-page-2"

	stub := &fakeObjectStorageClient{
		bucketPages: [][]objectstorage.BucketSummary{
			{{Namespace: &namespace, Name: &name1, CompartmentId: &compartmentID}},
			{{Namespace: &namespace, Name: &name2, CompartmentId: &compartmentID}},
		},
	}

	got, err := listBucketsInCompartment(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("listBucketsInCompartment() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("listBucketsInCompartment() returned %d buckets, want 2 (both pages)", len(got))
	}
	if *got[0].Name != name1 || *got[1].Name != name2 {
		t.Errorf("listBucketsInCompartment() = [%q, %q], want [%q, %q]", *got[0].Name, *got[1].Name, name1, name2)
	}
	if stub.listBucketsCalls != 2 {
		t.Errorf("ListBuckets called %d times, want 2 (one per page)", stub.listBucketsCalls)
	}
}

// TestListBucketsInCompartment_ListError proves a ListBuckets error propagates rather than being
// silently swallowed.
// TestListBucketsInCompartment_RequestsTags pins the one request field that decides whether
// settings.protect.tags can protect a bucket at all. ListBuckets omits tags unless Fields asks
// for them; without it BucketSummary comes back with nil FreeformTags/DefinedTags, Evaluate reads
// that as "carries no tags", and a bucket tagged Persistent=true is deleted anyway. Verified
// against the live tenancy: the same plan reported 3 buckets to remove before this field was
// sent and 2 protect-by-tag matches after.
func TestListBucketsInCompartment_RequestsTags(t *testing.T) {
	stub := &fakeObjectStorageClient{namespace: testNamespace}

	if _, err := listBucketsInCompartment(context.Background(), stub, testNamespace, testCompartmentOCID); err != nil {
		t.Fatalf("listBucketsInCompartment() error = %v, want nil", err)
	}

	var found bool
	for _, f := range stub.listBucketsFields {
		if f == objectstorage.ListBucketsFieldsTags {
			found = true
		}
	}
	if !found {
		t.Errorf("ListBuckets Fields = %v, want it to contain %q -- without it OCI returns no tags and protect-by-tag can never match a bucket",
			stub.listBucketsFields, objectstorage.ListBucketsFieldsTags)
	}
}

func TestListBucketsInCompartment_ListError(t *testing.T) {
	stub := &fakeObjectStorageClient{listBucketsErr: errors.New("boom")}

	_, err := listBucketsInCompartment(context.Background(), stub, "ns", testCompartmentOCID)
	if err == nil {
		t.Fatal("listBucketsInCompartment() error = nil, want non-nil")
	}
}
