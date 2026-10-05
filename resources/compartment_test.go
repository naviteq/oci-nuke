package resources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// testCompartmentOwnOCID/testCompartmentParentOCID are two DELIBERATELY different literal OCIDs --
// TestCompartment_GetCompartmentIDAndUniqueKey_UseOwnOCIDNotParent and
// TestCompartment_Properties_SetsParentCompartmentID both depend on these never colliding, since
// together they prove GetCompartmentID()/UniqueKey() and Properties() intentionally diverge on
// which OCID they return.
const (
	testCompartmentOwnOCID    = "ocid1.compartment.oc1..own"
	testCompartmentParentOCID = "ocid1.compartment.oc1..parent"

	testWorkRequestOCID = "ocid1.workrequest.oc1..wr"
)

// stubCompartmentClient implements compartmentClient against in-memory data -- zero network
// access. Each function field defaults to a reasonable success stub when nil, mirroring
// stubVaultClient's shape.
type stubCompartmentClient struct {
	getCompartmentFn    func(ctx context.Context, req identity.GetCompartmentRequest) (identity.GetCompartmentResponse, error)
	deleteCompartmentFn func(ctx context.Context, req identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error)
	getWorkRequestFn    func(ctx context.Context, req identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error)
	listCompartmentsFn  func(ctx context.Context, req identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error)
}

func (s *stubCompartmentClient) ListCompartments(
	ctx context.Context, req identity.ListCompartmentsRequest,
) (identity.ListCompartmentsResponse, error) {
	if s.listCompartmentsFn != nil {
		return s.listCompartmentsFn(ctx, req)
	}
	return identity.ListCompartmentsResponse{}, nil
}

func (s *stubCompartmentClient) GetCompartment(
	ctx context.Context, req identity.GetCompartmentRequest,
) (identity.GetCompartmentResponse, error) {
	if s.getCompartmentFn != nil {
		return s.getCompartmentFn(ctx, req)
	}
	return identity.GetCompartmentResponse{}, nil
}

func (s *stubCompartmentClient) DeleteCompartment(
	ctx context.Context, req identity.DeleteCompartmentRequest,
) (identity.DeleteCompartmentResponse, error) {
	if s.deleteCompartmentFn != nil {
		return s.deleteCompartmentFn(ctx, req)
	}
	wrID := testWorkRequestOCID
	return identity.DeleteCompartmentResponse{OpcWorkRequestId: &wrID}, nil
}

func (s *stubCompartmentClient) GetWorkRequest(
	ctx context.Context, req identity.GetWorkRequestRequest,
) (identity.GetWorkRequestResponse, error) {
	if s.getWorkRequestFn != nil {
		return s.getWorkRequestFn(ctx, req)
	}
	return identity.GetWorkRequestResponse{
		WorkRequest: identity.WorkRequest{Status: identity.WorkRequestStatusSucceeded},
	}, nil
}

// TestCompartment_GetCompartmentIDAndUniqueKey_UseOwnOCIDNotParent is the literal
// inversion-bug regression test T-06-02-01 requires: constructs a Compartment with Id and
// CompartmentId set to two DIFFERENT literal OCIDs, asserts both GetCompartmentID() and
// UniqueKey() return the Id value, never the CompartmentId value.
func TestCompartment_GetCompartmentIDAndUniqueKey_UseOwnOCIDNotParent(t *testing.T) {
	own := testCompartmentOwnOCID
	parent := testCompartmentParentOCID
	r := &Compartment{compartment: identity.Compartment{Id: &own, CompartmentId: &parent}}

	if got := r.GetCompartmentID(); got != own {
		t.Errorf("GetCompartmentID() = %q, want %q (own OCID, never parent %q)", got, own, parent)
	}
	if got := r.UniqueKey(); got != own {
		t.Errorf("UniqueKey() = %q, want %q (own OCID, never parent %q)", got, own, parent)
	}
}

