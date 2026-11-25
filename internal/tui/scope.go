package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mathwro/pim-manager/internal/pim"
)

type childScopeDiscoverer interface {
	DiscoverChildScopes(context.Context, pim.Scope) ([]pim.Scope, error)
}

type scopeLevel struct {
	parent   pim.Scope
	children []pim.Scope
	cursor   int
	query    string
}

type scopePickerState struct {
	assignment   pim.EligibleAssignment
	base         pim.EligibleAssignment
	stack        []scopeLevel
	query        string
	searchMode   bool
	loading      bool
	preparing    bool
	checkID      int
	cancel       context.CancelFunc
	err          error
	basePolicies map[string]pim.EligibleAssignment
}

type childScopesDiscoveredMsg struct {
	tenantID string
	checkID  int
	parent   pim.Scope
	children []pim.Scope
	push     bool
	err      error
}

type scopedAssignmentPreparedMsg struct {
	tenantID   string
	checkID    int
	assignment pim.EligibleAssignment
	err        error
}

func canNarrowScope(section Section, provider AssignmentProvider, assignment pim.EligibleAssignment) bool {
	if section != SectionAzureResources || assignment.Active {
		return false
	}
	if _, ok := provider.(childScopeDiscoverer); !ok {
		return false
	}
	baseType := assignment.Scope.Type
	return baseType == pim.ScopeTypeManagementGroup || baseType == pim.ScopeTypeSubscription
}

func canDrillScope(scope pim.Scope) bool {
	return scope.Type == pim.ScopeTypeManagementGroup || scope.Type == pim.ScopeTypeSubscription
}

func (m *Model) rememberBaseAssignment(assignment pim.EligibleAssignment) pim.EligibleAssignment {
	if m.scopePicker.basePolicies == nil {
		m.scopePicker.basePolicies = make(map[string]pim.EligibleAssignment)
	}
	if base, ok := m.scopePicker.basePolicies[assignment.ID]; ok {
		return base
	}
	m.scopePicker.basePolicies[assignment.ID] = assignment
	return assignment
}

func (m *Model) syncBaseAssignments(assignments []pim.EligibleAssignment) {
	if m.scopePicker.basePolicies == nil {
		m.scopePicker.basePolicies = make(map[string]pim.EligibleAssignment, len(assignments))
	} else {
		clear(m.scopePicker.basePolicies)
	}
	for _, assignment := range assignments {
		m.scopePicker.basePolicies[assignment.ID] = assignment
	}
}

func (m *Model) cancelScopePicker() {
	if m.scopePicker.cancel != nil {
		m.scopePicker.cancel()
		m.scopePicker.cancel = nil
	}
	m.scopePicker.loading = false
	m.scopePicker.preparing = false
	m.scopePicker.searchMode = false
	m.endOperation("scopes")
}

func (m Model) openScopePicker() (tea.Model, tea.Cmd) {
	assignment, ok := m.focusedAssignment()
	if !ok {
		return m, nil
	}
	if !m.policiesReady {
		if m.preparingPolicies {
			m.err = fmt.Errorf("wait for activation requirements to finish loading before narrowing scope")
		} else {
			m.err = fmt.Errorf("activation requirements are unavailable; press r to retry discovery")
		}
		return m, nil
	}
	provider := m.providerForSection(m.activeSection)
	discoverer, ok := provider.(childScopeDiscoverer)
	base := m.rememberBaseAssignment(assignment)
	if !ok || m.activeSection != SectionAzureResources || base.Active || !canDrillScope(base.Scope) {
		return m, nil
	}

	m.cancelScopePicker()
	m.err = nil
	m.scopePicker.assignment = assignment
	m.scopePicker.base = base
	m.scopePicker.stack = nil
	m.scopePicker.query = ""
	m.scopePicker.searchMode = false
	m.scopePicker.err = nil
	m.screen = ScreenScope
	return m.loadChildScopes(discoverer, base.Scope, true)
}

