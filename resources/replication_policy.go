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

// ReplicationPolicyResourceType is the registry.Registration.Name for ReplicationPolicy.
const ReplicationPolicyResourceType = "ReplicationPolicy"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ReplicationPolicyResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &ReplicationPolicy{},
		Lister:   &replicationPolicyLister{},
		// DependsOn is intentionally empty -- ReplicationPolicy is a leaf; Bucket declares the
		// edge (orchestrator decision D-2: registered as its own type with its own UniqueKey/
		// Filter/ReportLeftover, not folded into Bucket.Remove(), since ReplicationPolicySummary
		// has its own real Id -- unlike BackendSet/Certificate, Plan 04-08). A replication policy
		// on the source bucket must be removed before DeleteBucket is ever attempted against that
		// bucket, per T-04-27.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type replicationPolicyLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// replicationPolicyList, kept separate so it is unit-testable against a stub client without ever
// constructing a real ObjectStorage client.
func (l *replicationPolicyLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("replicationPolicyLister.List: unexpected opts type %T", opts)
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

	return replicationPolicyList(ctx, client, *nsResp.Value, o.CompartmentID)
}

// replicationPolicyList enumerates every bucket in the compartment (via
// listBucketsInCompartment, resources/object_storage_support.go), then paginates
// objectstorage.ListReplicationPolicies per bucket.
func replicationPolicyList(
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
		req := objectstorage.ListReplicationPoliciesRequest{NamespaceName: &namespace, BucketName: &bucketName}
		for {
			resp, err := client.ListReplicationPolicies(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing ReplicationPolicy for bucket %s/%s: %w", namespace, bucketName, err)
			}
			for j := range resp.Items {
				out = append(out, &ReplicationPolicy{
					client:        client,
					namespace:     namespace,
					bucketName:    bucketName,
					compartmentID: *bucket.CompartmentId,
					policy:        resp.Items[j],
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

// ReplicationPolicy wraps one objectstorage.ReplicationPolicySummary, plus the namespace/bucket
// name/compartmentID inherited from its owning (source) Bucket.
type ReplicationPolicy struct {
	client        objectStorageClient
	namespace     string
	bucketName    string
	compartmentID string
	policy        objectstorage.ReplicationPolicySummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. Returns the compartment ID inherited from the owning Bucket at list time.
func (r *ReplicationPolicy) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID.
// objectstorage.ReplicationPolicySummary HAS a real Id field (verified this session), one of only
// two types in this plan (alongside PreauthenticatedRequest) with a natural OCID-shaped identity.
func (r *ReplicationPolicy) UniqueKey() string { return *r.policy.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Unlike every lifecycle-driven Filter()
// elsewhere in this wave, ReplicationPolicySummary.Status is ACTIVE/CLIENT_ERROR (verified this
// session, NOT a lifecycle-state enum in the usual sense -- there is no "going" or "gone"
// transitional state for this type at all). Both statuses mean "present, attempt removal" --
// delete-and-see is the only signal OCI's API surface gives here, so Filter() returns nil
// unconditionally. This deliberately differs from every state-driven Filter() elsewhere in this
// wave; documented here rather than silently mirrored from an unrelated type.
func (r *ReplicationPolicy) Filter() error { return nil }

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// freeformTags/definedTags are passed nil -- ReplicationPolicySummary carries neither field.
func (r *ReplicationPolicy) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.policy
	return nil, nil, x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// This fires before Bucket.Remove() is ever attempted, per Bucket.DependsOn's ordering
// (T-04-27). If DeleteReplicationPolicy itself fails (e.g. a destination-side block, per
// orchestrator decision D-2's "do not guess about the destination side"), the item surfaces
// as a normal ItemStateFailed -> ReasonAPIError leftover through the existing ClassifyLeftover
// path -- no special-casing needed, this project's own default state-derivable behavior for
// any failed Remove().
func (r *ReplicationPolicy) Remove(ctx context.Context) error {
	x := r.policy
	_, err := r.client.DeleteReplicationPolicy(ctx, objectstorage.DeleteReplicationPolicyRequest{
		NamespaceName: &r.namespace,
		BucketName:    &r.bucketName,
		ReplicationId: x.Id,
	})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties --
// ReplicationPolicySummary has no CompartmentId/LifecycleState field of its own.
func (r *ReplicationPolicy) Properties() types.Properties {
	x := r.policy
	return types.NewProperties().
		Set(propID, x.Id).
		Set(propName, x.Name).
		Set(propBucket, r.bucketName).
		Set(propNamespace, r.namespace).
		Set("status", string(x.Status)).
		Set("destination_bucket_name", x.DestinationBucketName)
}
