package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

type fencingDiscoveryProvider struct {
	*scriptedProvider
	discoveryContext context.Context
}

func (p *fencingDiscoveryProvider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	p.discoveryContext = ctx
	return p.scriptedProvider.Discover(ctx)
}

type fencingPreparationProvider struct {
	*fencingDiscoveryProvider
	preparationContext context.Context
	prepared           []pim.EligibleAssignment
	prepareErr         error
}

func (p *fencingPreparationProvider) Prepare(ctx context.Context, _ []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	p.preparationContext = ctx
	return p.prepared, p.prepareErr
}

func TestCachedDiscoveryFencesLateSectionResults(t *testing.T) {
	for _, phase := range []string{"discovery", "policies"} {
		for _, outcome := range []struct {
			name string
			err  error
		}{
			{name: "success"},
			{name: "failure", err: errors.New("section request failed")},
			{name: "account unavailable", err: azureauth.ErrAccountUnavailable},
		} {
			for _, workflow := range []string{"assignments", "activation"} {
				t.Run(phase+"/"+outcome.name+"/"+workflow, func(t *testing.T) {
					azure := &scriptedProvider{
						discoveries: [][]pim.EligibleAssignment{{
							{ID: "azure-reader", DisplayName: "Azure Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H"}},
							{ID: "azure-owner", DisplayName: "Azure Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}},
						}},
						results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}, {Status: pim.ActivationStatusPendingApproval}},
					}
					entra := &fencingDiscoveryProvider{scriptedProvider: &scriptedProvider{
						discoveries: [][]pim.EligibleAssignment{{{ID: "entra-reader", DisplayName: "Entra Reader"}}},
					}}
					var provider AssignmentProvider = entra
					prepared := &fencingPreparationProvider{fencingDiscoveryProvider: entra, prepareErr: outcome.err,
						prepared: []pim.EligibleAssignment{{ID: "entra-reader", DisplayName: "Entra Reader"}}}
					if phase == "policies" {
						provider = prepared
					} else {
						entra.discoverErr = outcome.err
					}
					model := NewModel(Runtime{AzureResources: azure, Entra: provider, GraphConnected: func(context.Context) bool { return true }})
					model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
					next, cmd := model.beginDiscovery(SectionAzureResources, false)
					model, _ = startDiscoveryBatch(t, next.(Model), cmd)
					next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
					next, cmd = next.(Model).beginDiscovery(SectionEntra, false)
					model = next.(Model)
					late := preparationCommand(t, cmd)()
					pendingContext := entra.discoveryContext
					if phase == "policies" {
						next, cmd = model.Update(late)
						model = next.(Model)
						late = preparationCommand(t, cmd)()
						pendingContext = prepared.preparationContext
					}
					next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
					next, cmd = next.(Model).beginDiscovery(SectionAzureResources, false)
					model = next.(Model)
					if cmd != nil || azure.discoverCalls != 1 || !model.policiesReady {
						t.Fatalf("cached Azure assignments were not immediately usable: cmd=%v calls=%d ready=%v", cmd != nil, azure.discoverCalls, model.policiesReady)
					}
					if !errors.Is(pendingContext.Err(), context.Canceled) {
						t.Error("opening a cached section did not cancel the previous section's request")
					}
					if phase == "policies" {
						if _, ok := model.discoveryCache[discoveryKey{tenantID: "tenant-1", section: SectionEntra}]; ok {
							t.Error("canceled policy preparation retained an incomplete cache entry")
						}
					}
					model = sendRunes(model, "j ")
					model.query = "Azure"
					var accepted tea.Msg
					var activationContext context.Context
					if workflow == "activation" {
						model = sendRunes(model, "k ")
						model.prepareActivationForm()
						next, cmd = model.startActivation(model.assignmentList.selected(), azureauth.ARMAuthentication{AccessToken: "checked-token"})
						model, cmd = advanceActivityCommand(t, next.(Model), cmd)
						activationContext = model.activationContext
						accepted = cmd()
					}

					next, follow := model.Update(late)
					model = next.(Model)
					if follow != nil || model.err != nil || model.accountUnavailable() || !model.policiesReady || len(model.assignmentList.items) != 2 || model.assignmentList.items[0].ID != "azure-reader" || !model.assignmentList.selectedIDs["azure-owner"] || model.query != "Azure" {
						t.Fatalf("late %s changed the cached foreground workflow: screen=%s list=%#v selected=%#v query=%q ready=%v err=%v account=%v cmd=%v", phase, model.screen, model.assignmentList.items, model.assignmentList.selectedIDs, model.query, model.policiesReady, model.err, model.accountErr, follow != nil)
					}
					if workflow == "assignments" {
						if model.screen != ScreenAssignments || model.loading || model.listCursor != 1 {
							t.Fatalf("late result changed assignment navigation: screen=%s loading=%v cursor=%d", model.screen, model.loading, model.listCursor)
						}
						return
					}
					if model.screen != ScreenProgress || !model.loading || activationContext.Err() != nil {
						t.Fatalf("late result interrupted an accepted activation: screen=%s loading=%v context=%v", model.screen, model.loading, activationContext.Err())
					}
					next, cmd = model.Update(accepted)
					model = next.(Model)
					if model.activationDone != 1 || len(model.summary.activated) != 1 || cmd == nil {
						t.Fatalf("accepted result was discarded or next request was lost: done=%d summary=%#v next=%v", model.activationDone, model.summary, cmd != nil)
					}
					for range 3 {
						model, cmd = advanceActivityCommand(t, model, cmd)
					}
					if model.screen != ScreenSummary || len(azure.activated) != 2 || azure.activated[1].Assignment.ID != "azure-owner" || len(model.summary.activated) != 1 || len(model.summary.pendingApproval) != 1 {
						t.Fatalf("batch did not retain both outcomes: screen=%s requests=%#v summary=%#v", model.screen, azure.activated, model.summary)
					}
				})
			}
		}
	}
}

