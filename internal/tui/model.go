package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

type Screen string

const (
	ScreenHome         Screen = "home"
	ScreenAssignments  Screen = "assignments"
	ScreenDetails      Screen = "details"
	ScreenActivation   Screen = "activation"
	ScreenConfirmation Screen = "confirmation"
	ScreenProgress     Screen = "progress"
	ScreenSummary      Screen = "summary"
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
	Account        AccountProvider
}

type AssignmentProvider interface {
	Discover(context.Context) ([]pim.EligibleAssignment, error)
	Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}

type AccountProvider interface {
	Account(context.Context) (azureauth.Account, error)
}

type Model struct {
	runtime         Runtime
	screen          Screen
	selectedSection Section
	activeSection   Section
	sections        []Section
	sectionIndex    int
	query           string
	searchMode      bool
	listCursor      int
	assignmentList  assignmentList
	form            activationForm
	formField       activationFormField
	justification   textarea.Model
	duration        textinput.Model
	spinner         spinner.Model
	summaryViewport viewport.Model
	summary         summary
	loading         bool
	checkingAccount bool
	accountChecked  bool
	account         azureauth.Account
	accountErr      error
	err             error
	helpVisible     bool
	width           int
	height          int
}

type activationFormField int

const (
	formFieldJustification activationFormField = iota
	formFieldDuration
)

type assignmentsDiscoveredMsg struct {
	assignments []pim.EligibleAssignment
	err         error
}

type activationCompletedMsg struct {
	results []pim.ActivationResult
}

type accountCheckedMsg struct {
	account azureauth.Account
	err     error
}

func NewModel(runtime Runtime) Model {
	justification := textarea.New()
	justification.Placeholder = "Why is this access needed?"
	justification.CharLimit = 500
	justification.ShowLineNumbers = false
	justification.SetHeight(4)

	duration := textinput.New()
	duration.Prompt = ""
	duration.Placeholder = "PT1H"
	duration.CharLimit = 20
	duration.SetValue("PT1H")

	activity := spinner.New()
	activity.Spinner = spinner.Line
	activity.Style = spinnerStyle

	model := Model{
		runtime:         runtime,
		screen:          ScreenHome,
		selectedSection: SectionAzureResources,
		sections:        []Section{SectionAzureResources},
		assignmentList:  newAssignmentList(nil),
		form: activationForm{
			durationISO: "PT1H",
		},
		formField:       formFieldJustification,
		justification:   justification,
		duration:        duration,
		spinner:         activity,
		summaryViewport: viewport.New(0, 0),
		checkingAccount: runtime.Account != nil,
		width:           96,
		height:          30,
	}
	model.resizeComponents()
	return model
}

func (m Model) Init() tea.Cmd {
	if m.runtime.Account == nil {
		return nil
	}
	return m.checkAccount()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.resizeComponents()
		return m, nil
	case spinner.TickMsg:
		if !m.loading && !m.checkingAccount {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(typed)
		return m, cmd
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
		m.refreshSummaryViewport()
		return m, nil
	case accountCheckedMsg:
		m.checkingAccount = false
		m.accountChecked = true
		m.account = typed.account
		m.accountErr = typed.err
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if key.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.helpVisible {
		if key.Type == tea.KeyEsc || key.String() == "?" || key.String() == "q" {
			m.helpVisible = false
		}
		return m, nil
	}
	if !m.acceptingText() {
		switch key.String() {
		case "q":
			return m, tea.Quit
		case "?":
			m.helpVisible = true
			return m, nil
		}
	}

	switch m.screen {
	case ScreenHome:
		return m.updateHome(key)
	case ScreenAssignments:
		return m.updateAssignments(key)
	case ScreenDetails:
		return m.updateDetails(key)
	case ScreenActivation:
		return m.updateActivation(key)
	case ScreenConfirmation:
		return m.updateConfirmation(key)
	case ScreenProgress:
		return m, nil
	case ScreenSummary:
		return m.updateSummary(key)
	default:
		return m, nil
	}
}

func (m Model) View() string {
	var content string
	switch m.screen {
	case ScreenHome:
		content = m.viewHome()
	case ScreenAssignments:
		content = m.viewAssignments()
	case ScreenDetails:
		content = m.viewDetails()
	case ScreenActivation:
		content = m.viewActivation()
	case ScreenConfirmation:
		content = m.viewConfirmation()
	case ScreenProgress:
		content = m.viewProgress()
	case ScreenSummary:
		content = m.viewSummary()
	default:
		content = "pim-manager"
	}
	if m.helpVisible {
		content = m.viewHelp()
	}
	return appFrameStyle.Width(m.frameWidth()).Render(content)
}

func (m Model) updateHome(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyUp:
		m.moveSection(-1)
	case tea.KeyDown:
		m.moveSection(1)
	case tea.KeyEnter:
		if m.runtime.Account != nil && (m.checkingAccount || m.accountErr != nil) {
			return m, nil
		}
		return m.beginDiscovery(m.selectedSection)
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "k":
			m.moveSection(-1)
		case "j":
			m.moveSection(1)
		case "r":
			if m.runtime.Account != nil {
				m.checkingAccount = true
				m.accountErr = nil
				return m, tea.Batch(m.checkAccount(), m.spinner.Tick)
			}
		}
	}
	return m, nil
}

