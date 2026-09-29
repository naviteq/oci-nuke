package resources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// testDhcpVcnOCID is the owning VCN every case in this file hangs its option sets off.
const testDhcpVcnOCID = "ocid1.vcn.oc1..vcn"

// stubDhcpOptionsClient implements dhcpOptionsClient against in-memory data -- zero network
// access.
type stubDhcpOptionsClient struct {
	items       []core.DhcpOptions
	deleted     []string
	listErr     error
	vcns        map[string]core.Vcn
	vcnErr      error
	getVcnCalls int
}

func (s *stubDhcpOptionsClient) GetVcn(_ context.Context, req core.GetVcnRequest) (core.GetVcnResponse, error) {
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

func (s *stubDhcpOptionsClient) ListDhcpOptions(
	_ context.Context,
	_ core.ListDhcpOptionsRequest,
) (core.ListDhcpOptionsResponse, error) {
	if s.listErr != nil {
		return core.ListDhcpOptionsResponse{}, s.listErr
	}
	return core.ListDhcpOptionsResponse{Items: s.items}, nil
}

func (s *stubDhcpOptionsClient) DeleteDhcpOptions(
	_ context.Context,
	req core.DeleteDhcpOptionsRequest,
) (core.DeleteDhcpOptionsResponse, error) {
	s.deleted = append(s.deleted, *req.DhcpId)
	return core.DeleteDhcpOptionsResponse{}, nil
}

// TestDhcpOptionsLister_List proves dhcpOptionsList returns BOTH an AVAILABLE and a TERMINATED
// option set -- filtering is Filter()'s job, not List()'s.
func TestDhcpOptionsLister_List(t *testing.T) {
	availableID := "ocid1.dhcpoptions.oc1..available"
	terminatedID := "ocid1.dhcpoptions.oc1..terminated"
	compartmentID := testCompartmentOCID

	vcnID := testDhcpVcnOCID
	otherDefault := "ocid1.dhcpoptions.oc1..somebodyelsesdefault"
	stub := &stubDhcpOptionsClient{
		items: []core.DhcpOptions{
			{Id: &availableID, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateAvailable},
			{Id: &terminatedID, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateTerminated},
		},
		vcns: map[string]core.Vcn{vcnID: {Id: &vcnID, DefaultDhcpOptionsId: &otherDefault}},
	}

	got, err := dhcpOptionsList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("dhcpOptionsList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("dhcpOptionsList() returned %d resources, want 2", len(got))
	}
	if props := got[0].(*DhcpOptions).Properties(); props.Get("id") != availableID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), availableID)
	}
	if props := got[1].(*DhcpOptions).Properties(); props.Get("id") != terminatedID {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), terminatedID)
	}
}

// TestDhcpOptions_Filter is table-driven over every core.DhcpOptionsLifecycleStateEnum value --
// TestDhcpOptionsLister_List_CachesGetVcnPerVcnId proves two option sets sharing one VcnId cost
// exactly one GetVcn call, not two -- the same guarantee routeTableList makes, for the same
// reason: a compartment with many VCNs would otherwise multiply its own listing cost.
func TestDhcpOptionsLister_List_CachesGetVcnPerVcnId(t *testing.T) {
	compartmentID := testCompartmentOCID
	vcnID := "ocid1.vcn.oc1..shared"
	first := "ocid1.dhcpoptions.oc1..first"
	second := "ocid1.dhcpoptions.oc1..second"
	defaultID := "ocid1.dhcpoptions.oc1..default"

	stub := &stubDhcpOptionsClient{
		items: []core.DhcpOptions{
			{Id: &first, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateAvailable},
			{Id: &second, CompartmentId: &compartmentID, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateAvailable},
		},
		vcns: map[string]core.Vcn{vcnID: {Id: &vcnID, DefaultDhcpOptionsId: &defaultID}},
	}

	got, err := dhcpOptionsList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("dhcpOptionsList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("dhcpOptionsList() returned %d resources, want 2", len(got))
	}
	if stub.getVcnCalls != 1 {
		t.Errorf("GetVcn called %d times for two option sets sharing one VCN, want 1", stub.getVcnCalls)
	}
}

