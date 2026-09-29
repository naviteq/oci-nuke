// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. DedicatedVmHost was scaffolded by cmd/gen-resource; adjust
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
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// dedicatedVmHostClient is the narrow slice of core's client this lister needs -- a
// hand-written interface so DedicatedVmHost is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type dedicatedVmHostClient interface {
	ListDedicatedVmHosts(ctx context.Context, req core.ListDedicatedVmHostsRequest) (core.ListDedicatedVmHostsResponse, error)
	DeleteDedicatedVmHost(ctx context.Context, req core.DeleteDedicatedVmHostRequest) (core.DeleteDedicatedVmHostResponse, error)
}

// DedicatedVmHostResourceType is the registry.Registration.Name for DedicatedVmHost.
const DedicatedVmHostResourceType = "DedicatedVmHost"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     DedicatedVmHostResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &DedicatedVmHost{},
		Lister:   &dedicatedVmHostLister{},
		// DependsOn is intentionally empty -- declare it on whichever type actually has the
		// dependency, never on the type depended upon (04-RESEARCH.md Q1).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type dedicatedVmHostLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// dedicatedVmHostList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Compute client.
func (l *dedicatedVmHostLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("dedicatedVmHostLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Compute(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Compute client for %s: %w", o.Region, err)
	}

	return dedicatedVmHostList(ctx, client, o.CompartmentID)
}

// dedicatedVmHostList paginates core.ListDedicatedVmHosts and wraps every returned item as
// a DedicatedVmHost. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func dedicatedVmHostList(
	ctx context.Context,
	client dedicatedVmHostClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListDedicatedVmHostsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListDedicatedVmHosts(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing DedicatedVmHost in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &DedicatedVmHost{client: client, dedicatedVmHost: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// DedicatedVmHost wraps one core.DedicatedVmHostSummary.
type DedicatedVmHost struct {
	client          dedicatedVmHostClient
	dedicatedVmHost core.DedicatedVmHostSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *DedicatedVmHost) GetCompartmentID() string { return *r.dedicatedVmHost.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *DedicatedVmHost) UniqueKey() string { return *r.dedicatedVmHost.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// FAILED is a genuine error state OCI itself cannot resolve -- neither cleanly "present" nor
// "gone" (04-RESEARCH.md Q2's FAULTY/FAILED note). It is excluded like DELETING/DELETED, but
// additionally reported via ocinuke.ReportLeftover(ReasonAPIError) before returning the
// exclusion error, so a FAILED host surfaces as a labeled leftover rather than a silent scan-time
// drop -- the one place in this plan's five types where Filter()'s exclusion branch also reports.
func (r *DedicatedVmHost) Filter() error {
	switch r.dedicatedVmHost.LifecycleState {
	case core.DedicatedVmHostSummaryLifecycleStateCreating,
		core.DedicatedVmHostSummaryLifecycleStateActive,
		core.DedicatedVmHostSummaryLifecycleStateUpdating:
		return nil
	case core.DedicatedVmHostSummaryLifecycleStateFailed:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  DedicatedVmHostResourceType,
			ResourceID:    *r.dedicatedVmHost.Id,
			CompartmentID: *r.dedicatedVmHost.CompartmentId,
			Detail:        "dedicated VM host is FAILED",
		})
		return fmt.Errorf("DedicatedVmHost is %s, not available", r.dedicatedVmHost.LifecycleState)
	default:
		return fmt.Errorf("DedicatedVmHost is %s, not available", r.dedicatedVmHost.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
//
// core.DedicatedVmHostSummary (the ListDedicatedVmHosts element type) carries NO FreeformTags/
// DefinedTags fields at all -- verified this session against
// oci-go-sdk/v65/core/dedicated_vm_host_summary.go, unlike the full core.DedicatedVmHost type
// (a GET-only response) which does have them. nil is passed for both tag maps: min-age
// protection (TimeCreated, which IS present on the summary) still applies, but protect-by-tag
// cannot match this type from list-time data alone. This is a real, documented SDK-shape
// limitation, not an oversight -- see 04-04-SUMMARY.md's Threat Flags.
func (r *DedicatedVmHost) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.dedicatedVmHost
	return nil, nil, x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *DedicatedVmHost) Remove(ctx context.Context) error {
	x := r.dedicatedVmHost
	_, err := r.client.DeleteDedicatedVmHost(ctx, core.DeleteDedicatedVmHostRequest{DedicatedVmHostId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
// core.DedicatedVmHostSummary has no tag fields (see Remove() above), so no tags are set.
func (r *DedicatedVmHost) Properties() types.Properties {
	x := r.dedicatedVmHost
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, nil, nil)
}
