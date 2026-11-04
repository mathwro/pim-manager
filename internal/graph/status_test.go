package graph

import (
	"strings"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivationResultPreservesProvisioningAndApprovalOutcomes(t *testing.T) {
	for _, test := range []struct {
		status string
		want   pim.ActivationStatus
	}{
		{"Granted", pim.ActivationStatusActivated}, {"Provisioned", pim.ActivationStatusActivated}, {"ScheduleCreated", pim.ActivationStatusActivated},
		{"PendingApproval", pim.ActivationStatusPendingApproval}, {"PendingAdminDecision", pim.ActivationStatusPendingApproval},
		{"PendingProvisioning", pim.ActivationStatusPendingProvisioning}, {"PendingScheduleCreation", pim.ActivationStatusPendingProvisioning},
		{"Failed", pim.ActivationStatusFailed}, {"Denied", pim.ActivationStatusFailed}, {"unexpected", pim.ActivationStatusFailed},
	} {
		t.Run(test.status, func(t *testing.T) {
			result := ActivationResult(pim.EligibleAssignment{ID: "one"}, test.status)
			if result.Status != test.want || result.Retryable || !strings.Contains(result.Message, test.status) {
				t.Fatalf("wrong outcome: %#v", result)
			}
		})
	}
}
