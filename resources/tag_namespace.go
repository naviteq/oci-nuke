// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. TagNamespace is hand-written for the same Global-
// geography reason Policy/DynamicGroup are, plus a genuinely two-step Remove() (retire, then
// cascade-delete) that no existing type in this codebase performs -- see Remove() below.
package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// tagNamespaceClient is the narrow slice of identity's client this lister needs -- a
// hand-written interface so TagNamespace is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type tagNamespaceClient interface {
	ListTagNamespaces(ctx context.Context, req identity.ListTagNamespacesRequest) (identity.ListTagNamespacesResponse, error)
	UpdateTagNamespace(ctx context.Context, req identity.UpdateTagNamespaceRequest) (identity.UpdateTagNamespaceResponse, error)
	CascadeDeleteTagNamespace(
		ctx context.Context, req identity.CascadeDeleteTagNamespaceRequest,
	) (identity.CascadeDeleteTagNamespaceResponse, error)
}

// TagNamespaceResourceType is the registry.Registration.Name for TagNamespace.
const TagNamespaceResourceType = "TagNamespace"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     TagNamespaceResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &TagNamespace{},
		Lister:   &tagNamespaceLister{},
		// DependsOn: TagDefault must be scanned and removed before the namespace that owns it
		// (05-CONTEXT.md). Bare string literal -- TagDefault is registered in this same plan's
		// other file; matches the cross-plan-boundary DependsOn convention every Wave-5 file
		// follows (05-PATTERNS.md, Vcn's own precedent).
		DependsOn: []string{"TagDefault"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type tagNamespaceLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// tagNamespaceList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Identity client. BeforeList(ocinuke.Global) is the FIRST statement after
// the opts type-assertion -- before the client is ever constructed -- since TagNamespace is
// home-region-only, same as every other type in this file's plan.
func (l *tagNamespaceLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("tagNamespaceLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}

	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing IdentityClient for %s: %w", o.Region, err)
	}

	return tagNamespaceList(ctx, client, o.CompartmentID)
}

// tagNamespaceList paginates identity.ListTagNamespaces and wraps every returned item as a
// TagNamespace. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func tagNamespaceList(
	ctx context.Context,
	client tagNamespaceClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := identity.ListTagNamespacesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListTagNamespaces(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing TagNamespace in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &TagNamespace{client: client, ns: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// TagNamespace wraps one identity.TagNamespaceSummary.
type TagNamespace struct {
	client tagNamespaceClient
	ns     identity.TagNamespaceSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time. identity.TagNamespaceSummary.CompartmentId is mandatory:"false" -- EVERY field
// on this struct is (verified this session, oci-go-sdk/v65/identity/tag_namespace_summary.go),
// the most extreme nil-unsafety case in this wave besides Cluster/NodePool -- nil-checked here so
// a record missing it is dropped fail-closed by scopedLister rather than panicking mid-scan.
func (r *TagNamespace) GetCompartmentID() string {
	if r.ns.CompartmentId == nil {
		return ""
	}
	return *r.ns.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// identity.TagNamespaceSummary.Id is mandatory:"false" -- nil-checked here, matching
// GetCompartmentID's fail-closed shape, rather than dereferenced directly.
func (r *TagNamespace) UniqueKey() string {
	if r.ns.Id == nil {
		return ""
	}
	return *r.ns.Id
}

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/vcn.go's Filter() doc comment for the closest "mid-transition but not yet gone"
// precedent.
//
// Deviation from 05-05-PLAN.md's literal wording (documented, Rule 1 -- verified directly against
// the pinned SDK, not assumed): the plan describes excluding "RETIRING" as a mid-transition
// state, but identity.TagNamespaceLifecycleStateEnum (oci-go-sdk/v65/identity/tag_namespace.go)
// has no RETIRING value at all -- only ACTIVE, INACTIVE, DELETING, DELETED. The SDK's own doc
// comment on TagNamespace.LifecycleState confirms the real transition: "After retiring a
// tagnamespace, make sure its lifecycleState is INACTIVE before using it" -- retiring flips
// IsRetired to true while LifecycleState becomes INACTIVE, it does not enter a distinct
// "RETIRING" lifecycle state. Present is therefore ACTIVE and INACTIVE (a namespace already
// retired by a previous run, or manually, must still be picked up and cascade-deleted); DELETING/
// DELETED are excluded (going/gone, the hang-trap defense), as is any future SDK value this
// switch does not recognize (fail-safe exclusion).
func (r *TagNamespace) Filter() error {
	switch r.ns.LifecycleState {
	case identity.TagNamespaceLifecycleStateActive, identity.TagNamespaceLifecycleStateInactive:
		return nil
	default:
		return fmt.Errorf("TagNamespace is %s, not available", r.ns.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). TimeCreated is mandatory:"false" on TagNamespaceSummary, so
// resources/support.go's timeCreatedOrZero nil-safe accessor is used here rather than a direct
// dereference.
func (r *TagNamespace) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.ns
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove performs the mandatory two-step retire-then-cascade-delete: if the namespace is not
// already retired, it is retired first via UpdateTagNamespace(IsRetired: true); THEN
// CascadeDeleteTagNamespace is called unconditionally. This NEVER calls the simpler
// DeleteTagNamespace operation -- that operation has no work-request header and is documented for
// the already-empty-namespace case only (05-RESEARCH.md Anti-Pattern); calling it here on a
// namespace that still owns tag definitions would fail outright. A future contributor tempted to
// "simplify" this into a single DeleteTagNamespace call must not: CascadeDeleteTagNamespace is the
// only operation that also removes the namespace's own tag definitions and their values from
// every resource that carries them.
//
// The async cascade-delete work request needs no explicit polling code here -- convergence rides
// the existing lifecycle-state round-loop (Filter() above excludes DELETING/DELETED, so a
// still-in-flight cascade delete is simply re-listed and re-excluded on the next round until the
// work request finishes and the namespace disappears from ListTagNamespaces entirely
// (05-RESEARCH.md's "Don't Hand-Roll" table).
func (r *TagNamespace) Remove(ctx context.Context) error {
	if r.ns.IsRetired == nil || !*r.ns.IsRetired {
		if _, err := r.client.UpdateTagNamespace(ctx, identity.UpdateTagNamespaceRequest{
			TagNamespaceId:            r.ns.Id,
			UpdateTagNamespaceDetails: identity.UpdateTagNamespaceDetails{IsRetired: common.Bool(true)},
		}); err != nil {
			return fmt.Errorf("retiring TagNamespace %s: %w", safeDeref(r.ns.Id), err)
		}
	}
	_, err := r.client.CascadeDeleteTagNamespace(ctx, identity.CascadeDeleteTagNamespaceRequest{TagNamespaceId: r.ns.Id})
	return holdOn409(err)
}

// Properties is built directly here, NOT via resources/support.go's baseProperties --
// TagNamespaceSummary has every field mandatory:"false", so baseProperties' *string-typed
// id/name/compartmentID parameters (nil-safe on Set, but still semantically "this type always has
// these") do not fit as cleanly as a hand-built Properties() that is explicit about the
// nil-checks already performed by GetCompartmentID/UniqueKey above.
func (r *TagNamespace) Properties() types.Properties {
	x := r.ns
	props := types.NewProperties().
		Set(propID, x.Id).
		Set(propCompartmentID, x.CompartmentId).
		Set(propName, x.Name).
		Set(propLifecycleState, string(x.LifecycleState))
	if x.TimeCreated != nil {
		props.Set(propTimeCreated, x.TimeCreated.Time)
	}
	if x.IsRetired != nil {
		props.Set("is_retired", *x.IsRetired)
	}
	return props
}
