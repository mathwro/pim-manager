package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

type Screen string

const (
	ScreenTenants      Screen = "tenants"
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
	Entra             AssignmentProvider
	AzureResources    AssignmentProvider
	Groups            AssignmentProvider
	Tenants           TenantProvider
	StepUpCommand     func(string, bool, string) (*exec.Cmd, error)
	ARMAuthentication func(context.Context, bool, string) (azureauth.ARMAuthentication, error)
	CheckUpdate       func(context.Context) (string, error)
}

type AssignmentProvider interface {
	Discover(context.Context) ([]pim.EligibleAssignment, error)
	Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}

type assignmentPreparer interface {
	Prepare(context.Context, []pim.EligibleAssignment) ([]pim.EligibleAssignment, error)
}

type TenantProvider interface {
	Tenants(context.Context) ([]azureauth.Tenant, error)
}

type Model struct {
	runtime                Runtime
	screen                 Screen
	selectedSection        Section
	activeSection          Section
	sections               []Section
	sectionIndex           int
	query                  string
	searchMode             bool
	listCursor             int
	assignmentList         assignmentList
	form                   activationForm
	formField              activationFormField
	justification          textarea.Model
	duration               textinput.Model
	durationIndex          int
	spinner                spinner.Model
	summaryViewport        viewport.Model
	summary                summary
	loading                bool
	checkingAuthentication bool
	authenticationCheck    int
	checkingTenants        bool
	tenantCheck            int
	tenants                []azureauth.Tenant
	tenantIndex            int
	selectedTenant         azureauth.Tenant
	tenantErr              error
	availableUpdate        string
	discoveryCheck         int
	discoveryCancel        context.CancelFunc
	discoveryCancelKey     discoveryKey
	discoveryCache         map[discoveryKey]discoveryEntry
	policiesReady          bool
	preparingPolicies      bool
	waitingForPolicies     bool
	err                    error
	helpVisible            bool
	width                  int
	height                 int
}

type activationFormField int

const (
	formFieldJustification activationFormField = iota
	formFieldDuration
)

type discoveryKey struct {
	tenantID string
	section  Section
}

type discoveryEntry struct {
	assignments   []pim.EligibleAssignment
	policiesReady bool
	generation    int
}

type assignmentsDiscoveredMsg struct {
	assignments []pim.EligibleAssignment
	tenantID    string
	checkID     int
	section     Section
	err         error
}

type assignmentsPreparedMsg struct {
	assignments []pim.EligibleAssignment
	key         discoveryKey
	generation  int
	err         error
}

type activationCompletedMsg struct {
	results []pim.ActivationResult
}

type stepUpCompletedMsg struct {
	selected    []pim.EligibleAssignment
	origin      Screen
	principalID string
	err         error
}

type authenticationCheckedMsg struct {
	selected              []pim.EligibleAssignment
	authentication        azureauth.ARMAuthentication
	mfaRequired           bool
	authenticationContext string
	afterStepUp           bool
	expectedPrincipalID   string
	checkID               int
	origin                Screen
	err                   error
}

type tenantsCheckedMsg struct {
	tenants []azureauth.Tenant
	checkID int
	err     error
}

type updateCheckedMsg struct {
	version string
	err     error
}

func NewModel(runtime Runtime) Model {
	justification := textarea.New()
	justification.Prompt = ""
	justification.FocusedStyle.CursorLine = justification.FocusedStyle.Base
	justification.FocusedStyle.Placeholder = mutedStyle
	justification.BlurredStyle.Placeholder = mutedStyle
	justification.Placeholder = "Why is this access needed?"
	justification.CharLimit = 500
	justification.ShowLineNumbers = false
	justification.SetHeight(3)

	duration := textinput.New()
	duration.Prompt = ""
	duration.CharLimit = 20

	activity := spinner.New()
	activity.Spinner = spinner.Line
	activity.Style = spinnerStyle

	model := Model{
		runtime:         runtime,
		screen:          ScreenHome,
		selectedSection: SectionAzureResources,
		sections:        []Section{SectionAzureResources},
		assignmentList:  newAssignmentList(nil),
		discoveryCache:  make(map[discoveryKey]discoveryEntry),
		policiesReady:   true,
		form:            activationForm{durations: map[string]string{}},
		formField:       formFieldJustification,
		justification:   justification,
		duration:        duration,
		spinner:         activity,
		summaryViewport: viewport.New(0, 0),
		width:           96,
		height:          30,
	}
	if runtime.Tenants != nil {
		model.screen = ScreenTenants
		model.checkingTenants = true
		model.tenantCheck = 1
	}
	model.resizeComponents()
	return model
}

