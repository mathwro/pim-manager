package azureresources

import (
	"context"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestNormalizeEligibility(t *testing.T) {
	item := roleEligibilityScheduleInstance{
		ID:   "eligibility-1",
		Name: "eligibility-name",
		Properties: roleEligibilityProperties{
			Scope:                     "subscriptions/sub-1/resourceGroups/rg-prod",
			RoleDefinitionID:          "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/contributor",
			PrincipalID:               "principal-1",
			RoleEligibilityScheduleID: "schedule-1",
			ExpandedProperties: expandedProperties{
				Scope:          expandedScope{DisplayName: "rg-prod", Type: "resourcegroup"},
				RoleDefinition: expandedRoleDefinition{DisplayName: "Contributor"},
			},
		},
	}

	got := normalizeEligibility(item)

	if got.Source != pim.AssignmentSourceAzureResource {
		t.Fatalf("expected Azure Resource source, got %s", got.Source)
	}
	if got.DisplayName != "Contributor" {
		t.Fatalf("expected Contributor, got %q", got.DisplayName)
	}
	if got.Scope.Type != pim.ScopeTypeResourceGroup {
		t.Fatalf("expected resource group scope, got %s", got.Scope.Type)
	}
}

func TestActivationRequestBody(t *testing.T) {
	body := activationBody(pim.ActivationRequest{
		Assignment: pim.EligibleAssignment{
			PrincipalID:           "principal-1",
			RoleDefinitionID:      "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/contributor",
			EligibilityScheduleID: "schedule-1",
		},
		Justification: "Need resource access",
		DurationISO:   "PT3H",
	})

	if body.Properties.RequestType != "SelfActivate" {
		t.Fatalf("expected SelfActivate, got %q", body.Properties.RequestType)
	}
	if body.Properties.ScheduleInfo.Expiration.Duration != "PT3H" {
		t.Fatalf("expected PT3H, got %q", body.Properties.ScheduleInfo.Expiration.Duration)
	}
}

func TestDiscoverFetchesCurrentUsersEligibilitiesOnceAtTenantScope(t *testing.T) {
	tenantPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	arm := &providerFakeARM{responses: map[string]any{
		tenantPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{ID: "eligibility-1"}}},
	}}
	provider := NewProvider(arm)

	assignments, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %#v", assignments)
	}
	if len(arm.paths) != 1 || arm.paths[0] != tenantPath {
		t.Fatalf("expected one tenant-scope request %q, got %#v", tenantPath, arm.paths)
	}
}

func TestDiscoverFollowsPaginatedEligibilities(t *testing.T) {
	firstPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	nextPath := "https://management.azure.com/subscriptions/sub-1/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$skiptoken=next"
	arm := &providerFakeARM{
		responses: map[string]any{
			firstPath: eligibilityResponse{
				Value: []roleEligibilityScheduleInstance{{
					ID: "eligibility-1",
					Properties: roleEligibilityProperties{
						Scope:                     "/subscriptions/sub-1",
						RoleEligibilityScheduleID: "schedule-1",
						ExpandedProperties: expandedProperties{
							RoleDefinition: expandedRoleDefinition{DisplayName: "Reader"},
						},
					},
				}},
				NextLink: nextPath,
			},
			nextPath: eligibilityResponse{
				Value: []roleEligibilityScheduleInstance{{
					ID: "eligibility-2",
					Properties: roleEligibilityProperties{
						Scope:                     "/subscriptions/sub-1",
						RoleEligibilityScheduleID: "schedule-2",
						ExpandedProperties: expandedProperties{
							RoleDefinition: expandedRoleDefinition{DisplayName: "Contributor"},
						},
					},
				}},
			},
		},
	}
	provider := NewProvider(arm)

	assignments, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %#v", assignments)
	}
	if assignments[1].DisplayName != "Contributor" {
		t.Fatalf("expected paginated assignment, got %#v", assignments[1])
	}
}

type providerFakeARM struct {
	responses map[string]any
	paths     []string
}

func (f *providerFakeARM) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	if response, ok := f.responses[path]; ok {
		*out.(*eligibilityResponse) = response.(eligibilityResponse)
	}
	return nil
}

func (f *providerFakeARM) Put(context.Context, string, any, any) error {
	return nil
}
