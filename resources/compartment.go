// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// compartmentClient is the narrow slice of identity.IdentityClient this lister/resource needs --
// a hand-written interface so Compartment is stub-testable with zero network access, satisfied in
// production by pkg/clients.Cache.Identity. Unlike every other resource type in this codebase,
// Compartment needs a third method (GetWorkRequest) beyond the usual List.../Delete... pair,
// because DeleteCompartment is unconditionally asynchronous (06-RESEARCH.md Contradiction 1) --
// there is no synchronous "it's empty, proceed" signal to key off.
type compartmentClient interface {
	GetCompartment(ctx context.Context, req identity.GetCompartmentRequest) (identity.GetCompartmentResponse, error)
	DeleteCompartment(ctx context.Context, req identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error)
	GetWorkRequest(ctx context.Context, req identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error)
	ListCompartments(ctx context.Context, req identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error)
}

// CompartmentResourceType is the registry.Registration.Name for Compartment.
const CompartmentResourceType = "Compartment"

func init() {
	ocinuke.Register(&registry.Registration{
		Name:     CompartmentResourceType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &Compartment{},
		Lister:   &compartmentLister{},
		// No DependsOn -- 06-CONTEXT.md forbids a hand-maintained list of every other registered
		// type (a list like that rots silently the first time someone adds a type and forgets it,
		// failing in the direction of deleting a compartment too early). Correctness instead comes
		// from HandleWait re-attempting DeleteCompartment every round until OCI's own async work
		// request succeeds -- see HandleWait below -- combined with buildNukes' deepest-first Nuke
		// construction order (scope.Tree.DeletionOrder, Plan 06-01) ensuring every descendant
		// compartment's own Nuke has already run to completion before this compartment's Nuke is
		// even constructed.
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)
}

type compartmentLister struct{}

// List satisfies registry.Lister. Unlike every other lister in this codebase, this is NOT a
// ListXxx pagination loop over the compartment's contents -- it is a single GetCompartment call
// against the scanner's OWN CompartmentID, since the scanner built for compartment C always
// targets exactly C itself (each in-scope compartment gets its own Nuke, and this lister is
// registered once per Nuke -- 06-RESEARCH.md "The runNukes structural question"). It therefore
// always returns exactly zero or one resource.
//
// BeforeList(ocinuke.Global) is the FIRST statement after the opts type-assertion -- before the
// client is ever constructed -- since a compartment is a tenancy-wide Identity/IAM object, not a
// per-region resource, exactly like Policy/DynamicGroup/TagNamespace/TagDefault (all four call
// BeforeList(ocinuke.Global) too). buildNukes registers one scanner per subscribed region onto
// the SAME *libnuke.Nuke for this compartment; with Regional geography every one of those
// scanners would independently GetCompartment/List the identical compartment and enqueue its own
// queue.Item under a different Owner, so the same OCID would be listed -- and, on a destructive
// run, have DeleteCompartment attempted against it -- once per subscribed region inside one Nuke.
// Global collapses that back to exactly one queue item, enqueued only in the home region.
func (l *compartmentLister) List(ctx context.Context, opts interface{}) ([]resource.Resource, error) {
	o, ok := opts.(*ocinuke.ListerOpts)
	if !ok {
		return nil, fmt.Errorf("compartmentLister.List: unexpected opts type %T", opts)
	}
	if err := o.BeforeList(ocinuke.Global); err != nil {
		return nil, err
	}

	client, err := o.Clients.Identity(o.Region)
	if err != nil {
		return nil, fmt.Errorf("constructing Identity client for %s: %w", o.Region, err)
	}

	resp, err := client.GetCompartment(ctx, identity.GetCompartmentRequest{CompartmentId: &o.CompartmentID})
	if err != nil {
		return nil, fmt.Errorf("getting compartment %s: %w", o.CompartmentID, err)
	}

	return []resource.Resource{&Compartment{
		client:                   client,
		compartment:              resp.Compartment,
		hasBlocklistedDescendant: o.CompartmentHasBlocklistedDescendant,
		occupancy:                o.CompartmentOccupancy,
	}}, nil
}

// Compartment wraps one identity.Compartment, plus the run's own precomputed
// hasBlocklistedDescendant value (captured at list time, mirroring resources/vault.go's
// deletionWindowDays threading -- see pkg/ocinuke/listeropts.go's CompartmentHasBlocklistedDescendant
// doc comment) and workRequestID, set by Remove() and read by HandleWait() on this SAME struct
// instance -- see HandleWait's own doc comment for why this survives across libnuke rounds only
// because HandleWaitHook (unlike Filter()'s post-Remove() fallback) runs against item.Resource,
// never a freshly List()-produced copy.
type Compartment struct {
	client                   compartmentClient
	compartment              identity.Compartment
	workRequestID            string
	hasBlocklistedDescendant bool
	// occupancy and the four fields after it belong to waitUntilEmpty below.
	occupancy        func() ocinuke.Occupancy
	notEmpty         error
	notEmptyReported bool
	waitedForRun     bool
	refusedByOCI     bool
}

// GetCompartmentID satisfies ocinuke.CompartmentScoped -- mandatory, checked by ocinuke.Register
// at init() time (a missing implementation panics at process start, not at runtime).
//
// Compartment is the ONE resource type in this codebase whose GetCompartmentID() does NOT return
// the field literally named CompartmentId. On identity.Compartment, CompartmentId is the PARENT's
// OCID; Id is the compartment's OWN OCID (06-RESEARCH.md, ASVS V4). Returning *r.compartment.
// CompartmentId here would place the compartment in its parent's scope for scopedLister's in-scope
// re-verification, silently dropping the target compartment itself from that check -- the literal
// bug this divergence exists to warn against. Every other resource type's compartment_id field
// genuinely means "where does this resource live," which for a compartment is a DIFFERENT
// question from "what is this resource's own scope identity" -- see Properties() below, which
// deliberately answers the "where does it live" question with the parent OCID instead.
func (r *Compartment) GetCompartmentID() string { return *r.compartment.Id }

// UniqueKey satisfies resource.UniqueKeyGetter (SAFE-09) -- the compartment's own OCID, the same
// value GetCompartmentID() returns (self-referential). Never the display name: OCI rewrites a
// deleted compartment's name with a random suffix (observed live against a real tenancy this session,
// 06-CONTEXT.md: "avatrade-poc.jEpLPXHu"), so name-based matching is unsafe here.
func (r *Compartment) UniqueKey() string { return *r.compartment.Id }

// Filter is scan-time-only: it excludes an already-DELETED compartment (never reported as
// residue -- for billing and nesting purposes it no longer exists, and OCI's ~90-day retention of
// the record is not something the operator can act on) and a compartment with a blocklisted
// descendant (excluded AND reported via ocinuke.ReportLeftover with
// scope.ReasonCompartmentNotEmpty -- it cannot physically empty, so attempting it would burn retry
// budget and muddy the report; 06-CONTEXT.md's locked decision).
//
// This deliberately diverges from resources/instance.go's "any state not explicitly listed
// excludes" allow-list convention: identity.CompartmentLifecycleStateEnum has five values
// (Creating, Active, Inactive, Deleting, Deleted -- no Failed state on Compartment itself, a
// failed DeleteCompartment work request reverts LifecycleState to Active per Oracle's own docs,
// 06-RESEARCH.md Contradiction 1). Creating/Active/Inactive/Deleting are all "present" here --
// only Deleted, plus the blocklisted-descendant case, exclude. A future SDK release adding a new
// lifecycle-state value therefore fails OPEN (present) rather than closed for this one type,
// unlike every other resource type's Filter() -- an intentional, documented divergence, not an
// oversight, because an unrecognized Compartment lifecycle state should still be attempted (OCI's
// own DeleteCompartment/GetWorkRequest calls are the actual safety net here, not this switch).
//
// This is NOT the async-poll mechanism -- see HandleWait below for that. Filter() runs once at
// scan time, before any Remove() call in the entire Nuke has happened, and has no way to observe a
// work request's later outcome (06-RESEARCH.md Contradiction 2).
func (r *Compartment) Filter() error {
	if r.compartment.LifecycleState == identity.CompartmentLifecycleStateDeleted {
		return fmt.Errorf("Compartment is DELETED")
	}
	if r.hasBlocklistedDescendant {
		ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
			Reason:        scope.ReasonCompartmentNotEmpty,
			ResourceType:  CompartmentResourceType,
			ResourceID:    *r.compartment.Id,
			CompartmentID: *r.compartment.Id,
			Detail:        "a descendant compartment is blocklisted; this compartment can never physically empty",
		})
		return fmt.Errorf("Compartment %s has a blocklisted descendant", *r.compartment.Id)
	}
	return nil
}