// TestDhcpOptions_Filter_ExcludesTheVcnsDefaultSet is the regression test for a bug the e2e
// harness found on real infrastructure. ListDhcpOptions in a compartment DOES return the VCN's
// default set, and DeleteDhcpOptions on it is refused with
//
//	409 IncorrectState: "<ocid> is the default for VCN that is in use"
//
// (probed live against us-ashburn-1, not read off a doc page). So without this exclusion every
// destructive run over any compartment holding a VCN burned its whole retry budget on a resource
// that cannot be deleted and then reported it as a leftover -- a non-zero exit under
// --fail-on-leftover for a run that had in fact emptied the compartment.
//
// RouteTable and SecurityList both had this exclusion. DhcpOptions did not, and nothing noticed
// until something actually tried to delete one.
func TestDhcpOptions_Filter_ExcludesTheVcnsDefaultSet(t *testing.T) {
	vcnID := testDhcpVcnOCID
	defaultID := "ocid1.dhcpoptions.oc1..default"
	customID := "ocid1.dhcpoptions.oc1..custom"

	vcn := core.Vcn{Id: &vcnID, DefaultDhcpOptionsId: &defaultID}

	def := &DhcpOptions{opts: core.DhcpOptions{
		Id: &defaultID, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateAvailable,
	}, vcn: vcn}
	err := def.Filter()
	if err == nil {
		t.Fatal("Filter() on the VCN's default set = nil, want an exclusion -- OCI refuses the delete with " +
			"409 IncorrectState and the run reports a leftover it can never resolve")
	}
	if !strings.Contains(err.Error(), "default set for VCN") {
		t.Errorf("the exclusion should say why it is excluded; got: %v", err)
	}

	custom := &DhcpOptions{opts: core.DhcpOptions{
		Id: &customID, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateAvailable,
	}, vcn: vcn}
	if err := custom.Filter(); err != nil {
		t.Errorf("Filter() on a non-default set = %v, want nil -- excluding these would leave real resources behind", err)
	}
}

// TestDhcpOptions_Filter_FailsClosedWhenTheVcnIsUnknown pins the trust boundary: a GetVcn failure
// must exclude, never fall through to "not the default". Guessing wrong in that direction means
// attempting to delete a default set, which is the bug this file already carries a regression for.
func TestDhcpOptions_Filter_FailsClosedWhenTheVcnIsUnknown(t *testing.T) {
	id := "ocid1.dhcpoptions.oc1..unknownowner"
	vcnID := testDhcpVcnOCID

	r := &DhcpOptions{
		opts:   core.DhcpOptions{Id: &id, VcnId: &vcnID, LifecycleState: core.DhcpOptionsLifecycleStateAvailable},
		vcnErr: errors.New("fetching owning VCN: boom"),
	}
	err := r.Filter()
	if err == nil {
		t.Fatal("Filter() with an unresolvable owning VCN = nil, want an exclusion (fail closed)")
	}
	if !strings.Contains(err.Error(), "could not verify owning VCN") {
		t.Errorf("the error should name the reason it failed closed; got: %v", err)
	}
}

// PROVISIONING/AVAILABLE must return nil (present), TERMINATING/TERMINATED must both return
// non-nil (excluded). This IS the regression test for the hang trap.
func TestDhcpOptions_Filter(t *testing.T) {
	tests := []struct {
		state   core.DhcpOptionsLifecycleStateEnum
		present bool
	}{
		{core.DhcpOptionsLifecycleStateProvisioning, true},
		{core.DhcpOptionsLifecycleStateAvailable, true},
		{core.DhcpOptionsLifecycleStateTerminating, false},
		{core.DhcpOptionsLifecycleStateTerminated, false},
	}

	for _, tc := range tests {
		r := &DhcpOptions{}
		r.opts.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestDhcpOptions_Remove proves the SDK delete call fires with DhcpId, NOT DhcpOptionsId -- the
// real SDK naming irregularity this plan calls out explicitly, against a client that never
// touches the network.
func TestDhcpOptions_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubDhcpOptionsClient{}
	r := &DhcpOptions{client: stub}
	r.opts.Id = &id
	r.opts.CompartmentId = &compartmentID
	r.opts.LifecycleState = core.DhcpOptionsLifecycleStateAvailable
	r.opts.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s] (DhcpId field, not DhcpOptionsId)", stub.deleted, id)
	}
}
