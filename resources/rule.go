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
	"github.com/oracle/oci-go-sdk/v65/events"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// ruleClient is the narrow slice of events.EventsClient this lister needs -- a hand-written
// interface so Rule is stub-testable with zero network access. Every resource type gets its OWN
// narrow interface, scoped to exactly the calls it makes.
type ruleClient interface {
	ListRules(ctx context.Context, req events.ListRulesRequest) (events.ListRulesResponse, error)
	DeleteRule(ctx context.Context, req events.DeleteRuleRequest) (events.DeleteRuleResponse, error)
}

// RuleResourceType is the registry.Registration.Name for Rule.
const RuleResourceType = "Rule"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     RuleResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Rule{},
		Lister:   &ruleLister{},
		// DependsOn is intentionally empty -- 05-CONTEXT.md locks "Events Rule stands alone with
		// no edges."
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type ruleLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in ruleList, kept
// separate so it is unit-testable against a stub client without ever constructing a real Events
// client.
func (l *ruleLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("ruleLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Events(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Events client for %s: %w", o.Region, err)
	}

	return ruleList(ctx, client, o.CompartmentID)
}

// ruleList paginates events.ListRules and wraps every returned item as a Rule. Isolated from
// ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below exercises
// against a stub, with zero network access.
func ruleList(
	ctx context.Context,
	client ruleClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := events.ListRulesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListRules(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Rule in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Rule{client: client, rule: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Rule wraps one events.RuleSummary.
type Rule struct {
	client ruleClient
	rule   events.RuleSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Rule) GetCompartmentID() string { return *r.rule.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Rule) UniqueKey() string { return *r.rule.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// events.RuleSummary.LifecycleState is typed events.RuleLifecycleStateEnum directly (verified
// this session, events/rule_summary.go and events/rule.go) -- there is no "Summary"-prefixed
// lifecycle-enum alias for this type at all, unlike Stream/StreamPool/Subscription/
// NotificationTopic above. RuleLifecycleStateEnum has SEVEN values: CREATING, ACTIVE, INACTIVE,
// UPDATING, DELETING, DELETED, FAILED. Present = Creating, Active, Updating; excluded = Inactive,
// Deleting, Deleted, Failed. Any state not explicitly listed here excludes, so a future SDK
// release adding a new lifecycle-state value fails safe.
func (r *Rule) Filter() error {
	switch r.rule.LifecycleState {
	case events.RuleLifecycleStateCreating,
		events.RuleLifecycleStateActive,
		events.RuleLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Rule is %s, not available", r.rule.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix). Every identity/time field on RuleSummary is mandatory:"true" (verified this
// session), so a direct dereference is safe here.
func (r *Rule) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.rule
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Rule) Remove(ctx context.Context) error {
	x := r.rule
	_, err := r.client.DeleteRule(ctx, events.DeleteRuleRequest{RuleId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Rule) Properties() types.Properties {
	x := r.rule
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
