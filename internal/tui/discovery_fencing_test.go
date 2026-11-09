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
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	next, cmd := model.beginDiscovery(SectionAzureResources, false)
	model, prepare := startDiscoveryBatch(t, next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd = next.(Model).beginDiscovery(SectionAzureResources, false)
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
			model := NewModel(Runtime{AzureResources: provider})
			model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
			next, cmd := model.beginDiscovery(SectionAzureResources, false)
			model, prepare := startDiscoveryBatch(t, next.(Model), cmd)
			ready := preparationCommand(t, prepare)()
			model = sendRunes(model, " ")
			next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(Model)
			key := discoveryKey{tenantID: "tenant-1", section: SectionEntra}
			var late tea.Msg = assignmentsDiscoveredMsg{tenantID: "tenant-1", section: SectionEntra, checkID: model.discoveryCheck, err: azureauth.ErrAccountUnavailable}
			if phase == "policies" {
				model.discoveryCache[key] = discoveryEntry{assignments: []pim.EligibleAssignment{{ID: "entra"}}, generation: model.discoveryCheck}
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