// SafetyTags satisfies ocinuke.SafetyEvaluated, returning this resource's freeform tags,
// its already-flattened ("<namespace>.<key>") defined tags, and its creation time -- the
// three values ocinuke.Evaluate needs beyond compartment/resource identity (both already
// available via GetCompartmentID/UniqueKey). TimeCreated is mandatory:"true" on
// identity.Compartment (verified this session), matching every other type's direct-dereference
// convention -- no timeCreatedOrZero nil-guard needed.
func (r *Compartment) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	x := r.compartment
	return x.FreeformTags, flattenDefinedTags(x.DefinedTags), x.TimeCreated.Time
}

// Remove calls DeleteCompartment unconditionally and returns as soon as OCI accepts the call --
// it NEVER blocks waiting for the emptiness check, and a nil return here means only "accepted,"
// never "empty." The emptiness check happens inside the async work request DeleteCompartment
// hands back, discovered later exclusively via HandleWait's GetWorkRequest poll (06-RESEARCH.md
// Contradiction 1) -- Remove() itself has no synchronous refusal to key off, even for an obviously
// full compartment.
//
// A non-nil err from the SDK call itself (malformed OCID, permission failure) is returned as-is:
// a plain error, routing to ItemStateFailed's genuinely-non-retryable path (06-RESEARCH.md Open
// Question 2's adopted recommendation -- no distinct-error-code detection was added).
func (r *Compartment) Remove(ctx context.Context) error {
	if err := r.waitUntilEmpty(ctx); err != nil {
		return holdOn409(err)
	}

	resp, err := r.client.DeleteCompartment(ctx, identity.DeleteCompartmentRequest{CompartmentId: r.compartment.Id})
	if err != nil {
		return holdOn409(err)
	}
	if resp.OpcWorkRequestId == nil {
		// Defensive: a well-formed DeleteCompartment success should always carry a work request
		// OCID (it is how every later round finds out what happened). Never seen in practice, but
		// silently proceeding with an empty workRequestID would make HandleWait poll against "",
		// which is worse than failing loudly here.
		return fmt.Errorf("DeleteCompartment succeeded but returned no work request id for %s", *r.compartment.Id)
	}
	r.workRequestID = *resp.OpcWorkRequestId
	return nil
}

