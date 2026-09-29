package resources

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/types"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// testDefaultSecurityListOCID/testNonDefaultSecurityListOCID are shared, package-level test
// fixture OCIDs for this file's 3+ construction sites (goconst, min-occurrences 3).
const (
	testDefaultSecurityListOCID    = "ocid1.securitylist.oc1..default"
	testNonDefaultSecurityListOCID = "ocid1.securitylist.oc1..nondefault"
)

// stubSecurityListClient implements securityListClient against in-memory data -- zero network
// access. getVcnCalls counts real GetVcn invocations, letting the caching test below prove N
// security lists sharing one VcnId cost exactly one GetVcn call (T-04-16).
type stubSecurityListClient struct {
	items       []core.SecurityList
	vcns        map[string]core.Vcn
	vcnErr      error
	deleted     []string
	listErr     error
	getVcnCalls int
}

func (s *stubSecurityListClient) ListSecurityLists(
	_ context.Context,
	_ core.ListSecurityListsRequest,
) (core.ListSecurityListsResponse, error) {
	if s.listErr != nil {
		return core.ListSecurityListsResponse{}, s.listErr
	}
	return core.ListSecurityListsResponse{Items: s.items}, nil
}

func (s *stubSecurityListClient) DeleteSecurityList(
	_ context.Context,
	req core.DeleteSecurityListRequest,
) (core.DeleteSecurityListResponse, error) {
	s.deleted = append(s.deleted, *req.SecurityListId)
	return core.DeleteSecurityListResponse{}, nil
}

func (s *stubSecurityListClient) GetVcn(_ context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error) {
	s.getVcnCalls++
	if s.vcnErr != nil {
		return core.GetVcnResponse{}, s.vcnErr
	}
	vcn, ok := s.vcns[*req.VcnId]
	if !ok {
		return core.GetVcnResponse{}, errors.New("no such vcn in stub fixture")
	}
	return core.GetVcnResponse{Vcn: vcn}, nil
}

// TestSecurityListLister_List_CachesGetVcnPerVcnId proves two security lists sharing one VcnId
// cost exactly one GetVcn call, not two (T-04-16), and that both are returned by List()
// (filtering is Filter()'s job, not List()'s).
func TestSecurityListLister_List_CachesGetVcnPerVcnId(t *testing.T) {
	vcnID := testVcnOCID
	defaultSlID := testDefaultSecurityListOCID
	sl1ID := "ocid1.securitylist.oc1..sl1"
	sl2ID := "ocid1.securitylist.oc1..sl2"
	compartmentID := testCompartmentOCID

	stub := &stubSecurityListClient{
		items: []core.SecurityList{
			{Id: &sl1ID, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.SecurityListLifecycleStateAvailable},
			{Id: &sl2ID, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.SecurityListLifecycleStateAvailable},
		},
		vcns: map[string]core.Vcn{
			vcnID: {Id: &vcnID, DefaultSecurityListId: &defaultSlID},
		},
	}

	got, err := securityListList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("securityListList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("securityListList() returned %d resources, want 2", len(got))
	}
	if stub.getVcnCalls != 1 {
		t.Fatalf("stub.getVcnCalls = %d, want 1 (per-List()-call cache keyed by VcnId)", stub.getVcnCalls)
	}
}