func TestCachedDiscoveryKeepsSameSectionPreparation(t *testing.T) {
	provider := &fencingPreparationProvider{
		fencingDiscoveryProvider: &fencingDiscoveryProvider{scriptedProvider: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Azure Reader"}, {ID: "two", DisplayName: "Azure Owner"},
		}}}},
		prepared: []pim.EligibleAssignment{
			{ID: "one", DisplayName: "Azure Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H"}},
			{ID: "two", DisplayName: "Azure Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}},
		},
	}
	model := NewModel(Runtime{Entra: provider, GraphConnected: func(context.Context) bool { return true }})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	next, cmd := model.beginDiscovery(SectionEntra, false)
	model, prepare := startDiscoveryBatch(t, next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd = next.(Model).beginDiscovery(SectionEntra, false)
	model = next.(Model)
	if cmd != nil || !model.preparingPolicies || provider.discoverCalls != 1 {
		t.Fatalf("re-entry restarted or lost progressive preparation: cmd=%v preparing=%v discoveries=%d", cmd != nil, model.preparingPolicies, provider.discoverCalls)
	}
	model = sendRunes(model, "j ")
	model.query = "Azure"
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if !model.waitingForPolicies {
		t.Fatal("selected cached assignment did not wait for policy preparation")
	}
	ready := preparationCommand(t, prepare)()
	if provider.preparationContext.Err() != nil {
		t.Fatalf("re-entering the same cached section canceled its policy preparation: %v", provider.preparationContext.Err())
	}
	next, _ = model.Update(ready)
	model = next.(Model)
	if model.screen != ScreenActivation || !model.assignmentList.selectedIDs["two"] || model.query != "Azure" || model.listCursor != 1 || model.form.durations["two"] != "PT2H" {
		t.Fatalf("progressive cached result lost selection or prepared policy: screen=%s selected=%#v query=%q cursor=%d durations=%#v", model.screen, model.assignmentList.selectedIDs, model.query, model.listCursor, model.form.durations)
	}
}

