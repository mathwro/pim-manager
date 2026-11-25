package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

type childScopeTestProvider struct {
	*scriptedProvider
	childrenByScope    map[string][]pim.Scope
	descendantsByScope map[string][]pim.Scope
	policiesByScope    map[string]pim.ActivationPolicy
	activeScopes       map[string]bool
	childCalls         []string
	descCalls          []string
	prepareScopes      []string
	childErr           error
	prepareErr         error
}

func (p *childScopeTestProvider) DiscoverChildScopes(_ context.Context, parent pim.Scope) ([]pim.Scope, error) {
	p.childCalls = append(p.childCalls, parent.ID)
	if p.childErr != nil {
		return nil, p.childErr
	}
	return p.childrenByScope[parent.ID], nil
}

func (p *childScopeTestProvider) DiscoverDescendantScopes(_ context.Context, root pim.Scope) ([]pim.Scope, error) {
	p.descCalls = append(p.descCalls, root.ID)
	return p.descendantsByScope[root.ID], nil
}

func (p *childScopeTestProvider) PrepareTargetScope(ctx context.Context, assignment pim.EligibleAssignment) (pim.EligibleAssignment, error) {
	prepared, err := p.Prepare(ctx, []pim.EligibleAssignment{assignment})
	if err != nil {
		return pim.EligibleAssignment{}, err
	}
	out := prepared[0]
	out.Active = p.activeScopes[out.AzureScope]
	return out, nil
}
func (p *childScopeTestProvider) Prepare(_ context.Context, assignments []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	out := make([]pim.EligibleAssignment, len(assignments))
	copy(out, assignments)
	for index := range out {
		p.prepareScopes = append(p.prepareScopes, out[index].AzureScope)
		if policy, ok := p.policiesByScope[out[index].AzureScope]; ok {
			out[index].ActivationPolicy = policy
		}
	}
	return out, nil
}

func runScopeCommands(model Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return model
	}
	msg := cmd()
	switch typed := msg.(type) {
	case tea.BatchMsg:
		for _, child := range typed {
			model = runScopeCommands(model, child)
		}
	case assignmentsDiscoveredMsg, assignmentsPreparedMsg, activationCompletedMsg, tenantsCheckedMsg, authenticationCheckedMsg, activationItemStartedMsg, activationItemResultMsg, childScopesDiscoveredMsg, descendantScopesDiscoveredMsg, scopedAssignmentPreparedMsg:
		next, follow := model.Update(msg)
		model = runScopeCommands(next.(Model), follow)
	}
	return model
}

