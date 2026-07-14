package tui

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivationFormRequiresJustificationAndDuration(t *testing.T) {
	form := activationForm{justification: "", durationISO: "PT2H"}
	if form.valid() {
		t.Fatal("expected empty justification to be invalid")
	}

	form.justification = "Need access"
	form.durationISO = ""
	if form.valid() {
		t.Fatal("expected empty duration to be invalid")
	}

	form.durationISO = "PT2H"
	if !form.valid() {
		t.Fatal("expected complete form to be valid")
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
