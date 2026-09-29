package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// stubTagDefaultClient implements tagDefaultClient against in-memory data -- zero network access.
type stubTagDefaultClient struct {
	items   []identity.TagDefaultSummary
	deleted []string
	listErr error
}

func (s *stubTagDefaultClient) ListTagDefaults(
	_ context.Context,
	_ identity.ListTagDefaultsRequest,
) (identity.ListTagDefaultsResponse, error) {
	if s.listErr != nil {
		return identity.ListTagDefaultsResponse{}, s.listErr
	}
	return identity.ListTagDefaultsResponse{Items: s.items}, nil
}

func (s *stubTagDefaultClient) DeleteTagDefault(
	_ context.Context,
	req identity.DeleteTagDefaultRequest,
) (identity.DeleteTagDefaultResponse, error) {
	s.deleted = append(s.deleted, *req.TagDefaultId)
	return identity.DeleteTagDefaultResponse{}, nil
}

// TestTagDefaultLister_List proves tagDefaultList wraps every returned item, without ever
// constructing a real Identity client.
func TestTagDefaultLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	tagNamespaceID := "ocid1.tagnamespace.oc1..ns"
	tagDefinitionID := "ocid1.tagdefinition.oc1..def"
	tagDefinitionName := "CostCenter"
	value := "42"
	timeCreated := common.SDKTime{Time: time.Now()}
	isRequired := false

	stub := &stubTagDefaultClient{
		items: []identity.TagDefaultSummary{
			{
				Id: &id, CompartmentId: &compartmentID, TagNamespaceId: &tagNamespaceID,
				TagDefinitionId: &tagDefinitionID, TagDefinitionName: &tagDefinitionName,
				Value: &value, TimeCreated: &timeCreated, IsRequired: &isRequired,
				LifecycleState: identity.TagDefaultSummaryLifecycleStateActive,
			},
		},
	}

	got, err := tagDefaultList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("tagDefaultList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("tagDefaultList() returned %d resources, want 1", len(got))
	}
	td, ok := got[0].(*TagDefault)
	if !ok {
		t.Fatalf("tagDefaultList()[0] is %T, want *TagDefault", got[0])
	}
	if td.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", td.GetCompartmentID(), compartmentID)
	}
	if td.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", td.UniqueKey(), id)
	}
}

// TestTagDefault_Filter proves an empty (unset) LifecycleState and ACTIVE are both present --
// TagDefaultSummaryLifecycleStateEnum's own enum block declares only ACTIVE (verified against the
// pinned SDK), so a TagDefault with no populated lifecycle state is not necessarily "gone"; only
// an explicit, unrecognized non-empty value excludes.
func TestTagDefault_Filter(t *testing.T) {
	tests := []struct {
		state   identity.TagDefaultSummaryLifecycleStateEnum
		present bool
	}{
		{identity.TagDefaultSummaryLifecycleStateEnum(""), true},
		{identity.TagDefaultSummaryLifecycleStateActive, true},
		{identity.TagDefaultSummaryLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &TagDefault{}
		r.td.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with state %q = %v, want nil (present)", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with state %q = nil, want non-nil (excluded)", tc.state)
		}
	}
}

// TestTagDefault_Remove proves the SDK delete call fires with the right parameter, against a
// client that never touches the network.
func TestTagDefault_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubTagDefaultClient{}
	r := &TagDefault{client: stub}
	r.td.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestTagDefault_SafetyTags proves freeform/defined tags are both nil (TagDefaultSummary carries
// neither field) and creation time passes through unchanged.
func TestTagDefault_SafetyTags(t *testing.T) {
	timeCreated := common.SDKTime{Time: time.Now()}
	r := &TagDefault{}
	r.td.TimeCreated = &timeCreated

	freeform, defined, createdAt := r.SafetyTags()
	if freeform != nil {
		t.Errorf("SafetyTags() freeform = %v, want nil", freeform)
	}
	if defined != nil {
		t.Errorf("SafetyTags() defined = %v, want nil", defined)
	}
	if !createdAt.Equal(timeCreated.Time) {
		t.Errorf("SafetyTags() createdAt = %v, want %v", createdAt, timeCreated.Time)
	}
}

// TestTagDefault_DependedOnByTagNamespace proves TagNamespace's own registration declares
// DependsOn: ["TagDefault"] -- the cross-file edge this plan's decision requires (05-CONTEXT.md).
func TestTagDefault_DependedOnByTagNamespace(t *testing.T) {
	reg := registry.GetRegistration(TagNamespaceResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", TagNamespaceResourceType)
	}
	found := false
	for _, dep := range reg.DependsOn {
		if dep == TagDefaultResourceType {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("TagNamespace DependsOn = %v, want it to contain %q", reg.DependsOn, TagDefaultResourceType)
	}
}
