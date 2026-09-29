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

// testDefaultRouteTableOCID/testNonDefaultRouteTableOCID are shared, package-level test fixture
// OCIDs for this file's 3+ construction sites (goconst, min-occurrences 3).
//
// testVcnOCID is shared with resources/security_list_test.go too (goconst counts occurrences
// package-wide, not per-file) -- both TestRouteTable_Properties and TestSecurityList_Properties
// construct a VcnId fixture value, on top of each file's own pre-existing GetVcn-caching test.
const (
	testDefaultRouteTableOCID    = "ocid1.routetable.oc1..default"
	testNonDefaultRouteTableOCID = "ocid1.routetable.oc1..nondefault"
	testVcnOCID                  = "ocid1.vcn.oc1..id"
)

// stubRouteTableClient implements routeTableClient against in-memory data -- zero network
// access. getVcnCalls counts real GetVcn invocations, letting the caching test below prove N
// route tables sharing one VcnId cost exactly one GetVcn call (T-04-16).
type stubRouteTableClient struct {
	items       []core.RouteTable
	vcns        map[string]core.Vcn
	vcnErr      error
	deleted     []string
	listErr     error
	getVcnCalls int
}

func (s *stubRouteTableClient) ListRouteTables(
	_ context.Context,
	_ core.ListRouteTablesRequest,
) (core.ListRouteTablesResponse, error) {
	if s.listErr != nil {
		return core.ListRouteTablesResponse{}, s.listErr
	}
	return core.ListRouteTablesResponse{Items: s.items}, nil
}

func (s *stubRouteTableClient) DeleteRouteTable(
	_ context.Context,
	req core.DeleteRouteTableRequest,
) (core.DeleteRouteTableResponse, error) {
	s.deleted = append(s.deleted, *req.RtId)
	return core.DeleteRouteTableResponse{}, nil
}

