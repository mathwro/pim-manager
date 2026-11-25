package azureresources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
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

func TestDiscoverUsesScopedActiveLookupWithoutLoadingPolicies(t *testing.T) {
	scope := "/subscriptions/sub-1/resourceGroups/rg-prod"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	end := time.Now().UTC().Add(time.Hour)
	armClient := newProviderFakeARM(map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{
			Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner", RoleEligibilityScheduleID: "schedule-1",
		}}}},
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{Properties: roleAssignmentScheduleProperties{
			LinkedRoleEligibilityScheduleID: "schedule-1", AssignmentType: "Activated", Status: "Provisioned", EndDateTime: &end,
		}}}},
	})

	assignments, err := NewProvider(armClient).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || !assignments[0].Active || assignments[0].ActivationPolicy.MaximumDurationISO != "" {
		t.Fatalf("expected list-ready active assignment, got %#v", assignments)
	}
	if armClient.pinCalls != 1 || slices.Contains(armClient.recordedPaths(), "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01") {
		t.Fatalf("expected one pin and scoped active path, pins=%d paths=%#v", armClient.pinCalls, armClient.recordedPaths())
	}
}

func TestPrepareLoadsPoliciesOncePerNormalizedScope(t *testing.T) {
	scope := "/subscriptions/SUB-1/"
	policyPath := "/subscriptions/SUB-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{
		testPolicy("/subscriptions/sub-1", "reader", "PT8H"),
		testPolicy("/subscriptions/sub-1", "owner", "PT4H", "Justification"),
	}}})
	assignments := []pim.EligibleAssignment{
		{ID: "reader", AzureScope: scope, RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader"},
		{ID: "owner", AzureScope: "/subscriptions/sub-1", RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/owner"},
	}

	prepared, err := NewProvider(armClient).Prepare(context.Background(), assignments)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].ActivationPolicy.MaximumDurationISO != "PT8H" || prepared[1].ActivationPolicy.MaximumDurationISO != "PT4H" {
		t.Fatalf("unexpected policies: %#v", prepared)
	}
	if assignments[0].ActivationPolicy.MaximumDurationISO != "" {
		t.Fatal("Prepare mutated the list-ready input slice")
	}
	if armClient.pinCalls != 1 || countPath(armClient.recordedPaths(), policyPath) != 1 {
		t.Fatalf("expected one token and one scope request, pins=%d paths=%#v", armClient.pinCalls, armClient.recordedPaths())
	}
}

func TestDiscoverFollowsPaginatedEligibilities(t *testing.T) {
	scope := "/subscriptions/sub-1"
	firstPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	nextPath := "https://management.azure.com/subscriptions/sub-1/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$skiptoken=next"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{
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
	})

	assignments, err := NewProvider(armClient).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 2 || assignments[1].DisplayName != "Contributor" {
		t.Fatalf("expected paginated assignments, got %#v", assignments)
	}
	if countPath(armClient.recordedPaths(), nextPath) != 1 {
		t.Fatalf("expected eligibility next page, got %#v", armClient.recordedPaths())
	}
}

func TestMetadataLookupsFollowPagination(t *testing.T) {
	scope := "/subscriptions/sub-1"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activeNext := "https://management.azure.com/active-next"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	policyNext := "https://management.azure.com/policy-next"
	armClient := newProviderFakeARM(map[string]any{
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{}}, NextLink: activeNext},
		activeNext: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{}}},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{}}, NextLink: policyNext},
		policyNext: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{}}},
	})
	provider := NewProvider(armClient)
	active, activeErr := provider.discoverActiveAssignments(context.Background(), scope)
	policies, policyErr := provider.policiesForScope(context.Background(), scope, "")
	if activeErr != nil || policyErr != nil || len(active) != 2 || len(policies) != 2 {
		t.Fatalf("expected paginated metadata, active=%d policies=%d errors=%v/%v", len(active), len(policies), activeErr, policyErr)
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
	scope := "/subscriptions/sub-1/resourceGroups/rg-prod"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	armClient := &providerFakeARM{
		responses: map[string]any{eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{Scope: scope}}}}},
		errs:      map[string]error{activePath: errors.New("authorization denied")},
	}
	_, err := NewProvider(armClient).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list active Azure role assignments at "+scope) || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected active lookup context, got %v", err)
	}
}

