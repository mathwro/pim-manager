package azureresources

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

// A successful HTTP submission must not turn accepted ARM requests into failed
// activations, or report approval alone as completed provisioning.
func TestActivateNormalizesARMStatus(t *testing.T) {
	for _, test := range []struct {
		status string
		want   pim.ActivationStatus
	}{
		{"Accepted", pim.ActivationStatusPendingProvisioning},
		{"PendingEvaluation", pim.ActivationStatusPendingProvisioning},
		{"PendingProvisioning", pim.ActivationStatusPendingProvisioning},
		{"ProvisioningStarted", pim.ActivationStatusPendingProvisioning},
		{"PendingScheduleCreation", pim.ActivationStatusPendingProvisioning},
		{"PendingExternalProvisioning", pim.ActivationStatusPendingProvisioning},
		{"AdminApproved", pim.ActivationStatusPendingProvisioning},
		{"Granted", pim.ActivationStatusActivated},
		{"Provisioned", pim.ActivationStatusActivated},
		{"ScheduleCreated", pim.ActivationStatusActivated},
		{"PendingApproval", pim.ActivationStatusPendingApproval},
		{"PendingApprovalProvisioning", pim.ActivationStatusPendingApproval},
		{"PendingAdminDecision", pim.ActivationStatusPendingApproval},
		{"Denied", pim.ActivationStatusFailed},
		{"AdminDenied", pim.ActivationStatusFailed},
		{"Failed", pim.ActivationStatusFailed},
		{"FailedAsResourceIsLocked", pim.ActivationStatusFailed},
		{"Canceled", pim.ActivationStatusFailed},
		{"TimedOut", pim.ActivationStatusFailed},
		{"Invalid", pim.ActivationStatusFailed},
		{"PendingRevocation", pim.ActivationStatusFailed},
		{"Revoked", pim.ActivationStatusFailed},
		{"FutureUnknownStatus", pim.ActivationStatusFailed},
		{"", pim.ActivationStatusFailed},
	} {
		t.Run(test.status, func(t *testing.T) {
			calls := 0
			client := arm.NewClient(&http.Client{Transport: activationStatusTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPut || !strings.Contains(req.URL.Path, "/roleAssignmentScheduleRequests/") {
					t.Fatalf("unexpected activation request: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"properties":{"status":%q}}`, test.status))), Header: make(http.Header)}, nil
			})}, nil)
			assignment := pim.EligibleAssignment{ID: "eligible-role", AzureScope: "/subscriptions/sub-1"}
			result, err := NewProvider(client).Activate(arm.WithAccessToken(context.Background(), "checked-token"), pim.ActivationRequest{Assignment: assignment, DurationISO: "PT1H"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.want || result.Assignment.ID != assignment.ID {
				t.Fatalf("wrong activation outcome: %#v", result)
			}
			if result.CanRetry() || result.Retryable || calls != 1 {
				t.Fatalf("submitted request must not be retried: result=%#v calls=%d", result, calls)
			}
			if result.Message == "" || !strings.Contains(result.Message, test.status) {
				t.Fatalf("missing ARM status diagnostic: %#v", result)
			}
		})
	}
}

type activationStatusTransport func(*http.Request) (*http.Response, error)

func (f activationStatusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
