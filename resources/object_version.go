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

// ObjectVersionResourceType is the registry.Registration.Name for ObjectVersion.
const ObjectVersionResourceType = "ObjectVersion"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ObjectVersionResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &ObjectVersion{},
		Lister:   &objectVersionLister{},
		// DependsOn is intentionally empty -- ObjectVersion is a leaf; Bucket (resources/bucket.go)
		// declares the edge on itself instead.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type objectVersionLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// objectVersionList, kept separate so it is unit-testable against a stub client without ever
// constructing a real ObjectStorage client.
func (l *objectVersionLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("objectVersionLister.List: unexpected opts type %T", opts)
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

	return objectVersionList(ctx, client, *nsResp.Value, o.CompartmentID)
}

// objectVersionList enumerates every bucket in the compartment (via listBucketsInCompartment,
// resources/object_storage_support.go), then paginates objectstorage.ListObjectVersions per
// bucket -- NEVER ListObjects, which objectStorageClient does not even declare a method for (a
// compile-time guarantee, T-04-24, not just a runtime test). This is the exact enumeration path
// this plan closes: 04-RESEARCH.md Q4 live-verified a real bucket where ListObjects returned 1
// current object but ListObjectVersions returned 24 versions of that same key -- a versioned
// bucket cannot be deleted while any superseded version remains, and ListObjectVersions correctly
// covers both versioned AND non-versioned buckets uniformly (a non-versioned bucket simply returns
// one version per key).
func objectVersionList(
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
		req := objectstorage.ListObjectVersionsRequest{NamespaceName: &namespace, BucketName: &bucketName}
		for {
			resp, err := client.ListObjectVersions(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing ObjectVersion for bucket %s/%s: %w", namespace, bucketName, err)
			}
			for j := range resp.Items {
				out = append(out, &ObjectVersion{
					client:        client,
					namespace:     namespace,
					bucketName:    bucketName,
					compartmentID: *bucket.CompartmentId,
					ov:            resp.Items[j],
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

// ObjectVersion wraps one objectstorage.ObjectVersionSummary, plus the namespace/bucket name/
// compartmentID inherited from its owning Bucket -- ObjectVersionSummary itself carries none of
// the three (Object Storage's API is namespace+bucket scoped, not independently compartment-
// scoped, per this plan's own objective).
type ObjectVersion struct {
	client        objectStorageClient
	namespace     string
	bucketName    string
	compartmentID string
	ov            objectstorage.ObjectVersionSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. Returns the compartment ID inherited from the owning Bucket at list time, since
// ObjectVersionSummary has no CompartmentId field of its own.
func (r *ObjectVersion) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- a composite key, since an object
// version has no OCID of its own: namespace+"/"+bucketName+"/"+name+"/"+versionId. Both Name and
// VersionId are `mandatory:"true"` on objectstorage.ObjectVersionSummary (verified this session),
// safe to dereference directly.
func (r *ObjectVersion) UniqueKey() string {
	return r.namespace + "/" + r.bucketName + "/" + *r.ov.Name + "/" + *r.ov.VersionId
}

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. ObjectVersion has no LifecycleState field at
// all (verified this session, objectstorage/object_version_summary.go), mirroring Bucket's own
// no-lifecycle pattern above -- "gone" is entirely "absent from ListObjectVersions."
func (r *ObjectVersion) Filter() error { return nil }

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// freeformTags/definedTags are passed nil -- ObjectVersionSummary carries neither field, so
// this type does not support protect-by-tag matching (nothing to match against).
func (r *ObjectVersion) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.ov
	return nil, nil, timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// DeleteObject is called with BOTH ObjectName and VersionId set, targeting the exact
// superseded (or current) version this resource wraps, never a different version of the same
// key.
func (r *ObjectVersion) Remove(ctx context.Context) error {
	x := r.ov
	_, err := r.client.DeleteObject(ctx, objectstorage.DeleteObjectRequest{
		NamespaceName: &r.namespace,
		BucketName:    &r.bucketName,
		ObjectName:    x.Name,
		VersionId:     x.VersionId,
	})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties -- ObjectVersion
// has no Id/CompartmentId/LifecycleState field of its own. propBucket/propNamespace
// (resources/support.go) are reused since MultipartUpload/PreauthenticatedRequest/RetentionRule/
// ReplicationPolicy all set the same two keys.
func (r *ObjectVersion) Properties() types.Properties {
	x := r.ov
	return types.NewProperties().
		Set(propName, x.Name).
		Set(propBucket, r.bucketName).
		Set(propNamespace, r.namespace).
		Set("version_id", x.VersionId).
		Set("is_delete_marker", x.IsDeleteMarker).
		Set("size", x.Size)
}