func TestInactiveSectionMessagesCannotCancelForegroundPreparation(t *testing.T) {
	for _, phase := range []string{"discovery", "policies"} {
		t.Run(phase, func(t *testing.T) {
			provider := &fencingPreparationProvider{
				fencingDiscoveryProvider: &fencingDiscoveryProvider{scriptedProvider: &scriptedProvider{
					discoveries: [][]pim.EligibleAssignment{{{ID: "azure", DisplayName: "Azure Reader"}}},
				}},
				prepared: []pim.EligibleAssignment{{ID: "azure", DisplayName: "Azure Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}}},
			}
			model := NewModel(Runtime{Entra: provider, GraphConnected: func(context.Context) bool { return true }})
			model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
			next, cmd := model.beginDiscovery(SectionEntra, false)
			model, prepare := startDiscoveryBatch(t, next.(Model), cmd)
			ready := preparationCommand(t, prepare)()
			model = sendRunes(model, " ")
			next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(Model)
			key := discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}
			var late tea.Msg = assignmentsDiscoveredMsg{tenantID: "tenant-1", section: SectionAzureResources, checkID: model.discoveryCheck, err: azureauth.ErrAccountUnavailable}
			if phase == "policies" {
				model.discoveryCache[key] = discoveryEntry{assignments: []pim.EligibleAssignment{{ID: "azure"}}, generation: model.discoveryCheck}
				late = assignmentsPreparedMsg{key: key, generation: model.discoveryCheck, err: azureauth.ErrAccountUnavailable}
			}

			next, follow := model.Update(late)
			model = next.(Model)
			if follow != nil || provider.preparationContext.Err() != nil || !model.waitingForPolicies || !model.preparingPolicies || model.err != nil || model.accountUnavailable() {
				t.Fatalf("inactive %s interrupted foreground preparation: context=%v waiting=%v preparing=%v err=%v account=%v cmd=%v", phase, provider.preparationContext.Err(), model.waitingForPolicies, model.preparingPolicies, model.err, model.accountErr, follow != nil)
			}
			next, _ = model.Update(ready)
			model = next.(Model)
			if model.screen != ScreenActivation || model.form.durations["azure"] != "PT2H" {
				t.Fatalf("current policy completion was not accepted: screen=%s durations=%#v", model.screen, model.form.durations)
			}
		})
	}
}

func TestAzureInspectionCannotUnblockNewerPolicyRequest(t *testing.T) {
	for _, outcome := range []struct {
		name string
		err  error
	}{
		{"success", nil}, {"canceled", context.Canceled}, {"account unavailable", azureauth.ErrAccountUnavailable},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			provider := &fencingPreparationProvider{
				fencingDiscoveryProvider: &fencingDiscoveryProvider{scriptedProvider: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{
					{ID: "a"}, {ID: "b", AzureScope: "/subscriptions/b", EligibilityScheduleID: "current"},
				}}}},
				prepared:   []pim.EligibleAssignment{{ID: "a", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"}}},
				prepareErr: outcome.err,
			}
			model := NewModel(Runtime{AzureResources: provider})
			next, cmd := model.beginDiscovery(SectionAzureResources, false)
			model, _ = startDiscoveryBatch(t, next.(Model), cmd)
			next, inspect := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
			late := preparationCommand(t, inspect)()
			next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
			model = sendRunes(next.(Model), "j ")
			next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			provider.prepareErr = nil
			provider.prepared = []pim.EligibleAssignment{{
				ID: "b", AzureScope: "/SUBSCRIPTIONS/B/", EligibilityScheduleID: "stale",
				ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H", JustificationRequired: true},
			}}
			ready := preparationCommand(t, cmd)()
			next, follow := next.(Model).Update(late)
			model = next.(Model)
			if model.screen != ScreenAssignments || !model.waitingForPolicies || model.err != nil || model.accountUnavailable() || follow != nil || provider.preparationContext.Err() != nil {
				t.Fatalf("superseded inspection interrupted current request: screen=%s waiting=%v err=%v context=%v", model.screen, model.waitingForPolicies, model.err, provider.preparationContext.Err())
			}
			next, _ = model.Update(ready)
			model = next.(Model)
			selected := model.assignmentList.selected()
			if model.screen != ScreenActivation || model.form.durations["b"] != "PT1H" || len(selected) != 1 || !selected[0].ActivationPolicy.JustificationRequired || selected[0].EligibilityScheduleID != "current" || selected[0].AzureScope != "/subscriptions/b" {
				t.Fatalf("current policy did not prepare the selected target: screen=%s durations=%v selected=%+v", model.screen, model.form.durations, selected)
			}
		})
	}
}

func TestAzureSelectionRequiresEveryPolicy(t *testing.T) {
	provider := &fencingPreparationProvider{
		fencingDiscoveryProvider: &fencingDiscoveryProvider{scriptedProvider: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{
			{ID: "a"}, {ID: "b", DisplayName: "Reader", AzureScope: "/subscriptions/b"},
		}}}},
		prepared: []pim.EligibleAssignment{
			{ID: "a", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"}},
			{ID: "b", AzureScope: "/subscriptions/elsewhere", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H"}},
		},
	}
	model := NewModel(Runtime{AzureResources: provider})
	next, cmd := model.beginDiscovery(SectionAzureResources, false)
	model, _ = startDiscoveryBatch(t, next.(Model), cmd)
	model = sendRunes(model, "a")
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, follow := next.(Model).Update(preparationCommand(t, cmd)())
	model = next.(Model)
	if model.screen != ScreenAssignments || model.waitingForPolicies || model.err == nil || follow != nil {
		t.Fatalf("incomplete policy response did not block activation: screen=%s waiting=%v err=%v", model.screen, model.waitingForPolicies, model.err)
	}
	if got := model.assignmentList.items[1]; got.AzureScope != "/subscriptions/b" || got.ActivationPolicy.MaximumDurationISO != "" {
		t.Fatalf("wrong target's policy accepted: %+v", got)
	}
}

const (
	policyParentScope = "/subscriptions/production"
	policyChildScope  = policyParentScope + "/resourceGroups/app"
)

