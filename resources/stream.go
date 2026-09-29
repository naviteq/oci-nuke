// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry. Stream was scaffolded by cmd/gen-resource; adjust
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

// streamClient is the narrow slice of streaming.StreamAdminClient this lister needs -- a
// hand-written interface so Stream is stub-testable with zero network access. Every resource
// type gets its OWN narrow interface, scoped to exactly the calls it makes.
type streamClient interface {
	ListStreams(ctx context.Context, req streaming.ListStreamsRequest) (streaming.ListStreamsResponse, error)
	DeleteStream(ctx context.Context, req streaming.DeleteStreamRequest) (streaming.DeleteStreamResponse, error)
}

// StreamResourceType is the registry.Registration.Name for Stream.
const StreamResourceType = "Stream"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     StreamResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Stream{},
		Lister:   &streamLister{},
		// DependsOn is intentionally empty -- StreamPool (resources/stream_pool.go) declares the
		// dependency on ITS OWN registration ("StreamPool DependsOn: [\"Stream\"]", 05-CONTEXT.md),
		// never here (04-RESEARCH.md Q1: declare on the dependent, not the depended-upon).
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type streamLister struct{}

// List satisfies registry.Lister. The pagination/wrapping logic itself lives in
// streamList, kept separate so it is unit-testable against a stub client without ever
// constructing a real StreamAdmin client.
func (l *streamLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("streamLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Regional); err != nil {
		return nil, err
	}

	client, err := o.Clients.StreamAdmin(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing StreamAdmin client for %s: %w", o.Region, err)
	}

	return streamList(ctx, client, o.CompartmentID)
}

// streamList paginates streaming.ListStreams and wraps every returned item as a Stream.
// Isolated from ocinuke.ListerOpts/pkg/clients.Cache on purpose -- this is what the list test
// below exercises against a stub, with zero network access.
//
// streaming.ListStreamsRequest.CompartmentId is mandatory:"false" on the SDK side -- it is
// exclusive with the request's stream-pool-scoped field, and exactly one of the two is
// required. This lister always passes CompartmentId (never the pool-scoped field), satisfying
// the "one is required" constraint while keeping this a plain compartment-scoped list. Do NOT
// "fix" this into a mandatory:"true"-shaped assumption -- both fields are optional on the
// request struct, but the API rejects a request with neither set.
func streamList(
	ctx context.Context,
	client streamClient,
	compartmentID string,
) ([]resource.Resource, error) {
	var out []resource.Resource
	req := streaming.ListStreamsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListStreams(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Stream in %s: %w", compartmentID, err)
		}
		for i := range resp.Items {
			out = append(out, &Stream{client: client, stream: resp.Items[i]})
		}
		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return out, nil
}

// Stream wraps one streaming.StreamSummary.
type Stream struct {
	client streamClient
	stream streaming.StreamSummary
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
func (r *Stream) GetCompartmentID() string { return *r.stream.CompartmentId }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the OCID, never the display name.
func (r *Stream) UniqueKey() string { return *r.stream.Id }

// Filter is called from TWO structurally different contexts (04-RESEARCH.md Q2) -- see
// resources/instance.go's Filter() doc comment.
//
// streaming.StreamSummaryLifecycleStateEnum (verified this session, streaming/stream_summary.go)
// has exactly SIX values: CREATING, ACTIVE, DELETING, DELETED, FAILED, UPDATING. Present =
// Creating, Active, Updating; excluded = Deleting, Deleted, Failed. Any state not explicitly
// listed here excludes, so a future SDK release adding a new lifecycle-state value fails safe.
func (r *Stream) Filter() error {
	switch r.stream.LifecycleState {
	case streaming.StreamSummaryLifecycleStateCreating,
		streaming.StreamSummaryLifecycleStateActive,
		streaming.StreamSummaryLifecycleStateUpdating:
		return nil
	default:
		return fmt.Errorf("Stream is %s, not available", r.stream.LifecycleState)
	}
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). Read by ocinuke's scopedLister at SCAN time,
// before this resource can ever become a queue.Item -- see pkg/ocinuke/scoped_lister.go's
// SafetyEvaluated doc comment for why protection moved here from Remove() (04-13 plan/apply
// divergence fix).
func (r *Stream) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.stream
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove is a direct delete call -- protect-by-tag/min-age protection is now applied at SCAN
// time (SafetyTags above), before this resource can ever become a queue.Item, so a protected
// resource never reaches Remove() at all, in either dry-run or --no-dry-run mode.
func (r *Stream) Remove(ctx context.Context) error {
	x := r.stream
	_, err := r.client.DeleteStream(ctx, streaming.DeleteStreamRequest{StreamId: x.Id})
	return holdOn409(err)
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared helper so no resource file hand-duplicates a property-key string literal.
func (r *Stream) Properties() types.Properties {
	x := r.stream
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