// TestCompartment_Filter_TableDriven is table-driven over every
// identity.CompartmentLifecycleStateEnum value -- Creating/Active/Inactive/Deleting are present,
// Deleted is excluded (never reported), with hasBlocklistedDescendant left false throughout.
func TestCompartment_Filter_TableDriven(t *testing.T) {
	tests := []struct {
		state   identity.CompartmentLifecycleStateEnum
		present bool
	}{
		{identity.CompartmentLifecycleStateCreating, true},
		{identity.CompartmentLifecycleStateActive, true},
		{identity.CompartmentLifecycleStateInactive, true},
		{identity.CompartmentLifecycleStateDeleting, true},
		{identity.CompartmentLifecycleStateDeleted, false},
	}

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	for _, tc := range tests {
		id := testCompartmentOwnOCID
		r := &Compartment{compartment: identity.Compartment{Id: &id, LifecycleState: tc.state}}
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil", tc.state)
		}
	}
}

// TestCompartment_Filter_BlocklistedDescendant_ReportsCompartmentNotEmpty proves a compartment
// with hasBlocklistedDescendant true is excluded AND reported via ocinuke.ReportLeftover with
// scope.ReasonCompartmentNotEmpty, for any present LifecycleState.
func TestCompartment_Filter_BlocklistedDescendant_ReportsCompartmentNotEmpty(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testCompartmentOwnOCID
	r := &Compartment{
		compartment:              identity.Compartment{Id: &id, LifecycleState: identity.CompartmentLifecycleStateActive},
		hasBlocklistedDescendant: true,
	}

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with a blocklisted descendant = nil, want non-nil (must be excluded)")
	}
	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonCompartmentNotEmpty {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonCompartmentNotEmpty)
	}
	if got[0].ResourceType != CompartmentResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, CompartmentResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != id {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, id)
	}
}

// TestCompartment_Remove_RecordsWorkRequestID proves Remove() returns nil on a synchronous accept
// and records the returned OpcWorkRequestId onto the struct's own workRequestID field.
func TestCompartment_Remove_RecordsWorkRequestID(t *testing.T) {
	id := testCompartmentOwnOCID
	wantWorkRequestID := "ocid1.workrequest.oc1..custom"
	stub := &stubCompartmentClient{
		deleteCompartmentFn: func(_ context.Context, _ identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error) {
			wr := wantWorkRequestID
			return identity.DeleteCompartmentResponse{OpcWorkRequestId: &wr}, nil
		},
	}
	r := &Compartment{client: stub, compartment: identity.Compartment{Id: &id}}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if r.workRequestID != wantWorkRequestID {
		t.Errorf("workRequestID = %q, want %q", r.workRequestID, wantWorkRequestID)
	}
}

// TestCompartment_Remove_PropagatesSDKError proves a non-nil DeleteCompartment SDK error is
// returned as-is by Remove(), never masked or swallowed.
func TestCompartment_Remove_PropagatesSDKError(t *testing.T) {
	id := testCompartmentOwnOCID
	wantErr := errors.New("permission denied")
	stub := &stubCompartmentClient{
		deleteCompartmentFn: func(_ context.Context, _ identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error) {
			return identity.DeleteCompartmentResponse{}, wantErr
		},
	}
	r := &Compartment{client: stub, compartment: identity.Compartment{Id: &id}}

	err := r.Remove(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("Remove() error = %v, want exactly %v", err, wantErr)
	}
}

// TestCompartment_HandleWait_AcceptedAndInProgressReturnErrWaitResource proves both ACCEPTED and
// IN_PROGRESS work-request statuses return an error satisfying errors.As into
// liberrors.ErrWaitResource.
func TestCompartment_HandleWait_AcceptedAndInProgressReturnErrWaitResource(t *testing.T) {
	for _, status := range []identity.WorkRequestStatusEnum{
		identity.WorkRequestStatusAccepted,
		identity.WorkRequestStatusInProgress,
	} {
		stub := &stubCompartmentClient{
			getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
				return identity.GetWorkRequestResponse{WorkRequest: identity.WorkRequest{Status: status}}, nil
			},
		}
		r := &Compartment{client: stub, workRequestID: testWorkRequestOCID}

		err := r.HandleWait(context.Background())
		var waitErr liberrors.ErrWaitResource
		if !errors.As(err, &waitErr) {
			t.Errorf("HandleWait() with status %s error = %v, want an error satisfying errors.As into liberrors.ErrWaitResource", status, err)
		}
	}
}

