package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/events"
)

// stubRuleClient implements ruleClient against in-memory data -- zero network access, mirroring
// stubInstanceClient's own established seam shape.
type stubRuleClient struct {
	items   []events.RuleSummary
	deleted []string
	listErr error
}

func (s *stubRuleClient) ListRules(
	_ context.Context,
	_ events.ListRulesRequest,
) (events.ListRulesResponse, error) {
	if s.listErr != nil {
		return events.ListRulesResponse{}, s.listErr
	}
	return events.ListRulesResponse{Items: s.items}, nil
}

func (s *stubRuleClient) DeleteRule(
	_ context.Context,
	req events.DeleteRuleRequest,
) (events.DeleteRuleResponse, error) {
	s.deleted = append(s.deleted, *req.RuleId)
	return events.DeleteRuleResponse{}, nil
}

// TestRuleList_List proves ruleList wraps every events.RuleSummary returned by ListRules as a
// Rule, threading compartmentID through the request.
func TestRuleList_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	displayName := "notify-on-backup-complete"
	condition := "{}"
	isEnabled := true
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubRuleClient{
		items: []events.RuleSummary{{
			Id:             &id,
			DisplayName:    &displayName,
			LifecycleState: events.RuleLifecycleStateActive,
			Condition:      &condition,
			CompartmentId:  &compartmentID,
			IsEnabled:      &isEnabled,
			TimeCreated:    &timeCreated,
		}},
	}

	got, err := ruleList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("ruleList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("ruleList() returned %d resources, want 1", len(got))
	}
	rl, ok := got[0].(*Rule)
	if !ok {
		t.Fatalf("ruleList()[0] is %T, want *Rule", got[0])
	}
	if rl.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", rl.GetCompartmentID(), compartmentID)
	}
	if rl.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", rl.UniqueKey(), id)
	}
}

// TestRule_Filter is table-driven over every events.RuleLifecycleStateEnum value -- Rule has NO
// RuleSummaryLifecycleStateEnum alias; the field is typed events.RuleLifecycleStateEnum directly.
func TestRule_Filter(t *testing.T) {
	tests := []struct {
		state   events.RuleLifecycleStateEnum
		present bool
	}{
		{events.RuleLifecycleStateCreating, true},
		{events.RuleLifecycleStateActive, true},
		{events.RuleLifecycleStateUpdating, true},
		{events.RuleLifecycleStateInactive, false},
		{events.RuleLifecycleStateDeleting, false},
		{events.RuleLifecycleStateDeleted, false},
		{events.RuleLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &Rule{}
			r.rule.LifecycleState = tc.state
			err := r.Filter()
			if tc.present && err != nil {
				t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
			}
			if !tc.present && err == nil {
				t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
			}
		})
	}
}

// TestRule_Remove proves the SDK delete call fires with the right RuleId, against a client that
// never touches the network.
func TestRule_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubRuleClient{}
	r := &Rule{client: stub}
	r.rule.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