func (m Model) updateAssignments(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searchMode {
		switch key.Type {
		case tea.KeyEsc, tea.KeyEnter:
			m.searchMode = false
		case tea.KeyBackspace, tea.KeyCtrlH:
			m.query = trimLastRune(m.query)
			m.clampCursor()
		case tea.KeyCtrlU:
			m.query = ""
			m.clampCursor()
		case tea.KeyRunes, tea.KeySpace:
			m.query += key.String()
			m.clampCursor()
		}
		return m, nil
	}

	if m.loading {
		if key.Type == tea.KeyEsc {
			m.screen = ScreenHome
		}
		return m, nil
	}

	switch key.Type {
	case tea.KeyEsc:
		m.screen = ScreenHome
		m.err = nil
		return m, nil
	case tea.KeyUp:
		m.moveAssignment(-1)
	case tea.KeyDown:
		m.moveAssignment(1)
	case tea.KeySpace:
		m.toggleFocusedAssignment()
	case tea.KeyEnter:
		if len(m.assignmentList.selected()) == 0 {
			m.err = fmt.Errorf("select at least one assignment to continue")
			return m, nil
		}
		return m.openActivationForm()
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "/":
			m.searchMode = true
			m.err = nil
		case "j":
			m.moveAssignment(1)
		case "k":
			m.moveAssignment(-1)
		case "i":
			if len(m.assignmentList.filtered(m.query)) > 0 {
				m.screen = ScreenDetails
			}
		case "a":
			m.toggleAllFiltered()
		case "r":
			return m.beginDiscovery(m.activeSection)
		}
	}
	return m, nil
}

func (m Model) updateDetails(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyEsc || key.Type == tea.KeyEnter || key.String() == "b" {
		m.screen = ScreenAssignments
	}
	return m, nil
}

func (m Model) updateActivation(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.justification.Blur()
		m.duration.Blur()
		m.screen = ScreenAssignments
		m.err = nil
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab:
		return m.toggleFormFocus()
	case tea.KeyEnter:
		if m.formField == formFieldJustification {
			return m.focusDuration()
		}
		m.syncForm()
		if !m.form.valid() {
			m.err = fmt.Errorf("justification and duration are required")
			return m, nil
		}
		m.err = nil
		m.duration.Blur()
		m.screen = ScreenConfirmation
		return m, nil
	}

	if key.Type == tea.KeySpace {
		key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	}

	var cmd tea.Cmd
	if m.formField == formFieldJustification {
		m.justification, cmd = m.justification.Update(key)
	} else {
		m.duration, cmd = m.duration.Update(key)
	}
	m.syncForm()
	m.err = nil
	return m, cmd
}

func (m Model) updateConfirmation(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.screen = ScreenActivation
		return m.focusDuration()
	case tea.KeyEnter:
		selected := m.assignmentList.selected()
		if len(selected) == 0 {
			m.screen = ScreenAssignments
			m.err = fmt.Errorf("select at least one assignment to continue")
			return m, nil
		}
		m.screen = ScreenProgress
		m.loading = true
		m.err = nil
		return m, tea.Batch(m.activateSelected(selected), m.spinner.Tick)
	case tea.KeyRunes:
		if string(key.Runes) == "e" {
			m.screen = ScreenActivation
			return m.focusJustification()
		}
	}
	return m, nil
}