func TestPreparePropagatesPolicyLookupError(t *testing.T) {
	scope := "/subscriptions/sub-1"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	armClient := &providerFakeARM{errs: map[string]error{policyPath: errors.New("authorization denied")}}
	_, err := NewProvider(armClient).Prepare(context.Background(), []pim.EligibleAssignment{{AzureScope: scope}})
	if err == nil || !strings.Contains(err.Error(), "list Azure activation policies at "+scope) || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected policy lookup context, got %v", err)
	}
}

func TestPrepareRejectsMissingPolicyForEligibility(t *testing.T) {
	scope := "/subscriptions/sub-1"
	roleDefinitionID := scope + "/providers/Microsoft.Authorization/roleDefinitions/reader"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2Fsub-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Freader%27&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{policyPath: policyAssignmentResponse{}})
	assignments := []pim.EligibleAssignment{{
		AzureScope: scope, RoleDefinitionID: roleDefinitionID, DisplayName: "Reader",
	}}
	_, err := NewProvider(armClient).Prepare(context.Background(), assignments)
	if err == nil || !strings.Contains(err.Error(), "activation policy for Reader at "+scope) || !strings.Contains(err.Error(), "no activation policy") {
		t.Fatalf("expected contextual missing policy error, got %v", err)
	}
}

func TestPrepareRejectsMalformedPolicyForEligibility(t *testing.T) {
	scope := "/subscriptions/sub-1"
	roleDefinitionID := scope + "/providers/Microsoft.Authorization/roleDefinitions/reader"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2Fsub-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Freader%27&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
		RoleDefinitionID: roleDefinitionID,
		EffectiveRules:   []roleManagementPolicyRule{{ID: "Enablement_EndUser_Assignment"}},
	}}}}})
	assignments := []pim.EligibleAssignment{{AzureScope: scope, RoleDefinitionID: roleDefinitionID, DisplayName: "Reader"}}
	_, err := NewProvider(armClient).Prepare(context.Background(), assignments)
	if err == nil || !strings.Contains(err.Error(), "activation policy for Reader at "+scope) || !strings.Contains(err.Error(), "incomplete end-user activation policy") {
		t.Fatalf("expected contextual malformed policy error, got %v", err)
	}
}

func TestDiscoverCapsScopedActiveLookupsAtFour(t *testing.T) {
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	responses := map[string]any{}
	var eligibilities []roleEligibilityScheduleInstance
	for index := range 5 {
		scope := fmt.Sprintf("/subscriptions/sub-%d", index)
		eligibilities = append(eligibilities, roleEligibilityScheduleInstance{Properties: roleEligibilityProperties{Scope: scope}})
		responses[scope+"/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"] = activeAssignmentResponse{}
	}
	responses[eligibilityPath] = eligibilityResponse{Value: eligibilities}
	armClient := newProviderFakeARM(responses)
	started := make(chan string, 5)
	release := make(chan struct{})
	armClient.onGet = func(ctx context.Context, path string) error {
		if !strings.Contains(path, "/roleAssignmentScheduleInstances?") {
			return nil
		}
		started <- path
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewProvider(armClient).Discover(context.Background())
		done <- err
	}()
	for range 4 {
		<-started
	}
	select {
	case path := <-started:
		t.Fatalf("fifth request started before capacity was released: %s", path)
	default:
	}
	if got := armClient.maximumInFlight(); got != maxConcurrentScopeRequests {
		t.Fatalf("expected %d in-flight requests, got %d", maxConcurrentScopeRequests, got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := activeRequestCount(armClient.recordedPaths()); got != 5 {
		t.Fatalf("expected all five scoped requests, got %d paths=%#v", got, armClient.recordedPaths())
	}
}

func TestDiscoverCancelsQueuedScopedActiveLookupsAfterError(t *testing.T) {
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	responses := map[string]any{}
	var eligibilities []roleEligibilityScheduleInstance
	for index := range 5 {
		scope := fmt.Sprintf("/subscriptions/sub-%d", index)
		eligibilities = append(eligibilities, roleEligibilityScheduleInstance{Properties: roleEligibilityProperties{Scope: scope}})
		responses[scope+"/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"] = activeAssignmentResponse{}
	}
	responses[eligibilityPath] = eligibilityResponse{Value: eligibilities}
	armClient := newProviderFakeARM(responses)
	started := make(chan string, 5)
	failedPath := make(chan string, 1)
	releaseFailure := make(chan struct{})
	var first sync.Once
	armClient.onGet = func(ctx context.Context, path string) error {
		if !strings.Contains(path, "/roleAssignmentScheduleInstances?") {
			return nil
		}
		started <- path
		isFirst := false
		first.Do(func() {
			isFirst = true
			failedPath <- path
		})
		if isFirst {
			<-releaseFailure
			return errors.New("authorization denied")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewProvider(armClient).Discover(context.Background())
		done <- err
	}()
	for range 4 {
		<-started
	}
	close(releaseFailure)
	err := <-done
	failedScope := strings.Split(<-failedPath, "/providers/Microsoft.Authorization")[0]
	if err == nil || !strings.Contains(err.Error(), "list active Azure role assignments at "+failedScope) || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected wrapped scoped error, got %v", err)
	}
	if got := activeRequestCount(armClient.recordedPaths()); got != 4 {
		t.Fatalf("expected queued request cancellation after four starts, got %d paths=%#v", got, armClient.recordedPaths())
	}
}

func TestDiscoverKeepsParentSelectableWhenOnlyChildScopeIsActive(t *testing.T) {
	parentScope := "/subscriptions/sub-1"
	childScope := "/subscriptions/sub-1/resourceGroups/rg-prod"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := parentScope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	end := time.Now().UTC().Add(time.Hour)
	armClient := newProviderFakeARM(map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{
			ID: "eligibility-parent",
			Properties: roleEligibilityProperties{
				Scope:                     parentScope,
				RoleDefinitionID:          parentScope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
				RoleEligibilityScheduleID: "schedule-parent",
			},
		}}},
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{
			Properties: roleAssignmentScheduleProperties{
				Scope:                           childScope,
				LinkedRoleEligibilityScheduleID: "schedule-parent",
				AssignmentType:                  "Activated",
				Status:                          "Provisioned",
				EndDateTime:                     &end,
			},
		}}},
	})

	assignments, err := NewProvider(armClient).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(assignments))
	}
	if assignments[0].Active {
		t.Fatalf("expected parent scope to remain selectable when only child scope %s is active, got Active=true", childScope)
	}
}

