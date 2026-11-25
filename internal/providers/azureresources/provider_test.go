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

func TestPrepareRequestsOneFilteredPolicyLookupPerScopeAndRole(t *testing.T) {
	readerPath := "/subscriptions/SUB-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2FSUB-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Freader%27&api-version=2020-10-01"
	ownerPath := "/subscriptions/sub-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2Fsub-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Fowner%27&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{
		readerPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{testPolicy("/subscriptions/sub-1", "reader", "PT8H")}},
		ownerPath:  policyAssignmentResponse{Value: []roleManagementPolicyAssignment{testPolicy("/subscriptions/sub-1", "owner", "PT4H", "Justification")}},
	})
	assignments := []pim.EligibleAssignment{
		{ID: "reader", AzureScope: "/subscriptions/SUB-1/", RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader"},
		{ID: "owner", AzureScope: "/subscriptions/sub-1", RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/owner"},
		{ID: "reader-again", AzureScope: "subscriptions/sub-1", RoleDefinitionID: "/providers/Microsoft.Authorization/roleDefinitions/READER"},
	}

	prepared, err := NewProvider(armClient).Prepare(context.Background(), assignments)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].ActivationPolicy.MaximumDurationISO != "PT8H" || prepared[1].ActivationPolicy.MaximumDurationISO != "PT4H" || prepared[2].ActivationPolicy.MaximumDurationISO != "PT8H" {
		t.Fatalf("unexpected policies: %#v", prepared)
	}
	if assignments[0].ActivationPolicy.MaximumDurationISO != "" {
		t.Fatal("Prepare mutated the list-ready input slice")
	}
	paths := armClient.recordedPaths()
	if armClient.pinCalls != 1 || len(paths) != 2 || countPath(paths, readerPath) != 1 || countPath(paths, ownerPath) != 1 {
		t.Fatalf("expected one token and one filtered request per scope and role, pins=%d paths=%#v", armClient.pinCalls, paths)
	}
	for _, path := range paths {
		if !strings.Contains(path, "$filter=roleDefinitionId") {
			t.Fatalf("unexpected unfiltered policy request %s", path)
		}
	}
}

func TestPrepareTargetScopeLooksUpActiveStateAndPolicyConcurrently(t *testing.T) {
	scope := "/subscriptions/sub-1"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2Fsub-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Freader%27&api-version=2020-10-01"
	end := time.Now().UTC().Add(time.Hour)
	armClient := newProviderFakeARM(map[string]any{
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{Properties: roleAssignmentScheduleProperties{
			Scope: scope, LinkedRoleEligibilityScheduleID: "schedule-1", AssignmentType: "Activated", Status: "Provisioned", EndDateTime: &end,
		}}}},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{testPolicy(scope, "reader", "PT8H")}},
	})
	policyArrived := make(chan struct{})
	var once sync.Once
	armClient.onGet = func(ctx context.Context, path string) error {
		switch path {
		case policyPath:
			once.Do(func() { close(policyArrived) })
		case activePath:
			select {
			case <-policyArrived:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("active lookup was not concurrent with the policy lookup")
			}
		}
		return nil
	}

	prepared, err := NewProvider(armClient).PrepareTargetScope(context.Background(), pim.EligibleAssignment{
		DisplayName: "Reader", AzureScope: scope, EligibilityScheduleID: "schedule-1",
		RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/reader",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Active || prepared.ActiveUntil == nil || prepared.ActivationPolicy.MaximumDurationISO != "PT8H" {
		t.Fatalf("expected active state and policy, got %#v", prepared)
	}
	if armClient.pinCalls != 1 {
		t.Fatalf("expected one pinned token, got %d", armClient.pinCalls)
	}
}

func TestPrepareTargetScopeCancelsActiveLookupWhenPolicyLookupFails(t *testing.T) {
	scope := "/subscriptions/sub-1"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?$filter=roleDefinitionId+eq+%27%2Fsubscriptions%2Fsub-1%2Fproviders%2FMicrosoft.Authorization%2FroleDefinitions%2Freader%27&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{activePath: activeAssignmentResponse{}})
	armClient.errs = map[string]error{policyPath: errors.New("authorization denied")}
	armClient.onGet = func(ctx context.Context, path string) error {
		if path != activePath {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("active lookup was not canceled")
		}
	}

	_, err := NewProvider(armClient).PrepareTargetScope(context.Background(), pim.EligibleAssignment{
		AzureScope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/reader",
	})
	if err == nil || !strings.Contains(err.Error(), "list Azure activation policies at "+scope) || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected policy lookup error, got %v", err)
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