func (m Model) loadChildScopes(discoverer childScopeDiscoverer, parent pim.Scope, push bool) (tea.Model, tea.Cmd) {
	m.cancelScopePicker()
	ctx, cancel := context.WithCancel(m.tenantContext())
	m.scopePicker.cancel = cancel
	m.scopePicker.checkID++
	m.scopePicker.loading = true
	m.scopePicker.err = nil
	checkID := m.scopePicker.checkID
	tenantID := m.selectedTenant.ID
	m.beginOperation("scopes")
	m.recordActivity("Loading child scopes: "+displayScopeLabel(parent), false)
	cmd := func() tea.Msg {
		children, err := discoverer.DiscoverChildScopes(ctx, parent)
		return childScopesDiscoveredMsg{
			tenantID: tenantID,
			checkID:  checkID,
			parent:   parent,
			children: children,
			push:     push,
			err:      err,
		}
	}
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m Model) handleChildScopesDiscovered(msg childScopesDiscoveredMsg) (tea.Model, tea.Cmd) {
	if !m.scopePicker.loading || msg.checkID != m.scopePicker.checkID || msg.tenantID != m.selectedTenant.ID || m.screen != ScreenScope {
		return m, nil
	}
	m.cancelScopePicker()
	if msg.err != nil {
		m.recordActivity("Child scope discovery failed", true)
		if accountUnavailableError(msg.err) {
			m.markAccountUnavailable(msg.err)
			return m, nil
		}
		m.scopePicker.err = msg.err
		return m, nil
	}
	m.recordActivity(fmt.Sprintf("Child scopes ready: %d", len(msg.children)), false)
	level := scopeLevel{
		parent:   msg.parent,
		children: msg.children,
		cursor:   0,
	}
	if msg.push || len(m.scopePicker.stack) == 0 {
		m.scopePicker.stack = append(m.scopePicker.stack, level)
	} else {
		m.scopePicker.stack[len(m.scopePicker.stack)-1] = level
	}
	m.scopePicker.query = ""
	m.scopePicker.searchMode = false
	m.scopePicker.err = nil
	return m, nil
}

func (m Model) handleScopedAssignmentPrepared(msg scopedAssignmentPreparedMsg) (tea.Model, tea.Cmd) {
	if !m.scopePicker.preparing || msg.checkID != m.scopePicker.checkID || msg.tenantID != m.selectedTenant.ID || m.screen != ScreenScope {
		return m, nil
	}
	m.cancelScopePicker()
	if msg.err != nil {
		m.recordActivity("Scoped activation requirements failed", true)
		if accountUnavailableError(msg.err) {
			m.markAccountUnavailable(msg.err)
			return m, nil
		}
		m.scopePicker.err = msg.err
		return m, nil
	}
	m.applyScopedAssignment(msg.assignment)
	m.recordActivity("Scope selected: "+displayScope(msg.assignment), false)
	m.screen = ScreenAssignments
	m.err = nil
	return m, nil
}

func (m *Model) applyScopedAssignment(updated pim.EligibleAssignment) {
	for index := range m.assignmentList.items {
		if m.assignmentList.items[index].ID == updated.ID {
			m.assignmentList.items[index] = updated
			break
		}
	}
	key := discoveryKey{tenantID: m.selectedTenant.ID, section: m.activeSection}
	if entry, ok := m.discoveryCache[key]; ok {
		for index := range entry.assignments {
			if entry.assignments[index].ID == updated.ID {
				entry.assignments[index] = updated
				break
			}
		}
		m.discoveryCache[key] = entry
	}
	m.assignmentList.selectedIDs[updated.ID] = true
	delete(m.form.durations, updated.ID)
}

func (m Model) currentScopeOptions() []pim.Scope {
	if len(m.scopePicker.stack) == 0 {
		return nil
	}
	top := m.scopePicker.stack[len(m.scopePicker.stack)-1]
	options := make([]pim.Scope, 0, 1+len(top.children))
	options = append(options, top.parent)
	query := strings.ToLower(strings.TrimSpace(m.scopePicker.query))
	if query == "" {
		return append(options, top.children...)
	}
	for _, child := range top.children {
		haystack := strings.ToLower(child.DisplayName + " " + string(child.Type) + " " + child.ID)
		if strings.Contains(haystack, query) {
			options = append(options, child)
		}
	}
	return options
}

func (m *Model) clampScopeCursor() {
	if len(m.scopePicker.stack) == 0 {
		return
	}
	options := m.currentScopeOptions()
	top := &m.scopePicker.stack[len(m.scopePicker.stack)-1]
	if len(options) == 0 {
		top.cursor = 0
		return
	}
	if top.cursor < 0 {
		top.cursor = 0
	}
	if top.cursor >= len(options) {
		top.cursor = len(options) - 1
	}
}

func (m *Model) moveScopeCursor(delta int) {
	if len(m.scopePicker.stack) == 0 {
		return
	}
	m.scopePicker.stack[len(m.scopePicker.stack)-1].cursor += delta
	m.clampScopeCursor()
}

