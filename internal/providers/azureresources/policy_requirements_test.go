package azureresources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/pim"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
	"github.com/mathwro/pim-manager/internal/tui"
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
		{name: "approval justification independent of approval gate", enabled: `[]`, extra: `,{"id":"Approval_EndUser_Assignment","setting":{"isApprovalRequired":false,"isRequestorJustificationRequired":true}}`, justification: true},
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

			// Drive the real provider through discovery, preparation, selection, and form validation.
			model := tui.NewModel(tui.Runtime{AzureResources: provider})
			model = policyRequirementsKey(model, tea.KeyEnter)
			if test.unsupported != "" && !strings.Contains(model.View(), test.unsupported) {
				t.Fatalf("preparation diagnostic did not reach user:\n%s", model.View())
			}
			model = policyRequirementsKey(model, tea.KeySpace)
			model = policyRequirementsKey(model, tea.KeyEnter)
			if test.unsupported != "" {
				model = policyRequirementsKey(model, tea.KeyEnter)
				model = policyRequirementsKey(model, tea.KeyEnter)
				if strings.Contains(model.View(), "Justification —") {
					t.Fatal("unsupported policy reached activation form")
				}
			} else {
				model = policyRequirementsKey(model, tea.KeyEnter)
				model = policyRequirementsKey(model, tea.KeyEnter)
				blocked := strings.Contains(model.View(), "justification is required")
				if blocked != test.justification {
					t.Fatalf("empty justification blocked=%t, want %t:\n%s", blocked, test.justification, model.View())
				}
				if !test.justification && !strings.Contains(model.View(), "Review activation") {
					t.Fatalf("optional justification did not reach confirmation:\n%s", model.View())
				}
			}
			if client.puts != 0 {
				t.Fatalf("submitted %d activation requests before policy/form validation", client.puts)
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
	puts   int
}

func (c *policyRequirementsARM) PinAccessToken(ctx context.Context) (context.Context, error) {
	return ctx, nil
}

func (c *policyRequirementsARM) Get(_ context.Context, path string, target any) error {
	switch {
	case strings.Contains(path, "/roleManagementPolicyAssignments?"):
		return json.Unmarshal([]byte(c.policy), target)
	case strings.Contains(path, "/roleEligibilityScheduleInstances?"):
		return json.Unmarshal([]byte(`{"value":[{"id":"eligible-1","properties":{"scope":"/subscriptions/sub-1","roleDefinitionId":"reader","expandedProperties":{"roleDefinition":{"displayName":"Reader"}}}}]}`), target)
	case strings.Contains(path, "/roleAssignmentScheduleInstances?"):
		return json.Unmarshal([]byte(`{"value":[]}`), target)
	default:
		return fmt.Errorf("unexpected ARM GET %s", path)
	}
}

func (c *policyRequirementsARM) Put(context.Context, string, any, any) error {
	c.puts++
	return fmt.Errorf("unexpected activation submission")
}

func policyRequirementsKey(model tui.Model, key tea.KeyType) tui.Model {
	next, command := model.Update(tea.KeyMsg{Type: key})
	return policyRequirementsCommand(next.(tui.Model), command)
}

func policyRequirementsCommand(model tui.Model, command tea.Cmd) tui.Model {
	if command == nil {
		return model
	}
	message := command()
	switch typed := message.(type) {
	case tea.BatchMsg:
		for _, child := range typed {
			model = policyRequirementsCommand(model, child)
		}
		return model
	case spinner.TickMsg:
		return model
	default:
		next, follow := model.Update(message)
		// Only discovery returns follow-up policy work; input focus timers need not run.
		if strings.Contains(fmt.Sprintf("%T", message), "assignmentsDiscoveredMsg") {
			return policyRequirementsCommand(next.(tui.Model), follow)
		}
		return next.(tui.Model)
	}
}
