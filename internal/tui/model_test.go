package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

func sendRunes(model Model, text string) Model {
	for _, r := range text {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg = tea.KeyMsg{Type: tea.KeySpace}
		}
		next, _ := model.Update(msg)
		model = next.(Model)
	}
	return model
}

func runCommand(model Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return model
	}
	msg := cmd()
	switch typed := msg.(type) {
	case tea.BatchMsg:
		for _, child := range typed {
			model = runCommand(model, child)
		}
	case assignmentsDiscoveredMsg, activationCompletedMsg, accountCheckedMsg:
		next, _ := model.Update(msg)
		model = next.(Model)
	}
	return model
}

type fakeAssignmentProvider struct{}

func (fakeAssignmentProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
	return nil, nil
}

func (fakeAssignmentProvider) Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error) {
	return pim.ActivationResult{}, nil
}

func TestRuntimeAcceptsAssignmentProviders(t *testing.T) {
	var provider AssignmentProvider = fakeAssignmentProvider{}
	runtime := Runtime{
		Entra:          provider,
		AzureResources: provider,
		Groups:         provider,
	}

	model := NewModel(runtime)

	if model.runtime.Entra == nil {
		t.Fatal("expected Entra provider to be stored")
	}
	if model.runtime.AzureResources == nil {
		t.Fatal("expected Azure Resources provider to be stored")
	}
	if model.runtime.Groups == nil {
		t.Fatal("expected Groups provider to be stored")
	}
}

func TestHomeEnterOpensAzureResources(t *testing.T) {
	model := NewModel(Runtime{})

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)

	if got.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", got.screen)
	}
	if got.activeSection != SectionAzureResources {
		t.Fatalf("expected Azure Resources section, got %s", got.activeSection)
	}
}

type scriptedProvider struct {
	discoveries   [][]pim.EligibleAssignment
	discoverCalls int
	results       []pim.ActivationResult
	activateErr   []error
	discoverErr   error
	activated     []pim.ActivationRequest
}

func (p *scriptedProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
	p.discoverCalls++
	if p.discoverErr != nil {
		return nil, p.discoverErr
	}
	if len(p.discoveries) == 0 {
		return nil, nil
	}
	out := p.discoveries[0]
	p.discoveries = p.discoveries[1:]
	return out, nil
}

func (p *scriptedProvider) Activate(_ context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	p.activated = append(p.activated, request)
	if len(p.activateErr) > 0 {
		err := p.activateErr[0]
		p.activateErr = p.activateErr[1:]
		if err != nil {
			return pim.ActivationResult{}, err
		}
	}
	if len(p.results) == 0 {
		return pim.ActivationResult{}, nil
	}
	result := p.results[0]
	p.results = p.results[1:]
	result.Assignment = request.Assignment
	return result, nil
}

type scriptedAccountProvider struct {
	accounts []azureauth.Account
	errs     []error
	calls    int
}

func (p *scriptedAccountProvider) Account(context.Context) (azureauth.Account, error) {
	p.calls++
	var err error
	if len(p.errs) > 0 {
		err = p.errs[0]
		p.errs = p.errs[1:]
	}
	if err != nil {
		return azureauth.Account{}, err
	}
	var account azureauth.Account
	if len(p.accounts) > 0 {
		account = p.accounts[0]
		p.accounts = p.accounts[1:]
	}
	return account, err
}

