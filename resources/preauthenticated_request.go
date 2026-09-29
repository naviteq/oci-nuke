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

// PreauthenticatedRequestResourceType is the registry.Registration.Name for
// PreauthenticatedRequest.
const PreauthenticatedRequestResourceType = "PreauthenticatedRequest"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     PreauthenticatedRequestResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &PreauthenticatedRequest{},
		Lister:   &preauthenticatedRequestLister{},
		// DependsOn is intentionally empty -- PreauthenticatedRequest is a leaf; Bucket declares
		// the edge. A pre-authenticated request left in place blocks DeleteBucket
		// (04-RESEARCH.md Q4), so it must be removed before Bucket.Remove() is ever attempted.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type preauthenticatedRequestLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// preauthenticatedRequestList, kept separate so it is unit-testable against a stub client
// without ever constructing a real ObjectStorage client.
func (l *preauthenticatedRequestLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("preauthenticatedRequestLister.List: unexpected opts type %T", opts)
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

	return preauthenticatedRequestList(ctx, client, *nsResp.Value, o.CompartmentID)
}

// preauthenticatedRequestList enumerates every bucket in the compartment (via
// listBucketsInCompartment, resources/object_storage_support.go), then paginates
// objectstorage.ListPreauthenticatedRequests per bucket.
func preauthenticatedRequestList(
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
		req := objectstorage.ListPreauthenticatedRequestsRequest{NamespaceName: &namespace, BucketName: &bucketName}
		for {
			resp, err := client.ListPreauthenticatedRequests(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing PreauthenticatedRequest for bucket %s/%s: %w", namespace, bucketName, err)
			}
			for j := range resp.Items {
				out = append(out, &PreauthenticatedRequest{
					client:        client,
					namespace:     namespace,
					bucketName:    bucketName,
					compartmentID: *bucket.CompartmentId,
					par:           resp.Items[j],
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

// PreauthenticatedRequest wraps one objectstorage.PreauthenticatedRequestSummary, plus the
// namespace/bucket name/compartmentID inherited from its owning Bucket.
type PreauthenticatedRequest struct {
	client        objectStorageClient
	namespace     string
	bucketName    string
	compartmentID string
	par           objectstorage.PreauthenticatedRequestSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. Returns the compartment ID inherited from the owning Bucket at list time.
func (r *PreauthenticatedRequest) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID.
// objectstorage.PreauthenticatedRequestSummary HAS a real Id field (verified this session), one
// of only two types in this plan (alongside ReplicationPolicy) with a natural OCID-shaped
// identity, so no composite key is needed here.
func (r *PreauthenticatedRequest) UniqueKey() string { return *r.par.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. PreauthenticatedRequestSummary has no
// LifecycleState field at all (verified this session) -- "gone" is entirely "absent from
// ListPreauthenticatedRequests."
func (r *PreauthenticatedRequest) Filter() error { return nil }

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// freeformTags/definedTags are passed nil -- PreauthenticatedRequestSummary carries neither
// field.
func (r *PreauthenticatedRequest) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.par
	return nil, nil, x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *PreauthenticatedRequest) Remove(ctx context.Context) error {
	x := r.par
	_, err := r.client.DeletePreauthenticatedRequest(ctx, objectstorage.DeletePreauthenticatedRequestRequest{
		NamespaceName: &r.namespace,
		BucketName:    &r.bucketName,
		ParId:         x.Id,
	})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties --
// PreauthenticatedRequestSummary has no CompartmentId/LifecycleState field of its own.
func (r *PreauthenticatedRequest) Properties() types.Properties {
	x := r.par
	return types.NewProperties().
		Set(propID, x.Id).
		Set(propName, x.Name).
		Set(propBucket, r.bucketName).
		Set(propNamespace, r.namespace).
		Set("access_type", string(x.AccessType))
}