func (m Model) updateScope(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.scopePicker.loading || m.scopePicker.preparing {
		if key.Type == tea.KeyEsc {
			m.cancelScopePicker()
			if len(m.scopePicker.stack) == 0 {
				m.screen = ScreenAssignments
			}
		}
		return m, nil
	}

	if m.scopePicker.searchMode {
		switch key.Type {
		case tea.KeyEsc, tea.KeyEnter:
			m.scopePicker.searchMode = false
		case tea.KeyBackspace, tea.KeyCtrlH:
			m.scopePicker.query = trimLastRune(m.scopePicker.query)
			m.clampScopeCursor()
		case tea.KeyCtrlU:
			m.scopePicker.query = ""
			m.clampScopeCursor()
		case tea.KeyRunes, tea.KeySpace:
			m.scopePicker.query += key.String()
			m.clampScopeCursor()
		}
		return m, nil
	}

	switch key.Type {
	case tea.KeyEsc:
		if len(m.scopePicker.stack) > 1 {
			m.scopePicker.stack = m.scopePicker.stack[:len(m.scopePicker.stack)-1]
			m.scopePicker.query = m.scopePicker.stack[len(m.scopePicker.stack)-1].query
			m.scopePicker.err = nil
			return m, nil
		}
		m.cancelScopePicker()
		m.screen = ScreenAssignments
		m.err = nil
		return m, nil
	case tea.KeyLeft:
		if len(m.scopePicker.stack) > 1 {
			m.scopePicker.stack = m.scopePicker.stack[:len(m.scopePicker.stack)-1]
			m.scopePicker.query = m.scopePicker.stack[len(m.scopePicker.stack)-1].query
			m.scopePicker.err = nil
		}
		return m, nil
	case tea.KeyUp:
		m.moveScopeCursor(-1)
	case tea.KeyDown:
		m.moveScopeCursor(1)
	case tea.KeyRight:
		return m.drillFocusedScope()
	case tea.KeyEnter:
		return m.selectFocusedScope()
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "j":
			m.moveScopeCursor(1)
		case "k":
			m.moveScopeCursor(-1)
		case "l":
			return m.drillFocusedScope()
		case "h":
			if len(m.scopePicker.stack) > 1 {
				m.scopePicker.stack = m.scopePicker.stack[:len(m.scopePicker.stack)-1]
				m.scopePicker.query = m.scopePicker.stack[len(m.scopePicker.stack)-1].query
				m.scopePicker.err = nil
			}
		case "/":
			m.scopePicker.searchMode = true
			m.scopePicker.err = nil
		}
	}
	return m, nil
}

func (m Model) drillFocusedScope() (tea.Model, tea.Cmd) {
	options := m.currentScopeOptions()
	if len(m.scopePicker.stack) == 0 || len(options) == 0 {
		return m, nil
	}
	top := &m.scopePicker.stack[len(m.scopePicker.stack)-1]
	if top.cursor == 0 {
		return m, nil
	}
	chosen := options[top.cursor]
	if !canDrillScope(chosen) {
		return m, nil
	}
	discoverer, ok := m.providerForSection(m.activeSection).(childScopeDiscoverer)
	if !ok {
		return m, nil
	}
	top.query = m.scopePicker.query
	return m.loadChildScopes(discoverer, chosen, true)
}

