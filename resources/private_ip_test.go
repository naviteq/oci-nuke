package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubPrivateIpClient implements privateIpClient against in-memory data -- zero network access.
// privateIpsBySubnet is keyed by *core.Subnet.Id, mirroring the real per-subnet
// ListPrivateIps(SubnetId: ...) call this lister makes since ListPrivateIpsRequest has no
// CompartmentId parameter at all.
type stubPrivateIpClient struct {
	subnets             []core.Subnet
	subnetsErr          error
	privateIpsBySubnet  map[string][]core.PrivateIp
	listPrivateIpsErr   error
	listPrivateIpsCalls int
	deleted             []string
}

func (s *stubPrivateIpClient) ListSubnets(_ context.Context, _ core.ListSubnetsRequest) (core.ListSubnetsResponse, error) {
	if s.subnetsErr != nil {
		return core.ListSubnetsResponse{}, s.subnetsErr
	}
	return core.ListSubnetsResponse{Items: s.subnets}, nil
}

func (s *stubPrivateIpClient) ListPrivateIps(
	_ context.Context,
	req core.ListPrivateIpsRequest,
) (core.ListPrivateIpsResponse, error) {
	s.listPrivateIpsCalls++
	if s.listPrivateIpsErr != nil {
		return core.ListPrivateIpsResponse{}, s.listPrivateIpsErr
	}
	return core.ListPrivateIpsResponse{Items: s.privateIpsBySubnet[*req.SubnetId]}, nil
}

func (s *stubPrivateIpClient) DeletePrivateIp(
	_ context.Context,
	req core.DeletePrivateIpRequest,
) (core.DeletePrivateIpResponse, error) {
	s.deleted = append(s.deleted, *req.PrivateIpId)
	return core.DeletePrivateIpResponse{}, nil
}

// TestPrivateIpLister_List proves privateIpList enumerates PER-SUBNET, not per-compartment --
// two subnets each contribute one PrivateIp, and ListPrivateIps fires exactly once per subnet
// (T-04-19's bounded-call-volume shape), never once for the whole compartment.
func TestPrivateIpLister_List(t *testing.T) {
	compartmentID := testCompartmentOCID
	subnet1ID := "ocid1.subnet.oc1..one"
	subnet2ID := "ocid1.subnet.oc1..two"
	pi1ID := "ocid1.privateip.oc1..one"
	pi2ID := "ocid1.privateip.oc1..two"

	stub := &stubPrivateIpClient{
		subnets: []core.Subnet{
			{Id: &subnet1ID, CompartmentId: &compartmentID},
			{Id: &subnet2ID, CompartmentId: &compartmentID},
		},
		privateIpsBySubnet: map[string][]core.PrivateIp{
			subnet1ID: {{Id: &pi1ID, CompartmentId: &compartmentID, SubnetId: &subnet1ID}},
			subnet2ID: {{Id: &pi2ID, CompartmentId: &compartmentID, SubnetId: &subnet2ID}},
		},
	}

	got, err := privateIpList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("privateIpList() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("privateIpList() returned %d resources, want 2", len(got))
	}
	if stub.listPrivateIpsCalls != 2 {
		t.Fatalf("ListPrivateIps called %d times, want 2 (once per subnet)", stub.listPrivateIpsCalls)
	}

	gotIDs := map[string]bool{}
	for _, r := range got {
		gotIDs[r.(*PrivateIp).Properties().Get("id")] = true
	}
	if !gotIDs[pi1ID] || !gotIDs[pi2ID] {
		t.Fatalf("privateIpList() returned IDs %v, want both %q and %q", gotIDs, pi1ID, pi2ID)
	}
}

// TestPrivateIp_Filter proves the primary-IP exclusion is unconditional and independent of any
// lifecycle reasoning (core.PrivateIp has no LifecycleState field at all) -- a primary private IP
// must always be excluded (T-04-18), a secondary (non-primary) private IP must always proceed,
// and a nil IsPrimary (should not happen in practice, but defended against) must not exclude.
func TestPrivateIp_Filter(t *testing.T) {
	primary := true
	secondary := false

	tests := []struct {
		name      string
		isPrimary *bool
		present   bool
	}{
		{"primary excluded", &primary, false},
		{"secondary present", &secondary, true},
		{"nil IsPrimary present", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &PrivateIp{}
			r.pi.Id = &tc.name
			r.pi.IsPrimary = tc.isPrimary
			err := r.Filter()
			if tc.present && err != nil {
				t.Errorf("Filter() = %v, want nil (present)", err)
			}
			if !tc.present && err == nil {
				t.Errorf("Filter() = nil, want non-nil (excluded, hang-trap regression)")
			}
		})
	}
}

// TestPrivateIp_GetCompartmentID_NilSafe proves GetCompartmentID returns "" rather than
// panicking when CompartmentId is nil -- core.PrivateIp.CompartmentId is `mandatory:"false"` on
// the pinned SDK and can be nil on some records (verified this session). A "" compartment ID
// fails closed through scopedLister's existing out-of-scope drop.
func TestPrivateIp_GetCompartmentID_NilSafe(t *testing.T) {
	r := &PrivateIp{}
	r.pi.CompartmentId = nil

	if got := r.GetCompartmentID(); got != "" {
		t.Fatalf("GetCompartmentID() = %q, want \"\" (nil-safe)", got)
	}
}

// TestPrivateIp_GetCompartmentID_NonNil proves the non-nil path still returns the real value.
func TestPrivateIp_GetCompartmentID_NonNil(t *testing.T) {
	compartmentID := testCompartmentOCID
	r := &PrivateIp{}
	r.pi.CompartmentId = &compartmentID

	if got := r.GetCompartmentID(); got != compartmentID {
		t.Fatalf("GetCompartmentID() = %q, want %q", got, compartmentID)
	}
}

// TestPrivateIp_Remove proves the SDK delete call fires with the right parameter (PrivateIpId),
// against a client that never touches the network.
func TestPrivateIp_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	secondary := false
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubPrivateIpClient{}
	r := &PrivateIp{client: stub}
	r.pi.Id = &id
	r.pi.CompartmentId = &compartmentID
	r.pi.IsPrimary = &secondary
	r.pi.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestPrivateIp_Remove_NilTimeCreated proves Remove() does not panic when TimeCreated is nil --
// core.PrivateIp.TimeCreated is `mandatory:"false"` on the pinned SDK.
func TestPrivateIp_Remove_NilTimeCreated(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	secondary := false

	stub := &stubPrivateIpClient{}
	r := &PrivateIp{client: stub}
	r.pi.Id = &id
	r.pi.CompartmentId = &compartmentID
	r.pi.IsPrimary = &secondary
	r.pi.TimeCreated = nil

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestPrivateIpRegistration_MakesSubnetVisible proves the [CITED] plan finding: Subnet's
// DependsOn: ["PrivateIp"] (Plan 04-06) named an unregistered type until this file's init()
// ran, which made Subnet invisible to registry.GetNames()/GetListersV2() (04-06-SUMMARY.md's
// documented, self-resolving gap). Now that PrivateIp is registered, Subnet must reappear --
// this is the exact mechanism the `oci-nuke resource-types` CLI and tools/generate-docs both
// call, so this test is the real proof, not merely an assumption.
func TestPrivateIpRegistration_MakesSubnetVisible(t *testing.T) {
	names := registry.GetNames()
	found := false
	for _, n := range names {
		if n == SubnetResourceType {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("registry.GetNames() = %v, want it to contain %q (Subnet must be reachable "+
			"from root now that PrivateIp is registered)", names, SubnetResourceType)
	}
}