// waitUntilEmpty keeps DeleteCompartment from being called while the compartment cannot be empty.
// Without it, a compartment still being emptied by this run, or holding something that outlives
// the run, gets one doomed work request after another until --max-wait-retries runs out.
//
// While anything else in the compartment is still in flight in this run it holds, without an API
// call. Once nothing is, residue -- something the run reported it will not remove (a scheduled
// deletion, a protected or too-young resource, backup residue), a config-filtered or failed item,
// or a child compartment that is not DELETED -- makes it refuse. The refusal is a plain error, so
// libnuke stops after its failed-item rounds instead of waiting out the budget, and it is reported
// once as compartment-not-empty naming the residue. A later run deletes the compartment.
//
// When it does call DeleteCompartment and OCI's work request fails, HandleWait decides whether to
// try again. If the run never had anything in flight here, the refusal is final for this run.
//
// A nil occupancy (a Compartment built outside a Nuke, as tests do) skips the check.
func (r *Compartment) waitUntilEmpty(ctx context.Context) error {
	if r.occupancy == nil {
		return nil
	}
	if r.refusedByOCI {
		return r.notEmpty
	}

	occ := r.occupancy()
	if len(occ.InFlight) > 0 {
		r.waitedForRun = true
		return liberrors.ErrHoldResource(fmt.Sprintf(
			"waiting for %d resource(s) in this compartment to finish deleting", len(occ.InFlight)))
	}

	children, err := r.liveChildren(ctx)
	if err != nil {
		return err
	}
	occ.Residue = append(occ.Residue, children...)
	if len(occ.Residue) == 0 {
		r.notEmpty = nil
		return nil
	}

	detail := "still holds " + strings.Join(occ.Residue, "; ") + "; a later run deletes it once they are gone"
	r.refuse(detail)
	return r.notEmpty
}