func (m Model) selectFocusedScope() (tea.Model, tea.Cmd) {
	options := m.currentScopeOptions()
	if len(m.scopePicker.stack) == 0 || len(options) == 0 {
		return m, nil
	}
	cursor := m.scopePicker.stack[len(m.scopePicker.stack)-1].cursor
	chosen := options[cursor]
	base := m.scopePicker.base
	if strings.EqualFold(strings.TrimRight(chosen.ID, "/"), strings.TrimRight(base.AzureScope, "/")) {
		m.applyScopedAssignment(base)
		m.recordActivity("Scope reset: "+displayScope(base), false)
		m.screen = ScreenAssignments
		m.err = nil
		return m, nil
	}

	updated := base
	updated.AzureScope = chosen.ID
	updated.Scope = chosen
	preparer, ok := m.providerForSection(m.activeSection).(assignmentPreparer)
	if !ok {
		m.applyScopedAssignment(updated)
		m.recordActivity("Scope selected: "+displayScope(updated), false)
		m.screen = ScreenAssignments
		m.err = nil
		return m, nil
	}

	m.cancelScopePicker()
	ctx, cancel := context.WithCancel(m.tenantContext())
	m.scopePicker.cancel = cancel
	m.scopePicker.checkID++
	m.scopePicker.preparing = true
	m.scopePicker.err = nil
	checkID := m.scopePicker.checkID
	tenantID := m.selectedTenant.ID
	m.beginOperation("scopes")
	m.recordActivity("Loading activation requirements: "+displayScopeLabel(chosen), false)
	cmd := func() tea.Msg {
		prepared, err := preparer.Prepare(ctx, []pim.EligibleAssignment{updated})
		if err != nil {
			return scopedAssignmentPreparedMsg{tenantID: tenantID, checkID: checkID, err: err}
		}
		if len(prepared) > 0 {
			updated = prepared[0]
		}
		return scopedAssignmentPreparedMsg{tenantID: tenantID, checkID: checkID, assignment: updated}
	}
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func displayScopeLabel(scope pim.Scope) string {
	return displayScope(pim.EligibleAssignment{AzureScope: scope.ID, Scope: scope})
}

func (m Model) viewScope() string {
	var b strings.Builder
	b.WriteString(m.header("Target scope", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(2))
	b.WriteString("\n\n")

	roleLine := fmt.Sprintf("Role: %s  ·  Eligible at %s", displayName(m.scopePicker.base), displayScope(m.scopePicker.base))
	b.WriteString(violetStyle.Render(truncateText(roleLine, m.contentWidth())))
	b.WriteString("\n")

	if len(m.scopePicker.stack) > 0 {
		crumbs := make([]string, 0, len(m.scopePicker.stack))
		for _, level := range m.scopePicker.stack {
			crumbs = append(crumbs, displayScopeLabel(level.parent))
		}
		b.WriteString(mutedStyle.Render(truncateText("Path: "+strings.Join(crumbs, " > "), m.contentWidth())))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if m.scopePicker.loading {
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
			fmt.Sprintf("%s  Loading child scopes...", m.spinner.View()),
		))
		return b.String()
	}
	if m.scopePicker.preparing {
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
			fmt.Sprintf("%s  Loading activation requirements for selected scope...", m.spinner.View()),
		))
		return b.String()
	}
	if m.scopePicker.err != nil {
		b.WriteString(errorStyle.Width(m.contentWidth() - 4).Render("Could not load scope: " + m.scopePicker.err.Error()))
		b.WriteString("\n\n")
	}
	if len(m.scopePicker.stack) == 0 {
		return b.String()
	}

	searchLabel := "/  Filter child scopes by name or ID"
	if m.scopePicker.query != "" || m.scopePicker.searchMode {
		searchLabel = "/  " + m.scopePicker.query
	}
	if m.scopePicker.searchMode {
		searchLabel += "_"
		searchLabel = ansi.TruncateLeft(searchLabel, max(0, lipgloss.Width(searchLabel)-(m.contentWidth()-8)), "...")
	} else {
		searchLabel = truncateText(searchLabel, m.contentWidth()-8)
	}
	b.WriteString(fieldStyle.Width(m.contentWidth() - 4).Render(searchLabel))
	b.WriteString("\n\n")

	options := m.currentScopeOptions()
	cursor := m.scopePicker.stack[len(m.scopePicker.stack)-1].cursor
	visible := max(1, m.bodyHeight()-12)
	if m.scopePicker.err != nil {
		visible = max(1, visible-3)
	}
	visible = min(len(options), visible)
	start := cursor - visible + 1
	if start < 0 {
		start = 0
	}
	if start+visible > len(options) {
		start = len(options) - visible
	}

	for index := start; index < start+visible; index++ {
		option := options[index]
		marker := "  "
		if index == cursor {
			marker = "> "
		}
		prefix := ""
		suffix := ""
		if index == 0 {
			if len(m.scopePicker.stack) == 1 {
				prefix = "[Eligible] "
			} else {
				prefix = "[Current]  "
			}
		} else if canDrillScope(option) {
			suffix = "  ->"
		}
		label := fmt.Sprintf("%s%s%s%s", marker, prefix, displayScopeLabel(option), suffix)
		label = truncateText(label, m.contentWidth()-6)
		if index == cursor {
			label = activeCardStyle.Width(m.contentWidth() - 4).Render(label)
		}
		b.WriteString(label)
		b.WriteString("\n")
	}
	top := m.scopePicker.stack[len(m.scopePicker.stack)-1]
	if len(top.children) == 0 {
		b.WriteString(mutedStyle.Render("  No child scopes returned under this resource."))
		b.WriteString("\n")
	} else if len(options) == 1 && m.scopePicker.query != "" {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  No child scopes match %q.", m.scopePicker.query)))
		b.WriteString("\n")
	} else if start > 0 || start+visible < len(options) {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  Showing %d-%d of %d", start+1, start+visible, len(options))))
		b.WriteString("\n")
	}
	return b.String()
}
