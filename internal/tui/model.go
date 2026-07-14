package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/pim"
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

type Runtime struct {
	Entra          AssignmentProvider
	AzureResources AssignmentProvider
	Groups         AssignmentProvider
}

type AssignmentProvider interface {
	Discover(context.Context) ([]pim.EligibleAssignment, error)
	Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}

type Model struct {
	runtime         Runtime
	screen          Screen
	selectedSection Section
	activeSection   Section
	sections        []Section
	sectionIndex    int
	query           string
	listCursor      int
	assignmentList  assignmentList
	form            activationForm
	summary         summary
	loading         bool
	err             error
}

type assignmentsDiscoveredMsg struct {
	assignments []pim.EligibleAssignment
	err         error
}

type activationCompletedMsg struct {
	results []pim.ActivationResult
}

func NewModel(runtime Runtime) Model {
	return Model{
		runtime:         runtime,
		screen:          ScreenHome,
		selectedSection: SectionEntra,
		sections:        []Section{SectionEntra, SectionAzureResources, SectionGroups},
		assignmentList:  newAssignmentList(nil),
		form: activationForm{
			durationISO: "PT1H",
		},
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case assignmentsDiscoveredMsg:
		m.loading = false
		m.err = typed.err
		if typed.err != nil {
			m.assignmentList = newAssignmentList(nil)
			return m, nil
		}
		m.assignmentList = newAssignmentList(typed.assignments)
		m.listCursor = 0
		return m, nil
	case activationCompletedMsg:
		m.loading = false
		m.summary = newSummary(typed.results)
		m.screen = ScreenSummary
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch m.screen {
	case ScreenHome:
		return m.updateHome(key)
	case ScreenAssignments:
		return m.updateAssignments(key)
	case ScreenSummary:
		return m.updateSummary(key)
	}

	return m, nil
}

func (m Model) View() string {
	switch m.screen {
	case ScreenHome:
		return m.viewHome()
	case ScreenAssignments:
		return m.viewAssignments()
	case ScreenSummary:
		return m.viewSummary()
	default:
		return "pim-manager"
	}
}

func (m Model) updateHome(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyUp:
		if m.sectionIndex > 0 {
			m.sectionIndex--
		}
		m.selectedSection = m.sections[m.sectionIndex]
	case tea.KeyDown:
		if m.sectionIndex < len(m.sections)-1 {
			m.sectionIndex++
		}
		m.selectedSection = m.sections[m.sectionIndex]
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "k":
			if m.sectionIndex > 0 {
				m.sectionIndex--
			}
			m.selectedSection = m.sections[m.sectionIndex]
		case "j":
			if m.sectionIndex < len(m.sections)-1 {
				m.sectionIndex++
			}
			m.selectedSection = m.sections[m.sectionIndex]
		}
	case tea.KeyEnter:
		m.activeSection = m.selectedSection
		m.screen = ScreenAssignments
		m.loading = true
		m.err = nil
		m.query = ""
		m.assignmentList = newAssignmentList(nil)
		m.listCursor = 0
		return m, m.discoverAssignments()
	}
	return m, nil
}

func (m Model) updateAssignments(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.screen = ScreenHome
		return m, nil
	case tea.KeyUp:
		if m.listCursor > 0 {
			m.listCursor--
		}
	case tea.KeyDown:
		filtered := m.assignmentList.filtered(m.query)
		if m.listCursor < len(filtered)-1 {
			m.listCursor++
		}
	case tea.KeySpace:
		filtered := m.assignmentList.filtered(m.query)
		if len(filtered) == 0 || m.listCursor >= len(filtered) {
			return m, nil
		}
		m.assignmentList.toggle(filtered[m.listCursor].ID)
	case tea.KeyCtrlA:
		if m.loading || m.err != nil {
			return m, nil
		}
		if !m.form.valid() {
			m.err = fmt.Errorf("justification and duration are required")
			return m, nil
		}
		selected := m.assignmentList.selected()
		if len(selected) == 0 {
			m.err = fmt.Errorf("select at least one assignment")
			return m, nil
		}
		m.loading = true
		m.err = nil
		return m, m.activateSelected(selected)
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "k":
			if m.listCursor > 0 {
				m.listCursor--
			}
		case "j":
			filtered := m.assignmentList.filtered(m.query)
			if m.listCursor < len(filtered)-1 {
				m.listCursor++
			}
		}
	}
	return m, nil
}

