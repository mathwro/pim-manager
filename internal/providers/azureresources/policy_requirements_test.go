package azureresources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
)

// These fixtures catch dropped approval settings and silently ignored enablement requirements.
func TestARMPolicyRequirements(t *testing.T) {
	for _, test := range []struct {
		name, enabled, extra, unsupported string
		justification                     bool
	}{
		{name: "ticketing", enabled: `["Ticketing"]`, unsupported: "Ticketing"},
		{name: "unknown enabled requirement", enabled: `["FutureRequirement"]`, unsupported: "FutureRequirement"},
		{name: "approval requires justification", enabled: `[]`, extra: `,{"id":"Approval_EndUser_Assignment","setting":{"isApprovalRequired":true,"isRequestorJustificationRequired":true}}`, justification: true},
		{name: "approval justification ignored without approval gate", enabled: `[]`, extra: `,{"id":"Approval_EndUser_Assignment","setting":{"isApprovalRequired":false,"isRequestorJustificationRequired":true}}`},
		{name: "approval optional", enabled: `[]`, extra: `,{"id":"Approval_EndUser_Assignment","setting":{"isRequestorJustificationRequired":false}}`},
		{name: "approval cannot clear enablement justification", enabled: `["jUsTiFiCaTiOn"]`, extra: `,{"id":"Approval_EndUser_Assignment","setting":{"isRequestorJustificationRequired":false}}`, justification: true},
		{name: "missing approval optional", enabled: `[]`},
		{name: "informational and admin requirements ignored", enabled: `[]`, extra: `,{"id":"Enablement_Admin_Assignment","enabledRules":["Ticketing"]},{"id":"Notification_Requestor_EndUser_Assignment","notificationType":"Email"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &policyRequirementsARM{policy: fmt.Sprintf(`{"value":[{"properties":{"scope":"/subscriptions/sub-1","roleDefinitionId":"reader","effectiveRules":[{"id":"Expiration_EndUser_Assignment","maximumDuration":"PT4H"},{"id":"Enablement_EndUser_Assignment","enabledRules":%s},{"id":"AuthenticationContext_EndUser_Assignment","isEnabled":false}%s]}}]}`, test.enabled, test.extra)}
			provider := azureresources.NewProvider(client)
			prepared, err := provider.Prepare(context.Background(), []pim.EligibleAssignment{policyRequirementsAssignment()})
			if test.unsupported != "" {
				if err == nil || prepared != nil {
					t.Fatalf("unsupported requirement returned usable assignments: %#v, %v", prepared, err)
				}
				for _, detail := range []string{test.unsupported, "Reader", "/subscriptions/sub-1", "Azure portal"} {
					if !strings.Contains(err.Error(), detail) {
						t.Fatalf("diagnostic omits %q: %v", detail, err)
					}
				}
			} else {
				want := pim.ActivationPolicy{MaximumDurationISO: "PT4H", JustificationRequired: test.justification}
				if err != nil || len(prepared) != 1 || prepared[0].ActivationPolicy != want {
					t.Fatalf("policy = %#v, %v; want %#v", prepared, err, want)
				}
			}
		})
	}
}

func TestARMPolicySupportedRequirementsPreserved(t *testing.T) {
	client := &policyRequirementsARM{policy: `{"value":[{"properties":{"roleDefinitionId":"reader","effectiveRules":[{"id":"Expiration_EndUser_Assignment","maximumDuration":"PT8H"},{"id":"Enablement_EndUser_Assignment","enabledRules":["Justification","MultiFactorAuthentication"]},{"id":"AuthenticationContext_EndUser_Assignment","isEnabled":true,"claimValue":"c7"},{"id":"Approval_EndUser_Assignment","setting":{"isRequestorJustificationRequired":false}}]}}]}`}
	prepared, err := azureresources.NewProvider(client).Prepare(context.Background(), []pim.EligibleAssignment{policyRequirementsAssignment()})
	want := pim.ActivationPolicy{MaximumDurationISO: "PT8H", JustificationRequired: true, MFARequired: true, AuthenticationContext: "c7"}
	if err != nil || len(prepared) != 1 || prepared[0].ActivationPolicy != want {
		t.Fatalf("policy = %#v, %v; want %#v", prepared, err, want)
	}
}

func policyRequirementsAssignment() pim.EligibleAssignment {
	return pim.EligibleAssignment{ID: "eligible-1", Source: pim.AssignmentSourceAzureResource, DisplayName: "Reader", AzureScope: "/subscriptions/sub-1", RoleDefinitionID: "reader"}
}

type policyRequirementsARM struct {
	policy string
}

func (c *policyRequirementsARM) PinAccessToken(ctx context.Context) (context.Context, error) {
	return ctx, nil
}

func (c *policyRequirementsARM) Get(_ context.Context, path string, target any) error {
	switch {
	case strings.Contains(path, "/roleManagementPolicyAssignments?"):
		return json.Unmarshal([]byte(c.policy), target)
	default:
		return fmt.Errorf("unexpected ARM GET %s", path)
	}
}

func (c *policyRequirementsARM) Put(context.Context, string, any, any) error {
	return fmt.Errorf("unexpected activation submission")
}
