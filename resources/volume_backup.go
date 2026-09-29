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
	"github.com/oracle/oci-go-sdk/v65/core"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// volumeBackupClient is the narrow slice of core.BlockstorageClient this lister needs -- a
// hand-written interface so VolumeBackup is stub-testable with zero network access. Every
// resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type volumeBackupClient interface {
	ListVolumeBackups(ctx context.Context, req core.ListVolumeBackupsRequest) (core.ListVolumeBackupsResponse, error)
	DeleteVolumeBackup(ctx context.Context, req core.DeleteVolumeBackupRequest) (core.DeleteVolumeBackupResponse, error)
}

// VolumeBackupResourceType is the registry.Registration.Name for VolumeBackup.
const VolumeBackupResourceType = "VolumeBackup"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     VolumeBackupResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &VolumeBackup{},
		Lister:   &volumeBackupLister{},
		// DependsOn is intentionally empty -- nothing in this wave must be removed before a
		// volume backup.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type volumeBackupLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// volumeBackupList, kept separate so it is unit-testable against a stub client without ever
// constructing a real Blockstorage client.
func (l *volumeBackupLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("volumeBackupLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Blockstorage(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Blockstorage client for %s: %w", o.Region, err)
	}

	return volumeBackupList(ctx, client, o.CompartmentID)
}

// volumeBackupList paginates core.ListVolumeBackups and wraps every returned item as a
// VolumeBackup. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what
// the list test below exercises against a stub, with zero network access.
func volumeBackupList(
	ctx context.Context,
	client volumeBackupClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := core.ListVolumeBackupsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListVolumeBackups(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing VolumeBackup in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &VolumeBackup{client: client, backup: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// VolumeBackup wraps one core.VolumeBackup.
type VolumeBackup struct {
	client volumeBackupClient
	backup core.VolumeBackup
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *VolumeBackup) GetCompartmentID() string { return *r.backup.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// This is the ONLY safe identity key for a resource that can be recreated with the same name by
// Terraform drift-reconciliation mid-run (PITFALLS.md Pitfall 5).
func (r *VolumeBackup) UniqueKey() string { return *r.backup.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2): at scan time a
// non-nil return means "never attempt removal"; during HandleWait's post-Remove() polling, a
// non-nil return on a still-listed match means "already handled, converge now." Both meanings
// are satisfied by the same allow-list-of-"present" check -- any state not explicitly listed
// here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
//
// FAULTY is a genuine error state OCI itself cannot resolve -- neither cleanly "present" nor
// "gone" (04-RESEARCH.md Q2's FAULTY/FAILED note). It is excluded like TERMINATING/TERMINATED,
// but additionally reported via ocinuke.ReportLeftover(ReasonAPIError) before returning the
// exclusion error, so a FAULTY volume backup surfaces as a labeled leftover rather than a silent
// scan-time drop.
func (r *VolumeBackup) Filter() error {
	switch r.backup.LifecycleState {
	case core.VolumeBackupLifecycleStateCreating,
		core.VolumeBackupLifecycleStateAvailable,
		core.VolumeBackupLifecycleStateRequestReceived:
		return nil
	case core.VolumeBackupLifecycleStateFaulty:
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonAPIError,
			ResourceType:  VolumeBackupResourceType,
			ResourceID:    *r.backup.Id,
			CompartmentID: *r.backup.CompartmentId,
			Detail:        "volume backup is FAULTY",
		})
		return fmt.Errorf("VolumeBackup is %s, not available", r.backup.LifecycleState)
	default:
		return fmt.Errorf("VolumeBackup is %s, not available", r.backup.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *VolumeBackup) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.backup
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *VolumeBackup) Remove(ctx context.Context) error {
	x := r.backup
	_, err := r.client.DeleteVolumeBackup(ctx, core.DeleteVolumeBackupRequest{VolumeBackupId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *VolumeBackup) Properties() types.Properties {
	x := r.backup
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propSizeInGBs, x.SizeInGBs).
		Set(propVolumeID, x.VolumeId)
}
