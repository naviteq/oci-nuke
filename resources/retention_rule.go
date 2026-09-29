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
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// RetentionRuleResourceType is the registry.Registration.Name for RetentionRule.
const RetentionRuleResourceType = "RetentionRule"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     RetentionRuleResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &RetentionRule{},
		Lister:   &retentionRuleLister{},
		// DependsOn is intentionally empty -- RetentionRule is a leaf; Bucket declares the edge.
		// An UNLOCKED retention rule blocks DeleteBucket until removed; a LOCKED one is excluded
		// pre-emptively by Filter() below and never attempted at all (T-04-25).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type retentionRuleLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// retentionRuleList, kept separate so it is unit-testable against a stub client without ever
// constructing a real ObjectStorage client.
func (l *retentionRuleLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("retentionRuleLister.List: unexpected opts type %T", opts)
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

	return retentionRuleList(ctx, client, *nsResp.Value, o.CompartmentID)
}

// retentionRuleList enumerates every bucket in the compartment (via listBucketsInCompartment,
// resources/object_storage_support.go), then paginates objectstorage.ListRetentionRules per
// bucket.
func retentionRuleList(
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
		req := objectstorage.ListRetentionRulesRequest{NamespaceName: &namespace, BucketName: &bucketName}
		for {
			resp, err := client.ListRetentionRules(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing RetentionRule for bucket %s/%s: %w", namespace, bucketName, err)
			}
			for j := range resp.Items {
				out = append(out, &RetentionRule{
					client:        client,
					namespace:     namespace,
					bucketName:    bucketName,
					compartmentID: *bucket.CompartmentId,
					rule:          resp.Items[j],
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

// RetentionRule wraps one objectstorage.RetentionRuleSummary, plus the namespace/bucket name/
// compartmentID inherited from its owning Bucket.
type RetentionRule struct {
	client        objectStorageClient
	namespace     string
	bucketName    string
	compartmentID string
	rule          objectstorage.RetentionRuleSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. Returns the compartment ID inherited from the owning Bucket at list time.
func (r *RetentionRule) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID.
func (r *RetentionRule) UniqueKey() string { return *r.rule.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/nat_gateway.go's Filter() doc comment. Unlike every other type in this plan,
// RetentionRule's exclusion is NOT lifecycle-driven -- it is the one genuinely permanent
// constraint this domain has (04-RESEARCH.md Q4, [CITED]
// docs.oracle.com/en-us/iaas/Content/Object/Tasks/usingretentionrules.htm): "You cannot delete a
// time-bound retention rule that is locked... the rule can only be deleted by deleting the
// bucket... Locking a retention rule is an irreversible operation." A mandatory 14-day delay
// exists before TimeRuleLocked takes effect, during which the rule is still removable --
// TimeRuleLocked nil or in the future means still-removable (falls through to the nil return
// below); TimeRuleLocked in the past means permanently locked. A locked rule is excluded HERE,
// pre-emptively, via ReportLeftover(ReasonRetentionLocked) -- never attempted against
// DeleteRetentionRule and left to fail into a generic api-error (T-04-25).
func (r *RetentionRule) Filter() error {
	x := r.rule
	if x.TimeRuleLocked != nil && x.TimeRuleLocked.Before(time.Now()) {
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonRetentionLocked,
			ResourceType:  RetentionRuleResourceType,
			ResourceID:    *x.Id,
			CompartmentID: r.compartmentID,
			Detail:        "locked retention rule, removable only by deleting the bucket",
		})
		return fmt.Errorf("retention rule %s is locked, removable only by deleting the bucket", *x.Id)
	}
	return nil
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// freeformTags/definedTags are passed nil -- RetentionRuleSummary carries neither field.
func (r *RetentionRule) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.rule
	return nil, nil, x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// Reachable only for an unlocked, or not-yet-locked, rule -- Filter() above already excluded
// the locked case at both scan time and HandleWait's wait-time re-check, so
// DeleteRetentionRule is never attempted against a rule that would 409.
func (r *RetentionRule) Remove(ctx context.Context) error {
	x := r.rule
	_, err := r.client.DeleteRetentionRule(ctx, objectstorage.DeleteRetentionRuleRequest{
		NamespaceName:   &r.namespace,
		BucketName:      &r.bucketName,
		RetentionRuleId: x.Id,
	})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties --
// RetentionRuleSummary has no CompartmentId/LifecycleState field of its own. time_rule_locked is
// set only when the rule actually has one (nil TimeRuleLocked means never locked).
func (r *RetentionRule) Properties() types.Properties {
	x := r.rule
	props := types.NewProperties().
		Set(propID, x.Id).
		Set(propName, x.DisplayName).
		Set(propBucket, r.bucketName).
		Set(propNamespace, r.namespace)
	if x.TimeRuleLocked != nil {
		props.Set("time_rule_locked", x.TimeRuleLocked.Time)
	}
	return props
}