// TestCompartment_HandleWait_SucceededReturnsNil proves a SUCCEEDED work-request status returns
// nil, falling through to libnuke's own List()/Filter() re-check.
func TestCompartment_HandleWait_SucceededReturnsNil(t *testing.T) {
	stub := &stubCompartmentClient{
		getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
			return identity.GetWorkRequestResponse{
				WorkRequest: identity.WorkRequest{Status: identity.WorkRequestStatusSucceeded},
			}, nil
		},
	}
	r := &Compartment{client: stub, workRequestID: testWorkRequestOCID}

	if err := r.HandleWait(context.Background()); err != nil {
		t.Errorf("HandleWait() error = %v, want nil", err)
	}
}

// TestCompartment_HandleWait_FailedReturnsPlainErrorWithOCIMessage proves a FAILED work-request
// status returns a plain, non-liberrors.ErrWaitResource error whose message contains OCI's own
// error text -- proving it routes to ItemStateFailed, not ItemStateWaiting.
func TestCompartment_HandleWait_FailedReturnsPlainErrorWithOCIMessage(t *testing.T) {
	wantMessage := "compartment is not empty: 3 resources remain"
	stub := &stubCompartmentClient{
		getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
			return identity.GetWorkRequestResponse{
				WorkRequest: identity.WorkRequest{
					Status: identity.WorkRequestStatusFailed,
					Errors: []identity.WorkRequestError{
						{Message: common.String(wantMessage)},
					},
				},
			}, nil
		},
	}
	r := &Compartment{client: stub, workRequestID: testWorkRequestOCID}

	err := r.HandleWait(context.Background())
	if err == nil {
		t.Fatal("HandleWait() error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, wantMessage) {
		t.Errorf("HandleWait() error = %q, want it to contain %q", got, wantMessage)
	}
	var waitErr liberrors.ErrWaitResource
	if errors.As(err, &waitErr) {
		t.Errorf("HandleWait() error %v satisfies errors.As into liberrors.ErrWaitResource, "+
			"want it not to (must route to ItemStateFailed, not ItemStateWaiting)", err)
	}
}

// TestCompartment_SafetyTags_ReturnsFreeformDefinedAndCreatedAt proves the three return values
// match the underlying compartment fields exactly.
func TestCompartment_SafetyTags_ReturnsFreeformDefinedAndCreatedAt(t *testing.T) {
	id := testCompartmentOwnOCID
	timeCreated := common.SDKTime{Time: time.Now()}
	r := &Compartment{compartment: identity.Compartment{
		Id:           &id,
		TimeCreated:  &timeCreated,
		FreeformTags: map[string]string{testFreeformTagKey: testFreeformTagValue},
		DefinedTags: map[string]map[string]interface{}{
			testDefinedTagNamespace: {testDefinedTagKey: testDefinedTagFlatValue},
		},
	}}

	freeform, defined, createdAt := r.SafetyTags()
	if freeform[testFreeformTagKey] != testFreeformTagValue {
		t.Errorf("SafetyTags() freeform[%q] = %q, want %q", testFreeformTagKey, freeform[testFreeformTagKey], testFreeformTagValue)
	}
	if defined["ns.key"] != testDefinedTagFlatValue {
		t.Errorf("SafetyTags() defined[%q] = %q, want %q", "ns.key", defined["ns.key"], testDefinedTagFlatValue)
	}
	if !createdAt.Equal(timeCreated.Time) {
		t.Errorf("SafetyTags() createdAt = %v, want %v", createdAt, timeCreated.Time)
	}
}

