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
	"github.com/oracle/oci-go-sdk/v65/ons"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// notificationTopicClient is the narrow slice of ons.NotificationControlPlaneClient this lister
// needs -- a hand-written interface so NotificationTopic is stub-testable with zero network
// access. Every resource type gets its OWN narrow interface, scoped to exactly the calls it
// makes.
type notificationTopicClient interface {
	ListTopics(ctx context.Context, req ons.ListTopicsRequest) (ons.ListTopicsResponse, error)
	DeleteTopic(ctx context.Context, req ons.DeleteTopicRequest) (ons.DeleteTopicResponse, error)
}

// NotificationTopicResourceType is the registry.Registration.Name for NotificationTopic.
const NotificationTopicResourceType = "NotificationTopic"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     NotificationTopicResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &NotificationTopic{},
		Lister:   &notificationTopicLister{},
		// DependsOn: 05-CONTEXT.md locks "Topic DependsOn: [\"Subscription\"]" -- the registered
		// type name for "Topic" in this codebase is NotificationTopic (05-PATTERNS.md's
		// Recommended Project Structure). Subscriptions are removed first, before the topic they
		// are attached to. Bare string literal -- NotificationTopic does not import
		// subscription.go's ResourceType constant, matching Vcn's cross-plan-boundary convention.
		DependsOn: []string{"Subscription"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type notificationTopicLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// notificationTopicList, kept separate so it is unit-testable against a stub client without ever
// constructing a real NotificationControlPlane client.
func (l *notificationTopicLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("notificationTopicLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.NotificationControlPlane(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing NotificationControlPlane client for %s: %w", o.Region, err)
	}

	return notificationTopicList(ctx, client, o.CompartmentID)
}

// notificationTopicList paginates ons.ListTopics and wraps every returned item as a
// NotificationTopic. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is
// what the list test below exercises against a stub, with zero network access.
func notificationTopicList(
	ctx context.Context,
	client notificationTopicClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := ons.ListTopicsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListTopics(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing NotificationTopic in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &NotificationTopic{client: client, topic: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// NotificationTopic wraps one ons.NotificationTopicSummary.
type NotificationTopic struct {
	client notificationTopicClient
	topic  ons.NotificationTopicSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// NotificationTopicSummary.CompartmentId is mandatory:"true" (verified this session,
// ons/notification_topic_summary.go), so a direct dereference is safe here, matching every other
// mandatory:"true"-field type in this wave.
func (r *NotificationTopic) GetCompartmentID() string { return *r.topic.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09). NotificationTopicSummary has NO Id
// field at all (verified this session, ons/notification_topic_summary.go) -- its identity field
// is TopicId, mandatory:"true". This is a second, explicit SAFE-09 exception in this wave
// (alongside Bucket's namespace+name composite key), structurally closer to
// resources/volume_attachment.go's non-Id-named-getter shape than to Bucket's
// no-identity-field-at-all shape, since NotificationTopicSummary DOES have a single unique
// identity field, it is just not named Id.
func (r *NotificationTopic) UniqueKey() string { return *r.topic.TopicId }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// The lifecycle enum this field is typed against (verified this session,
// ons/notification_topic_summary.go) has exactly THREE values: ACTIVE, DELETING, CREATING -- no
// DELETED value exists at all, unlike every other lifecycle-bearing type in this wave. "Gone" for
// a NotificationTopic means "absent from a subsequent ListTopics" entirely, mirroring Bucket's
// no-terminal-lifecycle-state pattern -- do not go hunting for a Deleted constant, it does not
// exist on this enum. Present = Active; excluded = Creating, Deleting. Any state not explicitly
// listed here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
func (r *NotificationTopic) Filter() error {
	switch r.topic.LifecycleState {
	case ons.NotificationTopicSummaryLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("NotificationTopic is %s, not available", r.topic.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix). TimeCreated is mandatory:"true", so a direct dereference is safe here.
func (r *NotificationTopic) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.topic
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// By the time this call fires, WaitOnDependencies has already ensured every Subscription this
// topic's own DependsOn names has finished or permanently failed (04-RESEARCH.md Q3), so a
// Subscription is never left dangling after DeleteTopic is attempted.
func (r *NotificationTopic) Remove(ctx context.Context) error {
	x := r.topic
	_, err := r.client.DeleteTopic(ctx, ons.DeleteTopicRequest{TopicId: x.TopicId})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties -- baseProperties'
// first parameter is named `id` and expects an OCID-shaped identity field; NotificationTopic's
// identity field is TopicId (see UniqueKey above), so this is built directly to keep the property
// key ("id") pointed at the right underlying field without a misleading parameter name at the call
// site.
func (r *NotificationTopic) Properties() types.Properties {
	x := r.topic
	props := types.NewProperties().
		Set(propID, x.TopicId).
		Set(propCompartmentID, x.CompartmentId).
		Set(propLifecycleState, string(x.LifecycleState))

	if x.Name != nil {
		props.Set(propName, x.Name)
	}
	if x.TimeCreated != nil {
		props.Set(propTimeCreated, x.TimeCreated.Time)
	}

	for k, v := range x.FreeformTags {
		k := k
		props.SetTag(&k, v)
	}
	for ns, kv := range x.DefinedTags {
		for k, v := range kv {
			k := k
			props.SetTagWithPrefix(ns, &k, v)
		}
	}

	return props
}
