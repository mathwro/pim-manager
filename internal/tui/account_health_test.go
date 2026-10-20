package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

func accountTestModel(runtime Runtime) (Model, tea.Cmd) {
	if runtime.Tenants == nil {
		runtime.Tenants = tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
			return []azureauth.Tenant{{ID: "tenant-1", DisplayName: "Contoso"}}, nil
		})
	}
	model := NewModel(runtime)
	next, cmd := model.Update(tenantsCheckedMsg{
		tenants: []azureauth.Tenant{{ID: "tenant-1", DisplayName: "Contoso"}},
		checkID: model.tenantCheck,
	})
	return next.(Model), cmd
}

func accountCommandMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch typed := msg.(type) {
	case tea.BatchMsg:
		var messages []tea.Msg
		for _, child := range typed {
			messages = append(messages, accountCommandMessages(child)...)
		}
		return messages
	case spinner.TickMsg:
		return nil
	default:
		return []tea.Msg{msg}
	}
}

func runAccountCommand(model Model, cmd tea.Cmd) Model {
	for _, msg := range accountCommandMessages(cmd) {
		next, follow := model.Update(msg)
		model = runAccountCommand(next.(Model), follow)
	}
	return model
}

func foundAccount(context.Context) (azureauth.AccountHealth, error) {
	return azureauth.AccountHealth{
		TenantID: "tenant-1", PrincipalID: "user-1",
		DisplayName: "Ada Lovelace", UserPrincipalName: "ada@contoso.com",
	}, nil
}

func TestAccountHealthSelectionStartsBackgroundCheckWithoutBlockingDiscovery(t *testing.T) {
	calls := 0
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "reader", DisplayName: "Reader"}}}}
	model, check := accountTestModel(Runtime{
		AzureResources: provider,
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			calls++
			if got := azureauth.TenantFromContext(ctx); got != "tenant-1" {
				t.Fatalf("account check used tenant %q", got)
			}
			return foundAccount(ctx)
		},
	})
	if model.screen != ScreenHome || calls != 0 || !strings.Contains(strings.ToLower(model.View()), "checking") {
		t.Fatalf("tenant selection blocked home or failed to show checking: screen=%s calls=%d view=%s", model.screen, calls, model.View())
	}
	next, discover := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), discover)
	if model.screen != ScreenAssignments || provider.discoverCalls != 1 || calls != 0 {
		t.Fatalf("pending identity blocked discovery: screen=%s discoveries=%d checks=%d", model.screen, provider.discoverCalls, calls)
	}
	model = runAccountCommand(model, check)
	if calls != 1 || !strings.Contains(model.View(), "Ada Lovelace") || !strings.Contains(model.View(), "ada@contoso.com") {
		t.Fatalf("expected coalesced identity and actual account label, checks=%d view=%s", calls, model.View())
	}
}

func TestAccountHealthSuccessIsAccountFoundNotConnectedOrActive(t *testing.T) {
	model, check := accountTestModel(Runtime{CheckAccount: foundAccount})
	model = runAccountCommand(model, check)
	view := model.View()
	for _, want := range []string{"Account found", "Ada Lovelace", "ada@contoso.com", "Contoso", "tenant-1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q from verified account panel: %s", want, view)
		}
	}
	if strings.Contains(view, "CONNECTED") || strings.Contains(view, "Account active") {
		t.Fatalf("account existence must not claim connectivity or privileged activation: %s", view)
	}
}

func TestAccountHealthUnknownFailureDoesNotBlockPinnedARMActivation(t *testing.T) {
	for _, healthErr := range []error{context.DeadlineExceeded, errors.New("Graph permission denied")} {
		t.Run(healthErr.Error(), func(t *testing.T) {
			provider := &scriptedProvider{}
			model, check := accountTestModel(Runtime{
				AzureResources: provider,
				CheckAccount: func(context.Context) (azureauth.AccountHealth, error) {
					return azureauth.AccountHealth{}, healthErr
				},
				ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
					return azureauth.ARMAuthentication{PrincipalID: "user-1", AccessToken: "checked-arm-token", Satisfied: true}, nil
				},
			})
			model = runAccountCommand(model, check)
			if !strings.Contains(strings.ToLower(model.View()), "unverified") || strings.Contains(model.View(), "az login") {
				t.Fatalf("unknown Graph error should stay advisory: %s", model.View())
			}
			model.activeSection = SectionAzureResources
			model.screen = ScreenConfirmation
			model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader", PrincipalID: "group-1"}})
			model.assignmentList.toggle("reader")
			model.form.durations["reader"] = "PT1H"
			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = runAccountCommand(next.(Model), cmd)
			if len(provider.activated) != 1 || provider.tokens[0] != "checked-arm-token" || provider.activated[0].Assignment.PrincipalID != "user-1" || model.screen != ScreenSummary {
				t.Fatalf("advisory check blocked or changed ARM activation: requests=%#v tokens=%v screen=%s", provider.activated, provider.tokens, model.screen)
			}
		})
	}
}

