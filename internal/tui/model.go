package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/azureauth"
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
	formEditing     bool
	formField       activationFormField
	summary         summary
	loading         bool
	checkingAccount bool
	accountChecked  bool
	account         azureauth.Account
	accountErr      error
	err             error
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
	return Model{
		runtime:         runtime,
		screen:          ScreenHome,
		selectedSection: SectionEntra,
		sections:        []Section{SectionEntra, SectionAzureResources, SectionGroups},
		assignmentList:  newAssignmentList(nil),
		form: activationForm{
			durationISO: "PT1H",
		},
		formField:       formFieldJustification,
		checkingAccount: runtime.Account != nil,
	}
}

func (m Model) Init() tea.Cmd {
	if m.runtime.Account != nil {
		return m.checkAccount()
	}
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
		case "r":
			if m.runtime.Account != nil {
				m.checkingAccount = true
				m.accountErr = nil
				return m, m.checkAccount()
			}
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
	if m.searchMode {
		switch key.Type {
		case tea.KeyEsc, tea.KeyEnter:
			m.searchMode = false
		case tea.KeyBackspace, tea.KeyCtrlH:
			m.query = trimLastRune(m.query)
			m.clampCursor()
		case tea.KeyRunes:
			m.query += string(key.Runes)
			m.clampCursor()
		}
		return m, nil
	}

	switch key.Type {
	case tea.KeyEsc:
		if m.formEditing {
			m.formEditing = false
			return m, nil
		}
		m.screen = ScreenHome
		return m, nil
	case tea.KeyEnter:
		if m.formEditing {
			m.formEditing = false
		}
	case tea.KeyTab:
		if m.formEditing {
			if m.formField == formFieldJustification {
				m.formField = formFieldDuration
			} else {
				m.formField = formFieldJustification
			}
		}
	case tea.KeyBackspace, tea.KeyCtrlH:
		if m.formEditing {
			if m.formField == formFieldJustification {
				m.form.justification = trimLastRune(m.form.justification)
			} else {
				m.form.durationISO = trimLastRune(m.form.durationISO)
			}
		}
	case tea.KeyCtrlU:
		if m.formEditing {
			if m.formField == formFieldJustification {
				m.form.justification = ""
			} else {
				m.form.durationISO = ""
			}
		}
	case tea.KeyUp:
		if m.formEditing {
			return m, nil
		}
		if m.listCursor > 0 {
			m.listCursor--
		}
	case tea.KeyDown:
		if m.formEditing {
			return m, nil
		}
		filtered := m.assignmentList.filtered(m.query)
		if m.listCursor < len(filtered)-1 {
			m.listCursor++
		}
	case tea.KeySpace:
		if m.formEditing {
			if m.formField == formFieldJustification {
				m.form.justification += " "
			} else {
				m.form.durationISO += " "
			}
			return m, nil
		}
		filtered := m.assignmentList.filtered(m.query)
		if len(filtered) == 0 || m.listCursor >= len(filtered) {
			return m, nil
		}
		m.assignmentList.toggle(filtered[m.listCursor].ID)
	case tea.KeyCtrlA:
		if m.formEditing {
			return m, nil
		}
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
		if m.formEditing {
			if m.formField == formFieldJustification {
				m.form.justification += string(key.Runes)
			} else {
				m.form.durationISO += string(key.Runes)
			}
			return m, nil
		}
		switch string(key.Runes) {
		case "/":
			m.searchMode = true
			return m, nil
		case "k":
			if m.listCursor > 0 {
				m.listCursor--
			}
		case "j":
			filtered := m.assignmentList.filtered(m.query)
			if m.listCursor < len(filtered)-1 {
				m.listCursor++
			}
		case "e":
			m.formEditing = true
			m.formField = formFieldJustification
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

func (m Model) viewHome() string {
	var b strings.Builder
	b.WriteString("pim-manager\n\n")
	if m.runtime.Account != nil {
		switch {
		case m.checkingAccount:
			b.WriteString("Azure account: checking Azure CLI login state...\n\n")
		case m.accountErr != nil:
			if errors.Is(m.accountErr, azureauth.ErrNotLoggedIn) {
				b.WriteString("Azure account: sign-in required. Run: az login\n")
			} else {
				b.WriteString("Azure account: check failed.\n")
			}
			fmt.Fprintf(&b, "Details: %s\nRetry: press r to check again.\n\n", m.accountErr)
		case m.accountChecked:
			fmt.Fprintf(&b, "Azure account: %s\nTenant: %s\nSubscription: %s\n\n", valueOrUnknown(m.account.UserName), valueOrUnknown(m.account.TenantID), valueOrUnknown(m.account.SubscriptionID))
		}
	}
	b.WriteString("Sections:\n")
	for i, section := range m.sections {
		cursor := " "
		if i == m.sectionIndex {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %s\n", cursor, section)
	}
	if m.runtime.Account != nil {
		b.WriteString("\nEnter: discover assignments  j/k or arrows: move  r: retry account check  q: quit")
	} else {
		b.WriteString("\nEnter: discover assignments  j/k or arrows: move  q: quit")
	}
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
	searchState := "view mode"
	if m.searchMode {
		searchState = "search mode"
	}
	fmt.Fprintf(&b, "Search: %s (%s)\n", m.query, searchState)
	if m.searchMode {
		b.WriteString("Type to search  Backspace: edit  Enter/Esc: finish search\n")
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

	justificationPrefix := " "
	durationPrefix := " "
	formMode := "view mode"
	if m.formEditing {
		formMode = "edit mode"
		if m.formField == formFieldJustification {
			justificationPrefix = ">"
		} else {
			durationPrefix = ">"
		}
	}
	fmt.Fprintf(&b, "\nActivation form (%s)\n%s Justification: %s\n%s Duration: %s\n", formMode, justificationPrefix, m.form.justification, durationPrefix, m.form.durationISO)
	if m.formEditing {
		b.WriteString("Type to edit  Tab: switch field  Backspace/Ctrl+U: edit  Enter/Esc: finish form")
	} else {
		b.WriteString("Space: toggle selection  /: search  e: edit form  Ctrl+A: activate selected  Esc: back")
	}
	return b.String()
}

func (m Model) checkAccount() tea.Cmd {
	provider := m.runtime.Account
	if provider == nil {
		return nil
	}
	return func() tea.Msg {
		account, err := provider.Account(context.Background())
		return accountCheckedMsg{
			account: account,
			err:     err,
		}
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

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func trimLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
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
