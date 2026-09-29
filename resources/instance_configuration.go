// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. InstanceConfiguration is hand-written rather than
// scaffolded by cmd/gen-resource: unlike every other type in this wave,
// core.InstanceConfigurationSummary has NO LifecycleState field at all (verified against
// oci-go-sdk/v65/core/instance_configuration_summary.go this session), so the generator's
// lifecycle-switch Filter() template does not apply -- see Filter() below.
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

// instanceConfigurationClient is the narrow slice of core's ComputeManagementClient this lister
// needs -- a hand-written interface so InstanceConfiguration is stub-testable with zero network
// access. Every resource type gets its OWN narrow interface, scoped to exactly the calls it
// makes.
type instanceConfigurationClient interface {
	ListInstanceConfigurations(
		ctx context.Context, req core.ListInstanceConfigurationsRequest,
	) (core.ListInstanceConfigurationsResponse, error)
	DeleteInstanceConfiguration(
		ctx context.Context, req core.DeleteInstanceConfigurationRequest,
	) (core.DeleteInstanceConfigurationResponse, error)
}

// InstanceConfigurationResourceType is the registry.Registration.Name for InstanceConfiguration.
const InstanceConfigurationResourceType = "InstanceConfiguration"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     InstanceConfigurationResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &InstanceConfiguration{},
		Lister:   &instanceConfigurationLister{},
		// DependsOn is intentionally empty -- declare it on whichever type actually has the
		// dependency, never on the type depended upon (04-RESEARCH.md Q1). InstancePool
		// references an InstanceConfigurationId, but Plan 04-04's scope treats both as
		// independently-scanned leaves; nothing in this wave declares a dependency on
		// InstanceConfiguration.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type instanceConfigurationLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// instanceConfigurationList, kept separate so it is unit-testable against a stub client without
// ever constructing a real ComputeManagement client.
func (l *instanceConfigurationLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("instanceConfigurationLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.ComputeManagement(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing ComputeManagement client for %s: %w", o.Region, err)
	}

	return instanceConfigurationList(ctx, client, o.CompartmentID)
}

// instanceConfigurationList paginates core.ListInstanceConfigurations and wraps every returned
// item as an InstanceConfiguration. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on
// purpose -- this is what the list test below exercises against a stub, with zero network
// access.
func instanceConfigurationList(
	ctx context.Context,
	client instanceConfigurationClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListInstanceConfigurationsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListInstanceConfigurations(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing InstanceConfiguration in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &InstanceConfiguration{
				client:                client,
				instanceConfiguration: resp.Items[i],
			})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// InstanceConfiguration wraps one core.InstanceConfigurationSummary.
type InstanceConfiguration struct {
	client                instanceConfigurationClient
	instanceConfiguration core.InstanceConfigurationSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *InstanceConfiguration) GetCompartmentID() string {
	return *r.instanceConfiguration.CompartmentId
}

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *InstanceConfiguration) UniqueKey() string { return *r.instanceConfiguration.Id }

// Filter always returns nil: core.InstanceConfigurationSummary has NO LifecycleState field at
// all (verified this session, oci-go-sdk/v65/core/instance_configuration_summary.go). "Present"
// is therefore the only state this type can be in while ListInstanceConfigurations still
// returns it -- there is no in-flight "TERMINATING" equivalent to defend against, and "gone" is
// entirely "absent from a subsequent List()" (04-RESEARCH.md Q2/Q4's PrivateIp/Bucket
// precedent), never a Filter() branch. This is correct by construction, not a shortcut: adding a
// lifecycle-state switch here would be dead code with nothing to switch on.
func (r *InstanceConfiguration) Filter() error {
	return nil
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *InstanceConfiguration) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.instanceConfiguration
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *InstanceConfiguration) Remove(ctx context.Context) error {
	x := r.instanceConfiguration
	_, err := r.client.DeleteInstanceConfiguration(ctx, core.DeleteInstanceConfigurationRequest{InstanceConfigurationId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal. lifecycleState
// is passed as "" so baseProperties correctly omits the lifecycle_state key entirely, per Plan
// 04-02's Task 2 contract -- this type genuinely has no lifecycle state to report.
func (r *InstanceConfiguration) Properties() types.Properties {
	x := r.instanceConfiguration
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, "", x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