// TestCompartment_Properties_SetsParentCompartmentID proves Properties()'s compartment_id key
// equals the PARENT's OCID (compartment.CompartmentId) -- the opposite assertion from
// TestCompartment_GetCompartmentIDAndUniqueKey_UseOwnOCIDNotParent, both of which must pass
// simultaneously since Properties() and GetCompartmentID() deliberately diverge.
func TestCompartment_Properties_SetsParentCompartmentID(t *testing.T) {
	own := testCompartmentOwnOCID
	parent := testCompartmentParentOCID
	name := "test-compartment"
	r := &Compartment{compartment: identity.Compartment{
		Id: &own, CompartmentId: &parent, Name: &name,
		LifecycleState: identity.CompartmentLifecycleStateActive,
	}}

	props := r.Properties()
	if got := props.Get(propCompartmentID); got != parent {
		t.Errorf("Properties()[%q] = %q, want %q (the PARENT's OCID, never %q)", propCompartmentID, got, parent, own)
	}
	if got := props.Get(propID); got != own {
		t.Errorf("Properties()[%q] = %q, want %q", propID, got, own)
	}
}

// TestCompartment_HandleWait_InProgressReportsStillDeleting proves NR-787's naming fix: while the
// delete work request is unfinished, the compartment reports compartment-still-deleting through
// the ReportLeftover side channel, so a run that exhausts its wait budget mid-deletion says "not
// finished" instead of "not empty". Observed live: a run gave up at "max wait retries of 200
// exceeded" reporting compartment-not-empty, and the compartment reached DELETED three minutes
// later -- honest, and the wrong thing to send an operator looking for.
func TestCompartment_HandleWait_InProgressReportsStillDeleting(t *testing.T) {
	for _, status := range []identity.WorkRequestStatusEnum{
		identity.WorkRequestStatusAccepted,
		identity.WorkRequestStatusInProgress,
	} {
		t.Run(string(status), func(t *testing.T) {
			var reported []scope.SkipEvent
			restore := ocinuke.SetRunContext(
				func(string) bool { return true },
				func(e *scope.SkipEvent) { reported = append(reported, *e) },
			)
			defer restore()

			id := testCompartmentOwnOCID
			stub := &stubCompartmentClient{
				getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
					return identity.GetWorkRequestResponse{WorkRequest: identity.WorkRequest{Status: status}}, nil
				},
			}
			r := &Compartment{
				client:        stub,
				compartment:   identity.Compartment{Id: &id},
				workRequestID: testWorkRequestOCID,
			}

			if err := r.HandleWait(context.Background()); err == nil {
				t.Fatalf("HandleWait() with status %s returned nil; the item must stay waiting", status)
			}

			if len(reported) != 1 {
				t.Fatalf("HandleWait() reported %d leftovers, want 1: %+v", len(reported), reported)
			}
			if reported[0].Reason != scope.ReasonCompartmentStillDeleting {
				t.Errorf("reported reason = %q, want %q -- compartment-not-empty would send the "+
					"operator looking for contents that are already gone",
					reported[0].Reason, scope.ReasonCompartmentStillDeleting)
			}
			if reported[0].ResourceID != id {
				t.Errorf("reported resource id = %q, want %q", reported[0].ResourceID, id)
			}
			if !strings.Contains(reported[0].Detail, string(status)) {
				t.Errorf("reported detail = %q, want it to name the work-request status %q",
					reported[0].Detail, status)
			}
			if !strings.Contains(reported[0].Detail, testWorkRequestOCID) {
				t.Errorf("reported detail = %q, want it to name the work request %q",
					reported[0].Detail, testWorkRequestOCID)
			}
		})
	}
}

// TestCompartment_HandleWait_SucceededReportsNothing is the non-vacuousness control: a finished
// work request must not leave a still-deleting leftover behind for MergeSkipEvents to stamp onto
// a compartment that is, in fact, gone.
func TestCompartment_HandleWait_SucceededReportsNothing(t *testing.T) {
	var reported []scope.SkipEvent
	restore := ocinuke.SetRunContext(
		func(string) bool { return true },
		func(e *scope.SkipEvent) { reported = append(reported, *e) },
	)
	defer restore()

	id := testCompartmentOwnOCID
	stub := &stubCompartmentClient{
		getWorkRequestFn: func(_ context.Context, _ identity.GetWorkRequestRequest) (identity.GetWorkRequestResponse, error) {
			return identity.GetWorkRequestResponse{
				WorkRequest: identity.WorkRequest{Status: identity.WorkRequestStatusSucceeded},
			}, nil
		},
	}
	r := &Compartment{
		client:        stub,
		compartment:   identity.Compartment{Id: &id},
		workRequestID: testWorkRequestOCID,
	}

	if err := r.HandleWait(context.Background()); err != nil {
		t.Fatalf("HandleWait() error = %v, want nil", err)
	}
	if len(reported) != 0 {
		t.Errorf("a SUCCEEDED work request reported %d leftovers, want none: %+v", len(reported), reported)
	}
}

