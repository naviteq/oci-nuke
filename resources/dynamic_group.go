// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. DynamicGroup is hand-written for the same reason Policy
// is (Global geography, no generator --geography flag) plus a second, structural reason: a
// dynamic group's own CompartmentId is ALWAYS the tenancy root OCID (OCI only permits creating
// dynamic groups at the tenancy root) -- see GetCompartmentID's doc comment below for why this is
// registered honestly and then dropped by the existing, unmodified scopedLister fail-closed check
// rather than special-cased here (05-CONTEXT.md).
//
// That drop is still the mechanism, but it runs on a LISTED resource, so it never stopped the
// list call that cannot succeed in the first place: every non-root compartment answered 404 and
// the run logged one error line per compartment (NR-775). dynamicGroupLister.List now skips the
// call for any compartment that is not the tenancy root -- ocinuke.ListerOpts.BeforeListTenancyRoot,
// whose doc comment carries the reasoning, including why skipping the call is safer than
// suppressing its status code.
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

// dynamicGroupClient is the narrow slice of identity's client this lister needs -- a
// hand-written interface so DynamicGroup is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type dynamicGroupClient interface {
	ListDynamicGroups(ctx context.Context, req identity.ListDynamicGroupsRequest) (identity.ListDynamicGroupsResponse, error)
	DeleteDynamicGroup(ctx context.Context, req identity.DeleteDynamicGroupRequest) (identity.DeleteDynamicGroupResponse, error)
}

// DynamicGroupResourceType is the registry.Registration.Name for DynamicGroup.
const DynamicGroupResourceType = "DynamicGroup"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     DynamicGroupResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &DynamicGroup{},
		Lister:   &dynamicGroupLister{},
		// DependsOn is intentionally empty -- DynamicGroup is a leaf; nothing else in this wave
		// depends on a dynamic group still existing.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type dynamicGroupLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// dynamicGroupList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Identity client. BeforeList(ocinuke.Global) is the FIRST statement after
// the opts type-assertion -- before the client is ever constructed -- since DynamicGroup is
// home-region-only, same as every other type in this file's plan.
func (l *dynamicGroupLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("dynamicGroupLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}
	// Second guard, and the fix for NR-775: OCI only permits a dynamic group at the tenancy
	// root, and answers ListDynamicGroups against any other compartment with 404
	// NotAuthorizedOrNotFound rather than an empty list. Skipping the call outright -- rather
	// than making it and suppressing its status code -- is what keeps a genuine permission
	// failure visible; see BeforeListTenancyRoot's own doc comment for the full argument.
	if err := o.BeforeListTenancyRoot(); err != nil {
		return nil, err
	}

	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing IdentityClient for %s: %w", o.Region, err)
	}

	return dynamicGroupList(ctx, client, o.CompartmentID)
}

// dynamicGroupList paginates identity.ListDynamicGroups and wraps every returned item as a
// DynamicGroup. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func dynamicGroupList(
	ctx context.Context,
	client dynamicGroupClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := identity.ListDynamicGroupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDynamicGroups(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing DynamicGroup in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &DynamicGroup{client: client, dynamicGroup: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// DynamicGroup wraps one identity.DynamicGroup.
type DynamicGroup struct {
	client       dynamicGroupClient
	dynamicGroup identity.DynamicGroup
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. identity.DynamicGroup.CompartmentId is mandatory:"true" (verified this
// session) -- no nil-check needed. This is deliberately NOT the compartment the run was asked to
// search: a real dynamic group's CompartmentId is ALWAYS the tenancy root OCID (OCI only permits
// creating dynamic groups at the tenancy root), and the resolved in-scope compartment set built
// by Phase 2's scope resolver never contains the tenancy root itself (SAFE-06 refuses it as a
// target outright). Returning that value here -- rather than substituting or special-casing
// anything -- is what naturally makes pkg/ocinuke/scoped_lister.go's existing, unmodified
// fail-closed "compartment not in resolved scope" check drop every DynamicGroup instance
// automatically, reported as a ReasonOutOfScope SkipEvent (05-CONTEXT.md). See
// resources/export.go's GetCompartmentID for the closest structural analog: a value returned here
// is not derived from a "the type's own compartment" concept the way most types' GetCompartmentID
// is, it is simply whatever the wrapped SDK struct's own CompartmentId field says -- which, for
// this one type, is never in the resolved scope.
//
// Since ADR-0003 that drop is the DEFAULT rather than the only outcome: an operator who names
// DynamicGroup in the config's tenancy-root-types key admits it at this run's verified tenancy
// OCID, and only after the resolved set has already refused it. Nothing here changes either way --
// the allowance lives in ocinuke.TenancyRootAllowance and is read by scopedLister, which is
// precisely why this accessor must go on returning the honest value.
func (r *DynamicGroup) GetCompartmentID() string { return *r.dynamicGroup.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *DynamicGroup) UniqueKey() string { return *r.dynamicGroup.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment. Present = ACTIVE only; CREATING, INACTIVE,
// DELETING, and any future SDK value this switch does not recognize are all excluded (fail-safe
// exclusion).
func (r *DynamicGroup) Filter() error {
	switch r.dynamicGroup.LifecycleState {
	case identity.DynamicGroupLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("DynamicGroup is %s, not available", r.dynamicGroup.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time -- though for
// DynamicGroup this is moot in production, since the compartment-scope drop above (not
// protect-by-tag/min-age) is what actually removes every real instance before SafetyTags is ever
// consulted for a queue decision.
func (r *DynamicGroup) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.dynamicGroup
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call. Unreachable unless the operator names DynamicGroup in
// tenancy-root-types (ADR-0003) -- the tenancy-scope opt-in this comment used to describe as
// deferred, which turned out to need no change here, exactly as predicted. Remove() and the
// scope-drop remain two separately testable concerns (05-05-PLAN.md).
func (r *DynamicGroup) Remove(ctx context.Context) error {
	x := r.dynamicGroup
	_, err := r.client.DeleteDynamicGroup(ctx, identity.DeleteDynamicGroupRequest{DynamicGroupId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper.
func (r *DynamicGroup) Properties() types.Properties {
	x := r.dynamicGroup
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
