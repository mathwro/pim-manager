package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/pim"
)

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