func TestAccountHealthTransientFailureDoesNotSuppressNextCheck(t *testing.T) {
	calls := 0
	model, check := accountTestModel(Runtime{
		AzureResources: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "reader", DisplayName: "Reader"}}}},
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			calls++
			if calls == 1 {
				return azureauth.AccountHealth{}, fmt.Errorf("Graph self lookup: %w", context.DeadlineExceeded)
			}
			return foundAccount(ctx)
		},
	})
	model = runAccountCommand(model, check)
	next, discover := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), discover)
	if calls != 2 || !strings.Contains(model.View(), "Account found") || !strings.Contains(model.View(), "Reader") {
		t.Fatalf("inconclusive check suppressed live recovery: calls=%d view=%s", calls, model.View())
	}
}

func TestAccountHealthInconclusiveResultShowsFailureReason(t *testing.T) {
	for _, cause := range []error{
		fmt.Errorf("Graph token acquisition: %w", context.DeadlineExceeded),
		errors.New("Graph self lookup: HTTP 403: Authorization_RequestDenied"),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			model, check := accountTestModel(Runtime{CheckAccount: func(context.Context) (azureauth.AccountHealth, error) {
				return azureauth.AccountHealth{}, cause
			}})
			model = runAccountCommand(model, check)
			if !strings.Contains(model.View(), cause.Error()) || strings.Contains(model.View(), "Account found") {
				t.Fatalf("inconclusive identity check hid its reason: %s", model.View())
			}
		})
	}
}

func TestAccountHealthCachedSectionReentryRechecksOnlyStaleHealth(t *testing.T) {
	calls := 0
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "reader"}}}}
	model, check := accountTestModel(Runtime{
		AzureResources: provider,
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			calls++
			return foundAccount(ctx)
		},
	})
	model = runAccountCommand(model, check)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), cmd)
	if calls != 1 || provider.discoverCalls != 1 {
		t.Fatalf("fresh cached reentry repeated work: checks=%d discoveries=%d", calls, provider.discoverCalls)
	}
	model.accountCheckedAt = time.Now().Add(-6 * time.Minute)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), cmd)
	if calls != 2 || provider.discoverCalls != 1 || len(model.assignmentList.items) != 1 {
		t.Fatalf("stale health was not rechecked independently of cached roles: checks=%d discoveries=%d roles=%v", calls, provider.discoverCalls, model.assignmentList.items)
	}
}

func TestAccountHealthEmptyDiscoveryRechecksStaleHealth(t *testing.T) {
	calls := 0
	model, check := accountTestModel(Runtime{
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			calls++
			return foundAccount(ctx)
		},
	})
	model = runAccountCommand(model, check)
	model.accountCheckedAt = time.Now().Add(-6 * time.Minute)
	model.activeSection = SectionAzureResources
	model.screen = ScreenAssignments
	next, cmd := model.Update(assignmentsDiscoveredMsg{
		tenantID: "tenant-1", section: SectionAzureResources, checkID: model.discoveryCheck,
	})
	model = runAccountCommand(next.(Model), cmd)
	if calls != 2 {
		t.Fatalf("empty discovery failed to refresh stale identity: checks=%d", calls)
	}
}

func TestAccountHealthTenantSwitchCancelsAndRejectsOldResult(t *testing.T) {
	var checkedContext context.Context
	model, check := accountTestModel(Runtime{
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			checkedContext = ctx
			return azureauth.AccountHealth{}, azureauth.ErrAccountUnavailable
		},
	})
	old := accountCommandMessages(check)
	if len(old) != 1 {
		t.Fatalf("expected background identity result, got %d", len(old))
	}
	model.tenants = append(model.tenants, azureauth.Tenant{ID: "tenant-2", DisplayName: "Fabrikam"})
	model.tenantIndex = 1
	model.screen = ScreenTenants
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if checkedContext.Err() != context.Canceled {
		t.Fatalf("tenant switch did not cancel old check: %v", checkedContext.Err())
	}
	next, _ = model.Update(old[0])
	model = next.(Model)
	if model.selectedTenant.ID != "tenant-2" || strings.Contains(model.View(), "az login --tenant tenant-1") || strings.Contains(strings.ToLower(model.View()), "account unavailable") {
		t.Fatalf("old tenant identity result leaked into new tenant: %s", model.View())
	}
}

