package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// stubPolicyClient implements policyClient against in-memory data -- zero network access.
type stubPolicyClient struct {
	items   []identity.Policy
	deleted []string
	listErr error
}

func (s *stubPolicyClient) ListPolicies(
	_ context.Context,
	_ identity.ListPoliciesRequest,
) (identity.ListPoliciesResponse, error) {
	if s.listErr != nil {
		return identity.ListPoliciesResponse{}, s.listErr
	}
	return identity.ListPoliciesResponse{Items: s.items}, nil
}

func (s *stubPolicyClient) DeletePolicy(
	_ context.Context,
	req identity.DeletePolicyRequest,
) (identity.DeletePolicyResponse, error) {
	s.deleted = append(s.deleted, *req.PolicyId)
	return identity.DeletePolicyResponse{}, nil
}

// TestPolicyLister_List proves policyList wraps every returned item, without ever constructing a
// real Identity client.
func TestPolicyLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	name := "test-policy"
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubPolicyClient{
		items: []identity.Policy{
			{
				Id: &id, CompartmentId: &compartmentID, Name: &name,
				Statements:     []string{"Allow group Administrators to manage all-resources in tenancy"},
				TimeCreated:    &timeCreated,
				LifecycleState: identity.PolicyLifecycleStateActive,
			},
		},
	}

	got, err := policyList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("policyList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("policyList() returned %d resources, want 1", len(got))
	}
	p, ok := got[0].(*Policy)
	if !ok {
		t.Fatalf("policyList()[0] is %T, want *Policy", got[0])
	}
	if p.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", p.GetCompartmentID(), compartmentID)
	}
	if p.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", p.UniqueKey(), id)
	}
}

// TestPolicy_Filter is table-driven over every identity.PolicyLifecycleStateEnum value -- only
// ACTIVE must return nil (present); every other state must return non-nil (excluded).
func TestPolicy_Filter(t *testing.T) {
	tests := []struct {
		state   identity.PolicyLifecycleStateEnum
		present bool
	}{
		{identity.PolicyLifecycleStateActive, true},
		{identity.PolicyLifecycleStateCreating, false},
		{identity.PolicyLifecycleStateInactive, false},
		{identity.PolicyLifecycleStateDeleting, false},
		{identity.PolicyLifecycleStateDeleted, false},
		{identity.PolicyLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &Policy{}
		r.policy.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil", tc.state)
		}
	}
}

// TestPolicy_Remove proves the SDK delete call fires with the right parameter, against a client
// that never touches the network.
func TestPolicy_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubPolicyClient{}
	r := &Policy{client: stub}
	r.policy.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestPolicy_Properties_OmitsStatements proves Policy.Properties() never exposes the free-text
// Statements field (05-RESEARCH.md's Security Domain note) -- baseProperties' vocabulary is id/
// name/compartment_id/lifecycle_state/time_created/tags only, never a "statements" key.
func TestPolicy_Properties_OmitsStatements(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	name := "test-policy"
	timeCreated := common.SDKTime{Time: time.Now()}

	r := &Policy{}
	r.policy = identity.Policy{
		Id: &id, CompartmentId: &compartmentID, Name: &name,
		Statements:     []string{"Allow group Administrators to manage all-resources in tenancy"},
		TimeCreated:    &timeCreated,
		LifecycleState: identity.PolicyLifecycleStateActive,
	}

	props := r.Properties()
	if props.Get("statements") != "" {
		t.Errorf(
			"Properties() exposes a %q key = %q, want it never set (free-text IAM grant text must not be logged casually)",
			"statements", props.Get("statements"),
		)
	}
	if props.Get(propID) != id {
		t.Errorf("Properties()[%q] = %q, want %q", propID, props.Get(propID), id)
	}
}

// TestPolicy_SafetyTags proves SafetyTags flattens defined tags and passes freeform tags/creation
// time through unchanged.
func TestPolicy_SafetyTags(t *testing.T) {
	timeCreated := common.SDKTime{Time: time.Now()}
	r := &Policy{}
	r.policy.FreeformTags = map[string]string{testFreeformTagKey: testFreeformTagValue}
	r.policy.DefinedTags = map[string]map[string]interface{}{testDefinedTagNamespace: {testDefinedTagKey: testDefinedTagFlatValue}}
	r.policy.TimeCreated = &timeCreated

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