// gatedCompartment builds a Compartment whose Remove() goes through waitUntilEmpty, counting the
// calls it makes to OCI.
func gatedCompartment(occ ocinuke.Occupancy, children []identity.Compartment) (r *Compartment, deletes, lists *int) {
	deletes, lists = new(int), new(int)
	id := testCompartmentOwnOCID
	client := &stubCompartmentClient{
		deleteCompartmentFn: func(context.Context, identity.DeleteCompartmentRequest) (identity.DeleteCompartmentResponse, error) {
			*deletes++
			wrID := testWorkRequestOCID
			return identity.DeleteCompartmentResponse{OpcWorkRequestId: &wrID}, nil
		},
		listCompartmentsFn: func(context.Context, identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error) {
			*lists++
			return identity.ListCompartmentsResponse{Items: children}, nil
		},
	}
	r = &Compartment{
		client:      client,
		compartment: identity.Compartment{Id: &id},
		occupancy:   func() ocinuke.Occupancy { return occ },
	}
	return r, deletes, lists
}

// TestCompartment_Remove_HoldsWhileTheRunIsStillEmptyingIt: nothing is asked of OCI while other
// resources of the compartment are still in flight, and the item holds rather than fails.
func TestCompartment_Remove_HoldsWhileTheRunIsStillEmptyingIt(t *testing.T) {
	r, deletes, lists := gatedCompartment(ocinuke.Occupancy{InFlight: []string{"Subnet ocid1.subnet.oc1..a"}}, nil)

	err := r.Remove(context.Background())

	var hold liberrors.ErrHoldResource
	if !errors.As(err, &hold) {
		t.Fatalf("Remove() = %v, want ErrHoldResource", err)
	}
	if *deletes != 0 || *lists != 0 {
		t.Errorf("DeleteCompartment called %d, ListCompartments %d times; want neither", *deletes, *lists)
	}
}

// TestCompartment_Remove_RefusesOverResidue: once nothing is in flight, known residue means no
// DeleteCompartment, a plain error that stays the item's reason through HandleWait, and exactly
// one compartment-not-empty report however many rounds ask again.
func TestCompartment_Remove_RefusesOverResidue(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	residue := "Vault ocid1.vault.oc1..v (scheduled-deletion: vault already scheduled for deletion)"
	r, deletes, _ := gatedCompartment(ocinuke.Occupancy{Residue: []string{residue}}, nil)

	for round := 0; round < 3; round++ {
		err := r.Remove(context.Background())
		var hold liberrors.ErrHoldResource
		if err == nil || errors.As(err, &hold) {
			t.Fatalf("round %d: Remove() = %v, want a plain error", round, err)
		}
		if waitErr := r.HandleWait(context.Background()); waitErr == nil || waitErr.Error() != err.Error() {
			t.Fatalf("round %d: HandleWait() = %v, want Remove()'s error %v", round, waitErr, err)
		}
	}

	if *deletes != 0 {
		t.Errorf("DeleteCompartment called %d times, want 0", *deletes)
	}
	if len(got) != 1 {
		t.Fatalf("reported %d events, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonCompartmentNotEmpty || !strings.Contains(got[0].Detail, residue) {
		t.Errorf("event = %+v, want compartment-not-empty naming %q", got[0], residue)
	}
}

// TestCompartment_Remove_RefusesOverALiveChild: a child compartment that is not DELETED is
// residue; a DELETED one is not.
func TestCompartment_Remove_RefusesOverALiveChild(t *testing.T) {
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	liveID, liveName := "ocid1.compartment.oc1..live", "kept"
	goneID, goneName := "ocid1.compartment.oc1..gone", "gone"
	children := []identity.Compartment{
		{Id: &liveID, Name: &liveName, LifecycleState: identity.CompartmentLifecycleStateActive},
		{Id: &goneID, Name: &goneName, LifecycleState: identity.CompartmentLifecycleStateDeleted},
	}
	r, deletes, _ := gatedCompartment(ocinuke.Occupancy{}, children)

	err := r.Remove(context.Background())
	if err == nil || !strings.Contains(err.Error(), liveID) || strings.Contains(err.Error(), goneID) {
		t.Fatalf("Remove() = %v, want an error naming %s and not %s", err, liveID, goneID)
	}
	if *deletes != 0 {
		t.Errorf("DeleteCompartment called %d times, want 0", *deletes)
	}
}

// TestCompartment_Remove_DeletesOnceEmpty: no in-flight work, no residue, no live child -- the
// delete goes out as before.
func TestCompartment_Remove_DeletesOnceEmpty(t *testing.T) {
	r, deletes, _ := gatedCompartment(ocinuke.Occupancy{}, nil)

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() = %v, want nil", err)
	}
	if *deletes != 1 || r.workRequestID != testWorkRequestOCID {
		t.Errorf("DeleteCompartment called %d times, workRequestID %q; want 1 and %q", *deletes, r.workRequestID, testWorkRequestOCID)
	}
}

