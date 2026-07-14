package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

type Screen string

const (
	ScreenHome        Screen = "home"
	ScreenAssignments Screen = "assignments"
	ScreenActivation  Screen = "activation"
	ScreenSummary     Screen = "summary"
)

type Section string

const (
	SectionEntra          Section = "Entra Roles"
	SectionAzureResources Section = "Azure Resources"
	SectionGroups         Section = "Groups"
)

type Runtime struct{}

type Model struct {
	runtime         Runtime
	screen          Screen
	selectedSection Section
	activeSection   Section
	sections        []Section
}

func NewModel(runtime Runtime) Model {
	return Model{
		runtime:         runtime,
		screen:          ScreenHome,
		selectedSection: SectionEntra,
		sections:        []Section{SectionEntra, SectionAzureResources, SectionGroups},
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.screen == ScreenHome && key.Type == tea.KeyEnter {
		m.activeSection = m.selectedSection
		m.screen = ScreenAssignments
	}
	return m, nil
}

func (m Model) View() string {
	return "pim-manager"
}
