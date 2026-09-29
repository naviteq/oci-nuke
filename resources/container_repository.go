// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. ContainerRepository is hand-written rather than
// scaffolded by cmd/gen-resource: artifacts.ContainerRepositorySummary's LifecycleState field is
// typed ContainerRepositoryLifecycleStateEnum, declared in artifacts/container_repository.go --
// there is no "Summary"-prefixed alias of that enum type anywhere in the artifacts package
// (verified this session, grepped the whole package) -- so the generator's fixed
// Summary-suffixed-enum-name template line does not compile against this type.
package resources

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/artifacts"
	"github.com/oracle/oci-go-sdk/v65/common"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// containerRepositoryClient is the narrow slice of artifacts' client this lister needs -- a
// hand-written interface so ContainerRepository is stub-testable with zero network access.
// Every resource type gets its OWN narrow interface, scoped to exactly the calls it makes.
type containerRepositoryClient interface {
	ListContainerRepositories(
		ctx context.Context, req artifacts.ListContainerRepositoriesRequest,
	) (artifacts.ListContainerRepositoriesResponse, error)
	DeleteContainerRepository(
		ctx context.Context, req artifacts.DeleteContainerRepositoryRequest,
	) (artifacts.DeleteContainerRepositoryResponse, error)
}

// ContainerRepositoryResourceType is the registry.Registration.Name for ContainerRepository.
const ContainerRepositoryResourceType = "ContainerRepository"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     ContainerRepositoryResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &ContainerRepository{},
		Lister:   &containerRepositoryLister{},
		// DependsOn is intentionally empty -- image cleanup inside a repository is out of this
		// wave's scope (05-CONTEXT.md); only the repository object itself is registered, and
		// nothing else this wave registers depends on it.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type containerRepositoryLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// containerRepositoryList, kept separate so it is unit-testable against a stub client without
// ever constructing a real Artifacts client. OCIR's tenancy namespace comes back on every
// ContainerRepositorySummary item itself (Namespace, mandatory:"true") -- no separate namespace
// lookup call is needed (05-CONTEXT.md).
func (l *containerRepositoryLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("containerRepositoryLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.Artifacts(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Artifacts client for %s: %w", o.Region, err)
	}

	return containerRepositoryList(ctx, client, o.CompartmentID)
}

