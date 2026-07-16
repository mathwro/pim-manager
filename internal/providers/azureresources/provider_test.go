package azureresources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestDiscoverEnrichesActiveStateAndActivationPolicy(t *testing.T) {
	scope := "/subscriptions/sub-1"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	end := time.Now().UTC().Add(time.Hour)
	arm := &providerFakeARM{responses: map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{
			ID: "eligibility-1",
			Properties: roleEligibilityProperties{
				Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
				RoleEligibilityScheduleID: "schedule-1",
				ExpandedProperties:        expandedProperties{RoleDefinition: expandedRoleDefinition{DisplayName: "Owner"}},
			},
		}}},
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{Properties: roleAssignmentScheduleProperties{
			LinkedRoleEligibilityScheduleID: "/providers/Microsoft.Authorization/roleEligibilitySchedules/SCHEDULE-1",
			AssignmentType:                  "Activated", Status: "Provisioned", EndDateTime: &end,
		}}}},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
			Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
			EffectiveRules: []roleManagementPolicyRule{
				{ID: "Expiration_EndUser_Assignment", MaximumDuration: "PT4H"},
				{ID: "Enablement_EndUser_Assignment", EnabledRules: []string{"Justification"}},
			},
		}}}},
	}}

	assignments, err := NewProvider(arm).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 || !assignments[0].Active || assignments[0].ActiveUntil == nil {
		t.Fatalf("expected active assignment, got %#v", assignments)
	}
	if assignments[0].ActivationPolicy.MaximumDurationISO != "PT4H" || !assignments[0].ActivationPolicy.JustificationRequired {
		t.Fatalf("unexpected policy: %#v", assignments[0].ActivationPolicy)
	}
	if !assignments[0].ActiveUntil.Equal(end) {
		t.Fatalf("expected active expiry %s, got %v", end, assignments[0].ActiveUntil)
	}
	wantPaths := []string{eligibilityPath, activePath, policyPath}
	if len(arm.paths) != len(wantPaths) {
		t.Fatalf("expected paths %#v, got %#v", wantPaths, arm.paths)
	}
	for index := range wantPaths {
		if arm.paths[index] != wantPaths[index] {
			t.Fatalf("expected paths %#v, got %#v", wantPaths, arm.paths)
		}
	}
}

func TestDiscoverFollowsPaginatedEligibilities(t *testing.T) {
	scope := "/subscriptions/sub-1"
	firstPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	nextPath := "https://management.azure.com/subscriptions/sub-1/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$skiptoken=next"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := &providerFakeARM{responses: map[string]any{
		firstPath: eligibilityResponse{
			Value: []roleEligibilityScheduleInstance{{
				ID: "eligibility-1",
				Properties: roleEligibilityProperties{
					Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/reader",
					RoleEligibilityScheduleID: "schedule-1",
					ExpandedProperties:        expandedProperties{RoleDefinition: expandedRoleDefinition{DisplayName: "Reader"}},
				},
			}},
			NextLink: nextPath,
		},
		nextPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{
			ID: "eligibility-2",
			Properties: roleEligibilityProperties{
				Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/contributor",
				RoleEligibilityScheduleID: "schedule-2",
				ExpandedProperties:        expandedProperties{RoleDefinition: expandedRoleDefinition{DisplayName: "Contributor"}},
			},
		}}},
		activePath: activeAssignmentResponse{},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{
			testPolicy(scope, "reader", "PT8H"),
			testPolicy(scope, "contributor", "PT4H", "Justification"),
		}},
	}}

	assignments, err := NewProvider(arm).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 2 || assignments[1].DisplayName != "Contributor" {
		t.Fatalf("expected paginated assignments, got %#v", assignments)
	}
	if assignments[0].ActivationPolicy.MaximumDurationISO != "PT8H" || assignments[1].ActivationPolicy.MaximumDurationISO != "PT4H" {
		t.Fatalf("expected policies on both pages, got %#v", assignments)
	}
}

func TestMetadataLookupsFollowPagination(t *testing.T) {
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activeNext := "https://management.azure.com/active-next"
	policyPath := "/subscriptions/sub-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	policyNext := "https://management.azure.com/policy-next"
	arm := &providerFakeARM{responses: map[string]any{
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{}}, NextLink: activeNext},
		activeNext: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{}}},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{}}, NextLink: policyNext},
		policyNext: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{}}},
	}}
	provider := NewProvider(arm)
	active, activeErr := provider.discoverActiveAssignments(context.Background())
	policies, policyErr := provider.policiesForScope(context.Background(), "/subscriptions/sub-1")
	if activeErr != nil || policyErr != nil || len(active) != 2 || len(policies) != 2 {
		t.Fatalf("expected paginated metadata, active=%d policies=%d errors=%v/%v", len(active), len(policies), activeErr, policyErr)
	}
}

