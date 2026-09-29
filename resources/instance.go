// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. Instance was scaffolded by cmd/gen-resource; adjust
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
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// instanceClient is the narrow slice of core's client this lister needs -- a
// hand-written interface so Instance is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type instanceClient interface {
	ListInstances(ctx context.Context, req core.ListInstancesRequest) (core.ListInstancesResponse, error)
	TerminateInstance(ctx context.Context, req core.TerminateInstanceRequest) (core.TerminateInstanceResponse, error)
}

// InstanceResourceType is the registry.Registration.Name for Instance.
const InstanceResourceType = "Instance"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     InstanceResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Instance{},
		Lister:   &instanceLister{},
		// DependsOn is intentionally empty -- declare it on whichever type actually has the
		// dependency, never on the type depended upon (04-RESEARCH.md Q1).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type instanceLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// instanceList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Compute client.
func (l *instanceLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("instanceLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Compute(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Compute client for %s: %w", o.Region, err)
	}

	return instanceList(ctx, client, o.CompartmentID)
}

// instanceList paginates core.ListInstances and wraps every returned item as
// a Instance. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func instanceList(
	ctx context.Context,
	client instanceClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListInstancesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListInstances(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Instance in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Instance{client: client, instance: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Instance wraps one core.Instance.
type Instance struct {
	client   instanceClient
	instance core.Instance
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Instance) GetCompartmentID() string { return *r.instance.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *Instance) UniqueKey() string { return *r.instance.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
func (r *Instance) Filter() error {
	switch r.instance.LifecycleState {
	case core.InstanceLifecycleStateRunning,
		core.InstanceLifecycleStateStopped,
		core.InstanceLifecycleStateStarting,
		core.InstanceLifecycleStateStopping,
		core.InstanceLifecycleStateMoving,
		core.InstanceLifecycleStateCreatingImage:
		return nil
	default:
		return fmt.Errorf("Instance is %s, not available", r.instance.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *Instance) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.instance
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Instance) Remove(ctx context.Context) error {
	x := r.instance
	_, err := r.client.TerminateInstance(ctx, core.TerminateInstanceRequest{InstanceId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Instance) Properties() types.Properties {
	x := r.instance
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set("shape", x.Shape).
		Set(propAvailabilityDomain, x.AvailabilityDomain)
}
