package pim

import "testing"

func TestActivationResultStatusHelpers(t *testing.T) {
	tests := []struct {
		status    ActivationStatus
		pending   bool
		success   bool
		failure   bool
		retryable bool
	}{
		{ActivationStatusActivated, false, true, false, false},
		{ActivationStatusPendingApproval, true, false, false, false},
		{ActivationStatusFailed, false, false, true, true},
	}

	for _, tt := range tests {
		result := ActivationResult{Status: tt.status, Retryable: tt.retryable}
		if result.PendingApproval() != tt.pending {
			t.Fatalf("%s pending: expected %v", tt.status, tt.pending)
		}
		if result.Success() != tt.success {
			t.Fatalf("%s success: expected %v", tt.status, tt.success)
		}
		if result.Failure() != tt.failure {
			t.Fatalf("%s failure: expected %v", tt.status, tt.failure)
		}
		if result.CanRetry() != tt.retryable {
			t.Fatalf("%s retryable: expected %v", tt.status, tt.retryable)
		}
	}
}

func TestEligibleAssignmentDisplayScope(t *testing.T) {
	assignment := EligibleAssignment{
		DisplayName: "Contributor",
		Scope: Scope{
			DisplayName: "rg-prod",
			Type:        ScopeTypeResourceGroup,
		},
	}

	if got := assignment.DisplayScope(); got != "Resource Group: rg-prod" {
		t.Fatalf("expected display scope, got %q", got)
	}
}

func TestEligibleAssignmentDisplayScopeWithoutDisplayName(t *testing.T) {
	assignment := EligibleAssignment{
		DisplayName: "Contributor",
		Scope: Scope{
			Type: ScopeTypeSubscription,
		},
	}

	if got := assignment.DisplayScope(); got != string(ScopeTypeSubscription) {
		t.Fatalf("expected scope type, got %q", got)
	}
}
