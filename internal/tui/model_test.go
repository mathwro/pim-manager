package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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
