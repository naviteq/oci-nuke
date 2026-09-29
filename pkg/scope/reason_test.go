package scope_test

import (
	"testing"

	"github.com/naviteq/oci-nuke/pkg/scope"
)

// TestRefusalReason_ExactStringValues pins the exact string value of every scope.RefusalReason
// constant -- the original four Phase 2 shipped (out-of-scope, blocklisted, protected-by-tag,
// too-young), the four Phase 3 adds (scheduled-deletion, retention-locked,
// dependency-not-satisfied, api-error), the two Phase 5 adds (delete-protected,
// backup-residue), and Phase 6's addition (compartment-not-empty). A machine-readable reason that
// silently changes its wire value would break any downstream consumer (the JSON artifact, a
// future approval workflow) without a compile error, so every constant's literal value is
// asserted here by name.
func TestRefusalReason_ExactStringValues(t *testing.T) {
	tests := []struct {
		name   string
		reason scope.RefusalReason
		want   string
	}{
		{"ReasonOutOfScope", scope.ReasonOutOfScope, "out-of-scope"},
		{"ReasonBlocklisted", scope.ReasonBlocklisted, "blocklisted"},
		{"ReasonProtectedByTag", scope.ReasonProtectedByTag, "protected-by-tag"},
		{"ReasonTooYoung", scope.ReasonTooYoung, "too-young"},
		{"ReasonAPIError", scope.ReasonAPIError, "api-error"},
		{"ReasonScheduledDeletion", scope.ReasonScheduledDeletion, "scheduled-deletion"},
		{"ReasonRetentionLocked", scope.ReasonRetentionLocked, "retention-locked"},
		{"ReasonDependencyNotSatisfied", scope.ReasonDependencyNotSatisfied, "dependency-not-satisfied"},
		{"ReasonDeleteProtected", scope.ReasonDeleteProtected, "delete-protected"},
		{"ReasonBackupResidue", scope.ReasonBackupResidue, "backup-residue"},
		{"ReasonCompartmentNotEmpty", scope.ReasonCompartmentNotEmpty, "compartment-not-empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.reason) != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, string(tt.reason), tt.want)
			}
		})
	}
}