func (m Model) Init() tea.Cmd {
	var commands []tea.Cmd
	if m.runtime.Tenants != nil {
		commands = append(commands, m.checkTenants(m.tenantCheck), m.spinner.Tick)
	}
	if m.runtime.CheckUpdate != nil {
		commands = append(commands, m.checkUpdate())
	}
	if len(commands) == 0 {
		return nil
	}
	return tea.Batch(commands...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.resizeComponents()
		return m, nil
	case spinner.TickMsg:
		if !m.loading && !m.checkingTenants && !m.preparingPolicies && !m.waitingForPolicies {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(typed)
		return m, cmd
	case updateCheckedMsg:
		if typed.err == nil {
			m.availableUpdate = strings.TrimSpace(typed.version)
		}
		return m, nil
	case assignmentsDiscoveredMsg:
		key := discoveryKey{tenantID: typed.tenantID, section: typed.section}
		if typed.checkID != m.discoveryCheck || typed.tenantID != m.selectedTenant.ID {
			return m, nil
		}
		m.cancelDiscovery(false)
		m.loading = false
		m.err = typed.err
		if typed.err != nil {
			delete(m.discoveryCache, key)
			m.assignmentList = newAssignmentList(nil)
			return m, nil
		}
		entry := discoveryEntry{assignments: typed.assignments, policiesReady: true, generation: typed.checkID}
		m.assignmentList = newAssignmentList(typed.assignments)
		m.listCursor = 0
		m.policiesReady = true
		m.preparingPolicies = false
		if preparer, ok := m.providerForSection(typed.section).(assignmentPreparer); ok {
			entry.policiesReady = false
			m.policiesReady = false
			m.preparingPolicies = true
			m.discoveryCache[key] = entry
			ctx, cancel := context.WithCancel(azureauth.WithTenant(context.Background(), typed.tenantID))
			m.discoveryCancel = cancel
			m.discoveryCancelKey = key
			return m, tea.Batch(m.prepareAssignments(ctx, preparer, key, typed.checkID, typed.assignments), m.spinner.Tick)
		}
		m.discoveryCache[key] = entry
		return m, nil
	case assignmentsPreparedMsg:
		entry, ok := m.discoveryCache[typed.key]
		if !ok || entry.generation != typed.generation {
			return m, nil
		}
		m.cancelDiscovery(false)
		current := typed.key == (discoveryKey{tenantID: m.selectedTenant.ID, section: m.activeSection})
		if typed.err != nil {
			delete(m.discoveryCache, typed.key)
			if current {
				m.preparingPolicies = false
				m.waitingForPolicies = false
				m.err = typed.err
			}
			return m, nil
		}
		entry.assignments = typed.assignments
		entry.policiesReady = true
		m.discoveryCache[typed.key] = entry
		if !current {
			return m, nil
		}
		selected := m.assignmentList.selectedIDs
		m.assignmentList = newAssignmentList(typed.assignments)
		for id, enabled := range selected {
			if enabled {
				m.assignmentList.selectedIDs[id] = true
			}
		}
		m.clampCursor()
		m.policiesReady = true
		m.preparingPolicies = false
		m.err = nil
		if m.waitingForPolicies {
			m.waitingForPolicies = false
			return m.openActivationForm()
		}
		return m, nil
	case activationCompletedMsg:
		m.loading = false
		m.cancelDiscovery(true)
		delete(m.discoveryCache, discoveryKey{tenantID: m.selectedTenant.ID, section: m.activeSection})
		m.summary = newSummary(typed.results)
		m.screen = ScreenSummary
		m.refreshSummaryViewport()
		return m, nil
	case stepUpCompletedMsg:
		origin := typed.origin
		if origin == "" {
			origin = ScreenConfirmation
		}
		m.screen = origin
		if typed.err != nil {
			m.err = fmt.Errorf("step-up authentication failed: %w", typed.err)
			return m, nil
		}
		authenticationContext, mfaRequired, err := authenticationRequirement(typed.selected)
		if err != nil {
			m.err = err
			return m, nil
		}
		if m.runtime.ARMAuthentication == nil {
			return m.startActivation(typed.selected, azureauth.ARMAuthentication{})
		}
		return m.startAuthenticationCheck(typed.selected, mfaRequired, authenticationContext, true, origin, typed.principalID)
	case authenticationCheckedMsg:
		if !m.checkingAuthentication || typed.checkID != m.authenticationCheck {
			return m, nil
		}
		m.checkingAuthentication = false
		m.screen = typed.origin
		if typed.err != nil {
			m.err = fmt.Errorf("check Azure CLI ARM authentication: %w", typed.err)
			return m, nil
		}
		if typed.expectedPrincipalID != "" && !strings.EqualFold(typed.expectedPrincipalID, typed.authentication.PrincipalID) {
			m.err = fmt.Errorf("Azure CLI principal changed from %s to %s during authentication; sign in as the original account and retry", typed.expectedPrincipalID, typed.authentication.PrincipalID)
			return m, nil
		}
		if typed.authentication.Satisfied {
			return m.startActivation(typed.selected, typed.authentication)
		}
		if typed.afterStepUp {
			m.err = errors.New("step-up authentication completed but the ARM token does not satisfy the required claims")
			return m, nil
		}
		return m.startStepUp(typed.selected, typed.mfaRequired, typed.authenticationContext, typed.origin, typed.authentication.PrincipalID)
	case tenantsCheckedMsg:
		if typed.checkID != m.tenantCheck {
			return m, nil
		}
		m.checkingTenants = false
		m.tenantErr = typed.err
		if typed.err != nil {
			m.screen = ScreenTenants
			return m, nil
		}
		m.tenants = typed.tenants
		m.clampTenantCursor()
		if len(m.tenants) == 1 {
			m.selectTenant(0)
			return m, nil
		}
		m.screen = ScreenTenants
		if m.selectedTenant.ID != "" {
			index := m.indexOfTenant(m.selectedTenant.ID)
			if index < 0 {
				m.clearTenantWorkflow()
				m.selectedTenant = azureauth.Tenant{}
			} else {
				m.selectedTenant = m.tenants[index]
				m.tenantIndex = index
			}
		}
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
	case ScreenTenants:
		return m.updateTenants(key)
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
	case ScreenTenants:
		content = m.viewTenants()
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

func (m Model) updateTenants(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.checkingTenants {
		return m, nil
	}
	switch key.Type {
	case tea.KeyUp:
		m.moveTenant(-1)
	case tea.KeyDown:
		m.moveTenant(1)
	case tea.KeyEnter:
		if m.tenantErr == nil && len(m.tenants) > 0 {
			m.selectTenant(m.tenantIndex)
		}
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "j":
			m.moveTenant(1)
		case "k":
			m.moveTenant(-1)
		case "r":
			m.tenantCheck++
			m.checkingTenants = true
			m.tenantErr = nil
			return m, tea.Batch(m.checkTenants(m.tenantCheck), m.spinner.Tick)
		}
	}
	return m, nil
}

func (m Model) updateHome(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		if len(m.tenants) > 1 {
			m.screen = ScreenTenants
		}
	case tea.KeyUp:
		m.moveSection(-1)
	case tea.KeyDown:
		m.moveSection(1)
	case tea.KeyEnter:
		return m.beginDiscovery(m.selectedSection, false)
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "k":
			m.moveSection(-1)
		case "j":
			m.moveSection(1)
		}
	}
	return m, nil
}