func (m Model) updateSummary(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.screen = ScreenAssignments
		return m, nil
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "h":
			m.screen = ScreenHome
			return m, nil
		case "r":
			retryable := m.summary.retryableFailures()
			if len(retryable) == 0 {
				return m, nil
			}
			assignments := make([]pim.EligibleAssignment, 0, len(retryable))
			for _, result := range retryable {
				assignments = append(assignments, result.Assignment)
			}
			m.screen = ScreenProgress
			m.loading = true
			return m, tea.Batch(m.activateSelected(assignments), m.spinner.Tick)
		}
	}
	var cmd tea.Cmd
	m.summaryViewport, cmd = m.summaryViewport.Update(key)
	return m, cmd
}

func (m Model) beginDiscovery(section Section) (tea.Model, tea.Cmd) {
	m.activeSection = section
	m.screen = ScreenAssignments
	m.loading = true
	m.err = nil
	m.query = ""
	m.searchMode = false
	m.assignmentList = newAssignmentList(nil)
	m.listCursor = 0
	return m, tea.Batch(m.discoverAssignments(), m.spinner.Tick)
}

func (m Model) openActivationForm() (tea.Model, tea.Cmd) {
	m.screen = ScreenActivation
	m.err = nil
	return m.focusJustification()
}

func (m Model) focusJustification() (tea.Model, tea.Cmd) {
	m.formField = formFieldJustification
	m.duration.Blur()
	return m, m.justification.Focus()
}

func (m Model) focusDuration() (tea.Model, tea.Cmd) {
	m.formField = formFieldDuration
	m.justification.Blur()
	return m, m.duration.Focus()
}

func (m Model) toggleFormFocus() (tea.Model, tea.Cmd) {
	if m.formField == formFieldJustification {
		return m.focusDuration()
	}
	return m.focusJustification()
}

func (m *Model) syncForm() {
	m.form.justification = m.justification.Value()
	m.form.durationISO = m.duration.Value()
}

func (m Model) acceptingText() bool {
	return m.searchMode || m.screen == ScreenActivation
}

func (m *Model) moveSection(delta int) {
	m.sectionIndex += delta
	if m.sectionIndex < 0 {
		m.sectionIndex = 0
	}
	if m.sectionIndex >= len(m.sections) {
		m.sectionIndex = len(m.sections) - 1
	}
	m.selectedSection = m.sections[m.sectionIndex]
}

func (m *Model) moveAssignment(delta int) {
	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 {
		m.listCursor = 0
		return
	}
	m.listCursor += delta
	if m.listCursor < 0 {
		m.listCursor = 0
	}
	if m.listCursor >= len(filtered) {
		m.listCursor = len(filtered) - 1
	}
}

func (m *Model) toggleFocusedAssignment() {
	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 || m.listCursor >= len(filtered) {
		return
	}
	m.assignmentList.toggle(filtered[m.listCursor].ID)
	m.err = nil
}

func (m *Model) toggleAllFiltered() {
	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 {
		return
	}
	allSelected := true
	for _, assignment := range filtered {
		if !m.assignmentList.selectedIDs[assignment.ID] {
			allSelected = false
			break
		}
	}
	for _, assignment := range filtered {
		m.assignmentList.selectedIDs[assignment.ID] = !allSelected
	}
	m.err = nil
}

func (m Model) focusedAssignment() (pim.EligibleAssignment, bool) {
	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 || m.listCursor >= len(filtered) {
		return pim.EligibleAssignment{}, false
	}
	return filtered[m.listCursor], true
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
		return assignmentsDiscoveredMsg{assignments: assignments, err: err}
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
					Retryable:  activation.IsRetryable(err),
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

func (m Model) checkAccount() tea.Cmd {
	provider := m.runtime.Account
	if provider == nil {
		return nil
	}
	return func() tea.Msg {
		account, err := provider.Account(context.Background())
		return accountCheckedMsg{account: account, err: err}
	}
}

func (m *Model) clampCursor() {
	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 {
		m.listCursor = 0
		return
	}
	if m.listCursor >= len(filtered) {
		m.listCursor = len(filtered) - 1
	}
}

func trimLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}
