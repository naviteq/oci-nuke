// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. CapacityReservation was scaffolded by cmd/gen-resource
// (with two hand-fixes after generation, documented below) then adjusted freely -- this file is
// not regenerated automatically, re-running the generator only overwrites it if you choose to.
//
// Naming: this type is registered under the resource-type NAME "CapacityReservation" (matching
// CONTEXT.md's first-wave scope wording), even though the underlying OCI SDK struct and client
// methods use the longer "ComputeCapacityReservation*" names (core.ComputeCapacityReservation,
// ListComputeCapacityReservations, DeleteComputeCapacityReservation) -- verified this session by
// reading oci-go-sdk/v65/core/compute_capacity_reservation.go directly, not assumed from naming
// symmetry with the other four types in this wave. Do not "fix" CapacityReservationResourceType
// to match the SDK struct name; the shorter name is deliberate.
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

// capacityReservationClient is the narrow slice of core's client this lister needs -- a
// hand-written interface so CapacityReservation is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type capacityReservationClient interface {
	ListComputeCapacityReservations(
		ctx context.Context, req core.ListComputeCapacityReservationsRequest,
	) (core.ListComputeCapacityReservationsResponse, error)
	DeleteComputeCapacityReservation(
		ctx context.Context, req core.DeleteComputeCapacityReservationRequest,
	) (core.DeleteComputeCapacityReservationResponse, error)
}

// CapacityReservationResourceType is the registry.Registration.Name for CapacityReservation.
const CapacityReservationResourceType = "CapacityReservation"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     CapacityReservationResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &CapacityReservation{},
		Lister:   &capacityReservationLister{},
		// DependsOn is intentionally empty -- declare it on whichever type actually has the
		// dependency, never on the type depended upon (04-RESEARCH.md Q1).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type capacityReservationLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// capacityReservationList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Compute client.
func (l *capacityReservationLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("capacityReservationLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Compute(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Compute client for %s: %w", o.Region, err)
	}

	return capacityReservationList(ctx, client, o.CompartmentID)
}

// capacityReservationList paginates core.ListComputeCapacityReservations and wraps every returned item as
// a CapacityReservation. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func capacityReservationList(
	ctx context.Context,
	client capacityReservationClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListComputeCapacityReservationsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListComputeCapacityReservations(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing CapacityReservation in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &CapacityReservation{client: client, capacityReservation: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// CapacityReservation wraps one core.ComputeCapacityReservationSummary.
type CapacityReservation struct {
	client              capacityReservationClient
	capacityReservation core.ComputeCapacityReservationSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *CapacityReservation) GetCompartmentID() string { return *r.capacityReservation.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *CapacityReservation) UniqueKey() string { return *r.capacityReservation.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
func (r *CapacityReservation) Filter() error {
	switch r.capacityReservation.LifecycleState {
	case core.ComputeCapacityReservationLifecycleStateActive,
		core.ComputeCapacityReservationLifecycleStateCreating,
		core.ComputeCapacityReservationLifecycleStateUpdating,
		core.ComputeCapacityReservationLifecycleStateMoving:
		return nil
	default:
		return fmt.Errorf("CapacityReservation is %s, not available", r.capacityReservation.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *CapacityReservation) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.capacityReservation
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *CapacityReservation) Remove(ctx context.Context) error {
	x := r.capacityReservation
	_, err := r.client.DeleteComputeCapacityReservation(ctx, core.DeleteComputeCapacityReservationRequest{CapacityReservationId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *CapacityReservation) Properties() types.Properties {
	x := r.capacityReservation
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