func TestDiscoverChildScopesFiltersAndFollowsPagination(t *testing.T) {
	mgScope := "/providers/Microsoft.Management/managementGroups/mg-root"
	mgFirst := mgScope + "/providers/Microsoft.Authorization/eligibleChildResources?api-version=2020-10-01"
	mgNext := "https://management.azure.com" + mgScope + "/providers/Microsoft.Authorization/eligibleChildResources?$skiptoken=page2"
	subScope := "/subscriptions/sub-1/"
	subFiltered := "/subscriptions/sub-1/providers/Microsoft.Authorization/eligibleChildResources?$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01"

	armClient := newProviderFakeARM(map[string]any{
		mgFirst: childResourceResponse{
			Value: []eligibleChildResource{
				{ID: "/providers/Microsoft.Management/managementGroups/mg-child", Name: "Child MG", Type: "managementgroup"},
			},
			NextLink: mgNext,
		},
		mgNext: childResourceResponse{
			Value: []eligibleChildResource{
				{ID: "/subscriptions/sub-1", Name: "Prod Subscription", Type: "subscription"},
			},
		},
		subFiltered: childResourceResponse{
			Value: []eligibleChildResource{
				{ID: "/subscriptions/sub-1/resourceGroups/rg-app", Name: "rg-app", Type: "resourcegroup"},
			},
		},
	})
	provider := NewProvider(armClient)

	mgChildren, err := provider.DiscoverChildScopes(context.Background(), pim.Scope{
		ID:   mgScope,
		Type: pim.ScopeTypeManagementGroup,
	})
	if err != nil {
		t.Fatalf("DiscoverChildScopes(MG) returned error: %v", err)
	}
	wantMG := []pim.Scope{
		{ID: "/providers/Microsoft.Management/managementGroups/mg-child", DisplayName: "Child MG", Type: pim.ScopeTypeManagementGroup},
		{ID: "/subscriptions/sub-1", DisplayName: "Prod Subscription", Type: pim.ScopeTypeSubscription},
	}
	if !slices.Equal(mgChildren, wantMG) {
		t.Fatalf("unexpected MG children: got %#v, want %#v", mgChildren, wantMG)
	}

	subChildren, err := provider.DiscoverChildScopes(context.Background(), pim.Scope{
		ID:          subScope,
		DisplayName: "Prod Subscription",
		Type:        pim.ScopeTypeSubscription,
	})
	if err != nil {
		t.Fatalf("DiscoverChildScopes(Subscription) returned error: %v", err)
	}
	wantSub := []pim.Scope{
		{ID: "/subscriptions/sub-1/resourceGroups/rg-app", DisplayName: "rg-app", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "Prod Subscription"},
	}
	if !slices.Equal(subChildren, wantSub) {
		t.Fatalf("unexpected Subscription children: got %#v, want %#v", subChildren, wantSub)
	}
	if armClient.pinCalls != 2 {
		t.Fatalf("expected one token pin per DiscoverChildScopes call, got %d", armClient.pinCalls)
	}
}
func TestDiscoverChildScopesSkipsIndividualResourcesAndPropagatesErrors(t *testing.T) {
	subScope := "/subscriptions/sub-1"
	subFiltered := subScope + "/providers/Microsoft.Authorization/eligibleChildResources?$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{
		subFiltered: childResourceResponse{Value: []eligibleChildResource{
			{ID: "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Storage/storageAccounts/sa1", Name: "sa1", Type: "Microsoft.Storage/storageAccounts"},
			{ID: "/subscriptions/sub-1/resourceGroups/rg-1", Name: "rg-1", Type: "resourcegroup"},
		}},
	})
	provider := NewProvider(armClient)
	children, err := provider.DiscoverChildScopes(context.Background(), pim.Scope{ID: subScope, Type: pim.ScopeTypeSubscription})
	if err != nil {
		t.Fatalf("DiscoverChildScopes returned error: %v", err)
	}
	if len(children) != 1 || children[0].ID != "/subscriptions/sub-1/resourceGroups/rg-1" {
		t.Fatalf("expected only resource group child scope, got %#v", children)
	}

	rgChildren, err := provider.DiscoverChildScopes(context.Background(), pim.Scope{ID: "/subscriptions/sub-1/resourceGroups/rg-1", Type: pim.ScopeTypeResourceGroup})
	if err != nil || len(rgChildren) != 0 {
		t.Fatalf("expected resource group leaf to return no child scopes without ARM call, got %#v err=%v", rgChildren, err)
	}
	if armClient.pinCalls != 1 {
		t.Fatalf("expected resource group leaf not to pin token, pins=%d", armClient.pinCalls)
	}

	failing := &providerFakeARM{errs: map[string]error{subFiltered: errors.New("throttled")}}
	_, err = NewProvider(failing).DiscoverChildScopes(context.Background(), pim.Scope{ID: subScope, Type: pim.ScopeTypeSubscription})
	if err == nil || !strings.Contains(err.Error(), "list eligible child scopes at "+subScope) || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("expected wrapped child scope error, got %v", err)
	}
}