// refuse records why the compartment stays, and reports it the first time.
func (r *Compartment) refuse(detail string) {
	r.workRequestID = ""
	r.notEmpty = fmt.Errorf("compartment not empty: %s", detail)
	if r.notEmptyReported {
		return
	}
	r.notEmptyReported = true
	ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
		Reason:        scope.ReasonCompartmentNotEmpty,
		ResourceType:  CompartmentResourceType,
		ResourceID:    *r.compartment.Id,
		CompartmentID: *r.compartment.Id,
		Detail:        detail,
	})
}

// liveChildren lists this compartment's direct children that OCI has not finished deleting. A
// child's own Nuke has already run by now (deepest-first), so anything still here stays.
func (r *Compartment) liveChildren(ctx context.Context) ([]string, error) {
	var out []string
	req := identity.ListCompartmentsRequest{CompartmentId: r.compartment.Id}
	for {
		resp, err := r.client.ListCompartments(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing child compartments of %s: %w", *r.compartment.Id, err)
		}
		for i := range resp.Items {
			child := resp.Items[i]
			if child.LifecycleState == identity.CompartmentLifecycleStateDeleted {
				continue
			}
			out = append(out, fmt.Sprintf("Compartment %s (%s, %s)",
				derefOr(child.Id, "unknown"), derefOr(child.Name, "unnamed"), child.LifecycleState))
		}
		if resp.OpcNextPage == nil {
			return out, nil
		}
		req.Page = resp.OpcNextPage
	}
}

