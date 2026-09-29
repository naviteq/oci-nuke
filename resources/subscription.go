// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. Subscription was scaffolded by cmd/gen-resource; adjust
// freely -- this file is not regenerated automatically, re-running the generator only
// overwrites it if you choose to.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/ons"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// subscriptionClient is the narrow slice of ons.NotificationDataPlaneClient this lister needs --
// a hand-written interface so Subscription is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type subscriptionClient interface {
	ListSubscriptions(ctx context.Context, req ons.ListSubscriptionsRequest) (ons.ListSubscriptionsResponse, error)
	DeleteSubscription(ctx context.Context, req ons.DeleteSubscriptionRequest) (ons.DeleteSubscriptionResponse, error)
}

// SubscriptionResourceType is the registry.Registration.Name for Subscription.
const SubscriptionResourceType = "Subscription"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     SubscriptionResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Subscription{},
		Lister:   &subscriptionLister{},
		// DependsOn is intentionally empty on Subscription's own registration -- NotificationTopic
		// (resources/notification_topic.go) declares "NotificationTopic DependsOn:
		// [\"Subscription\"]" on ITS OWN registration (05-CONTEXT.md: "Topic DependsOn:
		// [\"Subscription\"]"), per the "DependsOn declared by the dependent" convention
		// (04-RESEARCH.md Q1) -- never here.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type subscriptionLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// subscriptionList, kept separate so it is unit-testable against a stub client without ever
// constructing a real NotificationDataPlane client.
func (l *subscriptionLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("subscriptionLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.NotificationDataPlane(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing NotificationDataPlane client for %s: %w", o.Region, err)
	}

	return subscriptionList(ctx, client, o.CompartmentID)
}

// subscriptionList paginates ons.ListSubscriptions and wraps every returned item as a
// Subscription. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func subscriptionList(
	ctx context.Context,
	client subscriptionClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := ons.ListSubscriptionsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListSubscriptions(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Subscription in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Subscription{client: client, subscription: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Subscription wraps one ons.SubscriptionSummary.
type Subscription struct {
	client       subscriptionClient
	subscription ons.SubscriptionSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Subscription) GetCompartmentID() string { return *r.subscription.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Subscription) UniqueKey() string { return *r.subscription.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// ons.SubscriptionSummaryLifecycleStateEnum (verified this session, ons/subscription_summary.go)
// has exactly THREE values: PENDING, ACTIVE, DELETED -- no DELETING intermediate state exists for
// Subscription, unlike every other lifecycle-bearing type in this wave. Present = Pending,
// Active; excluded = Deleted. Any state not explicitly listed here excludes, so a future SDK
// release adding a new lifecycle-state value fails safe.
func (r *Subscription) Filter() error {
	switch r.subscription.LifecycleState {
	case ons.SubscriptionSummaryLifecycleStatePending,
		ons.SubscriptionSummaryLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("Subscription is %s, not available", r.subscription.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// ons.SubscriptionSummary has NO common.SDKTime TimeCreated field at all (verified this session,
// ons/subscription_summary.go) -- its only creation-time field is CreatedTime *int64
// mandatory:"false" (Unix epoch milliseconds), a genuinely different shape from every other type
// in this wave. subscriptionCreatedAt below converts it, or returns the zero time.Time if nil --
// the same "never falsely protects" zero-value guarantee resources/support.go's
// timeCreatedOrZero already establishes for the nilable-pointer case, extended here to a field
// that isn't even the same type.
func (r *Subscription) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.subscription
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), subscriptionCreatedAt(x.CreatedTime)
}

// subscriptionCreatedAt converts ons.SubscriptionSummary.CreatedTime (Unix epoch milliseconds,
// mandatory:"false") to a time.Time, returning the zero time.Time if createdTime is nil.
func subscriptionCreatedAt(createdTime *int64) time.Time {
	if createdTime == nil {
		return time.Time{}
	}
	return time.UnixMilli(*createdTime)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Subscription) Remove(ctx context.Context) error {
	x := r.subscription
	_, err := r.client.DeleteSubscription(ctx, ons.DeleteSubscriptionRequest{SubscriptionId: x.Id})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties -- Subscription
// has no DisplayName/Name field and no common.SDKTime TimeCreated field (see SafetyTags above), so
// the shared helper's shape does not fit.
func (r *Subscription) Properties() types.Properties {
	x := r.subscription
	return types.NewProperties().
		Set(propID, x.Id).
		Set(propCompartmentID, x.CompartmentId).
		Set(propLifecycleState, string(x.LifecycleState)).
		Set("topic_id", x.TopicId).
		Set("protocol", x.Protocol)
}
