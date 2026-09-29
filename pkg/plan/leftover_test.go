package plan_test

import (
	"strings"
	"testing"

	"github.com/ekristen/libnuke/pkg/queue"

	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

func TestClassifyLeftover_FailedStateIsAPIError(t *testing.T) {
	item := &queue.Item{State: queue.ItemStateFailed, Reason: "delete API returned 409 Conflict"}

	got := plan.ClassifyLeftover(item)

	if got != scope.ReasonAPIError {
		t.Errorf("ClassifyLeftover(ItemStateFailed) = %q, want %q", got, scope.ReasonAPIError)
	}
}

func TestClassifyLeftover_DependencyStatesAreDependencyNotSatisfied(t *testing.T) {
	tests := []struct {
		name  string
		state queue.ItemState
	}{
		{"NewDependency", queue.ItemStateNewDependency},
		{"PendingDependency", queue.ItemStatePendingDependency},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &queue.Item{State: tt.state}

			got := plan.ClassifyLeftover(item)

			if got != scope.ReasonDependencyNotSatisfied {
				t.Errorf("ClassifyLeftover(%s) = %q, want %q", tt.name, got, scope.ReasonDependencyNotSatisfied)
			}
		})
	}
}

func TestClassifyLeftover_WaitingHoldPendingAreAPIErrorWithDetail(t *testing.T) {
	tests := []struct {
		name  string
		state queue.ItemState
	}{
		{"Waiting", queue.ItemStateWaiting},
		{"Hold", queue.ItemStateHold},
		{"Pending", queue.ItemStatePending},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &queue.Item{State: tt.state}

			got := plan.ClassifyLeftover(item)

			if got != scope.ReasonAPIError {
				t.Errorf("ClassifyLeftover(%s) = %q, want %q", tt.name, got, scope.ReasonAPIError)
			}
			if item.GetReason() == "" {
				t.Fatalf("ClassifyLeftover(%s) left item.Reason empty, want a detail naming the raw state", tt.name)
			}
			if !strings.Contains(item.GetReason(), tt.state.String()) {
				t.Errorf("item.Reason = %q, want it to name the raw state %q", item.GetReason(), tt.state.String())
			}
		})
	}
}

// TestClassifyLeftover_PreservesExistingReason proves ClassifyLeftover never overwrites a
// Reason a caller already populated for Waiting/Hold/Pending -- it only fills in a detail when
// none exists.
func TestClassifyLeftover_PreservesExistingReason(t *testing.T) {
	item := &queue.Item{State: queue.ItemStateWaiting, Reason: "already has a reason"}

	plan.ClassifyLeftover(item)

	if item.GetReason() != "already has a reason" {
		t.Errorf("item.Reason = %q, want it left untouched", item.GetReason())
	}
}
