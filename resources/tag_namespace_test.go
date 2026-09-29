package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// stepUpdate/stepCascade are the two callOrder labels stubTagNamespaceClient records, in the
// order Remove() invokes them, so TestTagNamespace_Remove_* can assert the exact two-step
// ordering (goconst, min-occurrences 3 -- each label is compared against in three or more
// places below).
const (
	stepUpdate  = "update"
	stepCascade = "cascade"
)

// stubTagNamespaceClient implements tagNamespaceClient against in-memory data -- zero network
// access. callOrder records stepUpdate/stepCascade in the order Remove() invokes them, so
// TestTagNamespace_Remove_* can assert the exact two-step ordering.
type stubTagNamespaceClient struct {
	items      []identity.TagNamespaceSummary
	callOrder  []string
	updateErr  error
	cascadeErr error
	listErr    error
}

func (s *stubTagNamespaceClient) ListTagNamespaces(
	_ context.Context,
	_ identity.ListTagNamespacesRequest,
) (identity.ListTagNamespacesResponse, error) {
	if s.listErr != nil {
		return identity.ListTagNamespacesResponse{}, s.listErr
	}
	return identity.ListTagNamespacesResponse{Items: s.items}, nil
}

func (s *stubTagNamespaceClient) UpdateTagNamespace(
	_ context.Context,
	_ identity.UpdateTagNamespaceRequest,
) (identity.UpdateTagNamespaceResponse, error) {
	s.callOrder = append(s.callOrder, stepUpdate)
	if s.updateErr != nil {
		return identity.UpdateTagNamespaceResponse{}, s.updateErr
	}
	return identity.UpdateTagNamespaceResponse{}, nil
}

func (s *stubTagNamespaceClient) CascadeDeleteTagNamespace(
	_ context.Context,
	_ identity.CascadeDeleteTagNamespaceRequest,
) (identity.CascadeDeleteTagNamespaceResponse, error) {
	s.callOrder = append(s.callOrder, stepCascade)
	if s.cascadeErr != nil {
		return identity.CascadeDeleteTagNamespaceResponse{}, s.cascadeErr
	}
	return identity.CascadeDeleteTagNamespaceResponse{}, nil
}

// TestTagNamespaceLister_List proves tagNamespaceList wraps every returned item, without ever
// constructing a real Identity client.
func TestTagNamespaceLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	name := "test-namespace"

	stub := &stubTagNamespaceClient{
		items: []identity.TagNamespaceSummary{
			{Id: &id, CompartmentId: &compartmentID, Name: &name, LifecycleState: identity.TagNamespaceLifecycleStateActive},
		},
	}

	got, err := tagNamespaceList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("tagNamespaceList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("tagNamespaceList() returned %d resources, want 1", len(got))
	}
	ns, ok := got[0].(*TagNamespace)
	if !ok {
		t.Fatalf("tagNamespaceList()[0] is %T, want *TagNamespace", got[0])
	}
	if ns.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", ns.GetCompartmentID(), compartmentID)
	}
}

// TestTagNamespace_GetCompartmentID_NilSafe proves GetCompartmentID returns "" (not a panic) when
// the wrapped summary's CompartmentId is nil -- every field on TagNamespaceSummary is
// mandatory:"false".
func TestTagNamespace_GetCompartmentID_NilSafe(t *testing.T) {
	r := &TagNamespace{}
	if got := r.GetCompartmentID(); got != "" {
		t.Errorf("GetCompartmentID() with nil CompartmentId = %q, want \"\"", got)
	}
}

// TestTagNamespace_UniqueKey_NilSafe proves UniqueKey returns "" (not a panic) when the wrapped
// summary's Id is nil.
func TestTagNamespace_UniqueKey_NilSafe(t *testing.T) {
	r := &TagNamespace{}
	if got := r.UniqueKey(); got != "" {
		t.Errorf("UniqueKey() with nil Id = %q, want \"\"", got)
	}
}

// TestTagNamespace_Filter proves ACTIVE and INACTIVE are both present (05-05-PLAN.md deviation:
// the real SDK has no RETIRING lifecycle value; retiring flips IsRetired while LifecycleState
// becomes INACTIVE, per identity.TagNamespaceSummary's own doc comment), and DELETING/DELETED/an
// unrecognized future value are all excluded.
func TestTagNamespace_Filter(t *testing.T) {
	tests := []struct {
		state   identity.TagNamespaceLifecycleStateEnum
		present bool
	}{
		{identity.TagNamespaceLifecycleStateActive, true},
		{identity.TagNamespaceLifecycleStateInactive, true},
		{identity.TagNamespaceLifecycleStateDeleting, false},
		{identity.TagNamespaceLifecycleStateDeleted, false},
		{identity.TagNamespaceLifecycleStateEnum(""), false},
		{identity.TagNamespaceLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &TagNamespace{}
		r.ns.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %q = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %q = nil, want non-nil", tc.state)
		}
	}
}