func (m Model) updateAssignments(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.waitingForPolicies {
		if key.Type == tea.KeyEsc {
			m.waitingForPolicies = false
		}
		return m, nil
	}

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
		if key.String() == "r" {
			return m.beginDiscovery(m.activeSection, true)
		}
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
		if !m.policiesReady {
			if m.preparingPolicies {
				m.waitingForPolicies = true
				m.err = nil
				return m, m.spinner.Tick
			}
			m.err = errors.New("activation requirements are unavailable; press r to retry discovery")
			return m, nil
		}
		return m.openActivationForm()
	case tea.KeyRunes:
		switch string(key.Runes) {
		case " ":
			m.toggleFocusedAssignment()
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
			return m.beginDiscovery(m.activeSection, true)
		}
	}
	return m, nil
}

func (m Model) updateDetails(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "r" && !m.policiesReady && !m.preparingPolicies {
		return m.beginDiscovery(m.activeSection, true)
	}
	if key.Type == tea.KeyEsc || key.Type == tea.KeyEnter || key.String() == "b" {
		m.screen = ScreenAssignments
	}
	return m, nil
}

func (m Model) updateActivation(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.syncForm()
		m.justification.Blur()
		m.duration.Blur()
		m.screen = ScreenAssignments
		m.err = nil
		return m, nil
	case tea.KeyTab:
		return m.moveFormFocus(1)
	case tea.KeyShiftTab:
		return m.moveFormFocus(-1)
	case tea.KeyEnter:
		selected := m.assignmentList.selected()
		if m.formField == formFieldJustification {
			return m.focusDuration(0)
		}
		m.syncForm()
		if m.durationIndex < len(selected)-1 {
			return m.focusDuration(m.durationIndex + 1)
		}
		if err := m.form.validate(selected); err != nil {
			m.err = err
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
	if m.checkingAuthentication {
		if key.Type != tea.KeyEsc {
			return m, nil
		}
		m.checkingAuthentication = false
		m.authenticationCheck++
		m.screen = ScreenActivation
		return m.focusDuration(m.durationIndex)
	}
	switch key.Type {
	case tea.KeyEsc:
		m.screen = ScreenActivation
		return m.focusDuration(m.durationIndex)
	case tea.KeyUp:
		m.durationIndex = max(0, m.durationIndex-1)
	case tea.KeyDown:
		m.durationIndex = min(max(0, len(m.assignmentList.selected())-1), m.durationIndex+1)
	case tea.KeyEnter:
		selected := m.assignmentList.selected()
		if len(selected) == 0 {
			m.screen = ScreenAssignments
			m.err = fmt.Errorf("select at least one assignment to continue")
			return m, nil
		}
		return m.beginAuthentication(selected)
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "k":
			m.durationIndex = max(0, m.durationIndex-1)
		case "j":
			m.durationIndex = min(max(0, len(m.assignmentList.selected())-1), m.durationIndex+1)
		case "e":
			m.screen = ScreenActivation
			return m.focusJustification()
		}
	}
	return m, nil
}

