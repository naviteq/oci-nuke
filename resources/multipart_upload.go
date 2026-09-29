// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// MultipartUploadResourceType is the registry.Registration.Name for MultipartUpload.
const MultipartUploadResourceType = "MultipartUpload"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     MultipartUploadResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &MultipartUpload{},
		Lister:   &multipartUploadLister{},
		// DependsOn is intentionally empty -- MultipartUpload is a leaf; Bucket declares the edge.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type multipartUploadLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// multipartUploadList, kept separate so it is unit-testable against a stub client without ever
// constructing a real ObjectStorage client.
func (l *multipartUploadLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("multipartUploadLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.ObjectStorage(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing ObjectStorageClient for %s: %w", o.Region, err)
	}

	nsResp, err := client.GetNamespace(ctx, objectstorage.GetNamespaceRequest{})
	if err != nil {
		return nil, fmt.Errorf("getting Object Storage namespace for %s: %w", o.Region, err)
	}

	return multipartUploadList(ctx, client, *nsResp.Value, o.CompartmentID)
}

// multipartUploadList enumerates every bucket in the compartment (via listBucketsInCompartment,
// resources/object_storage_support.go), then paginates objectstorage.ListMultipartUploads per
// bucket. An uncommitted multipart upload blocks DeleteBucket (04-RESEARCH.md Q4) -- this is one
// of the pre-emptive-cleanup types Bucket.DependsOn names.
func multipartUploadList(
	ctx context.Context,
	client objectStorageClient,
	namespace, compartmentID string,
) ([]resource.Resource, error) {
	buckets, err := listBucketsInCompartment(ctx, client, namespace, compartmentID)
	if err != nil {
		return nil, err
	}

	var out []resource.Resource
	for i := range buckets {
		bucket := buckets[i]
		bucketName := *bucket.Name
		req := objectstorage.ListMultipartUploadsRequest{NamespaceName: &namespace, BucketName: &bucketName}
		for {
			resp, err := client.ListMultipartUploads(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing MultipartUpload for bucket %s/%s: %w", namespace, bucketName, err)
			}
			for j := range resp.Items {
				out = append(out, &MultipartUpload{
					client:        client,
					namespace:     namespace,
					bucketName:    bucketName,
					compartmentID: *bucket.CompartmentId,
					mu:            resp.Items[j],
				})
			}
			if resp.OpcNextPage == nil {
				break
			}
			req.Page = resp.OpcNextPage
		}
	}
	return out, nil
}

// MultipartUpload wraps one objectstorage.MultipartUpload, plus the namespace/bucket name/
// compartmentID inherited from its owning Bucket -- the SDK struct's own Namespace/Bucket fields
// name the same values, but this plan threads them down from the shared bucket enumeration for
// consistency with ObjectVersion/PreauthenticatedRequest/RetentionRule/ReplicationPolicy.
type MultipartUpload struct {
	client        objectStorageClient
	namespace     string
	bucketName    string
	compartmentID string
	mu            objectstorage.MultipartUpload
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. Returns the compartment ID inherited from the owning Bucket at list time.
func (r *MultipartUpload) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- a composite key, since an in-progress
// multipart upload has no OCID: namespace+"/"+bucketName+"/"+object+"/"+uploadId. Both Object and
// UploadId are `mandatory:"true"` on objectstorage.MultipartUpload (verified this session), safe
// to dereference directly.
func (r *MultipartUpload) UniqueKey() string {
	return r.namespace + "/" + r.bucketName + "/" + *r.mu.Object + "/" + *r.mu.UploadId
}

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. MultipartUpload has no LifecycleState field at
// all (verified this session, objectstorage/multipart_upload.go) -- "gone" is entirely "absent
// from ListMultipartUploads."
func (r *MultipartUpload) Filter() error { return nil }

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// freeformTags/definedTags are passed nil -- objectstorage.MultipartUpload carries neither
// field.
func (r *MultipartUpload) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.mu
	return nil, nil, x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// AbortMultipartUpload is called with both ObjectName and UploadId set, targeting the exact
// in-progress upload this resource wraps.
func (r *MultipartUpload) Remove(ctx context.Context) error {
	x := r.mu
	_, err := r.client.AbortMultipartUpload(ctx, objectstorage.AbortMultipartUploadRequest{
		NamespaceName: &r.namespace,
		BucketName:    &r.bucketName,
		ObjectName:    x.Object,
		UploadId:      x.UploadId,
	})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties -- MultipartUpload
// has no Id/CompartmentId/LifecycleState field of its own. propBucket/propNamespace
// (resources/support.go) are reused since ObjectVersion/PreauthenticatedRequest/RetentionRule/
// ReplicationPolicy all set the same two keys.
func (r *MultipartUpload) Properties() types.Properties {
	x := r.mu
	return types.NewProperties().
		Set(propBucket, r.bucketName).
		Set(propNamespace, r.namespace).
		Set("object", x.Object).
		Set("upload_id", x.UploadId)
}