func TestDiscoverLoadsPolicyOncePerNormalizedScope(t *testing.T) {
	firstScope := "/subscriptions/SUB-1/"
	secondScope := "/subscriptions/sub-1"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := "/subscriptions/SUB-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := &providerFakeARM{responses: map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{
			{Properties: roleEligibilityProperties{Scope: firstScope, RoleDefinitionID: firstScope + "providers/Microsoft.Authorization/roleDefinitions/reader", RoleEligibilityScheduleID: "schedule-1"}},
			{Properties: roleEligibilityProperties{Scope: secondScope, RoleDefinitionID: secondScope + "/providers/Microsoft.Authorization/roleDefinitions/owner", RoleEligibilityScheduleID: "schedule-2"}},
		}},
		activePath: activeAssignmentResponse{},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{
			testPolicy(secondScope, "reader", "PT8H"),
			testPolicy(secondScope, "owner", "PT4H", "Justification"),
		}},
	}}
	if _, err := NewProvider(arm).Discover(context.Background()); err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	count := 0
	for _, path := range arm.paths {
		if strings.Contains(path, "/roleManagementPolicyAssignments?") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one policy lookup for normalized shared scope, got %d paths=%#v", count, arm.paths)
	}
}

func TestDiscoverPropagatesEligibilityLookupError(t *testing.T) {
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	arm := &providerFakeARM{errs: map[string]error{eligibilityPath: errors.New("authorization denied")}}
	_, err := NewProvider(arm).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list eligible Azure role assignments") || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected eligibility lookup context, got %v", err)
	}
}

func TestDiscoverPropagatesActiveLookupError(t *testing.T) {
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	arm := &providerFakeARM{
		responses: map[string]any{eligibilityPath: eligibilityResponse{}},
		errs:      map[string]error{activePath: errors.New("authorization denied")},
	}
	_, err := NewProvider(arm).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list active Azure role assignments") || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected active lookup context, got %v", err)
	}
}

func TestDiscoverPropagatesPolicyLookupError(t *testing.T) {
	scope := "/subscriptions/sub-1"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := &providerFakeARM{
		responses: map[string]any{
			eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{Scope: scope}}}},
			activePath:      activeAssignmentResponse{},
		},
		errs: map[string]error{policyPath: errors.New("authorization denied")},
	}
	_, err := NewProvider(arm).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list Azure activation policies at "+scope) || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected policy lookup context, got %v", err)
	}
}

func TestDiscoverRejectsMissingPolicyForEligibility(t *testing.T) {
	scope := "/subscriptions/sub-1"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := &providerFakeARM{responses: map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{
			Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/reader",
			ExpandedProperties: expandedProperties{RoleDefinition: expandedRoleDefinition{DisplayName: "Reader"}},
		}}}},
		activePath: activeAssignmentResponse{},
		policyPath: policyAssignmentResponse{},
	}}
	_, err := NewProvider(arm).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "activation policy for Reader at "+scope) || !strings.Contains(err.Error(), "no activation policy") {
		t.Fatalf("expected contextual missing policy error, got %v", err)
	}
}

func TestDiscoverRejectsMalformedPolicyForEligibility(t *testing.T) {
	scope := "/subscriptions/sub-1"
	roleDefinitionID := scope + "/providers/Microsoft.Authorization/roleDefinitions/reader"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := &providerFakeARM{responses: map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{
			Scope: scope, RoleDefinitionID: roleDefinitionID,
			ExpandedProperties: expandedProperties{RoleDefinition: expandedRoleDefinition{DisplayName: "Reader"}},
		}}}},
		activePath: activeAssignmentResponse{},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
			RoleDefinitionID: roleDefinitionID,
			EffectiveRules:   []roleManagementPolicyRule{{ID: "Enablement_EndUser_Assignment"}},
		}}}},
	}}
	_, err := NewProvider(arm).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "activation policy for Reader at "+scope) || !strings.Contains(err.Error(), "incomplete end-user activation policy") {
		t.Fatalf("expected contextual malformed policy error, got %v", err)
	}
}

type providerFakeARM struct {
	responses map[string]any
	errs      map[string]error
	paths     []string
}

func (f *providerFakeARM) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	if err := f.errs[path]; err != nil {
		return err
	}
	response, ok := f.responses[path]
	if !ok {
		return fmt.Errorf("unexpected GET %s", path)
	}
	switch target := out.(type) {
	case *eligibilityResponse:
		*target = response.(eligibilityResponse)
	case *activeAssignmentResponse:
		*target = response.(activeAssignmentResponse)
	case *policyAssignmentResponse:
		*target = response.(policyAssignmentResponse)
	default:
		return fmt.Errorf("unsupported response target %T", out)
	}
	return nil
}

func (f *providerFakeARM) Put(context.Context, string, any, any) error {
	return nil
}