func (m Model) beginAuthentication(selected []pim.EligibleAssignment) (tea.Model, tea.Cmd) {
	authenticationContext, mfaRequired, err := authenticationRequirement(selected)
	if err != nil {
		m.err = err
		return m, nil
	}
	if m.runtime.ARMAuthentication != nil && m.activeSection == SectionAzureResources {
		return m.startAuthenticationCheck(selected, mfaRequired, authenticationContext, false, m.screen, "")
	}
	if !mfaRequired && authenticationContext == "" {
		return m.startActivation(selected, azureauth.ARMAuthentication{})
	}
	if m.runtime.ARMAuthentication != nil {
		return m.startAuthenticationCheck(selected, mfaRequired, authenticationContext, false, m.screen, "")
	}
	return m.startStepUp(selected, mfaRequired, authenticationContext, m.screen, "")
}

func (m Model) startStepUp(selected []pim.EligibleAssignment, mfaRequired bool, authenticationContext string, origin Screen, principalID string) (tea.Model, tea.Cmd) {
	if m.runtime.StepUpCommand == nil {
		m.err = errors.New("step-up authentication is unavailable")
		return m, nil
	}
	command, err := m.runtime.StepUpCommand(m.selectedTenant.ID, mfaRequired, authenticationContext)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.err = nil
	return m, tea.ExecProcess(command, func(err error) tea.Msg {
		return stepUpCompletedMsg{selected: selected, origin: origin, principalID: principalID, err: err}
	})
}