// HandleWait satisfies resource.HandleWaitHook -- the phase's central, genuinely novel mechanic
// (06-RESEARCH.md, no local analog before Plan 06-01's fault-injection proof). libnuke's own
// n.HandleWait calls this on item.Resource, the SAME struct instance Remove() populated, never a
// freshly List()-produced copy -- the only mechanism that lets workRequestID (set by Remove())
// survive into a later poll round. Filter() cannot do this job: libnuke's fallback path calls
// Filter() on a fresh List() result, which has no access to anything Remove() recorded
// (06-RESEARCH.md Contradiction 2).
//
// ACCEPTED/IN_PROGRESS keep the item ItemStateWaiting (liberrors.ErrWaitResource), bounded by
// MaxWaitRetries, never counted toward libnuke's handleFailure hard-abort. SUCCEEDED returns nil,
// falling through to libnuke's own List()/Filter() re-check, which now finds the compartment
// DELETED and Filter() excludes it -> ItemStateFinished. FAILED returns a plain error carrying
// OCI's own error text (COMP-03's Detail requirement) -> ItemStateFailed -> on the very next
// HandleQueue pass, the ItemStateFailed branch re-issues DeleteCompartment in the same pass
// (Remove() always succeeds synchronously when the call itself is well-formed), so the item never
// sits in ItemStateFailed across two consecutive rounds and handleFailure's failedCount keeps
// resetting to 0 instead of accumulating toward its abort threshold -- proven against a real
// libnuke.Nuke.Run() by Plan 06-01's resources_test/compartment_fault_injection_test.go. Any
// other/unknown status fails toward waiting (liberrors.ErrWaitResource), never toward a false
// Failed.
func (r *Compartment) HandleWait(ctx context.Context) error {
	// HandleQueue calls this right after Remove() on a Failed item. When waitUntilEmpty refused,
	// there is no work request to poll, and its reason must stay the item's reason.
	if r.workRequestID == "" {
		if r.notEmpty != nil {
			return r.notEmpty
		}
		return fmt.Errorf("no compartment delete work request to poll")
	}

	resp, err := r.client.GetWorkRequest(ctx, identity.GetWorkRequestRequest{WorkRequestId: &r.workRequestID})
	if err != nil {
		return err
	}

	switch resp.Status {
	case identity.WorkRequestStatusAccepted, identity.WorkRequestStatusInProgress:
		// Reported every poll round, not only on the last one: nothing here can tell which round
		// is the last, and MergeSkipEvents overwrites rather than accumulates. Without it a run
		// that exhausts its wait budget mid-deletion reports compartment-not-empty -- honest,
		// and the wrong thing (NR-787).
		// Id is read defensively rather than dereferenced: List() always sets it, but HandleWait
		// is reachable from a unit test that constructs only client and workRequestID, and a
		// leftover report is not worth a panic.
		if r.compartment.Id != nil {
			ocinuke.ReportLeftover(ocinuke.CurrentReporter, &scope.SkipEvent{
				Reason:        scope.ReasonCompartmentStillDeleting,
				ResourceType:  CompartmentResourceType,
				ResourceID:    *r.compartment.Id,
				CompartmentID: *r.compartment.Id,
				Detail: "delete work request " + r.workRequestID + " is " +
					string(resp.Status) + ", not finished",
			})
		}
		return liberrors.ErrWaitResource("compartment delete work request in progress")
	case identity.WorkRequestStatusSucceeded:
		return nil
	case identity.WorkRequestStatusFailed:
		message := "no reason given"
		if len(resp.Errors) > 0 && resp.Errors[0].Message != nil {
			message = *resp.Errors[0].Message
		}
		// Nothing of this run's was in the compartment, so nothing is about to leave it either:
		// what OCI found is outside the run's reach (another region, an unsupported type), and
		// asking again only repeats the refusal until the wait budget is gone.
		if r.occupancy != nil && !r.waitedForRun {
			r.refusedByOCI = true
			r.refuse("OCI refused the delete: " + message + "; a later run tries again")
			return r.notEmpty
		}
		return fmt.Errorf("compartment delete work request failed: %s", message)
	default:
		return liberrors.ErrWaitResource("compartment delete work request status " + string(resp.Status))
	}
}

// Properties exposes the filter vocabulary this type supports, built from resources/support.go's
// shared baseProperties helper. Passing x.CompartmentId (the PARENT's OCID) here is CORRECT and a
// DIFFERENT concern from GetCompartmentID()/UniqueKey() above: Properties()'s compartment_id
// filterable property means "where does this resource live," which for a compartment genuinely is
// its parent, while GetCompartmentID() means "what is this resource's own scope identity for
// re-verification," which must be the compartment's own OCID. Do not conflate the two, and do not
// "fix" one to match the other.
func (r *Compartment) Properties() types.Properties {
	x := r.compartment
	return baseProperties(x.Id, x.Name, x.CompartmentId, string(x.LifecycleState), x.TimeCreated, x.FreeformTags, x.DefinedTags)
}
