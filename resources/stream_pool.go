// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. StreamPool was scaffolded by cmd/gen-resource; adjust
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
	"github.com/oracle/oci-go-sdk/v65/streaming"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
)

// streamPoolClient is the narrow slice of streaming.StreamAdminClient this lister needs -- a
// hand-written interface so StreamPool is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type streamPoolClient interface {
	ListStreamPools(ctx context.Context, req streaming.ListStreamPoolsRequest) (streaming.ListStreamPoolsResponse, error)
	DeleteStreamPool(ctx context.Context, req streaming.DeleteStreamPoolRequest) (streaming.DeleteStreamPoolResponse, error)
}

// StreamPoolResourceType is the registry.Registration.Name for StreamPool.
const StreamPoolResourceType = "StreamPool"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     StreamPoolResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &StreamPool{},
		Lister:   &streamPoolLister{},
		// DependsOn: 05-CONTEXT.md locks "StreamPool DependsOn: [\"Stream\"]" -- StreamPool
		// depends on Stream, so every Stream is removed first, before the pool that hosts them.
		// Bare string literal -- StreamPool does not import stream.go's ResourceType constant,
		// matching Vcn's cross-plan-boundary convention even though both live in this same plan.
		DependsOn: []string{"Stream"},
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type streamPoolLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// streamPoolList, kept separate so it is unit-testable against a stub client without ever
// constructing a real StreamAdmin client.
func (l *streamPoolLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("streamPoolLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.StreamAdmin(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing StreamAdmin client for %s: %w", o.Region, err)
	}

	return streamPoolList(ctx, client, o.CompartmentID)
}

// streamPoolList paginates streaming.ListStreamPools and wraps every returned item as a
// StreamPool. Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the
// list test below exercises against a stub, with zero network access.
func streamPoolList(
	ctx context.Context,
	client streamPoolClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := streaming.ListStreamPoolsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListStreamPools(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing StreamPool in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &StreamPool{client: client, pool: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// StreamPool wraps one streaming.StreamPoolSummary.
type StreamPool struct {
	client streamPoolClient
	pool   streaming.StreamPoolSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *StreamPool) GetCompartmentID() string { return *r.pool.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *StreamPool) UniqueKey() string { return *r.pool.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// streaming.StreamPoolSummaryLifecycleStateEnum (verified this session,
// streaming/stream_pool_summary.go) has exactly SIX values: CREATING, ACTIVE, DELETING, DELETED,
// FAILED, UPDATING. Present = Creating, Active, Updating; excluded = Deleting, Deleted, Failed.
// Any state not explicitly listed here excludes, so a future SDK release adding a new
// lifecycle-state value fails safe.
func (r *StreamPool) Filter() error {
	switch r.pool.LifecycleState {
	case streaming.StreamPoolSummaryLifecycleStateCreating,
		streaming.StreamPoolSummaryLifecycleStateActive,
		streaming.StreamPoolSummaryLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("StreamPool is %s, not available", r.pool.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *StreamPool) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.pool
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
//
// By the time this call fires, WaitOnDependencies has already ensured every Stream this pool's
// own DependsOn names has finished or permanently failed (04-RESEARCH.md Q3), so a Stream is
// never left dangling after DeleteStreamPool is attempted.
func (r *StreamPool) Remove(ctx context.Context) error {
	x := r.pool
	_, err := r.client.DeleteStreamPool(ctx, streaming.DeleteStreamPoolRequest{StreamPoolId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *StreamPool) Properties() types.Properties {
	x := r.pool
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