func (m Model) startAuthenticationCheck(selected []pim.EligibleAssignment, mfaRequired bool, authenticationContext string, afterStepUp bool, origin Screen, expectedPrincipalID string) (tea.Model, tea.Cmd) {
	m.authenticationCheck++
	m.checkingAuthentication = true
	m.err = nil
	return m, m.checkAuthentication(selected, mfaRequired, authenticationContext, afterStepUp, m.authenticationCheck, origin, expectedPrincipalID)
}

func (m Model) checkAuthentication(selected []pim.EligibleAssignment, mfaRequired bool, authenticationContext string, afterStepUp bool, checkID int, origin Screen, expectedPrincipalID string) tea.Cmd {
	ctx := m.tenantContext()
	return func() tea.Msg {
		authentication, err := m.runtime.ARMAuthentication(ctx, mfaRequired, authenticationContext)
		return authenticationCheckedMsg{
			selected: selected, authentication: authentication,
			mfaRequired: mfaRequired, authenticationContext: authenticationContext,
			afterStepUp: afterStepUp, checkID: checkID, origin: origin,
			expectedPrincipalID: expectedPrincipalID, err: err,
		}
	}
}

func authenticationRequirement(assignments []pim.EligibleAssignment) (string, bool, error) {
	authenticationContext := ""
	mfaRequired := false
	for _, assignment := range assignments {
		policy := assignment.ActivationPolicy
		mfaRequired = mfaRequired || policy.MFARequired
		candidate := strings.TrimSpace(policy.AuthenticationContext)
		if candidate == "" {
			continue
		}
		if authenticationContext != "" && authenticationContext != candidate {
			return "", false, fmt.Errorf("selected assignments require different authentication contexts %q and %q; activate them in separate batches", authenticationContext, candidate)
		}
		authenticationContext = candidate
	}
	return authenticationContext, mfaRequired, nil
}

func (m Model) startActivation(selected []pim.EligibleAssignment, authentication azureauth.ARMAuthentication) (tea.Model, tea.Cmd) {
	m.screen = ScreenProgress
	m.loading = true
	m.err = nil
	return m, tea.Batch(m.activateSelected(selected, authentication), m.spinner.Tick)
}

func (m Model) updateSummary(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.checkingAuthentication {
		if key.Type == tea.KeyEsc {
			m.checkingAuthentication = false
			m.authenticationCheck++
			return m.beginDiscovery(m.activeSection, true)
		}
		return m, nil
	}
	switch key.Type {
	case tea.KeyEsc:
		return m.beginDiscovery(m.activeSection, true)
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
			return m.beginAuthentication(assignments)
		}
	}
	var cmd tea.Cmd
	m.summaryViewport, cmd = m.summaryViewport.Update(key)
	return m, cmd
}