func (s *stubRouteTableClient) GetVcn(_ context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error) {
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

// TestRouteTableLister_List_CachesGetVcnPerVcnId proves two route tables sharing one VcnId cost
// exactly one GetVcn call, not two (T-04-16), and that both are returned by List() (filtering is
// Filter()'s job, not List()'s).
func TestRouteTableLister_List_CachesGetVcnPerVcnId(t *testing.T) {
	vcnID := testVcnOCID
	defaultRtID := testDefaultRouteTableOCID
	rt1ID := "ocid1.routetable.oc1..rt1"
	rt2ID := "ocid1.routetable.oc1..rt2"
	compartmentID := testCompartmentOCID

	stub := &stubRouteTableClient{
		items: []core.RouteTable{
			{Id: &rt1ID, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.RouteTableLifecycleStateAvailable},
			{Id: &rt2ID, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.RouteTableLifecycleStateAvailable},
		},
		vcns: map[string]core.Vcn{
			vcnID: {Id: &vcnID, DefaultRouteTableId: &defaultRtID},
		},
	}

	got, err := routeTableList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("routeTableList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("routeTableList() returned %d resources, want 2", len(got))
	}
	if stub.getVcnCalls != 1 {
		t.Fatalf("stub.getVcnCalls = %d, want 1 (per-List()-call cache keyed by VcnId)", stub.getVcnCalls)
	}
}

// TestRouteTable_Filter_LifecycleState is table-driven over every core.RouteTableLifecycleStateEnum
// value -- PROVISIONING/AVAILABLE must return nil (present, and not the default), TERMINATING/
// TERMINATED must both return non-nil (excluded).
func TestRouteTable_Filter_LifecycleState(t *testing.T) {
	nonDefaultID := testNonDefaultRouteTableOCID
	defaultID := testDefaultRouteTableOCID
	vcn := core.Vcn{DefaultRouteTableId: &defaultID}

	tests := []struct {
		state   core.RouteTableLifecycleStateEnum
		present bool
	}{
		{core.RouteTableLifecycleStateProvisioning, true},
		{core.RouteTableLifecycleStateAvailable, true},
		{core.RouteTableLifecycleStateTerminating, false},
		{core.RouteTableLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &RouteTable{vcn: vcn}
		r.rt.Id = &nonDefaultID
		r.rt.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestRouteTable_Filter_DefaultRouteTableExcluded is the T-04-15 fault-injection test: a
// default-ID-matching, AVAILABLE route table is excluded even though its lifecycle state alone
// would say "proceed," while a non-default, AVAILABLE route table proceeds -- proving BOTH
// exclusion paths independently, per this plan's <behavior> requirement.
func TestRouteTable_Filter_DefaultRouteTableExcluded(t *testing.T) {
	defaultID := testDefaultRouteTableOCID
	nonDefaultID := testNonDefaultRouteTableOCID
	vcn := core.Vcn{DefaultRouteTableId: &defaultID}

	t.Run("default route table excluded despite AVAILABLE state", func(t *testing.T) {
		r := &RouteTable{vcn: vcn}
		r.rt.Id = &defaultID
		r.rt.LifecycleState = core.RouteTableLifecycleStateAvailable
		if err := r.Filter(); err == nil {
			t.Fatal("Filter() = nil, want non-nil (default route table must never be attempted)")
		}
	})

	t.Run("non-default route table proceeds", func(t *testing.T) {
		r := &RouteTable{vcn: vcn}
		r.rt.Id = &nonDefaultID
		r.rt.LifecycleState = core.RouteTableLifecycleStateAvailable
		if err := r.Filter(); err != nil {
			t.Fatalf("Filter() = %v, want nil (non-default route table must proceed)", err)
		}
	})
}

// TestRouteTable_Filter_GetVcnErrorFailsClosed proves T-04-15's trust-boundary requirement: a
// GetVcn error inside Filter() returns a non-nil (excluding) error, never falling through to nil
// on a fetch failure.
func TestRouteTable_Filter_GetVcnErrorFailsClosed(t *testing.T) {
	id := "ocid1.routetable.oc1..id"
	r := &RouteTable{vcnErr: errors.New("transient API error")}
	r.rt.Id = &id
	r.rt.LifecycleState = core.RouteTableLifecycleStateAvailable

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() = nil, want non-nil (a GetVcn fetch failure must fail closed, never assume not-default)")
	}
}

// TestRouteTable_Remove proves the SDK delete call fires with the exact request field RtId (NOT
// RouteTableId) -- the stub asserts the exact field, not merely that some delete call fired.
func TestRouteTable_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubRouteTableClient{}
	r := &RouteTable{client: stub}
	r.rt.Id = &id
	r.rt.CompartmentId = &compartmentID
	r.rt.LifecycleState = core.RouteTableLifecycleStateAvailable
	r.rt.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s] (RtId field must carry the id)", stub.deleted, id)
	}
}

// TestRouteTable_Properties proves Properties() returns EXACTLY the key set baseProperties plus
// RouteTable's own vcn_id documents -- id/compartment_id/name/lifecycle_state/time_created/
// vcn_id, no more and no fewer (04-VERIFICATION.md Gap 1: this file had zero Properties()
// assertions of any kind before this test, one of only two types -- alongside SecurityList --
// with none at all). types.Properties is a plain map[string]string, so reflect.DeepEqual against
// a fully-populated expected map proves the exact set directly, not merely a spot-checked key.
// want is built from types.NewProperties() (never a bare map literal) so it carries the same
// "_tagPrefix" bookkeeping entry types.NewProperties() itself always sets -- internal state, never
// a documented property key (types.Properties.String() filters "_"-prefixed keys out of its own
// rendering), but still a real map entry a bare-literal "want" would wrongly omit.
func TestRouteTable_Properties(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	vcnID := testVcnOCID
	name := "rt-test"
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r := &RouteTable{}
	r.rt.Id = &id
	r.rt.CompartmentId = &compartmentID
	r.rt.DisplayName = &name
	r.rt.LifecycleState = core.RouteTableLifecycleStateAvailable
	r.rt.TimeCreated = &common.SDKTime{Time: createdAt}
	r.rt.VcnId = &vcnID

	got := r.Properties()
	want := types.NewProperties().
		Set(propID, id).
		Set(propCompartmentID, compartmentID).
		Set(propName, name).
		Set(propLifecycleState, string(core.RouteTableLifecycleStateAvailable)).
		Set(propTimeCreated, createdAt).
		Set(propVcnID, vcnID)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Properties() = %+v, want exactly %+v", got, want)
	}
}