func TestPrepareUsesSingleRoleFilterWhenScopeHasOneRole(t *testing.T) {
	scope := "/subscriptions/sub-1"
	roleID := "/providers/Microsoft.Authorization/roleDefinitions/owner-guid"
	filteredPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2Fsub-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Fowner-guid%27&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{
		filteredPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{
			testPolicy(scope, "owner-guid", "PT2H", "Justification"),
		}},
	})

	prepared, err := NewProvider(armClient).Prepare(context.Background(), []pim.EligibleAssignment{{
		ID:               "owner-1",
		DisplayName:      "Owner",
		AzureScope:       scope,
		RoleDefinitionID: roleID,
	}})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if len(prepared) != 1 || prepared[0].ActivationPolicy.MaximumDurationISO != "PT2H" || !prepared[0].ActivationPolicy.JustificationRequired {
		t.Fatalf("unexpected prepared assignment: %#v", prepared)
	}
	if countPath(armClient.recordedPaths(), filteredPath) != 1 {
		t.Fatalf("expected filtered policy path %s, got %#v", filteredPath, armClient.recordedPaths())
	}
}

func TestDiscoverDescendantScopesMergesResourceGraphAndRecursiveEligibleFallback(t *testing.T) {
	mgRoot := "/providers/Microsoft.Management/managementGroups/mg-root"
	mgPlatform := "/providers/Microsoft.Management/managementGroups/mg-platform"
	subStanding := "/subscriptions/sub-standing"
	subEligibleOnly := "/subscriptions/sub-eligible-only"
	rgStanding := "/subscriptions/sub-standing/resourceGroups/rg-standing"
	rgEligibleOnly := "/subscriptions/sub-eligible-only/resourceGroups/rg-secret"

	armClient := newProviderFakeARM(map[string]any{
		mgRoot + "/providers/Microsoft.Authorization/eligibleChildResources?api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{{ID: mgPlatform, Name: "Platform MG", Type: "managementgroup"}},
		},
		mgPlatform + "/providers/Microsoft.Authorization/eligibleChildResources?api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{
				{ID: subStanding, Name: "Standing Sub", Type: "subscription"},
				{ID: subEligibleOnly, Name: "Secret Sub", Type: "subscription"},
			},
		},
		subStanding + "/providers/Microsoft.Authorization/eligibleChildResources?$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{{ID: rgStanding, Name: "rg-standing", Type: "resourcegroup"}},
		},
		subEligibleOnly + "/providers/Microsoft.Authorization/eligibleChildResources?$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{{ID: rgEligibleOnly, Name: "rg-secret", Type: "resourcegroup"}},
		},
	})
	armClient.postResponses = map[string][]resourceGraphResponse{
		"/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01": {{
			Data: []resourceGraphRow{
				{ID: mgPlatform, Name: "mg-platform", DisplayName: "Platform MG", Type: "microsoft.management/managementgroups"},
				{ID: subStanding, Name: "Standing Sub", Type: "microsoft.resources/subscriptions"},
				{ID: rgStanding, Name: "rg-standing", Type: "microsoft.resources/subscriptions/resourcegroups"},
			},
		}},
	}

	descendants, err := NewProvider(armClient).DiscoverDescendantScopes(context.Background(), pim.Scope{
		ID:          mgRoot,
		DisplayName: "Root MG",
		Type:        pim.ScopeTypeManagementGroup,
	})
	if err != nil {
		t.Fatalf("DiscoverDescendantScopes returned error: %v", err)
	}
	gotIDs := make([]string, 0, len(descendants))
	for _, scope := range descendants {
		gotIDs = append(gotIDs, scope.ID)
	}
	byID := make(map[string]pim.Scope, len(descendants))
	for _, scope := range descendants {
		byID[scope.ID] = scope
	}
	for _, want := range []string{mgPlatform, subStanding, subEligibleOnly, rgStanding, rgEligibleOnly} {
		if !slices.Contains(gotIDs, want) {
			t.Fatalf("missing descendant scope %s in %#v", want, descendants)
		}
	}
	if byID[rgStanding].SubscriptionName != "Standing Sub" {
		t.Fatalf("expected rgStanding SubscriptionName %q, got %q", "Standing Sub", byID[rgStanding].SubscriptionName)
	}
	if byID[rgEligibleOnly].SubscriptionName != "Secret Sub" {
		t.Fatalf("expected rgEligibleOnly SubscriptionName %q, got %q", "Secret Sub", byID[rgEligibleOnly].SubscriptionName)
	}
	if armClient.postCalls != 1 {
		t.Fatalf("expected 1 Resource Graph POST call, got %d", armClient.postCalls)
	}
	if armClient.pinCalls != 1 {
		t.Fatalf("expected 1 pinned token across ARG + recursive walk, got %d", armClient.pinCalls)
	}
}