func (m Model) beginDiscovery(section Section, refresh bool) (tea.Model, tea.Cmd) {
	m.activeSection = section
	m.screen = ScreenAssignments
	key := discoveryKey{tenantID: m.selectedTenant.ID, section: section}
	if refresh {
		m.cancelDiscovery(true)
	}
	if refresh {
		delete(m.discoveryCache, key)
	}
	if entry, ok := m.discoveryCache[key]; ok {
		m.loading = false
		m.err = nil
		m.assignmentList = newAssignmentList(entry.assignments)
		m.policiesReady = entry.policiesReady
		m.preparingPolicies = !entry.policiesReady
		m.waitingForPolicies = false
		return m, nil
	}
	m.cancelDiscovery(true)
	m.loading = true
	m.policiesReady = false
	m.preparingPolicies = false
	m.waitingForPolicies = false
	m.err = nil
	m.query = ""
	m.searchMode = false
	m.assignmentList = newAssignmentList(nil)
	m.listCursor = 0
	m.discoveryCheck++
	ctx, cancel := context.WithCancel(azureauth.WithTenant(context.Background(), m.selectedTenant.ID))
	m.discoveryCancel = cancel
	m.discoveryCancelKey = key
	return m, tea.Batch(m.discoverAssignments(ctx, m.discoveryCheck, m.selectedTenant.ID, section), m.spinner.Tick)
}

func (m Model) openActivationForm() (tea.Model, tea.Cmd) {
	m.formField = formFieldJustification
	m.prepareActivationForm()
	m.screen = ScreenActivation
	m.err = nil
	return m.focusJustification()
}

func (m *Model) prepareActivationForm() {
	selected := m.assignmentList.selected()
	durations := make(map[string]string, len(selected))
	for _, assignment := range selected {
		value := strings.TrimSpace(m.form.durations[assignment.ID])
		if value == "" {
			value = assignment.ActivationPolicy.MaximumDurationISO
		}
		durations[assignment.ID] = value
	}
	m.form.durations = durations
	if m.durationIndex >= len(selected) {
		m.durationIndex = max(0, len(selected)-1)
	}
}

func (m Model) focusJustification() (tea.Model, tea.Cmd) {
	m.syncForm()
	m.formField = formFieldJustification
	m.duration.Blur()
	return m, m.justification.Focus()
}

func (m Model) focusDuration(index int) (tea.Model, tea.Cmd) {
	selected := m.assignmentList.selected()
	if len(selected) == 0 {
		return m.focusJustification()
	}
	m.syncForm()
	m.durationIndex = min(max(index, 0), len(selected)-1)
	m.formField = formFieldDuration
	m.justification.Blur()
	m.duration.SetValue(m.form.durations[selected[m.durationIndex].ID])
	return m, m.duration.Focus()
}

func (m Model) moveFormFocus(delta int) (tea.Model, tea.Cmd) {
	selected := m.assignmentList.selected()
	position := 0
	if m.formField == formFieldDuration {
		position = m.durationIndex + 1
	}
	position = (position + delta + len(selected) + 1) % (len(selected) + 1)
	if position == 0 {
		return m.focusJustification()
	}
	return m.focusDuration(position - 1)
}

func (m *Model) syncForm() {
	m.form.justification = m.justification.Value()
	if m.formField != formFieldDuration {
		return
	}
	selected := m.assignmentList.selected()
	if m.durationIndex < len(selected) {
		m.form.durations[selected[m.durationIndex].ID] = m.duration.Value()
	}
}

func (m Model) acceptingText() bool {
	return m.searchMode || m.screen == ScreenActivation
}

func (m *Model) selectTenant(index int) {
	if index < 0 || index >= len(m.tenants) {
		return
	}
	next := m.tenants[index]
	if m.selectedTenant.ID != next.ID {
		m.clearTenantWorkflow()
	}
	m.selectedTenant = next
	m.tenantIndex = index
	m.screen = ScreenHome
	m.tenantErr = nil
}

func (m *Model) clearTenantWorkflow() {
	m.cancelDiscovery(true)
	m.discoveryCheck++
	m.authenticationCheck++
	m.checkingAuthentication = false
	m.loading = false
	m.policiesReady = false
	m.preparingPolicies = false
	m.waitingForPolicies = false
	m.activeSection = ""
	m.query = ""
	m.searchMode = false
	m.assignmentList = newAssignmentList(nil)
	m.form = activationForm{durations: map[string]string{}}
	m.justification.SetValue("")
	m.duration.SetValue("")
	m.durationIndex = 0
	m.summary = summary{}
	m.err = nil
}

