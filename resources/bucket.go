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
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// BucketResourceType is the registry.Registration.Name for Bucket.
const BucketResourceType = "Bucket"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     BucketResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Bucket{},
		Lister:   &bucketLister{},
		// DependsOn names every dependent type this plan registers -- a bucket cannot be deleted
		// while any current/superseded object version, uncommitted multipart upload,
		// pre-authenticated request, unlocked retention rule, or replication policy still exists
		// (04-RESEARCH.md Q4). Bare string literals -- Bucket does not import the other five
		// types' ResourceType constants, matching the cross-plan-boundary convention Vcn already
		// established (this is within-plan, but the same five sibling files are large enough that
		// bare strings keep this init() block self-contained and readable).
		DependsOn: []string{
			"ObjectVersion", "MultipartUpload", "PreauthenticatedRequest", "RetentionRule", "ReplicationPolicy",
		},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type bucketLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in bucketList, kept
// separate so it is unit-testable against a stub client without ever constructing a real
// ObjectStorage client.
func (l *bucketLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("bucketLister.List: unexpected opts type %T", opts)
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

	return bucketList(ctx, client, *nsResp.Value, o.CompartmentID)
}

// bucketList calls listBucketsInCompartment (resources/object_storage_support.go) and wraps every
// returned objectstorage.BucketSummary as a Bucket. Isolated from ocinuke.ListerOpts/
// pkg/clients.Cache on purpose -- this is what the list test below exercises against a stub, with
// zero network access.
func bucketList(
	ctx context.Context,
	client objectStorageClient,
	namespace, compartmentID string,
) ([]resource.Resource, error) {
	buckets, err := listBucketsInCompartment(ctx, client, namespace, compartmentID)
	if err != nil {
		return nil, err
	}

	out := make([]resource.Resource, 0, len(buckets))
	for i := range buckets {
		b := buckets[i]
		out = append(out, &Bucket{
			client:        client,
			namespace:     namespace,
			name:          *b.Name,
			compartmentID: *b.CompartmentId,
			freeformTags:  b.FreeformTags,
			definedTags:   b.DefinedTags,
			timeCreated:   b.TimeCreated,
		})
	}
	return out, nil
}

// Bucket wraps one objectstorage.BucketSummary (ListBuckets's own return shape -- distinct from
// the full objectstorage.Bucket struct GetBucket would return, and NOT fetched here since nothing
// this type needs requires the extra GetBucket round trip).
type Bucket struct {
	client        objectStorageClient
	namespace     string
	name          string
	compartmentID string
	freeformTags  map[string]string
	definedTags   map[string]map[string]interface{}
	timeCreated   *common.SDKTime
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Bucket) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09). BucketSummary has NO Id field at all
// (verified this session, objectstorage/bucket_summary.go -- zero matches for an Id field), so
// this is a second, explicit SAFE-09 exception in this wave (alongside ObjectVersion/RetentionRule's
// own no-natural-OCID composite keys): namespace+"/"+name, safe because bucket names are unique
// per namespace and immutable for the lifetime of the bucket.
func (r *Bucket) UniqueKey() string { return r.namespace + "/" + r.name }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Bucket has NO LifecycleState field at all
// (verified this session, grepped objectstorage/bucket.go and bucket_summary.go, zero matches),
// mirroring InstanceConfiguration/PrivateIp's already-established no-lifecycle pattern (Plans
// 04-04/04-07) -- "gone" is entirely "absent from ListBuckets," so there is nothing to switch on
// here.
func (r *Bucket) Filter() error { return nil }

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *Bucket) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return r.freeformTags, flattenDefinedTags(r.definedTags), timeCreatedOrZero(r.timeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// By the time this call fires, WaitOnDependencies has already ensured every dependent type
// this Bucket's own DependsOn names has finished or permanently failed (04-RESEARCH.md Q3) --
// including, per T-04-27, ReplicationPolicy, so a replication policy is never left dangling
// after DeleteBucket is attempted.
func (r *Bucket) Remove(ctx context.Context) error {
	_, err := r.client.DeleteBucket(ctx, objectstorage.DeleteBucketRequest{
		NamespaceName: &r.namespace,
		BucketName:    &r.name,
	})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties -- Bucket has
// no Id (baseProperties' first parameter) and no LifecycleState, so the shared helper's shape does
// not fit. propNamespace (resources/support.go) is reused here rather than a bare "namespace"
// string literal since ObjectVersion/MultipartUpload/PreauthenticatedRequest/RetentionRule/
// ReplicationPolicy all set the same property key -- goconst's 3-occurrence threshold requires it.
func (r *Bucket) Properties() types.Properties {
	return types.NewProperties().
		Set(propName, r.name).
		Set(propCompartmentID, r.compartmentID).
		Set(propNamespace, r.namespace)
}