func TestDiscoverDescendantScopesFallsBackCleanlyWhenResourceGraphFails(t *testing.T) {
	subScope := "/subscriptions/sub-1"
	rgScope := "/subscriptions/sub-1/resourceGroups/rg-app"
	armClient := newProviderFakeARM(map[string]any{
		subScope + "/providers/Microsoft.Authorization/eligibleChildResources?$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{{ID: rgScope, Name: "rg-app", Type: "resourcegroup"}},
		},
	})
	armClient.postErr = errors.New("Resource Graph forbidden")

	descendants, err := NewProvider(armClient).DiscoverDescendantScopes(context.Background(), pim.Scope{
		ID:   subScope,
		Type: pim.ScopeTypeSubscription,
	})
	if err != nil {
		t.Fatalf("expected fallback to succeed when Resource Graph fails, got %v", err)
	}
	if len(descendants) != 1 || descendants[0].ID != rgScope {
		t.Fatalf("expected fallback descendant %s, got %#v", rgScope, descendants)
	}
}

func TestDescendantSearchKeepsResultsWhenSubtreeFails(t *testing.T) {
	const root = "/providers/Microsoft.Management/managementGroups/root"
	const sub = "/subscriptions/sub"
	const graphRG = sub + "/resourceGroups/graph"
	const treeRG = sub + "/resourceGroups/tree"
	const endpoint = "/providers/Microsoft.Authorization/eligibleChildResources?"
	client := newProviderFakeARM(map[string]any{
		root + endpoint + "api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{{ID: sub, Type: "subscription"}},
		},
		sub + endpoint + "$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01": childResourceResponse{
			Value: []eligibleChildResource{{ID: treeRG, Type: "resourcegroup"}}, NextLink: "/next",
		},
	})
	throttled := errors.New("throttled")
	client.errs = map[string]error{"/next": throttled}
	client.postResponses = map[string][]resourceGraphResponse{
		"/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01": {{
			Data: []resourceGraphRow{{ID: graphRG, Type: "resourcegroup"}},
		}},
	}
	scopes, err := NewProvider(client).DiscoverDescendantScopes(context.Background(), pim.Scope{ID: root, Type: pim.ScopeTypeManagementGroup})
	if !errors.Is(err, throttled) {
		t.Fatalf("expected partial-search diagnostic; got %v", err)
	}
	for _, id := range []string{sub, graphRG, treeRG} {
		if !slices.ContainsFunc(scopes, func(s pim.Scope) bool { return s.ID == id }) {
			t.Errorf("lost known descendant %s: %v", id, scopes)
		}
	}
}