func TestAccountHealthRefreshRejectsEarlierSameTenantResult(t *testing.T) {
	calls := 0
	model, check := accountTestModel(Runtime{
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			calls++
			if calls == 1 {
				return azureauth.AccountHealth{}, azureauth.ErrAccountUnavailable
			}
			return foundAccount(ctx)
		},
	})
	old := accountCommandMessages(check)
	if len(old) != 1 {
		t.Fatalf("expected first identity check, got %d messages", len(old))
	}
	next, refresh := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = runAccountCommand(next.(Model), refresh)
	next, _ = model.Update(old[0])
	model = next.(Model)
	if calls != 2 || !strings.Contains(model.View(), "Ada Lovelace") || strings.Contains(model.View(), "az login") {
		t.Fatalf("refresh accepted obsolete identity failure: checks=%d view=%s", calls, model.View())
	}
}

func TestAccountHealthUnavailableClearsWorkflowAndRefreshRecovers(t *testing.T) {
	healthy := false
	tenantCalls := 0
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "new-reader"}}}}
	model, check := accountTestModel(Runtime{
		Tenants: tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
			tenantCalls++
			return []azureauth.Tenant{{ID: "tenant-1", DisplayName: "Contoso renamed"}}, nil
		}),
		AzureResources: provider,
		CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
			if !healthy {
				return azureauth.AccountHealth{}, fmt.Errorf("deleted user: %w", azureauth.ErrAccountUnavailable)
			}
			return foundAccount(ctx)
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "old-owner"}})
	model.assignmentList.toggle("old-owner")
	model.form.justification = "obsolete"
	model.form.durations["old-owner"] = "PT1H"
	key := discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}
	model.discoveryCache[key] = discoveryEntry{assignments: model.assignmentList.items, policiesReady: true}
	model = runAccountCommand(model, check)
	if len(model.assignmentList.items) != 0 || len(model.form.durations) != 0 || model.form.justification != "" {
		t.Fatalf("unavailable account retained workflow: assignments=%v form=%#v", model.assignmentList.items, model.form)
	}
	if _, ok := model.discoveryCache[key]; ok {
		t.Fatal("unavailable account retained tenant discovery cache")
	}
	for _, want := range []string{"az login --tenant tenant-1", "r"} {
		if !strings.Contains(model.View(), want) {
			t.Fatalf("missing recovery guidance %q: %s", want, model.View())
		}
	}
	next, submit := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), submit)
	if len(provider.activated) != 0 {
		t.Fatalf("unavailable account submitted stale request: %v", provider.activated)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if !strings.Contains(model.View(), "az login --tenant tenant-1") {
		t.Fatalf("back navigation hid persistent sign-in recovery: %s", model.View())
	}
	healthy = true
	next, refresh := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = runAccountCommand(next.(Model), refresh)
	if tenantCalls != 1 || provider.discoverCalls != 1 || model.selectedTenant.DisplayName != "Contoso renamed" || len(model.assignmentList.items) != 1 || model.assignmentList.items[0].ID != "new-reader" || strings.Contains(model.View(), "az login") {
		t.Fatalf("sign-in recovery failed to reload identity, tenants and affected workflow: tenants=%d discoveries=%d tenant=%#v roles=%v view=%s", tenantCalls, provider.discoverCalls, model.selectedTenant, model.assignmentList.items, model.View())
	}
}

func TestAccountHealthSuccessfulIdentityMustMatchPreactivationPrincipal(t *testing.T) {
	provider := &scriptedProvider{}
	model, check := accountTestModel(Runtime{
		AzureResources: provider,
		CheckAccount:   foundAccount,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{PrincipalID: "different-user", AccessToken: "different-token", Satisfied: true}, nil
		},
	})
	model = runAccountCommand(model, check)
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "old-owner"}})
	model.assignmentList.toggle("old-owner")
	model.form.durations["old-owner"] = "PT1H"
	model.discoveryCache[discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}] = discoveryEntry{assignments: model.assignmentList.items, policiesReady: true}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), cmd)
	if len(provider.activated) != 0 || len(model.assignmentList.items) != 0 || len(model.discoveryCache) != 0 || len(model.form.durations) != 0 {
		t.Fatalf("principal drift activated or retained old-account roles: requests=%v roles=%v cache=%v form=%#v", provider.activated, model.assignmentList.items, model.discoveryCache, model.form)
	}
	if model.err == nil || !strings.Contains(strings.ToLower(model.err.Error()), "principal changed") {
		t.Fatalf("expected actionable account drift error, got %v", model.err)
	}
}

