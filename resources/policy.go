// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. Policy is hand-written rather than scaffolded by
// cmd/gen-resource: the generator hardcodes ocinuke.Regional in its template with no
// --geography flag (05-RESEARCH.md Pattern 4), and Policy is this wave's first Global-geography
// type -- home-region-only, per 05-CONTEXT.md's IAM decision.
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

// policyClient is the narrow slice of identity's client this lister needs -- a hand-written
// interface so Policy is stub-testable with zero network access. Every resource type gets its
// OWN narrow interface, scoped to exactly the calls it makes.
type policyClient interface {
	ListPolicies(ctx context.Context, req identity.ListPoliciesRequest) (identity.ListPoliciesResponse, error)
	DeletePolicy(ctx context.Context, req identity.DeletePolicyRequest) (identity.DeletePolicyResponse, error)
}

// PolicyResourceType is the registry.Registration.Name for Policy.
const PolicyResourceType = "Policy"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     PolicyResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Policy{},
		Lister:   &policyLister{},
		// DependsOn is intentionally empty -- Policy is a leaf; nothing else in this wave depends
		// on a policy still existing.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type policyLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in policyList, kept
// separate so it is unit-testable against a stub client without ever constructing a real Identity
// client. BeforeList(ocinuke.Global) is the FIRST statement after the opts type-assertion --
// before the client is ever constructed -- since Policy is home-region-only (05-CONTEXT.md: IAM
// is Global, never Regional): a scanner instance running against a non-home region must skip this
// lister entirely rather than issue a redundant (or, worse, silently empty) API call.
func (l *policyLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("policyLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}

	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing IdentityClient for %s: %w", o.Region, err)
	}

	return policyList(ctx, client, o.CompartmentID)
}

// policyList paginates identity.ListPolicies and wraps every returned item as a Policy. Isolated
// from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test below
// exercises against a stub, with zero network access.
func policyList(
	ctx context.Context,
	client policyClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := identity.ListPoliciesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListPolicies(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Policy in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Policy{client: client, policy: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Policy wraps one identity.Policy.
type Policy struct {
	client policyClient
	policy identity.Policy
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// identity.Policy.CompartmentId is mandatory:"true" (verified this session) -- no nil-check
// needed, unlike this wave's TagNamespace.
func (r *Policy) GetCompartmentID() string { return *r.policy.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Policy) UniqueKey() string { return *r.policy.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment. Present = ACTIVE only; CREATING, INACTIVE,
// DELETING, and any future SDK value this switch does not recognize are all excluded (fail-safe
// exclusion) -- an INACTIVE policy is not attempted here since it is not the common, expected
// path and this wave's convention is to fail closed on anything other than the one
// unambiguously-present state.
func (r *Policy) Filter() error {
	switch r.policy.LifecycleState {
	case identity.PolicyLifecycleStateActive:
		return nil
	default:
		return fmt.Errorf("Policy is %s, not available", r.policy.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time, before this resource
// can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's SafetyEvaluated doc comment
// for why protection moved here from Remove() (04-13 plan/apply divergence fix).
func (r *Policy) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.policy
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Policy) Remove(ctx context.Context) error {
	x := r.policy
	_, err := r.client.DeletePolicy(ctx, identity.DeletePolicyRequest{PolicyId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper. Statements (free-text IAM grant text) is deliberately NOT
// included -- 05-RESEARCH.md's Security Domain note: Policy.Statements should not be logged or
// exposed casually outside a dedicated, later-justified filter surface, which this wave does not
// need.
func (r *Policy) Properties() types.Properties {
	x := r.policy
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