type providerFakeARM struct {
	mu            sync.Mutex
	responses     map[string]any
	postResponses map[string][]resourceGraphResponse
	errs          map[string]error
	postErr       error
	paths         []string
	postCalls     int
	pinCalls      int
	onGet         func(context.Context, string) error
	inFlight      int
	maxInFlight   int
}

func newProviderFakeARM(responses map[string]any) *providerFakeARM {
	return &providerFakeARM{responses: responses}
}

func (f *providerFakeARM) PinAccessToken(ctx context.Context) (context.Context, error) {
	f.mu.Lock()
	f.pinCalls++
	f.mu.Unlock()
	return arm.WithAccessToken(ctx, "phase-token"), nil
}

func (f *providerFakeARM) Get(ctx context.Context, path string, out any) error {
	f.mu.Lock()
	f.paths = append(f.paths, path)
	err := f.errs[path]
	response, ok := f.responses[path]
	onGet := f.onGet
	if onGet != nil {
		f.inFlight++
		if f.inFlight > f.maxInFlight {
			f.maxInFlight = f.inFlight
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if onGet != nil {
		err = onGet(ctx, path)
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if !ok {
		if strings.HasPrefix(path, "/subscriptions?") {
			if target, ok := out.(*subscriptionListResponse); ok {
				*target = subscriptionListResponse{}
				return nil
			}
		}
		return fmt.Errorf("unexpected GET %s", path)
	}
	switch target := out.(type) {
	case *eligibilityResponse:
		*target = response.(eligibilityResponse)
	case *activeAssignmentResponse:
		*target = response.(activeAssignmentResponse)
	case *policyAssignmentResponse:
		*target = response.(policyAssignmentResponse)
	case *childResourceResponse:
		*target = response.(childResourceResponse)
	case *subscriptionListResponse:
		*target = response.(subscriptionListResponse)
	default:
		return fmt.Errorf("unsupported response target %T", out)
	}
	return nil
}

func (f *providerFakeARM) recordedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths)
}

func (f *providerFakeARM) maximumInFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInFlight
}

func activeRequestCount(paths []string) int {
	count := 0
	for _, path := range paths {
		if strings.Contains(path, "/roleAssignmentScheduleInstances?") {
			count++
		}
	}
	return count
}

func countPath(paths []string, want string) int {
	count := 0
	for _, path := range paths {
		if path == want {
			count++
		}
	}
	return count
}

func (f *providerFakeARM) Post(_ context.Context, path string, _ any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postCalls++
	if f.postErr != nil {
		return f.postErr
	}
	queue := f.postResponses[path]
	if len(queue) == 0 {
		return nil
	}
	f.postResponses[path] = queue[1:]
	if target, ok := out.(*resourceGraphResponse); ok {
		*target = queue[0]
	}
	return nil
}

func (f *providerFakeARM) Put(context.Context, string, any, any) error {
	return nil
}
