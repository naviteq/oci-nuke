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

// applicationClient is the narrow slice of functions.FunctionsManagementClient this lister
// needs -- a hand-written interface so Application is stub-testable with zero network access.
type applicationClient interface {
	ListApplications(
		ctx context.Context, req functions.ListApplicationsRequest,
	) (functions.ListApplicationsResponse, error)
	DeleteApplication(
		ctx context.Context, req functions.DeleteApplicationRequest,
	) (functions.DeleteApplicationResponse, error)
}

// ApplicationResourceType is the registry.Registration.Name for Application.
const ApplicationResourceType = "Application"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ApplicationResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Application{},
		Lister:   &applicationLister{},
		// DependsOn: a Function can only be discovered by walking every Application
		// (functions.ListFunctionsRequest has NO CompartmentId field at all --
		// resources/functions_support.go), so every Function must be removed before the
		// Application that hosts it is removed out from under it -- 05-CONTEXT.md's locked
		// direction: "Application DependsOn: [\"Function\"]" (Functions scanned/removed first).
		DependsOn: []string{"Function"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type applicationLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in applicationList,
// kept separate so it is unit-testable against a stub client without ever constructing a real
// FunctionsManagement client.
func (l *applicationLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("applicationLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.FunctionsManagement(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing FunctionsManagementClient for %s: %w", o.Region, err)
	}

	return applicationList(ctx, client, o.CompartmentID)
}

// applicationList paginates functions.ListApplications and wraps every returned item as an
// Application. Deliberately kept independent from functions_support.go's listApplicationIDs
// (which only needs the .Id values for Function's nested walk) rather than sharing one low-level
// pagination helper between them -- a small amount of duplicated pagination-loop code, matching
// resources/export.go's own precedent of not sharing pagination logic across every list function
// in the same file. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func applicationList(
	ctx context.Context,
	client applicationClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := functions.ListApplicationsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListApplications(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Application in %s: %w", compartmentID, err)
		}

		for i := range resp.Items {
			out = append(out, &Application{client: client, application: resp.Items[i]})
		}

		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Application wraps one functions.ApplicationSummary.
type Application struct {
	client      applicationClient
	application functions.ApplicationSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// ApplicationSummary.CompartmentId is mandatory:"false" (verified this session,
// functions/application_summary.go) -- nil-checked here rather than dereferenced directly, so a
// real record with no compartment recorded fails closed (scopedLister drops it as out-of-scope)
// instead of panicking.
func (r *Application) GetCompartmentID() string {
	if r.application.CompartmentId == nil {
		return ""
	}
	return *r.application.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Application) UniqueKey() string { return *r.application.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// functions.ApplicationLifecycleStateEnum (verified this session, functions/application.go) has
// exactly SEVEN values: CREATING, ACTIVE, INACTIVE, UPDATING, DELETING, DELETED, FAILED. Present
// = Creating, Active, Updating; excluded = Inactive, Deleting, Deleted, Failed -- the same enum
// shape as Function's own Filter() below. Any state not explicitly listed here excludes, so a
// future SDK release adding a new lifecycle-state value fails safe.
func (r *Application) Filter() error {
	switch r.application.LifecycleState {
	case functions.ApplicationLifecycleStateCreating,
		functions.ApplicationLifecycleStateActive,
		functions.ApplicationLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Application is %s, not available", r.application.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three values
// ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). ApplicationSummary.TimeCreated is mandatory:"false" (verified this
// session), so timeCreatedOrZero keeps this nil-safe rather than dereferencing directly.
func (r *Application) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.application
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), timeCreatedOrZero(x.TimeCreated)
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Application) Remove(ctx context.Context) error {
	x := r.application
	_, err := r.client.DeleteApplication(ctx, functions.DeleteApplicationRequest{ApplicationId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Application) Properties() types.Properties {
	x := r.application
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