func TestModelDiscoversSelectedSectionAndActivatesSelection(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Global Reader"},
			{ID: "two", DisplayName: "Privileged Role Administrator"},
		}},
		results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}},
	}
	model := NewModel(Runtime{AzureResources: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)
	if !strings.Contains(model.View(), "Eligible assignments") {
		t.Fatalf("expected assignments screen, got %q", model.View())
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenActivation || cmd == nil {
		t.Fatalf("expected focused activation form, got screen %s", model.screen)
	}
	model = sendRunes(model, "Need access now")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = next.(Model)
	model = sendRunes(model, "PT2H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenConfirmation {
		t.Fatalf("expected confirmation screen, got %s", model.screen)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenActivation {
		t.Fatalf("expected Esc to return to activation form, got %s", model.screen)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if model.screen != ScreenSummary {
		t.Fatalf("expected summary screen, got %s", model.screen)
	}
	if len(provider.activated) != 1 || provider.activated[0].Justification != "Need access now" || provider.activated[0].DurationISO != "PT2H" {
		t.Fatalf("unexpected activation requests: %#v", provider.activated)
	}
	if !strings.Contains(model.View(), "1 activated") {
		t.Fatalf("expected rendered summary, got %q", model.View())
	}
}

func TestModelShowsDiscoveryErrorWithoutLeavingTUI(t *testing.T) {
	model := NewModel(Runtime{AzureResources: &scriptedProvider{discoverErr: errors.New("az login required")}})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if model.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", model.screen)
	}
	if !strings.Contains(model.View(), "az login required") {
		t.Fatalf("expected discovery error in view, got %q", model.View())
	}
}

func TestModelMarksRetryableProviderFailuresAndShowsRetryAction(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{{ID: "one", DisplayName: "Global Reader"}}},
		activateErr: []error{activation.NewRetryableError(errors.New("temporary Azure error"))},
	}
	model := NewModel(Runtime{AzureResources: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	model = sendRunes(model, "Need access")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if len(model.summary.retryableFailures()) != 1 {
		t.Fatalf("expected one retryable failure, got %#v", model.summary.failed)
	}
	view := model.View()
	if !strings.Contains(view, "1 failed") || !strings.Contains(view, "retry failures") {
		t.Fatalf("expected retryable summary action, got %q", view)
	}
}

func TestAssignmentsSearchModeFiltersAndSelection(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Global Reader", Scope: pim.Scope{DisplayName: "Tenant"}},
			{ID: "two", DisplayName: "Contributor", Scope: pim.Scope{DisplayName: "rg-prod"}},
		}},
	}
	model := NewModel(Runtime{AzureResources: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	model = next.(Model)
	if !model.searchMode {
		t.Fatal("expected search mode to be enabled")
	}
	model = sendRunes(model, "globalx")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = next.(Model)
	if model.query != "global" {
		t.Fatalf("expected query to be edited, got %q", model.query)
	}
	view := model.View()
	if !strings.Contains(view, "/  global_") {
		t.Fatalf("expected search query in view, got %q", view)
	}
	if !strings.Contains(view, "Global Reader") || strings.Contains(view, "Contributor") {
		t.Fatalf("expected filtered assignments in view, got %q", view)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.searchMode {
		t.Fatal("expected search mode to exit on Enter")
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	selected := model.assignmentList.selected()
	if len(selected) != 1 || selected[0].ID != "one" {
		t.Fatalf("expected filtered assignment selected, got %#v", selected)
	}
}

func TestHomeShowsLoginGuidanceAndRetriesAccountLookup(t *testing.T) {
	account := &scriptedAccountProvider{
		accounts: []azureauth.Account{{SubscriptionID: "sub-1", TenantID: "tenant-1", UserName: "user@example.com"}},
		errs:     []error{azureauth.ErrNotLoggedIn, nil},
	}
	model := NewModel(Runtime{Account: account})

	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected Init command to check account")
	}
	model = runCommand(model, cmd)
	view := model.View()
	if !strings.Contains(view, "az login") || !strings.Contains(view, "check sign-in") {
		t.Fatalf("expected login guidance and retry hint, got %q", view)
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = runCommand(next.(Model), cmd)
	view = model.View()
	if !strings.Contains(view, "user@example.com") || !strings.Contains(view, "sub-1") {
		t.Fatalf("expected account context in view after retry, got %q", view)
	}
	if account.calls != 2 {
		t.Fatalf("expected two account calls, got %d", account.calls)
	}
}

func TestSummaryViewListsPerAssignmentStatuses(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenSummary
	model.activeSection = SectionEntra
	model.summary = newSummary([]pim.ActivationResult{
		{
			Assignment: pim.EligibleAssignment{DisplayName: "Global Reader"},
			Status:     pim.ActivationStatusActivated,
			Message:    "Granted",
		},
		{
			Assignment: pim.EligibleAssignment{DisplayName: "Privileged Role Administrator"},
			Status:     pim.ActivationStatusPendingApproval,
			Message:    "PendingApproval",
		},
		{
			Assignment: pim.EligibleAssignment{DisplayName: "Billing Reader"},
			Status:     pim.ActivationStatusFailed,
			Message:    "PolicyBlocked",
		},
	})
	model.refreshSummaryViewport()

	view := model.View()
	if !strings.Contains(view, "- Global Reader: activated (Granted)") {
		t.Fatalf("expected activated row in summary, got %q", view)
	}
	if !strings.Contains(view, "- Privileged Role Administrator: pending_approval (PendingApproval)") {
		t.Fatalf("expected pending_approval row in summary, got %q", view)
	}
	if !strings.Contains(view, "- Billing Reader: failed (PolicyBlocked)") {
		t.Fatalf("expected failed row in summary, got %q", view)
	}
}

func TestModelSupportsHelpBackNavigationAndQuit(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{{ID: "one", DisplayName: "Global Reader"}}},
	}
	model := NewModel(Runtime{AzureResources: provider})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenAssignments {
		t.Fatalf("expected activation Esc to return to assignments, got %s", model.screen)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenHome {
		t.Fatalf("expected assignments Esc to return home, got %s", model.screen)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model = next.(Model)
	if !model.helpVisible || !strings.Contains(model.View(), "Keyboard guide") {
		t.Fatalf("expected contextual help overlay, got %q", model.View())
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.helpVisible {
		t.Fatal("expected Esc to close help")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected q to quit outside text input")
	}
}

func TestDisplayScopeUsesCompactAzureScopeLabels(t *testing.T) {
	tests := []struct {
		name      string
		scopeType pim.ScopeType
		want      string
	}{
		{name: "management group", scopeType: pim.ScopeTypeManagementGroup, want: "MG: scope-name"},
		{name: "subscription", scopeType: pim.ScopeTypeSubscription, want: "Sub: scope-name"},
		{name: "resource group", scopeType: pim.ScopeTypeResourceGroup, want: "RG: scope-name"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assignment := pim.EligibleAssignment{Scope: pim.Scope{DisplayName: "scope-name", Type: test.scopeType}}
			if got := displayScope(assignment); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestWindowResizeUsesAvailableWidthAndHeight(t *testing.T) {
	model := NewModel(Runtime{})
	next, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	model = next.(Model)

	if got, want := model.frameWidth(), 154; got != want {
		t.Fatalf("expected frame width %d, got %d", want, got)
	}
	if got, want := model.assignmentVisibleRows(), 31; got != want {
		t.Fatalf("expected %d visible assignment rows, got %d", want, got)
	}
}

func TestAssignmentColumnsFavorScopeText(t *testing.T) {
	model := NewModel(Runtime{})
	next, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	model = next.(Model)

	if model.scopeColumnWidth() <= model.roleColumnWidth() {
		t.Fatalf("expected scope column wider than role column, got role=%d scope=%d", model.roleColumnWidth(), model.scopeColumnWidth())
	}
}

func TestAssignmentsViewFitsMinimumSupportedTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 20)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:          "assignment",
			DisplayName: "Privileged Role Administrator",
			Scope: pim.Scope{
				DisplayName: "production-management-group-with-long-name",
				Type:        pim.ScopeTypeManagementGroup,
			},
		}
	}

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)

	if got, want := lipgloss.Height(model.View()), 26; got > want {
		t.Fatalf("expected assignments view height at most %d, got %d", want, got)
	}
}

func TestAssignmentsValidationErrorFitsMinimumSupportedTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 20)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:          "assignment",
			DisplayName: "Privileged Role Administrator",
			Scope: pim.Scope{
				DisplayName: "production-management-group-with-long-name",
				Type:        pim.ScopeTypeManagementGroup,
			},
		}
	}

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	view := model.View()
	if want := "select at least one assignment to continue"; !strings.Contains(view, want) {
		t.Fatalf("expected validation error %q to remain visible, got %q", want, view)
	}
	if got, want := lipgloss.Height(view), 26; got > want {
		t.Fatalf("expected assignments view height at most %d, got %d", want, got)
	}
}

func TestNewModelPausesGraphPIMSections(t *testing.T) {
	entra := &scriptedProvider{}
	azureResources := &scriptedProvider{}
	groups := &scriptedProvider{}
	model := NewModel(Runtime{
		Entra:          entra,
		AzureResources: azureResources,
		Groups:         groups,
	})

	if len(model.sections) != 1 || model.sections[0] != SectionAzureResources {
		t.Fatalf("expected only Azure Resources, got %#v", model.sections)
	}
	if model.selectedSection != SectionAzureResources {
		t.Fatalf("expected Azure Resources selected, got %s", model.selectedSection)
	}
	view := model.View()
	if !strings.Contains(view, "Entra Roles and Groups are paused") || !strings.Contains(view, "Microsoft Graph PIM permissions") {
		t.Fatalf("expected paused-section guidance, got %q", view)
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = runCommand(next.(Model), cmd)
	if azureResources.discoverCalls != 1 || entra.discoverCalls != 0 || groups.discoverCalls != 0 {
		t.Fatalf("expected only Azure Resources discovery, got entra=%d azure=%d groups=%d", entra.discoverCalls, azureResources.discoverCalls, groups.discoverCalls)
	}
}