func TestNarrowScopeDrillsManagementGroupToResourceGroupAndActivatesChildScope(t *testing.T) {
	mgScope := "/providers/Microsoft.Management/managementGroups/mg-root"
	subScope := "/subscriptions/sub-1"
	rgScope := "/subscriptions/sub-1/resourceGroups/rg-app"

	provider := &childScopeTestProvider{
		scriptedProvider: &scriptedProvider{
			discoveries: [][]pim.EligibleAssignment{{{
				ID:                    "owner-mg",
				Source:                pim.AssignmentSourceAzureResource,
				Kind:                  pim.AssignmentKindAzureRole,
				DisplayName:           "Owner",
				PrincipalID:           "group-principal",
				RoleDefinitionID:      mgScope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
				AzureScope:            mgScope,
				EligibilityScheduleID: "schedule-mg",
				Scope:                 pim.Scope{ID: mgScope, DisplayName: "Tenant Root Group", Type: pim.ScopeTypeManagementGroup},
			}}},
			results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated, Message: "Provisioned"}},
		},
		childrenByScope: map[string][]pim.Scope{
			mgScope: {
				{ID: subScope, DisplayName: "Production", Type: pim.ScopeTypeSubscription},
			},
			subScope: {
				{ID: rgScope, DisplayName: "rg-app", Type: pim.ScopeTypeResourceGroup},
			},
		},
		policiesByScope: map[string]pim.ActivationPolicy{
			mgScope: {MaximumDurationISO: "PT8H"},
			rgScope: {MaximumDurationISO: "PT2H", JustificationRequired: true, AuthenticationContext: "c1"},
		},
	}

	model := NewModel(Runtime{
		AzureResources: provider,
		Tenants: tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
			return []azureauth.Tenant{{ID: "tenant-1", DisplayName: "Contoso"}}, nil
		}),
		ARMAuthentication: func(_ context.Context, _ bool, authCtx string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{
				AccessToken: "pinned-arm-token",
				PrincipalID: "user-principal",
				Satisfied:   authCtx == "c1",
			}, nil
		},
	})
	model = runScopeCommands(model, model.Init())

	// Open Azure Resources and complete initial discovery + parent policy prep.
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)
	if model.screen != ScreenAssignments {
		t.Fatalf("expected ScreenAssignments, got %s", model.screen)
	}

	// Press 's' to open scope narrowing on the Management Group assignment.
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = runScopeCommands(next.(Model), cmd)
	if model.screen != ScreenScope {
		t.Fatalf("expected ScreenScope, got %s", model.screen)
	}
	if !strings.Contains(model.View(), "Production") {
		t.Fatalf("expected child subscription in scope picker view: %q", model.View())
	}

	// Move down to "Production" subscription (index 1, after index 0 = current scope) and press Right to drill into Resource Groups.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = runScopeCommands(next.(Model), cmd)
	if !strings.Contains(model.View(), "rg-app") {
		t.Fatalf("expected child resource group after drilling into subscription: %q", model.View())
	}

	// Move down to "rg-app" (index 1) and press Enter to choose it as the activation scope.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	if model.screen != ScreenAssignments {
		t.Fatalf("expected return to ScreenAssignments after choosing scope, got %s", model.screen)
	}
	selected := model.assignmentList.selected()
	if len(selected) != 1 {
		t.Fatalf("expected narrowed assignment to be selected, got %#v", selected)
	}
	if selected[0].AzureScope != rgScope || selected[0].Scope.ID != rgScope || selected[0].Scope.Type != pim.ScopeTypeResourceGroup {
		t.Fatalf("expected narrowed scope %s, got %#v", rgScope, selected[0])
	}
	if selected[0].ActivationPolicy.MaximumDurationISO != "PT2H" || !selected[0].ActivationPolicy.JustificationRequired || selected[0].ActivationPolicy.AuthenticationContext != "c1" {
		t.Fatalf("expected child scope policy to be prepared and applied, got %#v", selected[0].ActivationPolicy)
	}
	if !strings.Contains(model.View(), "rg-app") {
		t.Fatalf("expected narrowed scope visible on assignment list: %q", model.View())
	}

	// Continue to activation form -> enter justification -> review -> activate.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenActivation {
		t.Fatalf("expected ScreenActivation, got %s", model.screen)
	}
	model = sendRunes(model, "Scoped deployment fix")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenConfirmation {
		t.Fatalf("expected ScreenConfirmation, got %s (err=%v)", model.screen, model.err)
	}
	if !strings.Contains(model.View(), "rg-app") {
		t.Fatalf("expected narrowed scope in confirmation view: %q", model.View())
	}

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)
	if model.screen != ScreenSummary {
		t.Fatalf("expected ScreenSummary, got %s (err=%v)", model.screen, model.err)
	}
	if len(provider.activated) != 1 {
		t.Fatalf("expected 1 activation request, got %d", len(provider.activated))
	}
	req := provider.activated[0]
	if req.Assignment.AzureScope != rgScope {
		t.Fatalf("expected activation AzureScope %s, got %s", rgScope, req.Assignment.AzureScope)
	}
	if req.Assignment.EligibilityScheduleID != "schedule-mg" {
		t.Fatalf("expected parent EligibilityScheduleID preserved, got %s", req.Assignment.EligibilityScheduleID)
	}
	if req.Assignment.PrincipalID != "user-principal" {
		t.Fatalf("expected group-derived principal rewritten to user-principal, got %s", req.Assignment.PrincipalID)
	}
	if req.DurationISO != "PT2H" {
		t.Fatalf("expected default duration from child scope policy PT2H, got %s", req.DurationISO)
	}
}
func TestScopePickerSupportsNavigationFilterResetAndLayoutBounds(t *testing.T) {
	mgScope := "/providers/Microsoft.Management/managementGroups/mg-root"
	subScope := "/subscriptions/sub-1"
	rgAlpha := "/subscriptions/sub-1/resourceGroups/rg-alpha"
	rgBeta := "/subscriptions/sub-1/resourceGroups/rg-beta"

	provider := &childScopeTestProvider{
		scriptedProvider: &scriptedProvider{
			discoveries: [][]pim.EligibleAssignment{{
				{
					ID:                    "owner-mg",
					Source:                pim.AssignmentSourceAzureResource,
					Kind:                  pim.AssignmentKindAzureRole,
					DisplayName:           "Owner",
					RoleDefinitionID:      mgScope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
					AzureScope:            mgScope,
					EligibilityScheduleID: "schedule-mg",
					Scope:                 pim.Scope{ID: mgScope, DisplayName: "Root MG", Type: pim.ScopeTypeManagementGroup},
				},
				{
					ID:                    "reader-rg",
					Source:                pim.AssignmentSourceAzureResource,
					Kind:                  pim.AssignmentKindAzureRole,
					DisplayName:           "Reader",
					RoleDefinitionID:      rgAlpha + "/providers/Microsoft.Authorization/roleDefinitions/reader",
					AzureScope:            rgAlpha,
					EligibilityScheduleID: "schedule-rg",
					Scope:                 pim.Scope{ID: rgAlpha, DisplayName: "rg-alpha", Type: pim.ScopeTypeResourceGroup},
				},
			}},
		},
		childrenByScope: map[string][]pim.Scope{
			mgScope:  {{ID: subScope, DisplayName: "Prod Sub", Type: pim.ScopeTypeSubscription}},
			subScope: {{ID: rgAlpha, DisplayName: "rg-alpha", Type: pim.ScopeTypeResourceGroup}, {ID: rgBeta, DisplayName: "rg-beta", Type: pim.ScopeTypeResourceGroup}},
		},
		policiesByScope: map[string]pim.ActivationPolicy{
			mgScope:  {MaximumDurationISO: "PT8H"},
			rgAlpha:  {MaximumDurationISO: "PT4H"},
			rgBeta:   {MaximumDurationISO: "PT1H", JustificationRequired: true},
			subScope: {MaximumDurationISO: "PT3H"},
		},
	}

	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1", DisplayName: "Contoso"}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	// Leaf Resource Group assignment (index 1) must ignore 's'.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = next.(Model)
	if cmd != nil || model.screen != ScreenAssignments {
		t.Fatalf("expected leaf Resource Group assignment not to open scope picker, screen=%s", model.screen)
	}

	// Move back to MG assignment (index 0), open scope picker, drill to sub, filter for "beta", select rg-beta.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = runScopeCommands(next.(Model), cmd)

	for _, size := range []struct{ width, height int }{{96, 30}, {80, 24}, {48, 24}} {
		resized, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		assertActivityFrameFits(t, resized.(Model).View(), size.width, size.height)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	model = runScopeCommands(next.(Model), cmd)

	// Filter child scopes with '/'
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = next.(Model)
	model = sendRunes(model, "beta")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	options := model.currentScopeOptions()
	if len(options) != 2 || options[1].ID != rgBeta {
		t.Fatalf("expected parent + rg-beta in filtered options, got %#v", options)
	}

	// Press Left/'h' to go back up to MG level, then drill again and pick rg-beta.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = next.(Model)
	if len(model.scopePicker.stack) != 1 {
		t.Fatalf("expected stack popped to 1 level, got %d", len(model.scopePicker.stack))
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = runScopeCommands(next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	if model.assignmentList.items[0].AzureScope != rgBeta {
		t.Fatalf("expected scope narrowed to %s, got %s", rgBeta, model.assignmentList.items[0].AzureScope)
	}

	// Inspect details ('i') shows both Target scope ID and original Eligible scope.
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	model = next.(Model)
	detailsView := model.View()
	if !strings.Contains(detailsView, "Root MG") || !strings.Contains(detailsView, "rg-beta") {
		t.Fatalf("expected details view to show both narrowed target scope and original eligible scope: %q", detailsView)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)

	// Re-open scope picker on narrowed assignment -> starts from original Root MG -> press Enter on [Eligible] index 0 to reset.
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = runScopeCommands(next.(Model), cmd)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	if model.assignmentList.items[0].AzureScope != mgScope || model.assignmentList.selectedIDs["owner-mg"] {
		t.Fatalf("expected scope reset to original MG and deselected, got %#v selected=%#v", model.assignmentList.items[0], model.assignmentList.selectedIDs)
	}
}

func TestScopePickerIgnoresStaleResponsesAfterTenantSwitch(t *testing.T) {
	mgScope := "/providers/Microsoft.Management/managementGroups/mg-root"
	provider := &childScopeTestProvider{
		scriptedProvider: &scriptedProvider{
			discoveries: [][]pim.EligibleAssignment{{{
				ID:          "owner-mg",
				Source:      pim.AssignmentSourceAzureResource,
				Kind:        pim.AssignmentKindAzureRole,
				DisplayName: "Owner",
				AzureScope:  mgScope,
				Scope:       pim.Scope{ID: mgScope, DisplayName: "Root MG", Type: pim.ScopeTypeManagementGroup},
			}}},
		},
		childrenByScope: map[string][]pim.Scope{
			mgScope: {{ID: "/subscriptions/sub-1", DisplayName: "Prod", Type: pim.ScopeTypeSubscription}},
		},
		policiesByScope: map[string]pim.ActivationPolicy{
			mgScope: {MaximumDurationISO: "PT8H"},
		},
	}
	model := NewModel(Runtime{
		AzureResources: provider,
		Tenants: tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
			return []azureauth.Tenant{{ID: "tenant-1", DisplayName: "One"}, {ID: "tenant-2", DisplayName: "Two"}}, nil
		}),
	})
	model = runScopeCommands(model, model.Init())
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	// Start scope loading, capture pending cmd before executing it, then switch tenants.
	next, staleCmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = next.(Model)
	model.selectTenant(1)

	model = runScopeCommands(model, staleCmd)
	if model.screen != ScreenHome || len(model.scopePicker.stack) != 0 {
		t.Fatalf("expected stale child-scope response ignored after tenant switch, screen=%s stack=%d", model.screen, len(model.scopePicker.stack))
	}
}

func TestAzureResourcesLoadsPoliciesLazilyAndSearchesDeepDescendantsImmediately(t *testing.T) {
	mgRoot := "/providers/Microsoft.Management/managementGroups/mg-root"
	mgPlatform := "/providers/Microsoft.Management/managementGroups/mg-platform"
	subDeep := "/subscriptions/sub-deep-prod"
	rgDeep := "/subscriptions/sub-deep-prod/resourceGroups/rg-payments"
	otherSub := "/subscriptions/sub-unselected"

	provider := &childScopeTestProvider{
		scriptedProvider: &scriptedProvider{
			discoveries: [][]pim.EligibleAssignment{{{
				ID:                    "owner-root",
				Source:                pim.AssignmentSourceAzureResource,
				Kind:                  pim.AssignmentKindAzureRole,
				DisplayName:           "Owner",
				RoleDefinitionID:      mgRoot + "/providers/Microsoft.Authorization/roleDefinitions/owner",
				AzureScope:            mgRoot,
				EligibilityScheduleID: "schedule-root",
				Scope:                 pim.Scope{ID: mgRoot, DisplayName: "Tenant Root Group", Type: pim.ScopeTypeManagementGroup},
			}, {
				ID:                    "reader-other",
				Source:                pim.AssignmentSourceAzureResource,
				Kind:                  pim.AssignmentKindAzureRole,
				DisplayName:           "Reader",
				RoleDefinitionID:      otherSub + "/providers/Microsoft.Authorization/roleDefinitions/reader",
				AzureScope:            otherSub,
				EligibilityScheduleID: "schedule-other",
				Scope:                 pim.Scope{ID: otherSub, DisplayName: "Unselected Sub", Type: pim.ScopeTypeSubscription},
			}}},
		},
		childrenByScope: map[string][]pim.Scope{
			mgRoot: {{ID: mgPlatform, DisplayName: "Platform MG", Type: pim.ScopeTypeManagementGroup}},
		},
		descendantsByScope: map[string][]pim.Scope{
			mgRoot: {
				{ID: mgPlatform, DisplayName: "Platform MG", Type: pim.ScopeTypeManagementGroup},
				{ID: subDeep, DisplayName: "Payments Production", Type: pim.ScopeTypeSubscription},
				{ID: rgDeep, DisplayName: "rg-payments", Type: pim.ScopeTypeResourceGroup},
			},
		},
		policiesByScope: map[string]pim.ActivationPolicy{
			subDeep: {MaximumDurationISO: "PT3H", JustificationRequired: true},
		},
	}

	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	// Opening Azure Resources must NOT prefetch policies for all scopes up front.
	if len(provider.prepareScopes) != 0 {
		t.Fatalf("expected zero upfront Prepare calls on Azure Resources open, got %v", provider.prepareScopes)
	}

	// Press 's' immediately from top menu and search directly for deep subscription several MG levels down.
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = runScopeCommands(next.(Model), cmd)
	if model.screen != ScreenScope {
		t.Fatalf("expected ScreenScope immediately without waiting for policies, got %s", model.screen)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = next.(Model)
	model = sendRunes(model, "Payments Prod")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	// Auto-focuses first matching descendant (index 1) so Enter selects it directly without navigating intermediate MGs.
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)

	if model.screen != ScreenAssignments {
		t.Fatalf("expected return to ScreenAssignments, got %s (err=%v)", model.screen, model.err)
	}
	if model.assignmentList.items[0].AzureScope != subDeep {
		t.Fatalf("expected deep subscription %s selected directly from top menu, got %s", subDeep, model.assignmentList.items[0].AzureScope)
	}
	if len(provider.prepareScopes) != 1 || provider.prepareScopes[0] != subDeep {
		t.Fatalf("expected Prepare called only for chosen target scope %s, got %v", subDeep, provider.prepareScopes)
	}
}

func TestScopePickerShowsDisambiguatedSubscriptionOnResourceGroups(t *testing.T) {
	mgRoot := "/providers/Microsoft.Management/managementGroups/mg-root"
	subDev := "/subscriptions/sub-dev"
	subProd := "/subscriptions/sub-prod"
	rgDev := subDev + "/resourceGroups/rg-shared"
	rgProd := subProd + "/resourceGroups/rg-shared"

	provider := &childScopeTestProvider{
		scriptedProvider: &scriptedProvider{
			discoveries: [][]pim.EligibleAssignment{{{
				ID:          "owner-root",
				Source:      pim.AssignmentSourceAzureResource,
				Kind:        pim.AssignmentKindAzureRole,
				DisplayName: "Owner",
				AzureScope:  mgRoot,
				Scope:       pim.Scope{ID: mgRoot, DisplayName: "Tenant Root Group", Type: pim.ScopeTypeManagementGroup},
			}}},
		},
		descendantsByScope: map[string][]pim.Scope{
			mgRoot: {
				{ID: rgDev, DisplayName: "rg-shared", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "gnsn-pbuildinglab-development"},
				{ID: rgProd, DisplayName: "rg-shared", Type: pim.ScopeTypeResourceGroup, SubscriptionName: "gnsn-pbuildinglab-production"},
			},
		},
	}

	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)
	next, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model = next.(Model)

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = runScopeCommands(next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = sendRunes(next.(Model), "rg-shared")

	view := model.View()
	if !strings.Contains(view, "RG: rg-shared (gnsn-pbuildinglab-development)") || !strings.Contains(view, "RG: rg-shared (gnsn-pbuildinglab-production)") {
		t.Fatalf("expected resource groups to show subscription names in scope picker, got:\n%s", view)
	}
}
