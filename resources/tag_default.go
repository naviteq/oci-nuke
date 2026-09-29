// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. TagDefault is hand-written for the same Global-
// geography reason Policy/DynamicGroup/TagNamespace are; TagNamespace declares
// DependsOn: ["TagDefault"] so tag defaults are scanned and removed before the namespace that
// owns them (05-CONTEXT.md).
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// tagDefaultClient is the narrow slice of identity's client this lister needs -- a hand-written
// interface so TagDefault is stub-testable with zero network access. Every resource type gets
// its OWN narrow interface, scoped to exactly the calls it makes.
type tagDefaultClient interface {
	ListTagDefaults(ctx context.Context, req identity.ListTagDefaultsRequest) (identity.ListTagDefaultsResponse, error)
	DeleteTagDefault(ctx context.Context, req identity.DeleteTagDefaultRequest) (identity.DeleteTagDefaultResponse, error)
}

// TagDefaultResourceType is the registry.Registration.Name for TagDefault.
const TagDefaultResourceType = "TagDefault"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     TagDefaultResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &TagDefault{},
		Lister:   &tagDefaultLister{},
		// DependsOn is intentionally empty -- TagNamespace declares the edge on ITS OWN
		// registration (resources/tag_namespace.go), per the "DependsOn is always declared by
		// the type that has the dependency" convention (04-RESEARCH.md Q1).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type tagDefaultLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// tagDefaultList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Identity client. BeforeList(ocinuke.Global) is the FIRST statement after
// the opts type-assertion -- before the client is ever constructed -- since TagDefault is
// home-region-only, same as every other type in this file's plan.
func (l *tagDefaultLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("tagDefaultLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}

	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing IdentityClient for %s: %w", o.Region, err)
	}

	return tagDefaultList(ctx, client, o.CompartmentID)
}

// tagDefaultList paginates identity.ListTagDefaults and wraps every returned item as a
// TagDefault. CompartmentId is set on the request even though ListTagDefaultsRequest.CompartmentId
// is mandatory:"false" -- verified this session -- to scope the list to the run's target subtree
// rather than the whole tenancy. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose --
// this is what the list test below exercises against a stub, with zero network access.
func tagDefaultList(
	ctx context.Context,
	client tagDefaultClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := identity.ListTagDefaultsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListTagDefaults(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing TagDefault in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &TagDefault{client: client, td: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// TagDefault wraps one identity.TagDefaultSummary.
type TagDefault struct {
	client tagDefaultClient
	td     identity.TagDefaultSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. identity.TagDefaultSummary.CompartmentId is mandatory:"true" (verified this
// session) -- no nil-check needed.
func (r *TagDefault) GetCompartmentID() string { return *r.td.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *TagDefault) UniqueKey() string { return *r.td.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment. Unlike every other type in this file's plan,
// empty/unset LifecycleState is treated as PRESENT here, not excluded:
// identity.TagDefaultSummaryLifecycleStateEnum is mandatory:"false" on TagDefaultSummary and its
// enum block only ever declares ACTIVE (verified this session,
// oci-go-sdk/v65/identity/tag_default_summary.go -- there is no CREATING/DELETING/DELETED value
// to even switch on). A TagDefault with no populated lifecycle state is not necessarily "gone" --
// only an EXPLICIT non-ACTIVE, non-empty value would exclude, and none exists in this SDK
// version, so the only way this switch's default branch is ever reached is a genuinely
// unrecognized future value, which correctly fails closed.
func (r *TagDefault) Filter() error {
	switch r.td.LifecycleState {
	case "", identity.TagDefaultSummaryLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("TagDefault is %s, not available", r.td.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). freeformTags/definedTags are passed nil -- TagDefaultSummary
// carries neither field (verified this session), mirroring resources/retention_rule.go's own
// no-tag-fields precedent.
func (r *TagDefault) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, r.td.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *TagDefault) Remove(ctx context.Context) error {
	x := r.td
	_, err := r.client.DeleteTagDefault(ctx, identity.DeleteTagDefaultRequest{TagDefaultId: x.Id})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties --
// TagDefaultSummary has no DisplayName field (TagDefinitionName is the closest analog, used as
// the "name" property here) and no tag fields of its own.
func (r *TagDefault) Properties() types.Properties {
	x := r.td
	props := types.NewProperties().
		Set(propID, x.Id).
		Set(propCompartmentID, x.CompartmentId).
		Set(propName, x.TagDefinitionName).
		Set(propLifecycleState, string(x.LifecycleState)).
		Set("tag_namespace_id", x.TagNamespaceId).
		Set("value", x.Value)
	if x.TimeCreated != nil {
		props.Set(propTimeCreated, x.TimeCreated.Time)
	}
	return props
}