func azureScopePolicyModel(t *testing.T) Model {
	t.Helper()
	parent := pim.Scope{ID: policyParentScope, DisplayName: "Production", Type: pim.ScopeTypeSubscription}
	child := pim.Scope{ID: policyChildScope, DisplayName: "App", Type: pim.ScopeTypeResourceGroup}
	provider := &childScopeTestProvider{
		scriptedProvider: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{
			ID: "reader", DisplayName: "Reader", Scope: parent, AzureScope: parent.ID, EligibilityScheduleID: "eligible",
		}}}},
		childrenByScope: map[string][]pim.Scope{parent.ID: {child}},
		policiesByScope: map[string]pim.ActivationPolicy{
			parent.ID: {MaximumDurationISO: "PT8H"}, child.ID: {MaximumDurationISO: "PT1H"},
		},
	}
	model := NewModel(Runtime{AzureResources: provider, Entra: &scriptedProvider{}, GraphConnected: func(context.Context) bool { return true }})
	next, cmd := model.beginDiscovery(SectionAzureResources, false)
	model, _ = startDiscoveryBatch(t, next.(Model), cmd)
	return model
}

func narrowAzurePolicyTarget(t *testing.T, model Model) Model {
	t.Helper()
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = sendRunes(runScopeCommands(next.(Model), cmd), "j")
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runScopeCommands(next.(Model), cmd)
	if targets := model.assignmentList.selected(); len(targets) != 1 || targets[0].AzureScope != policyChildScope {
		t.Fatalf("child target was not selected: %+v", targets)
	}
	return model
}

func TestLateAzureInspectionKeepsNarrowedTarget(t *testing.T) {
	model := azureScopePolicyModel(t)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	late := preparationCommand(t, cmd)().(assignmentsPreparedMsg)
	late.assignments[0].EligibilityScheduleID = "stale"
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = narrowAzurePolicyTarget(t, next.(Model))
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, _ = next.(Model).Update(late)
	model = next.(Model)
	selected := model.assignmentList.selected()
	if model.screen != ScreenConfirmation || len(selected) != 1 || selected[0].AzureScope != policyChildScope || selected[0].ActivationPolicy.MaximumDurationISO != "PT1H" {
		t.Fatalf("late inspection changed the reviewed target: screen=%s selected=%+v", model.screen, selected)
	}
	key := discoveryKey{section: SectionAzureResources}
	if cached := model.discoveryCache[key].assignments[0]; cached.AzureScope != policyChildScope || cached.ActivationPolicy.MaximumDurationISO != "PT1H" {
		t.Fatalf("late inspection replaced the cached child: %+v", cached)
	}
	if base := model.scopePicker.basePolicies["reader"]; base.AzureScope != policyParentScope || base.EligibilityScheduleID != "eligible" || base.ActivationPolicy.MaximumDurationISO != "PT8H" {
		t.Fatalf("inspection did not merge only the eligible scope's policy: %+v", base)
	}
}

func TestCachedAzureEntryRetainsEligibleScopeAfterSectionSwitch(t *testing.T) {
	model := azureScopePolicyModel(t)
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = narrowAzurePolicyTarget(t, next.(Model))
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd := next.(Model).beginDiscovery(SectionEntra, false)
	model, _ = startDiscoveryBatch(t, next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd = next.(Model).beginDiscovery(SectionAzureResources, false)
	model = next.(Model)
	if cmd != nil || model.assignmentList.items[0].AzureScope != policyChildScope {
		t.Fatalf("cached target was not retained: cmd=%v rows=%+v", cmd != nil, model.assignmentList.items)
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = runScopeCommands(next.(Model), cmd)
	if model.screen != ScreenScope || model.scopePicker.base.AzureScope != policyParentScope {
		t.Fatalf("cached entry lost its eligible root: screen=%s root=%s", model.screen, model.scopePicker.base.AzureScope)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenAssignments || model.assignmentList.items[0].AzureScope != policyParentScope {
		t.Fatalf("could not reset to the original eligibility: screen=%s rows=%+v", model.screen, model.assignmentList.items)
	}
}

func TestCanceledAzurePolicyRequestCannotInvalidateAccount(t *testing.T) {
	model := azureScopePolicyModel(t)
	model = sendRunes(model, " ")
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	late := preparationCommand(t, cmd)().(assignmentsPreparedMsg)
	late.err = azureauth.ErrAccountUnavailable
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, _ = next.(Model).Update(late)
	model = next.(Model)
	if model.screen != ScreenAssignments || model.waitingForPolicies || model.err != nil || model.accountUnavailable() || len(model.assignmentList.selected()) != 1 {
		t.Fatalf("canceled lookup changed the workflow: screen=%s err=%v account=%v", model.screen, model.err, model.accountErr)
	}
}