func TestAccountHealthOldResultCannotInterruptActivation(t *testing.T) {
	provider := &scriptedProvider{}
	model, check := accountTestModel(Runtime{
		AzureResources: provider,
		CheckAccount: func(context.Context) (azureauth.AccountHealth, error) {
			return azureauth.AccountHealth{}, azureauth.ErrAccountUnavailable
		},
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{PrincipalID: "user-1", AccessToken: "checked-token", Satisfied: true}, nil
		},
	})
	old := accountCommandMessages(check)
	if len(old) != 1 {
		t.Fatalf("expected pending account result, got %d", len(old))
	}
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader"}})
	model.assignmentList.toggle("reader")
	model.form.durations["reader"] = "PT1H"
	next, authenticate := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	messages := accountCommandMessages(authenticate)
	if len(messages) != 1 {
		t.Fatalf("expected ARM authentication result, got %d", len(messages))
	}
	next, activate := model.Update(messages[0])
	model = next.(Model)
	next, _ = model.Update(old[0])
	model = next.(Model)
	if model.screen != ScreenProgress || len(model.assignmentList.items) != 1 || strings.Contains(model.View(), "az login") {
		t.Fatalf("late background health changed in-flight activation: screen=%s roles=%v view=%s", model.screen, model.assignmentList.items, model.View())
	}
	model = runAccountCommand(model, activate)
	if len(provider.activated) != 1 || provider.tokens[0] != "checked-token" {
		t.Fatalf("late result changed checked-token submission: requests=%v tokens=%v", provider.activated, provider.tokens)
	}
}

func TestAccountHealthEmptyStateDistinguishesIdentityAndSearch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		complete bool
		want     string
	}{
		{name: "checking", want: "Checking"},
		{name: "unverified", complete: true, err: context.DeadlineExceeded, want: "unverified"},
		{name: "found", complete: true, want: "Account found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, check := accountTestModel(Runtime{
				AzureResources: &scriptedProvider{},
				CheckAccount: func(ctx context.Context) (azureauth.AccountHealth, error) {
					if tc.err != nil {
						return azureauth.AccountHealth{}, tc.err
					}
					return foundAccount(ctx)
				},
			})
			if tc.complete {
				model = runAccountCommand(model, check)
			}
			next, discover := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = runAccountCommand(next.(Model), discover)
			view := model.View()
			if !strings.Contains(view, tc.want) || !strings.Contains(view, "Contoso") || !strings.Contains(view, "tenant-1") {
				t.Fatalf("empty roles obscured account/tenant state %q: %s", tc.want, view)
			}
			if tc.name == "found" && (!strings.Contains(view, "Ada Lovelace") || !strings.Contains(view, "ada@contoso.com")) {
				t.Fatalf("verified empty state omitted account: %s", view)
			}
			model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}})
			model.query = "owner"
			if view := model.View(); !strings.Contains(view, `No assignments match "owner"`) || strings.Contains(view, "No eligible assignments") {
				t.Fatalf("search no-match conflated with empty discovery: %s", view)
			}
		})
	}
}

func TestAccountHealthDiscoveryErrorsDoNotClaimEmptyEligibility(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		detail      string
		unavailable bool
	}{
		{name: "transport", err: errors.New("network unavailable"), detail: "network unavailable"},
		{name: "authorization", err: arm.ResponseError{StatusCode: 403, Status: "Forbidden", Body: `{"error":{"code":"AuthorizationFailed","message":"not allowed"}}`}, detail: "not allowed"},
		{name: "unusable sign-in", err: fmt.Errorf("token: %w", azureauth.ErrNotLoggedIn), unavailable: true},
		{name: "ARM revoked token", err: arm.ResponseError{StatusCode: 401, Status: "Unauthorized", Body: `{"error":{"code":"InvalidAuthenticationToken","message":"token revoked"}}`}, unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, check := accountTestModel(Runtime{AzureResources: &scriptedProvider{discoverErr: tc.err}, CheckAccount: foundAccount})
			model = runAccountCommand(model, check)
			next, discover := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = runAccountCommand(next.(Model), discover)
			view := model.View()
			if strings.Contains(view, "No eligible assignments") || strings.Contains(view, "No assignments match") {
				t.Fatalf("discovery error was presented as an empty result: %s", view)
			}
			if got := strings.Contains(view, "az login --tenant tenant-1"); got != tc.unavailable {
				t.Fatalf("wrong sign-in recovery classification for %v: %s", tc.err, view)
			}
			if !strings.Contains(view, tc.detail) && !tc.unavailable {
				t.Fatalf("discovery failure details lost: %s", view)
			}
		})
	}
}