// failingWorkRequests makes every DeleteCompartment work request of r end FAILED.
func failingWorkRequests(r *Compartment) {
	msg := "compartment has resources in il-jerusalem-1"
	r.client.(*stubCompartmentClient).getWorkRequestFn = func(
		context.Context, identity.GetWorkRequestRequest,
	) (identity.GetWorkRequestResponse, error) {
		return identity.GetWorkRequestResponse{WorkRequest: identity.WorkRequest{
			Status: identity.WorkRequestStatusFailed,
			Errors: []identity.WorkRequestError{{Message: &msg}},
		}}, nil
	}
}

// TestCompartment_FailedDelete_IsFinalWhenTheRunHadNothingThere: OCI's refusal of a compartment
// the run never had to wait for is reported once and not asked again.
func TestCompartment_FailedDelete_IsFinalWhenTheRunHadNothingThere(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) { got = append(got, evt) })
	defer restore()

	r, deletes, _ := gatedCompartment(ocinuke.Occupancy{}, nil)
	failingWorkRequests(r)

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() = %v, want nil", err)
	}
	for round := 0; round < 3; round++ {
		if err := r.HandleWait(context.Background()); err == nil || !strings.Contains(err.Error(), "il-jerusalem-1") {
			t.Fatalf("round %d: HandleWait() = %v, want OCI's reason", round, err)
		}
		if err := r.Remove(context.Background()); err == nil {
			t.Fatalf("round %d: Remove() = nil, want the refusal again", round)
		}
	}

	if *deletes != 1 {
		t.Errorf("DeleteCompartment called %d times, want 1", *deletes)
	}
	if len(got) != 1 || got[0].Reason != scope.ReasonCompartmentNotEmpty {
		t.Errorf("events = %+v, want one compartment-not-empty", got)
	}
}

// TestCompartment_FailedDelete_IsRetriedAfterTheRunEmptiedIt: when the run was still emptying the
// compartment, a failed delete may be OCI catching up, so it is issued again.
func TestCompartment_FailedDelete_IsRetriedAfterTheRunEmptiedIt(t *testing.T) {
	occ := ocinuke.Occupancy{InFlight: []string{"Subnet s"}}
	r, deletes, _ := gatedCompartment(occ, nil)
	r.occupancy = func() ocinuke.Occupancy { return occ }
	failingWorkRequests(r)

	if err := r.Remove(context.Background()); err == nil {
		t.Fatal("Remove() = nil while a subnet is in flight, want a hold")
	}
	occ = ocinuke.Occupancy{}
	for round := 0; round < 2; round++ {
		if err := r.Remove(context.Background()); err != nil {
			t.Fatalf("round %d: Remove() = %v, want nil", round, err)
		}
		if err := r.HandleWait(context.Background()); err == nil {
			t.Fatalf("round %d: HandleWait() = nil, want the failure", round)
		}
	}

	if *deletes != 2 {
		t.Errorf("DeleteCompartment called %d times, want 2", *deletes)
	}
}
