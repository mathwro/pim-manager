package tui

import (
	"strings"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivationFormRequiresJustificationOnlyWhenPolicyRequiresIt(t *testing.T) {
	optional := []pim.EligibleAssignment{{
		ID: "optional", DisplayName: "Reader",
		ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"},
	}}
	required := []pim.EligibleAssignment{{
		ID: "required", DisplayName: "Owner",
		ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H", JustificationRequired: true},
	}}
	form := activationForm{durations: map[string]string{"optional": "PT8H", "required": "PT4H"}}

	if err := form.validate(optional); err != nil {
		t.Fatalf("expected optional justification to be valid: %v", err)
	}
	if err := form.validate(required); err == nil || !strings.Contains(err.Error(), "justification") {
		t.Fatalf("expected required justification error, got %v", err)
	}
	form.justification = "Need access"
	if err := form.validate(required); err != nil {
		t.Fatalf("expected completed required form to be valid: %v", err)
	}
}

func TestActivationFormRequiresEveryAssignmentDuration(t *testing.T) {
	selected := []pim.EligibleAssignment{
		{ID: "one", DisplayName: "Contributor"},
		{ID: "two", DisplayName: "Owner"},
	}
	form := activationForm{durations: map[string]string{"one": "PT8H"}}

	err := form.validate(selected)
	if err == nil || !strings.Contains(err.Error(), "Owner") {
		t.Fatalf("expected Owner duration error, got %v", err)
	}
}

func TestSummaryGroupsResults(t *testing.T) {
	summary := newSummary([]pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusPendingApproval},
		{Status: pim.ActivationStatusFailed, Retryable: true},
	})

	if len(summary.activated) != 1 || len(summary.pendingApproval) != 1 || len(summary.failed) != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.retryableFailures()) != 1 {
		t.Fatalf("expected one retryable failure")
	}
}

func TestSummaryKeepsAcceptedProvisioningSeparateFromFailuresAndApproval(t *testing.T) {
	result := pim.ActivationResult{Status: pim.ActivationStatus("pending_provisioning"), Retryable: true}
	summary := newSummary([]pim.ActivationResult{result})
	if len(summary.pendingProvisioning) != 1 || len(summary.failed) != 0 || len(summary.activated) != 0 || len(summary.pendingApproval) != 0 {
		t.Fatalf("accepted provisioning was misclassified: %#v", summary)
	}
	if len(summary.retryableFailures()) != 0 {
		t.Fatal("accepted provisioning was offered for resubmission")
	}
}

func TestNewSummaryWithInputsClonesAndTrimsInputs(t *testing.T) {
	durations := map[string]string{"one": " PT2H "}
	s := newSummaryWithInputs(
		[]pim.ActivationResult{{Assignment: pim.EligibleAssignment{ID: "one"}, Status: pim.ActivationStatusFailed, Retryable: true}},
		"  Need access  ",
		durations,
	)
	durations["one"] = "PT8H"
	if s.justification != "Need access" || s.durations["one"] != "PT2H" {
		t.Fatalf("expected summary to snapshot trimmed justification and cloned durations, got %#v", s)
	}
}
