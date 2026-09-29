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
	"github.com/oracle/oci-go-sdk/v65/functions"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// functionClient is the narrow slice of functions.FunctionsManagementClient this lister needs --
// applicationIDLister's ListApplications (resources/functions_support.go's shared per-Application
// walk, since functions.ListFunctionsRequest has NO CompartmentId field at all) plus
// ListFunctions/DeleteFunction. In production this is satisfied directly by ONE
// functions.FunctionsManagementClient -- Application and Function share the same client (see
// resources/application.go).
type functionClient interface {
	applicationIDLister
	ListFunctions(ctx context.Context, req functions.ListFunctionsRequest) (functions.ListFunctionsResponse, error)
	DeleteFunction(ctx context.Context, req functions.DeleteFunctionRequest) (functions.DeleteFunctionResponse, error)
}

// FunctionResourceType is the registry.Registration.Name for Function.
const FunctionResourceType = "Function"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     FunctionResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Function{},
		Lister:   &functionLister{},
		// DependsOn is intentionally empty -- Application declares Function in ITS OWN
		// DependsOn instead (resources/application.go), ensuring functions are removed before
		// the application that hosts them (04-RESEARCH.md Q1: declare on the dependent, never
		// on the type depended upon).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type functionLister struct{}

// List satisfies registry.Lister. The nested-enumeration/wrapping logic itself lives in
// functionList, kept separate so it is unit-testable against a stub client without ever
// constructing a real FunctionsManagement client.
func (l *functionLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("functionLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.FunctionsManagement(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing FunctionsManagementClient for %s: %w", o.Region, err)
	}

	return functionList(ctx, client, o.CompartmentID)
}

// functionList walks every Application in compartmentID (listApplicationIDs,
// resources/functions_support.go -- functions.ListFunctionsRequest has NO CompartmentId field at
// all, verified this session, functions/list_functions_request_response.go, ApplicationId
// mandatory:"true" is the ONLY field), then fully paginates functions.ListFunctions scoped by
// each ApplicationId, wrapping every returned item as a Function that inherits the compartment
// this whole walk was scoped to.
//
// This is the CompartmentId-fallback Pitfall 5 closes: FunctionSummary.CompartmentId is itself
// mandatory:"false" (verified this session, functions/function_summary.go) -- if a Function's
// own CompartmentId came back nil and were read directly, scopedLister would silently drop it as
// out-of-scope even though its owning Application is correctly in scope. Rather than reading
// EITHER the Application's or the Function's own optional CompartmentId field (both
// mandatory:"false", so a two-level optional chain), every constructed Function inherits
// compartmentID -- the SAME value this walk's own listApplicationIDs(ctx, client, compartmentID)
// call used to scope ListApplications in the first place. That value is guaranteed non-nil and
// is what OCI itself used to filter which applications (and therefore which functions) this walk
// discovers, making it a STRONGER source of truth than either optional response field -- the
// same "thread the parent's known-good CompartmentId down" shape
// resources/export.go's Export.compartmentID/GetCompartmentID() established, one level removed
// (thread the SCOPE's CompartmentId down, since the intermediate Application's own field is
// unreliable in the same way).
func functionList(
	ctx context.Context,
	client functionClient,
	compartmentID string,
) ([]resource.Resource, error) {
	appIDs, err := listApplicationIDs(ctx, client, compartmentID)
	if err != nil {
		return nil, err
	}

	var out []resource.Resource
	for _, appID := range appIDs {
		appID := appID
		req := functions.ListFunctionsRequest{ApplicationId: &appID}
		for {
			resp, err := client.ListFunctions(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("listing Function under application %s: %w", appID, err)
			}

			for i := range resp.Items {
				out = append(out, &Function{client: client, function: resp.Items[i], compartmentID: compartmentID})
			}

			if resp.OpcNextPage == nil {
				break
			}
			req.Page = resp.OpcNextPage
		}
	}
	return out, nil
}

// Function wraps one functions.FunctionSummary, plus the compartment this Function was
// discovered in -- see functionList's doc comment above for why this is an inherited field
// rather than a read off function.CompartmentId directly (functions/function_summary.go,
// mandatory:"false").
type Function struct {
	client        functionClient
	function      functions.FunctionSummary
	compartmentID string
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime). Returns the
// inherited field, never function.CompartmentId directly -- see the struct doc comment.
func (r *Function) GetCompartmentID() string { return r.compartmentID }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Function) UniqueKey() string { return *r.function.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// functions.FunctionLifecycleStateEnum (verified this session, functions/function.go) has
// exactly SEVEN values: CREATING, ACTIVE, INACTIVE, UPDATING, DELETING, DELETED, FAILED. Present
// = Creating, Active, Updating; excluded = Inactive, Deleting, Deleted, Failed. Any state not
// explicitly listed here excludes, so a future SDK release adding a new lifecycle-state value
// fails safe.
func (r *Function) Filter() error {
	switch r.function.LifecycleState {
	case functions.FunctionLifecycleStateCreating,
		functions.FunctionLifecycleStateActive,
		functions.FunctionLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Function is %s, not available", r.function.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three
// values ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). FunctionSummary.TimeCreated is mandatory:"false" (verified this
// session), so timeCreatedOrZero keeps this nil-safe.
func (r *Function) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.function
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Function) Remove(ctx context.Context) error {
	x := r.function
	_, err := r.client.DeleteFunction(ctx, functions.DeleteFunctionRequest{FunctionId: x.Id})
	return holdOn409(err)
}

// Properties is built via resources/support.go's baseProperties (passing the inherited
// compartmentID, never function.CompartmentId directly), plus application_id -- the parent
// Application's OCID this Function was discovered under. "application_id" is not promoted to a
// resources/support.go propX constant since this is its only use site in this wave
// (goconst min-occurrences: 3).
func (r *Function) Properties() types.Properties {
	x := r.function
	compartmentID := r.compartmentID
	return baseProperties(x.Id, x.DisplayName, &compartmentID, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set("application_id", x.ApplicationId)
}