func (m Model) updateSummary(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.screen = ScreenAssignments
		return m, nil
	case tea.KeyCtrlA:
		if m.loading {
			return m, nil
		}
		retryable := m.summary.retryableFailures()
		if len(retryable) == 0 {
			return m, nil
		}
		assignments := make([]pim.EligibleAssignment, 0, len(retryable))
		for _, result := range retryable {
			assignments = append(assignments, result.Assignment)
		}
		m.loading = true
		return m, m.activateSelected(assignments)
	}
	return m, nil
}

func (m Model) providerForSection(section Section) AssignmentProvider {
	switch section {
	case SectionEntra:
		return m.runtime.Entra
	case SectionAzureResources:
		return m.runtime.AzureResources
	case SectionGroups:
		return m.runtime.Groups
	default:
		return nil
	}
}

func (m Model) discoverAssignments() tea.Cmd {
	provider := m.providerForSection(m.activeSection)
	if provider == nil {
		return func() tea.Msg {
			return assignmentsDiscoveredMsg{err: fmt.Errorf("provider for %s is unavailable", m.activeSection)}
		}
	}
	return func() tea.Msg {
		assignments, err := provider.Discover(context.Background())
		return assignmentsDiscoveredMsg{
			assignments: assignments,
			err:         err,
		}
	}
}

func (m Model) activateSelected(selected []pim.EligibleAssignment) tea.Cmd {
	provider := m.providerForSection(m.activeSection)
	justification := strings.TrimSpace(m.form.justification)
	duration := strings.TrimSpace(m.form.durationISO)
	return func() tea.Msg {
		results := make([]pim.ActivationResult, 0, len(selected))
		if provider == nil {
			for _, assignment := range selected {
				results = append(results, pim.ActivationResult{
					Assignment: assignment,
					Status:     pim.ActivationStatusFailed,
					Message:    fmt.Sprintf("provider for %s is unavailable", m.activeSection),
				})
			}
			return activationCompletedMsg{results: results}
		}

		for _, assignment := range selected {
			request := pim.ActivationRequest{
				Assignment:    assignment,
				Justification: justification,
				DurationISO:   duration,
			}
			result, err := provider.Activate(context.Background(), request)
			if err != nil {
				results = append(results, pim.ActivationResult{
					Assignment: assignment,
					Status:     pim.ActivationStatusFailed,
					Message:    err.Error(),
				})
				continue
			}
			if result.Assignment.ID == "" {
				result.Assignment = assignment
			}
			results = append(results, result)
		}
		return activationCompletedMsg{results: results}
	}
}

func (m Model) viewHome() string {
	var b strings.Builder
	b.WriteString("pim-manager\n\nSections:\n")
	for i, section := range m.sections {
		cursor := " "
		if i == m.sectionIndex {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %s\n", cursor, section)
	}
	b.WriteString("\nEnter: discover assignments  j/k or arrows: move  q: quit")
	return b.String()
}

func (m Model) viewAssignments() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Section: %s\n", m.activeSection)
	if m.loading {
		b.WriteString("Loading assignments...\n")
	}
	if m.err != nil {
		fmt.Fprintf(&b, "Error: %s\n", m.err)
	}

	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 && !m.loading {
		b.WriteString("No assignments found.\n")
	}

	for i, assignment := range filtered {
		cursor := " "
		if i == m.listCursor {
			cursor = ">"
		}
		checked := " "
		if m.assignmentList.selectedIDs[assignment.ID] {
			checked = "x"
		}
		scope := assignment.DisplayScope()
		if strings.TrimSpace(scope) == "" {
			scope = "scope unavailable"
		}
		fmt.Fprintf(&b, "%s [%s] %s (%s)\n", cursor, checked, assignment.DisplayName, scope)
	}

	fmt.Fprintf(&b, "\nJustification: %s\nDuration: %s\n", m.form.justification, m.form.durationISO)
	b.WriteString("Space: toggle selection  Ctrl+A: activate selected  Esc: back")
	return b.String()
}

func (m Model) viewSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Summary for %s\n\n", m.activeSection)
	fmt.Fprintf(&b, "activated: %d\n", len(m.summary.activated))
	fmt.Fprintf(&b, "pending_approval: %d\n", len(m.summary.pendingApproval))
	fmt.Fprintf(&b, "failed: %d\n", len(m.summary.failed))
	fmt.Fprintf(&b, "retryable_failures: %d\n", len(m.summary.retryableFailures()))

	if len(m.summary.failed) > 0 {
		b.WriteString("\nFailures:\n")
		for _, result := range m.summary.failed {
			fmt.Fprintf(&b, "- %s: %s\n", result.Assignment.DisplayName, result.Message)
		}
	}
	if len(m.summary.retryableFailures()) > 0 {
		b.WriteString("\nCtrl+A: retry retryable failures  Esc: back")
	} else {
		b.WriteString("\nEsc: back")
	}
	return b.String()
}
