package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

func TestTenantRefreshErrorPreservesActivationWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name           string
		commandsBefore int
		acceptedResult bool
		wantScreen     Screen
	}{
		{name: "preflight", commandsBefore: 0, wantScreen: ScreenConfirmation},
		{name: "before first request", commandsBefore: 1, wantScreen: ScreenProgress},
		{name: "accepted request awaiting result", commandsBefore: 2, acceptedResult: true, wantScreen: ScreenProgress},
		{name: "between queued requests", commandsBefore: 3, wantScreen: ScreenProgress},
		{name: "completed summary", commandsBefore: 6, wantScreen: ScreenSummary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refreshErr := errors.New("tenant refresh endpoint unavailable")
			assignments := []pim.EligibleAssignment{
				{ID: "reader", DisplayName: "Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H"}},
				{ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H"}},
			}
			provider := &scriptedProvider{
				discoveries: [][]pim.EligibleAssignment{assignments, assignments},
				results: []pim.ActivationResult{
					{Status: pim.ActivationStatusActivated},
					{Status: pim.ActivationStatusPendingProvisioning},
				},
			}
			tenantCalls := 0
			model, _ := accountTestModel(Runtime{
				AzureResources: provider,
				Tenants: tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
					tenantCalls++
					if tenantCalls == 1 {
						return nil, refreshErr
					}
					return []azureauth.Tenant{{ID: "tenant-1", DisplayName: "Recovered tenant"}}, nil
				}),
				ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
					return azureauth.ARMAuthentication{PrincipalID: "user-1", AccessToken: "checked-token", Satisfied: true}, nil
				},
			})
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = runAccountCommand(next.(Model), cmd)
			next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			model = next.(Model)
			var lateRefresh tenantsCheckedMsg
			for _, msg := range accountCommandMessages(cmd) {
				if refresh, ok := msg.(tenantsCheckedMsg); ok {
					lateRefresh = refresh
					continue
				}
				next, follow := model.Update(msg)
				model = runAccountCommand(next.(Model), follow)
			}
			if lateRefresh.err != refreshErr || model.screen != ScreenAssignments || model.loading {
				t.Fatalf("refresh did not leave discovery ready with a pending tenant error: screen=%s loading=%v error=%v", model.screen, model.loading, lateRefresh.err)
			}

			model = sendRunes(model, "a")
			for range 4 {
				next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
				model = next.(Model)
			}
			if model.screen != ScreenConfirmation {
				t.Fatalf("could not review refreshed assignments: screen=%s error=%v", model.screen, model.err)
			}
			next, pending := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(Model)
			for range tc.commandsBefore {
				model, pending = advanceActivityCommand(t, model, pending)
			}
			if tc.acceptedResult {
				accepted := activityCommandMessage(t, pending)
				if result, ok := accepted.(activationItemResultMsg); !ok || !result.result.Success() {
					t.Fatalf("request was not accepted before tenant refresh failed: %#v", accepted)
				}
				pending = func() tea.Msg { return accepted }
			}

			next, _ = model.Update(lateRefresh)
			model = next.(Model)
			if model.screen != tc.wantScreen {
				t.Errorf("background tenant error replaced workflow: got %s, want %s", model.screen, tc.wantScreen)
			}
			if view := model.View(); !strings.Contains(view, refreshErr.Error()) || !strings.Contains(view, "r to retry") {
				t.Errorf("tenant diagnostic or retry guidance is not visible: %s", view)
			}
			model = runAccountCommand(model, pending)
			if model.screen != ScreenSummary || len(model.summary.results) != 2 || len(model.summary.activated) != 1 || len(model.summary.pendingProvisioning) != 1 {
				t.Fatalf("tenant error lost accepted results or queued work: screen=%s summary=%#v requests=%#v", model.screen, model.summary, provider.activated)
			}
			if model.summary.results[0].Assignment.ID != "reader" || model.summary.results[1].Assignment.ID != "owner" || len(provider.activated) != 2 {
				t.Fatalf("batch results no longer match the two submitted assignments: results=%#v requests=%#v", model.summary.results, provider.activated)
			}
			if provider.tokens[0] != "checked-token" || provider.tokens[1] != "checked-token" || provider.activationTenants[0] != "tenant-1" || provider.activationTenants[1] != "tenant-1" {
				t.Fatalf("tenant error changed checked activation identity: tokens=%v tenants=%v", provider.tokens, provider.activationTenants)
			}
			if !strings.Contains(model.View(), refreshErr.Error()) {
				t.Fatal("summary hid the unresolved tenant diagnostic")
			}

			model = sendRunes(model, "h")
			next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			model = runAccountCommand(next.(Model), cmd)
			if model.screen != ScreenHome || model.tenantErr != nil || tenantCalls != 2 || !strings.Contains(model.View(), "Recovered tenant") || strings.Contains(model.View(), refreshErr.Error()) {
				t.Fatalf("tenant diagnostic did not recover through the advertised retry: screen=%s error=%v calls=%d view=%s", model.screen, model.tenantErr, tenantCalls, model.View())
			}
		})
	}
}

func TestTenantRefreshStartupErrorRetainsRecovery(t *testing.T) {
	for _, cause := range []error{azureauth.ErrNotLoggedIn, errors.New("tenant endpoint unavailable")} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			model := NewModel(Runtime{Tenants: tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
				calls++
				if calls == 1 {
					return nil, cause
				}
				return []azureauth.Tenant{{ID: "tenant-1"}}, nil
			})})
			model = runAccountCommand(model, model.Init())
			if model.screen != ScreenTenants || !strings.Contains(model.View(), cause.Error()) || !strings.Contains(model.View(), "r to retry") {
				t.Fatalf("startup error did not show tenant recovery: screen=%s view=%s", model.screen, model.View())
			}
			next, retry := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			model = runAccountCommand(next.(Model), retry)
			if model.screen != ScreenHome || model.selectedTenant.ID != "tenant-1" || model.tenantErr != nil || calls != 2 {
				t.Fatalf("startup tenant retry failed: screen=%s tenant=%s error=%v calls=%d", model.screen, model.selectedTenant.ID, model.tenantErr, calls)
			}
		})
	}
}
