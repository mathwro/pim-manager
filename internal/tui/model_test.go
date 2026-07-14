package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

func TestHomeEnterMovesToSelectedSection(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedSection = SectionGroups

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)

	if got.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", got.screen)
	}
	if got.activeSection != SectionGroups {
		t.Fatalf("expected groups section, got %s", got.activeSection)
	}
}

type scriptedProvider struct {
	discoveries [][]pim.EligibleAssignment
	results     []pim.ActivationResult
	activateErr []error
	discoverErr error
	activated   []pim.ActivationRequest
}

func (p *scriptedProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
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
	model := NewModel(Runtime{Entra: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	msg := cmd()
	next, _ = model.Update(msg)
	model = next.(Model)
	if !strings.Contains(model.View(), "e: edit form") {
		t.Fatalf("expected edit-form hint, got %q", model.View())
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	model = next.(Model)
	model = sendRunes(model, "Need access now")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = next.(Model)
	model = sendRunes(model, "PT2H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	model = next.(Model)
	msg = cmd()
	next, _ = model.Update(msg)
	model = next.(Model)

	if model.screen != ScreenSummary {
		t.Fatalf("expected summary screen, got %s", model.screen)
	}
	if len(provider.activated) != 1 || provider.activated[0].Justification != "Need access now" || provider.activated[0].DurationISO != "PT2H" {
		t.Fatalf("unexpected activation requests: %#v", provider.activated)
	}
	if !strings.Contains(model.View(), "activated") {
		t.Fatalf("expected rendered summary, got %q", model.View())
	}
}

func TestModelShowsDiscoveryErrorWithoutLeavingTUI(t *testing.T) {
	model := NewModel(Runtime{Entra: &scriptedProvider{discoverErr: errors.New("az login required")}})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(cmd())
	model = next.(Model)

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
	model := NewModel(Runtime{Entra: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(cmd())
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	model = next.(Model)
	model = sendRunes(model, "Need access")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	model = next.(Model)
	next, _ = model.Update(cmd())
	model = next.(Model)

	if len(model.summary.retryableFailures()) != 1 {
		t.Fatalf("expected one retryable failure, got %#v", model.summary.failed)
	}
	view := model.View()
	if !strings.Contains(view, "retryable_failures: 1") {
		t.Fatalf("expected retryable summary count, got %q", view)
	}
	if !strings.Contains(view, "Ctrl+A: retry retryable failures") {
		t.Fatalf("expected retry hint, got %q", view)
	}
}

func TestAssignmentsSearchModeFiltersAndSelection(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Global Reader", Scope: pim.Scope{DisplayName: "Tenant"}},
			{ID: "two", DisplayName: "Contributor", Scope: pim.Scope{DisplayName: "rg-prod"}},
		}},
	}
	model := NewModel(Runtime{Entra: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(cmd())
	model = next.(Model)

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
	if !strings.Contains(view, "Search: global") {
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
	next, _ := model.Update(cmd())
	model = next.(Model)
	view := model.View()
	if !strings.Contains(view, "az login") || !strings.Contains(view, "r: retry account check") {
		t.Fatalf("expected login guidance and retry hint, got %q", view)
	}

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = next.(Model)
	if cmd == nil {
		t.Fatal("expected retry command")
	}
	next, _ = model.Update(cmd())
	model = next.(Model)
	view = model.View()
	if !strings.Contains(view, "user@example.com") || !strings.Contains(view, "sub-1") {
		t.Fatalf("expected account context in view after retry, got %q", view)
	}
	if account.calls != 2 {
		t.Fatalf("expected two account calls, got %d", account.calls)
	}
}