// TestSecurityList_Filter_LifecycleState is table-driven over every
// core.SecurityListLifecycleStateEnum value -- PROVISIONING/AVAILABLE must return nil (present,
// and not the default), TERMINATING/TERMINATED must both return non-nil (excluded).
func TestSecurityList_Filter_LifecycleState(t *testing.T) {
	nonDefaultID := testNonDefaultSecurityListOCID
	defaultID := testDefaultSecurityListOCID
	vcn := core.Vcn{DefaultSecurityListId: &defaultID}

	tests := []struct {
		state   core.SecurityListLifecycleStateEnum
		present bool
	}{
		{core.SecurityListLifecycleStateProvisioning, true},
		{core.SecurityListLifecycleStateAvailable, true},
		{core.SecurityListLifecycleStateTerminating, false},
		{core.SecurityListLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &SecurityList{vcn: vcn}
		r.sl.Id = &nonDefaultID
		r.sl.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestSecurityList_Filter_DefaultSecurityListExcluded is the T-04-15 fault-injection test: a
// default-ID-matching, AVAILABLE security list is excluded even though its lifecycle state alone
// would say "proceed," while a non-default, AVAILABLE security list proceeds -- proving BOTH
// exclusion paths independently, per this plan's <behavior> requirement.
func TestSecurityList_Filter_DefaultSecurityListExcluded(t *testing.T) {
	defaultID := testDefaultSecurityListOCID
	nonDefaultID := testNonDefaultSecurityListOCID
	vcn := core.Vcn{DefaultSecurityListId: &defaultID}

	t.Run("default security list excluded despite AVAILABLE state", func(t *testing.T) {
		r := &SecurityList{vcn: vcn}
		r.sl.Id = &defaultID
		r.sl.LifecycleState = core.SecurityListLifecycleStateAvailable
		if err := r.Filter(); err == nil {
			t.Fatal("Filter() = nil, want non-nil (default security list must never be attempted)")
		}
	})

	t.Run("non-default security list proceeds", func(t *testing.T) {
		r := &SecurityList{vcn: vcn}
		r.sl.Id = &nonDefaultID
		r.sl.LifecycleState = core.SecurityListLifecycleStateAvailable
		if err := r.Filter(); err != nil {
			t.Fatalf("Filter() = %v, want nil (non-default security list must proceed)", err)
		}
	})
}

// TestSecurityList_Filter_GetVcnErrorFailsClosed proves T-04-15's trust-boundary requirement: a
// GetVcn error inside Filter() returns a non-nil (excluding) error, never falling through to nil
// on a fetch failure.
func TestSecurityList_Filter_GetVcnErrorFailsClosed(t *testing.T) {
	id := "ocid1.securitylist.oc1..id"
	r := &SecurityList{vcnErr: errors.New("transient API error")}
	r.sl.Id = &id
	r.sl.LifecycleState = core.SecurityListLifecycleStateAvailable

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() = nil, want non-nil (a GetVcn fetch failure must fail closed, never assume not-default)")
	}
}

// TestSecurityList_Remove proves the SDK delete call fires with the right parameter
// (SecurityListId), against a client that never touches the network.
func TestSecurityList_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubSecurityListClient{}
	r := &SecurityList{client: stub}
	r.sl.Id = &id
	r.sl.CompartmentId = &compartmentID
	r.sl.LifecycleState = core.SecurityListLifecycleStateAvailable
	r.sl.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestSecurityList_Properties proves Properties() returns EXACTLY the key set baseProperties plus
// SecurityList's own vcn_id documents -- id/compartment_id/name/lifecycle_state/time_created/
// vcn_id, no more and no fewer (04-VERIFICATION.md Gap 1: this file had zero Properties()
// assertions of any kind before this test, one of only two types -- alongside RouteTable -- with
// none at all). types.Properties is a plain map[string]string, so reflect.DeepEqual against a
// fully-populated expected map proves the exact set directly, not merely a spot-checked key.
// want is built from types.NewProperties() (never a bare map literal) so it carries the same
// "_tagPrefix" bookkeeping entry types.NewProperties() itself always sets -- internal state, never
// a documented property key (types.Properties.String() filters "_"-prefixed keys out of its own
// rendering), but still a real map entry a bare-literal "want" would wrongly omit.
func TestSecurityList_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	vcnID := testVcnOCID
	name := "sl-test"
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r := &SecurityList{}
	r.sl.Id = &id
	r.sl.CompartmentId = &compartmentID
	r.sl.DisplayName = &name
	r.sl.LifecycleState = core.SecurityListLifecycleStateAvailable
	r.sl.TimeCreated = &common.SDKTime{Time: createdAt}
	r.sl.VcnId = &vcnID

	got := r.Properties()
	want := types.NewProperties().
		Set(propID, id).
		Set(propCompartmentID, compartmentID).
		Set(propName, name).
		Set(propLifecycleState, string(core.SecurityListLifecycleStateAvailable)).
		Set(propTimeCreated, createdAt).
		Set(propVcnID, vcnID)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Properties() = %+v, want exactly %+v", got, want)
	}
}
