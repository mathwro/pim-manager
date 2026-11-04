package graph

import (
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

// ActivationResult maps a Graph PIM schedule request status shared by directory
// role and group activations.
func ActivationResult(assignment pim.EligibleAssignment, status string) pim.ActivationResult {
	result := pim.ActivationResult{Assignment: assignment, Message: status}
	switch strings.ToLower(status) {
	case "granted", "provisioned", "schedulecreated":
		result.Status = pim.ActivationStatusActivated
	case "pendingapproval", "pendingapprovalprovisioning", "pendingadmindecision":
		result.Status = pim.ActivationStatusPendingApproval
	case "pendingprovisioning", "pendingschedulecreation":
		result.Status = pim.ActivationStatusPendingProvisioning
	default:
		result.Status = pim.ActivationStatusFailed
		if status == "" {
			result.Message = "Graph activation response did not include a status; check PIM before resubmitting"
		}
	}
	return result
}