// TestTagNamespace_Remove_RetiresThenCascadeDeletesWhenNotRetired proves Remove() calls
// UpdateTagNamespace (to set IsRetired: true) BEFORE CascadeDeleteTagNamespace when IsRetired is
// nil.
func TestTagNamespace_Remove_RetiresThenCascadeDeletesWhenNotRetired(t *testing.T) {
	id := testResourceOCID
	stub := &stubTagNamespaceClient{}
	r := &TagNamespace{client: stub, ns: identity.TagNamespaceSummary{Id: &id}}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	want := []string{stepUpdate, stepCascade}
	if len(stub.callOrder) != 2 || stub.callOrder[0] != want[0] || stub.callOrder[1] != want[1] {
		t.Fatalf("callOrder = %v, want %v", stub.callOrder, want)
	}
}

// TestTagNamespace_Remove_RetiresThenCascadeDeletesWhenExplicitlyFalse proves the same ordering
// when IsRetired is explicitly false (not nil).
func TestTagNamespace_Remove_RetiresThenCascadeDeletesWhenExplicitlyFalse(t *testing.T) {
	id := testResourceOCID
	notRetired := false
	stub := &stubTagNamespaceClient{}
	r := &TagNamespace{client: stub, ns: identity.TagNamespaceSummary{Id: &id, IsRetired: &notRetired}}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	want := []string{stepUpdate, stepCascade}
	if len(stub.callOrder) != 2 || stub.callOrder[0] != want[0] || stub.callOrder[1] != want[1] {
		t.Fatalf("callOrder = %v, want %v", stub.callOrder, want)
	}
}

// TestTagNamespace_Remove_SkipsRetireWhenAlreadyRetired proves Remove() calls
// CascadeDeleteTagNamespace ONLY -- never UpdateTagNamespace -- when IsRetired is already true.
func TestTagNamespace_Remove_SkipsRetireWhenAlreadyRetired(t *testing.T) {
	id := testResourceOCID
	retired := true
	stub := &stubTagNamespaceClient{}
	r := &TagNamespace{client: stub, ns: identity.TagNamespaceSummary{Id: &id, IsRetired: &retired}}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	want := []string{stepCascade}
	if len(stub.callOrder) != 1 || stub.callOrder[0] != want[0] {
		t.Fatalf("callOrder = %v, want %v (UpdateTagNamespace must never be called when already retired)", stub.callOrder, want)
	}
}

// TestTagNamespace_Remove_NeverCallsBareDeleteTagNamespace is a compile-time-adjacent proof: this
// package's tagNamespaceClient interface has no DeleteTagNamespace method at all -- if Remove()
// were ever changed to call it, the file would fail to compile against this interface. This test
// documents that guarantee explicitly rather than leaving it implicit.
func TestTagNamespace_Remove_NeverCallsBareDeleteTagNamespace(t *testing.T) {
	var _ tagNamespaceClient = (*stubTagNamespaceClient)(nil)
	// tagNamespaceClient (resources/tag_namespace.go) declares exactly ListTagNamespaces,
	// UpdateTagNamespace, CascadeDeleteTagNamespace -- no DeleteTagNamespace method exists on the
	// interface Remove() is written against, so the simpler, wrong operation is unreachable by
	// construction, not merely by convention.
}

// TestTagNamespace_SafetyTags_NilSafeTimeCreated proves SafetyTags returns the zero time.Time
// (via timeCreatedOrZero) rather than panicking when TimeCreated is nil.
func TestTagNamespace_SafetyTags_NilSafeTimeCreated(t *testing.T) {
	r := &TagNamespace{}
	_, _, createdAt := r.SafetyTags()
	if !createdAt.IsZero() {
		t.Errorf("SafetyTags() createdAt = %v, want zero time.Time", createdAt)
	}
}

// TestTagNamespace_SafetyTags proves freeform/defined tags and creation time pass through
// correctly when populated.
func TestTagNamespace_SafetyTags(t *testing.T) {
	timeCreated := common.SDKTime{Time: time.Now()}
	r := &TagNamespace{}
	r.ns.FreeformTags = map[string]string{testFreeformTagKey: testFreeformTagValue}
	r.ns.DefinedTags = map[string]map[string]interface{}{testDefinedTagNamespace: {testDefinedTagKey: testDefinedTagFlatValue}}
	r.ns.TimeCreated = &timeCreated

	freeform, defined, createdAt := r.SafetyTags()
	if freeform[testFreeformTagKey] != testFreeformTagValue {
		t.Errorf("SafetyTags() freeform = %v, want env=%s", freeform, testFreeformTagValue)
	}
	if defined["ns.key"] != testDefinedTagFlatValue {
		t.Errorf("SafetyTags() defined = %v, want ns.key=%s", defined, testDefinedTagFlatValue)
	}
	if !createdAt.Equal(timeCreated.Time) {
		t.Errorf("SafetyTags() createdAt = %v, want %v", createdAt, timeCreated.Time)
	}
}
