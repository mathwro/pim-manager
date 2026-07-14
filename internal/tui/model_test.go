package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/pim"
)

func sendRunes(model Model, text string) Model {
	for _, r := range text {
		next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
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
	result := p.results[0]
	p.results = p.results[1:]
	result.Assignment = request.Assignment
	return result, nil
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
	model = sendRunes(model, "Need access")
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
	if len(provider.activated) != 1 || provider.activated[0].Justification != "Need access" || provider.activated[0].DurationISO != "PT2H" {
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