func TestDiscoverCapsScopedActiveLookupsAtEight(t *testing.T) {
	const limit = 8
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	responses := map[string]any{}
	var eligibilities []roleEligibilityScheduleInstance
	for index := range limit + 1 {
		scope := fmt.Sprintf("/subscriptions/sub-%d", index)
		eligibilities = append(eligibilities, roleEligibilityScheduleInstance{Properties: roleEligibilityProperties{Scope: scope}})
		responses[scope+"/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"] = activeAssignmentResponse{}
	}
	responses[eligibilityPath] = eligibilityResponse{Value: eligibilities}
	armClient := newProviderFakeARM(responses)
	started := make(chan string, limit+1)
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
	for range limit {
		receiveWithin(t, started, "scoped active lookup")
	}
	select {
	case path := <-started:
		t.Fatalf("request beyond the concurrency limit started before capacity was released: %s", path)
	default:
	}
	if got := armClient.maximumInFlight(); got != limit {
		t.Fatalf("expected %d in-flight requests, got %d", limit, got)
	}
	close(release)
	if err := receiveWithin(t, done, "Discover"); err != nil {
		t.Fatal(err)
	}
	if got := activeRequestCount(armClient.recordedPaths()); got != limit+1 {
		t.Fatalf("expected all %d scoped requests, got %d paths=%#v", limit+1, got, armClient.recordedPaths())
	}
}

func TestDiscoverCancelsQueuedScopedActiveLookupsAfterError(t *testing.T) {
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	responses := map[string]any{}
	var eligibilities []roleEligibilityScheduleInstance
	for index := range maxConcurrentScopeRequests + 1 {
		scope := fmt.Sprintf("/subscriptions/sub-%d", index)
		eligibilities = append(eligibilities, roleEligibilityScheduleInstance{Properties: roleEligibilityProperties{Scope: scope}})
		responses[scope+"/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"] = activeAssignmentResponse{}
	}
	responses[eligibilityPath] = eligibilityResponse{Value: eligibilities}
	armClient := newProviderFakeARM(responses)
	started := make(chan string, maxConcurrentScopeRequests+1)
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
	for range maxConcurrentScopeRequests {
		receiveWithin(t, started, "scoped active lookup")
	}
	close(releaseFailure)
	err := receiveWithin(t, done, "Discover")
	failedScope := strings.Split(<-failedPath, "/providers/Microsoft.Authorization")[0]
	if err == nil || !strings.Contains(err.Error(), "list active Azure role assignments at "+failedScope) || !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected wrapped scoped error, got %v", err)
	}
	if got := activeRequestCount(armClient.recordedPaths()); got != maxConcurrentScopeRequests {
		t.Fatalf("expected queued request cancellation after %d starts, got %d paths=%#v", maxConcurrentScopeRequests, got, armClient.recordedPaths())
	}
}

func TestDiscoverListsSubscriptionNamesDuringActiveLookups(t *testing.T) {
	scope := "/subscriptions/sub-1/resourceGroups/rg-app"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	subscriptionsPath := "/subscriptions?api-version=2022-12-01"
	armClient := newProviderFakeARM(map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{
			Scope: scope, ExpandedProperties: expandedProperties{Scope: expandedScope{Type: "resourcegroup"}},
		}}}},
		activePath:        activeAssignmentResponse{},
		subscriptionsPath: subscriptionListResponse{Value: []subscriptionItem{{SubscriptionID: "SUB-1", DisplayName: "Prod"}}},
	})
	listed := make(chan struct{})
	var once sync.Once
	armClient.onGet = func(ctx context.Context, path string) error {
		switch path {
		case subscriptionsPath:
			once.Do(func() { close(listed) })
		case activePath:
			select {
			case <-listed:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("subscription listing did not run during active lookups")
			}
		}
		return nil
	}

	assignments, err := NewProvider(armClient).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || assignments[0].Scope.SubscriptionName != "Prod" {
		t.Fatalf("expected subscription label from listing, got %#v", assignments)
	}
}