func (m Model) tenantContext() context.Context {
	return azureauth.WithTenant(context.Background(), m.selectedTenant.ID)
}

func (m *Model) moveTenant(delta int) {
	m.tenantIndex += delta
	m.clampTenantCursor()
}

func (m *Model) clampTenantCursor() {
	if len(m.tenants) == 0 {
		m.tenantIndex = 0
		return
	}
	m.tenantIndex = min(max(m.tenantIndex, 0), len(m.tenants)-1)
}

func (m Model) indexOfTenant(tenantID string) int {
	for index, tenant := range m.tenants {
		if strings.EqualFold(tenant.ID, tenantID) {
			return index
		}
	}
	return -1
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
	allSelected := true
	selectable := 0
	for _, assignment := range filtered {
		if assignment.Active {
			delete(m.assignmentList.selectedIDs, assignment.ID)
			continue
		}
		selectable++
		if !m.assignmentList.selectedIDs[assignment.ID] {
			allSelected = false
		}
	}
	if selectable == 0 {
		return
	}
	for _, assignment := range filtered {
		if !assignment.Active {
			m.assignmentList.selectedIDs[assignment.ID] = !allSelected
		}
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

func (m Model) discoverAssignments(ctx context.Context, checkID int, tenantID string, section Section) tea.Cmd {
	provider := m.providerForSection(section)
	if provider == nil {
		return func() tea.Msg {
			return assignmentsDiscoveredMsg{tenantID: tenantID, checkID: checkID, section: section, err: fmt.Errorf("provider for %s is unavailable", section)}
		}
	}
	return func() tea.Msg {
		assignments, err := provider.Discover(ctx)
		return assignmentsDiscoveredMsg{assignments: assignments, tenantID: tenantID, checkID: checkID, section: section, err: err}
	}
}

func (m *Model) cancelDiscovery(deletePending bool) {
	if m.discoveryCancel != nil {
		m.discoveryCancel()
		m.discoveryCancel = nil
	}
	if deletePending {
		delete(m.discoveryCache, m.discoveryCancelKey)
	}
	m.discoveryCancelKey = discoveryKey{}
}

func (m Model) prepareAssignments(ctx context.Context, preparer assignmentPreparer, key discoveryKey, generation int, assignments []pim.EligibleAssignment) tea.Cmd {
	return func() tea.Msg {
		prepared, err := preparer.Prepare(ctx, assignments)
		return assignmentsPreparedMsg{assignments: prepared, key: key, generation: generation, err: err}
	}
}

func (m Model) activateSelected(selected []pim.EligibleAssignment, authentication azureauth.ARMAuthentication) tea.Cmd {
	provider := m.providerForSection(m.activeSection)
	justification := strings.TrimSpace(m.form.justification)
	ctx := arm.WithAccessToken(m.tenantContext(), authentication.AccessToken)
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
			requestAssignment := assignment
			if m.activeSection == SectionAzureResources && authentication.PrincipalID != "" {
				requestAssignment.PrincipalID = authentication.PrincipalID
			}
			request := pim.ActivationRequest{
				Assignment:    requestAssignment,
				Justification: justification,
				DurationISO:   strings.TrimSpace(m.form.durations[assignment.ID]),
			}
			result, err := provider.Activate(ctx, request)
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

func (m Model) checkTenants(checkID int) tea.Cmd {
	provider := m.runtime.Tenants
	if provider == nil {
		return nil
	}
	return func() tea.Msg {
		tenants, err := provider.Tenants(context.Background())
		return tenantsCheckedMsg{tenants: tenants, checkID: checkID, err: err}
	}
}

func (m Model) checkUpdate() tea.Cmd {
	return func() tea.Msg {
		version, err := m.runtime.CheckUpdate(context.Background())
		return updateCheckedMsg{version: version, err: err}
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
