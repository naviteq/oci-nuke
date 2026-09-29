package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// stubInstanceConfigurationClient implements instanceConfigurationClient against in-memory data
// -- zero network access, mirroring the seam pkg/scope's IdentityClient/RegionClient already
// establish.
type stubInstanceConfigurationClient struct {
	items   []core.InstanceConfigurationSummary
	deleted []string
	listErr error
}

func (s *stubInstanceConfigurationClient) ListInstanceConfigurations(
	_ context.Context,
	_ core.ListInstanceConfigurationsRequest,
) (core.ListInstanceConfigurationsResponse, error) {
	if s.listErr != nil {
		return core.ListInstanceConfigurationsResponse{}, s.listErr
	}
	return core.ListInstanceConfigurationsResponse{Items: s.items}, nil
}

func (s *stubInstanceConfigurationClient) DeleteInstanceConfiguration(
	_ context.Context,
	req core.DeleteInstanceConfigurationRequest,
) (core.DeleteInstanceConfigurationResponse, error) {
	s.deleted = append(s.deleted, *req.InstanceConfigurationId)
	return core.DeleteInstanceConfigurationResponse{}, nil
}

// TestInstanceConfigurationLister_List proves instanceConfigurationList returns exactly one
// resource, wrapping the stub's single item, without ever constructing a real ComputeManagement
// client. A second call with the item removed from the stub proves "gone" is naturally handled
// by List() alone -- no Filter() branch is involved, per this type's Filter() contract.
func TestInstanceConfigurationLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubInstanceConfigurationClient{
		items: []core.InstanceConfigurationSummary{
			{Id: &id, CompartmentId: &compartmentID},
		},
	}

	got, err := instanceConfigurationList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("instanceConfigurationList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("instanceConfigurationList() returned %d resources, want 1", len(got))
	}
	if props := got[0].(*InstanceConfiguration).Properties(); props.Get("id") != id {
		t.Fatalf("Properties()[%q] = %q, want %q", "id", props.Get("id"), id)
	}
	if props := got[0].(*InstanceConfiguration).Properties(); props.Get("lifecycle_state") != "" {
		t.Fatalf("Properties()[%q] = %q, want empty (no lifecycle field on this type)", "lifecycle_state", props.Get("lifecycle_state"))
	}

	// "gone" case: the stub simulates deletion by returning zero items -- List() naturally stops
	// returning the item, no Filter() branch involved.
	stub.items = nil
	got, err = instanceConfigurationList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("instanceConfigurationList() (gone) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("instanceConfigurationList() (gone) returned %d resources, want 0", len(got))
	}
}

// TestInstanceConfiguration_Filter proves Filter() always returns nil regardless of any field
// value -- this type has no LifecycleState field at all, so "present" is the only reachable
// state while still listed.
func TestInstanceConfiguration_Filter(t *testing.T) {
	r := &InstanceConfiguration{}
	if err := r.Filter(); err != nil {
		t.Errorf("Filter() on zero-value InstanceConfiguration = %v, want nil", err)
	}

	id := testResourceOCID
	displayName := "some-config"
	r2 := &InstanceConfiguration{}
	r2.instanceConfiguration.Id = &id
	r2.instanceConfiguration.DisplayName = &displayName
	if err := r2.Filter(); err != nil {
		t.Errorf("Filter() on populated InstanceConfiguration = %v, want nil", err)
	}
}

// TestInstanceConfiguration_Remove proves the SDK delete call fires with the right parameter,
// against a client that never touches the network.
func TestInstanceConfiguration_Remove(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubInstanceConfigurationClient{}
	r := &InstanceConfiguration{client: stub}
	r.instanceConfiguration.Id = &id
	r.instanceConfiguration.CompartmentId = &compartmentID
	r.instanceConfiguration.TimeCreated = &timeCreated

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