func TestDiscoverIgnoresSubscriptionListingFailure(t *testing.T) {
	scope := "/subscriptions/sub-1/resourceGroups/rg-app"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	armClient := newProviderFakeARM(map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{
			Scope: scope, ExpandedProperties: expandedProperties{Scope: expandedScope{Type: "resourcegroup"}},
		}}}},
		activePath: activeAssignmentResponse{},
	})
	armClient.errs = map[string]error{"/subscriptions?api-version=2022-12-01": errors.New("forbidden")}

	assignments, err := NewProvider(armClient).Discover(context.Background())
	if err != nil {
		t.Fatalf("subscription listing is best-effort, got %v", err)
	}
	if len(assignments) != 1 || assignments[0].Scope.SubscriptionName != "sub-1" {
		t.Fatalf("expected subscription ID label fallback, got %#v", assignments)
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
		{ID: "/providers/Microsoft.Management/managementGroups/mg-child", DisplayName: "Child MG", Type: pim.ScopeTypeManagementGroup, ParentID: mgScope},
		{ID: "/subscriptions/sub-1", DisplayName: "Prod Subscription", Type: pim.ScopeTypeSubscription, ParentID: mgScope},
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
		{ID: "/subscriptions/sub-1/resourceGroups/rg-app", DisplayName: "rg-app", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "Prod Subscription", ParentID: "/subscriptions/sub-1"},
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

const resourceGraphPath = "/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01"

func TestIndexDescendantScopesSetsParentsAndSubscriptionNames(t *testing.T) {
	mgRoot := "/providers/Microsoft.Management/managementGroups/mg-root"
	mgPlatform := "/providers/Microsoft.Management/managementGroups/mg-platform"
	subProd := "/subscriptions/sub-prod"
	rgApp := subProd + "/resourceGroups/rg-app"
	rgOrphan := "/subscriptions/sub-other/resourceGroups/rg-x"
	armClient := newProviderFakeARM(nil)
	armClient.postResponses = map[string][]any{resourceGraphPath: {
		resourceGraphResponse{SkipToken: "page-2", Data: []resourceGraphRow{
			{ID: mgRoot + "/", Name: "mg-root", DisplayName: "Root MG", Type: "microsoft.management/managementgroups", Parent: "/providers/Microsoft.Management/managementGroups/tenant"},
			{ID: mgPlatform, Name: "mg-platform", DisplayName: "Platform MG", Type: "microsoft.management/managementgroups", Parent: mgRoot},
			{ID: subProd, Name: "Prod Sub", Type: "microsoft.resources/subscriptions", Parent: "mg-platform"},
		}},
		resourceGraphResponse{Data: []resourceGraphRow{
			{ID: rgApp, Name: "rg-app", Type: "microsoft.resources/subscriptions/resourcegroups"},
			{ID: rgOrphan, Name: "rg-x", Type: "microsoft.resources/subscriptions/resourcegroups"},
		}},
	}}

	scopes, err := NewProvider(armClient).IndexDescendantScopes(context.Background(), pim.Scope{ID: mgRoot, DisplayName: "Root MG", Type: pim.ScopeTypeManagementGroup})
	if err != nil {
		t.Fatal(err)
	}
	want := []pim.Scope{
		{ID: mgPlatform, DisplayName: "Platform MG", Type: pim.ScopeTypeManagementGroup, ParentID: mgRoot},
		{ID: subProd, DisplayName: "Prod Sub", Type: pim.ScopeTypeSubscription, ParentID: mgPlatform},
		{ID: rgApp, DisplayName: "rg-app", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "Prod Sub", ParentID: subProd},
		{ID: rgOrphan, DisplayName: "rg-x", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "sub-other", ParentID: "/subscriptions/sub-other"},
	}
	if !slices.Equal(scopes, want) {
		t.Fatalf("unexpected indexed scopes:\n got %#v\nwant %#v", scopes, want)
	}
	if armClient.pinCalls != 1 || len(armClient.postBodies) != 2 || len(armClient.recordedPaths()) != 0 {
		t.Fatalf("expected one token and two Resource Graph pages only, pins=%d posts=%d gets=%#v", armClient.pinCalls, len(armClient.postBodies), armClient.recordedPaths())
	}
	first := armClient.postBodies[0]
	if !slices.Equal(first.ManagementGroups, []string{"mg-root"}) || armClient.postBodies[1].Options.SkipToken != "page-2" {
		t.Fatalf("unexpected Resource Graph requests %#v", armClient.postBodies)
	}
	for _, fragment := range []string{
		"extend parent = case(type =~ 'microsoft.management/managementgroups', tostring(properties.details.parent.id), type =~ 'microsoft.resources/subscriptions', tostring(properties.managementGroupAncestorsChain[0].name), '')",
		", parent",
	} {
		if !strings.Contains(first.Query, fragment) {
			t.Fatalf("Resource Graph query %q lacks %q", first.Query, fragment)
		}
	}
}

func TestIndexDescendantScopesUnderSubscriptionUsesRootName(t *testing.T) {
	armClient := newProviderFakeARM(nil)
	armClient.postResponses = map[string][]any{resourceGraphPath: {
		resourceGraphResponse{Data: []resourceGraphRow{{ID: "/subscriptions/sub-1/resourceGroups/rg-app", Name: "rg-app", Type: "microsoft.resources/subscriptions/resourcegroups"}}},
	}}
	scopes, err := NewProvider(armClient).IndexDescendantScopes(context.Background(), pim.Scope{ID: "/subscriptions/sub-1/", DisplayName: "Prod", Type: pim.ScopeTypeSubscription})
	if err != nil {
		t.Fatal(err)
	}
	want := []pim.Scope{{ID: "/subscriptions/sub-1/resourceGroups/rg-app", DisplayName: "rg-app", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "Prod", ParentID: "/subscriptions/sub-1"}}
	if !slices.Equal(scopes, want) || !slices.Equal(armClient.postBodies[0].Subscriptions, []string{"sub-1"}) {
		t.Fatalf("unexpected subscription index %#v request %#v", scopes, armClient.postBodies)
	}

	leaf, err := NewProvider(armClient).IndexDescendantScopes(context.Background(), pim.Scope{ID: "/subscriptions/sub-1/resourceGroups/rg-app", Type: pim.ScopeTypeResourceGroup})
	if leaf != nil || err != nil || armClient.pinCalls != 1 {
		t.Fatalf("resource group root must not query, got %#v err=%v pins=%d", leaf, err, armClient.pinCalls)
	}
}

func TestIndexDescendantScopesReturnsPartialResultsOnPageFailure(t *testing.T) {
	throttled := errors.New("throttled")
	armClient := newProviderFakeARM(nil)
	armClient.postResponses = map[string][]any{resourceGraphPath: {
		resourceGraphResponse{SkipToken: "page-2", Data: []resourceGraphRow{{ID: "/subscriptions/sub-1", Name: "Sub One", Type: "microsoft.resources/subscriptions", Parent: "mg-root"}}},
		throttled,
	}}
	scopes, err := NewProvider(armClient).IndexDescendantScopes(context.Background(), pim.Scope{ID: "/providers/Microsoft.Management/managementGroups/mg-root", Type: pim.ScopeTypeManagementGroup})
	if !errors.Is(err, throttled) {
		t.Fatalf("expected page failure, got %v", err)
	}
	if len(scopes) != 1 || scopes[0].ID != "/subscriptions/sub-1" || scopes[0].ParentID != "/providers/Microsoft.Management/managementGroups/mg-root" {
		t.Fatalf("expected partial index, got %#v", scopes)
	}
}

func TestWalkDescendantScopesSkipsIndexedSubscriptions(t *testing.T) {
	const endpoint = "/providers/Microsoft.Authorization/eligibleChildResources?"
	const rgFilter = "$filter=resourceType+eq+%27resourcegroup%27&api-version=2020-10-01"
	mgRoot := "/providers/Microsoft.Management/managementGroups/mg-root"
	mgChild := "/providers/Microsoft.Management/managementGroups/mg-child"
	subIndexed := "/subscriptions/sub-indexed"
	subWalked := "/subscriptions/sub-walked"
	subDeep := "/subscriptions/sub-deep"
	rgWalked := subWalked + "/resourceGroups/rg-walked"
	armClient := newProviderFakeARM(map[string]any{
		mgRoot + endpoint + "api-version=2020-10-01": childResourceResponse{Value: []eligibleChildResource{
			{ID: mgChild, Name: "Child MG", Type: "managementgroup"},
			{ID: subIndexed, Name: "Indexed Sub", Type: "subscription"},
			{ID: subWalked, Name: "Walked Sub", Type: "subscription"},
		}},
		mgChild + endpoint + "api-version=2020-10-01": childResourceResponse{Value: []eligibleChildResource{{ID: subDeep, Name: "Deep Sub", Type: "subscription"}}},
		subWalked + endpoint + rgFilter:               childResourceResponse{Value: []eligibleChildResource{{ID: rgWalked, Name: "rg-walked", Type: "resourcegroup"}}},
		subDeep + endpoint + rgFilter:                 childResourceResponse{},
	})
	indexed := []pim.Scope{
		{ID: "/SUBSCRIPTIONS/sub-indexed/", Type: pim.ScopeTypeSubscription},
		{ID: subIndexed + "/resourceGroups/rg-indexed", Type: pim.ScopeTypeResourceGroup},
	}

	scopes, err := NewProvider(armClient).WalkDescendantScopes(context.Background(), pim.Scope{ID: mgRoot, Type: pim.ScopeTypeManagementGroup}, indexed)
	if err != nil {
		t.Fatal(err)
	}
	want := []pim.Scope{
		{ID: mgChild, DisplayName: "Child MG", Type: pim.ScopeTypeManagementGroup, ParentID: mgRoot},
		{ID: subDeep, DisplayName: "Deep Sub", Type: pim.ScopeTypeSubscription, ParentID: mgChild},
		{ID: subIndexed, DisplayName: "Indexed Sub", Type: pim.ScopeTypeSubscription, ParentID: mgRoot},
		{ID: subWalked, DisplayName: "Walked Sub", Type: pim.ScopeTypeSubscription, ParentID: mgRoot},
		{ID: rgWalked, DisplayName: "rg-walked", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "Walked Sub", ParentID: subWalked},
	}
	if !slices.Equal(scopes, want) {
		t.Fatalf("unexpected walk:\n got %#v\nwant %#v", scopes, want)
	}
	for _, path := range armClient.recordedPaths() {
		if strings.HasPrefix(path, subIndexed) {
			t.Fatalf("indexed subscription children were listed: %s", path)
		}
	}
	if armClient.pinCalls != 1 || armClient.postCalls != 0 {
		t.Fatalf("expected one token and no Resource Graph calls, pins=%d posts=%d", armClient.pinCalls, armClient.postCalls)
	}
}

func TestWalkDescendantScopesKeepsResultsWhenSubtreeFails(t *testing.T) {
	const root = "/providers/Microsoft.Management/managementGroups/root"
	const sub = "/subscriptions/sub"
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
	scopes, err := NewProvider(client).WalkDescendantScopes(context.Background(), pim.Scope{ID: root, Type: pim.ScopeTypeManagementGroup}, nil)
	if !errors.Is(err, throttled) {
		t.Fatalf("expected partial-walk diagnostic; got %v", err)
	}
	for _, id := range []string{sub, treeRG} {
		if !slices.ContainsFunc(scopes, func(s pim.Scope) bool { return s.ID == id }) {
			t.Errorf("lost known descendant %s: %v", id, scopes)
		}
	}
}

func receiveWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

type providerFakeARM struct {
	mu            sync.Mutex
	responses     map[string]any
	postResponses map[string][]any
	postBodies    []resourceGraphRequest
	errs          map[string]error
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

// Post answers queued resourceGraphResponse or error values in order.
func (f *providerFakeARM) Post(_ context.Context, path string, body any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postCalls++
	if request, ok := body.(resourceGraphRequest); ok {
		f.postBodies = append(f.postBodies, request)
	}
	queue := f.postResponses[path]
	if len(queue) == 0 {
		return fmt.Errorf("unexpected POST %s", path)
	}
	f.postResponses[path] = queue[1:]
	switch item := queue[0].(type) {
	case error:
		return item
	case resourceGraphResponse:
		*out.(*resourceGraphResponse) = item
		return nil
	default:
		return fmt.Errorf("unsupported queued POST response %T", item)
	}
}

func (f *providerFakeARM) Put(context.Context, string, any, any) error {
	return nil
}