// containerRepositoryList paginates artifacts.ListContainerRepositories and wraps every returned
// item as a ContainerRepository. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose --
// this is what the list test below exercises against a stub, with zero network access.
func containerRepositoryList(
	ctx context.Context,
	client containerRepositoryClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := artifacts.ListContainerRepositoriesRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListContainerRepositories(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing ContainerRepository in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &ContainerRepository{client: client, repository: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// ContainerRepository wraps one artifacts.ContainerRepositorySummary.
type ContainerRepository struct {
	client     containerRepositoryClient
	repository artifacts.ContainerRepositorySummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
// ContainerRepositorySummary.CompartmentId is mandatory:"true" (verified this session) --
// dereferenced directly, no nil-check needed; do not "fix" this into an unnecessary nil-check.
func (r *ContainerRepository) GetCompartmentID() string { return *r.repository.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
// ContainerRepositorySummary.Id is mandatory:"true" (verified this session) -- dereferenced
// directly, no nil-check needed.
func (r *ContainerRepository) UniqueKey() string { return *r.repository.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
// artifacts.ContainerRepositoryLifecycleStateEnum (verified this session,
// artifacts/container_repository.go) has exactly three values: AVAILABLE, DELETING, DELETED.
// AVAILABLE is present; DELETING/DELETED are excluded (going/gone, the hang-trap defense), as is
// any future SDK value this switch does not recognize (fail-safe exclusion). Image cleanup
// inside a repository is out of this wave's scope (05-CONTEXT.md) -- only the repository
// object's own lifecycle gates this Filter().
func (r *ContainerRepository) Filter() error {
	switch r.repository.LifecycleState {
	case artifacts.ContainerRepositoryLifecycleStateAvailable:
		return nil
	default:
		return fmt.Errorf("ContainerRepository is %s, not available", r.repository.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags, its
// already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the three
// values ocinuke.Evaluate needs beyond compartment/resource identity (both already available via
// GetCompartmentID/UniqueKey). Unlike Cluster/NodePool, ContainerRepositorySummary.TimeCreated
// is mandatory:"true" (verified this session), so it is dereferenced directly, no nil-safety
// wrapper needed.
func (r *ContainerRepository) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.repository
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// R-WR-03 (05-REVIEW-resources.md): image cleanup inside a repository stays out of this wave's
// scope (no DependsOn edge, no Image/ContainerImage resource type -- see init()'s own comment),
// so DeleteContainerRepository against a repository that still holds images is expected to keep
// failing every run until an operator (or a future phase) removes the images by hand. What this
// classifies is HOW that failure is reported: OCI's artifacts service returns a 409 Conflict
// common.ServiceError for a non-empty repository (the SDK carries no typed
// "NotEmpty"/"RepositoryNotEmpty" field -- only the generic ServiceError{StatusCode, Code,
// Message} shape), so the HTTP status code is the only structurally reliable signal this client
// interface exposes; on that specific status, Remove() reports
// scope.ReasonDependencyNotSatisfied (the closest fit already in pkg/scope/reason.go: the
// repository is, in substance, waiting on another resource -- its own images -- to be removed
// first, exactly what that reason's doc comment describes, even though no formal DependsOn edge
// is declared for it this phase) via ocinuke.ReportLeftover before returning the original error,
// so pkg/plan.MergeSkipEvents overwrites the generic ReasonAPIError default with this more
// specific one (pkg/plan/build.go's own documented precedent: "a Remove()-time report is
// strictly more specific than the generic api-error default"). Any OTHER failure (permissions,
// network, a genuinely unrelated 4xx/5xx) is deliberately left unclassified and falls through to
// the ReasonAPIError default -- this narrow check must never swallow or relabel an unrelated
// error as "waiting on images."
//
// NR-787: this is the one Remove() in the package that does NOT route its error through
// holdOn409, and the exception is deliberate. holdOn409 turns any 409 into ErrHoldResource
// because an OCI 409 on a delete normally means "not right now" -- but the 409 here is known to
// mean "not until someone deletes the images", and image cleanup is out of scope, so it cannot
// clear inside this run. Holding on it would spend the whole MaxWaitRetries budget (200 rounds,
// ~17 minutes at the default runSleep) re-issuing a request whose answer is already known, and
// re-report the same leftover on every round, to arrive at the same report the terminal path
// produces immediately. If an Image resource type ever lands and gains a DependsOn edge, delete
// this branch and let holdOn409 handle it like everything else.
func (r *ContainerRepository) Remove(ctx context.Context) error {
	x := r.repository
	_, err := r.client.DeleteContainerRepository(ctx, artifacts.DeleteContainerRepositoryRequest{RepositoryId: x.Id})
	if err != nil {
		if svcErr, ok := common.IsServiceError(err); ok && svcErr.GetHTTPStatusCode() == http.StatusConflict {
			ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
				Reason:        scope.ReasonDependencyNotSatisfied,
				ResourceType:  ContainerRepositoryResourceType,
				ResourceID:    *x.Id,
				CompartmentID: *x.CompartmentId,
				Detail:        "container repository still has images; image cleanup is out of scope this wave",
			})
		}
		return err
	}
	return nil
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper, plus namespace -- reused via propNamespace since Bucket/
// MountTarget/RetentionRule/ContainerRepository together push this key past the goconst
// 3-occurrence threshold.
func (r *ContainerRepository) Properties() types.Properties {
	x := r.repository
	return baseProperties(x.Id, x.DisplayName, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags).
		Set(propNamespace, x.Namespace)
}