func TestAccountHealthARMAuthenticationSignInFailureShowsPersistentRecovery(t *testing.T) {
	model, check := accountTestModel(Runtime{
		CheckAccount: foundAccount,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{}, fmt.Errorf("refresh token expired: %w", azureauth.ErrNotLoggedIn)
		},
	})
	model = runAccountCommand(model, check)
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader"}})
	model.assignmentList.toggle("reader")
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), cmd)
	if !strings.Contains(model.View(), "az login --tenant tenant-1") || len(model.assignmentList.items) != 0 {
		t.Fatalf("unusable ARM sign-in did not clear workflow and show recovery: %s", model.View())
	}
}

func TestAccountHealthBatchAuthRejectionPreservesEveryOutcome(t *testing.T) {
	provider := &scriptedProvider{
		results: []pim.ActivationResult{
			{Status: pim.ActivationStatusActivated},
			{Status: pim.ActivationStatusPendingApproval},
		},
		activateErr: []error{nil, fmt.Errorf("sign-in revoked: %w", azureauth.ErrAccountUnavailable), nil},
	}
	model, check := accountTestModel(Runtime{
		AzureResources: provider,
		CheckAccount:   foundAccount,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{PrincipalID: "user-1", AccessToken: "checked-token", Satisfied: true}, nil
		},
	})
	model = runAccountCommand(model, check)
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "reader", DisplayName: "Reader"},
		{ID: "owner", DisplayName: "Owner"},
		{ID: "contributor", DisplayName: "Contributor"},
	})
	for _, id := range []string{"reader", "owner", "contributor"} {
		model.assignmentList.toggle(id)
		model.form.durations[id] = "PT1H"
	}
	model.discoveryCache[discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}] = discoveryEntry{assignments: model.assignmentList.items, policiesReady: true}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runAccountCommand(next.(Model), cmd)
	if len(provider.activated) != 3 || provider.activated[2].Assignment.ID != "contributor" {
		t.Fatalf("individual authentication rejection stopped the batch: requests=%#v", provider.activated)
	}
	if model.screen != ScreenSummary || len(model.summary.results) != 3 {
		t.Fatalf("authentication rejection discarded batch outcomes: screen=%s results=%#v", model.screen, model.summary.results)
	}
	for index, want := range []pim.ActivationStatus{pim.ActivationStatusActivated, pim.ActivationStatusFailed, pim.ActivationStatusPendingApproval} {
		if got := model.summary.results[index].Status; got != want {
			t.Fatalf("result %d: got %s, want %s", index, got, want)
		}
	}
	if len(model.assignmentList.items) != 0 || len(model.discoveryCache) != 0 || len(model.form.durations) != 0 {
		t.Fatalf("post-batch unusable account retained workflow: roles=%v cache=%v form=%#v", model.assignmentList.items, model.discoveryCache, model.form)
	}
	view := model.View()
	for _, want := range []string{"az login --tenant tenant-1", "Reader", "Owner", "Contributor"} {
		if !strings.Contains(view, want) {
			t.Fatalf("summary omitted recovery or outcome %q: %s", want, view)
		}
	}
}

func TestAccountHealthUnavailableSummaryRefreshReloadsWithoutRetryingActivations(t *testing.T) {
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "new-reader", DisplayName: "Reader"}}}}
	model, check := accountTestModel(Runtime{AzureResources: provider, CheckAccount: foundAccount})
	model = runAccountCommand(model, check)
	model.screen = ScreenSummary
	model.activeSection = SectionAzureResources
	model.accountRecoverySection = SectionAzureResources
	model.accountErr = azureauth.ErrAccountUnavailable
	model.summary = newSummary([]pim.ActivationResult{{
		Assignment: pim.EligibleAssignment{ID: "old-owner", DisplayName: "Owner"},
		Status:     pim.ActivationStatusFailed, Message: "sign-in revoked",
	}})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = runAccountCommand(next.(Model), cmd)
	if model.screen != ScreenAssignments || provider.discoverCalls != 1 || len(model.assignmentList.items) != 1 || model.assignmentList.items[0].ID != "new-reader" {
		t.Fatalf("summary recovery did not reload affected workflow: screen=%s discoveries=%d roles=%v", model.screen, provider.discoverCalls, model.assignmentList.items)
	}
	if len(provider.activated) != 0 || len(model.assignmentList.selected()) != 0 || strings.Contains(model.View(), "az login") {
		t.Fatalf("summary recovery retried activation or retained stale sign-in warning: requests=%v selected=%v view=%s", provider.activated, model.assignmentList.selected(), model.View())
	}
}
