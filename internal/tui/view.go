package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

type keyHint struct {
	key   string
	label string
}

func (m Model) viewTenants() string {
	var b strings.Builder
	title := "Azure CLI sign-in"
	if !m.checkingTenants && m.tenantErr == nil && len(m.tenants) > 1 {
		title = "Choose Azure tenant"
	}
	b.WriteString(m.header(title, "Account"))
	b.WriteString("\n")
	b.WriteString(m.stepLine(0))
	b.WriteString("\n\n")
	if m.checkingTenants {
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(fmt.Sprintf("%s  Checking Azure CLI tenants...", m.spinner.View())))
		b.WriteString(m.footer([]keyHint{{"q", "quit"}}))
		return b.String()
	}
	if m.tenantErr != nil {
		guidance := "Resolve the Azure CLI error, then press r to retry."
		if errors.Is(m.tenantErr, azureauth.ErrNotLoggedIn) {
			guidance = "Run az login, then press r to retry."
		}
		b.WriteString(errorStyle.Width(m.contentWidth() - 4).Render("Azure sign-in required\n" + guidance + "\n" + mutedStyle.Render(m.tenantErr.Error())))
		b.WriteString(m.footer([]keyHint{{"r", "retry"}, {"?", "help"}, {"q", "quit"}}))
		return b.String()
	}
	start, end := m.tenantWindow(len(m.tenants))
	for index := start; index < end; index++ {
		tenant := m.tenants[index]
		marker := "  "
		style := cardStyle
		if index == m.tenantIndex {
			marker = "> "
			style = activeCardStyle
		}
		label := truncateText(tenantLabel(tenant), m.contentWidth()-8)
		body := fmt.Sprintf("%s%s\n  %s", marker, label, mutedStyle.Render(tenant.ID))
		b.WriteString(style.Width(m.contentWidth() - 4).Render(body))
		b.WriteString("\n")
	}
	if start > 0 || end < len(m.tenants) {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  Showing %d-%d of %d\n", start+1, end, len(m.tenants))))
	}
	b.WriteString(m.footer([]keyHint{{"up/down", "move"}, {"enter", "select"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"}}))
	return b.String()
}

func tenantLabel(tenant azureauth.Tenant) string {
	if tenant.DisplayName != "" && tenant.DefaultDomain != "" {
		return fmt.Sprintf("%s (%s)", tenant.DisplayName, tenant.DefaultDomain)
	}
	if tenant.DisplayName != "" {
		return tenant.DisplayName
	}
	if tenant.DefaultDomain != "" {
		return tenant.DefaultDomain
	}
	return tenant.ID
}

func (m Model) viewHome() string {
	var b strings.Builder
	b.WriteString(m.header("Access console", "Home"))
	b.WriteString("\n")
	b.WriteString(m.stepLine(1))
	b.WriteString("\n\n")
	b.WriteString(m.tenantPanel())
	b.WriteString("\n\n")
	b.WriteString(violetStyle.Render("Choose where to request privileged access"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Assignments stay inside one Azure PIM area per activation batch."))
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("Entra Roles and Groups are paused until Azure CLI can request the required\nMicrosoft Graph PIM permissions."))
	b.WriteString("\n\n")

	descriptions := map[Section]string{
		SectionEntra:          "Directory roles across your Microsoft Entra tenant",
		SectionAzureResources: "RBAC roles for subscriptions, groups, and resources",
		SectionGroups:         "Privileged Access Group member and owner access",
	}
	for index, section := range m.sections {
		marker := "  "
		style := cardStyle
		if index == m.sectionIndex {
			marker = "> "
			style = activeCardStyle
		}
		body := fmt.Sprintf("%s%s\n  %s", marker, section, mutedStyle.Render(descriptions[section]))
		b.WriteString(style.Width(m.contentWidth() - 4).Render(body))
		b.WriteString("\n")
	}

	hints := []keyHint{{"up/down", "move"}, {"enter", "open"}, {"?", "help"}, {"q", "quit"}}
	if len(m.tenants) > 1 {
		hints = append([]keyHint{{"esc", "change account"}}, hints...)
	}
	b.WriteString(m.footer(hints))
	return b.String()
}

func (m Model) viewAssignments() string {
	var b strings.Builder
	b.WriteString(m.header("Eligible assignments", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(2))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
			fmt.Sprintf("%s  Discovering %s...\n%s", m.spinner.View(), m.activeSection, mutedStyle.Render("Azure may take a moment to return every eligible assignment.")),
		))
		b.WriteString(m.footer([]keyHint{{"esc", "back"}, {"q", "quit"}}))
		return b.String()
	}
	if assignmentError := m.assignmentError(); assignmentError != "" {
		b.WriteString(assignmentError)
		b.WriteString("\n")
	}
	if m.preparingPolicies {
		loading := m.policyLoadingBanner()
		if m.waitingForPolicies {
			b.WriteString(warningStyle.Render(loading))
		} else {
			b.WriteString(mutedStyle.Render(loading))
		}
	}

	searchLabel := "/  Search roles, scopes, and assignment types"
	if m.query != "" || m.searchMode {
		searchLabel = "/  " + m.query
	}
	if m.searchMode {
		searchLabel += "_"
	}
	b.WriteString(fieldStyle.Width(m.contentWidth() - 4).Render(searchLabel))
	b.WriteString("\n")
	selectedCount := 0
	selectableCount := 0
	activeCount := 0
	for _, assignment := range m.assignmentList.items {
		switch {
		case assignment.Active:
			activeCount++
		case m.assignmentList.selectedIDs[assignment.ID]:
			selectedCount++
		default:
			selectableCount++
		}
	}
	b.WriteString(fmt.Sprintf("%s  %s  %s\n\n",
		accentStyle.Render(fmt.Sprintf("%d selected", selectedCount)),
		mutedStyle.Render(fmt.Sprintf("%d selectable", selectableCount)),
		successStyle.Render(fmt.Sprintf("%d active", activeCount)),
	))

	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 {
		message := "No eligible assignments were returned."
		if m.query != "" {
			message = fmt.Sprintf("No assignments match %q.", m.query)
		}
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(message + "\n" + mutedStyle.Render("Clear the search or retry discovery.")))
	} else {
		start, end := m.assignmentWindow(len(filtered))
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %-9s%-*s  %s", "STATE", m.roleColumnWidth(), "ROLE", "SCOPE")))
		b.WriteString("\n")
		for index := start; index < end; index++ {
			assignment := filtered[index]
			cursor := "  "
			if index == m.listCursor {
				cursor = "> "
			}
			state := "[ ]"
			if assignment.Active {
				state = "[ACTIVE]"
			} else if m.assignmentList.selectedIDs[assignment.ID] {
				state = "[✓]"
			}
			role := truncateText(displayName(assignment), m.roleColumnWidth())
			scope := truncateText(displayScope(assignment), m.scopeColumnWidth())
			row := fmt.Sprintf("%s%-9s%-*s  %s", cursor, state, m.roleColumnWidth(), role, scope)
			if index == m.listCursor {
				row = activeCardStyle.Width(m.contentWidth() - 4).Render(row)
			} else if assignment.Active {
				row = successStyle.Render(row)
			}
			b.WriteString(row)
			b.WriteString("\n")
		}
		if start > 0 || end < len(filtered) {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("    Showing %d-%d of %d", start+1, end, len(filtered))))
			b.WriteString("\n")
		}
	}

	b.WriteString(m.assignmentFooter())
	return b.String()
}

func (m Model) viewDetails() string {
	var b strings.Builder
	b.WriteString(m.header("Assignment details", string(m.activeSection)))
	b.WriteString("\n\n")
	assignment, ok := m.focusedAssignment()
	if !ok {
		b.WriteString(errorStyle.Render("The focused assignment is no longer available."))
		b.WriteString(m.footer([]keyHint{{"esc", "back"}}))
		return b.String()
	}
	b.WriteString(accentStyle.Render(displayName(assignment)))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render(displayScope(assignment)))
	b.WriteString("\n\n")
	status := "Available"
	activeUntil := ""
	if assignment.Active {
		status = "Active"
		if assignment.ActiveUntil != nil {
			activeUntil = assignment.ActiveUntil.Local().Format("2006-01-02 15:04 MST")
		}
	}
	justification := "Optional"
	if assignment.ActivationPolicy.JustificationRequired {
		justification = "Required"
	}
	authentication := "Not required"
	if assignment.ActivationPolicy.AuthenticationContext != "" {
		authentication = "Authentication context " + assignment.ActivationPolicy.AuthenticationContext
	} else if assignment.ActivationPolicy.MFARequired {
		authentication = "MFA"
	}
	maximumDuration := assignment.ActivationPolicy.MaximumDurationISO
	switch {
	case m.preparingPolicies:
		maximumDuration = "Loading..."
		justification = "Loading..."
		authentication = "Loading..."
	case !m.policiesReady:
		maximumDuration = "Unavailable"
		justification = "Unavailable"
		authentication = "Unavailable"
	}
	rows := [][2]string{
		{"Status", status},
		{"Active until", activeUntil},
		{"Maximum duration", maximumDuration},
		{"Justification", justification},
		{"Authentication", authentication},
		{"Source", string(assignment.Source)},
		{"Assignment type", string(assignment.Kind)},
		{"Scope type", string(assignment.Scope.Type)},
		{"Assignment ID", assignment.ID},
		{"Role definition", assignment.RoleDefinitionID},
		{"Eligibility schedule", assignment.EligibilityScheduleID},
		{"Principal", assignment.PrincipalID},
		{"Condition", assignment.Condition},
	}
	for _, row := range rows {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("%-22s %s\n", mutedStyle.Render(row[0]), row[1]))
	}
	if !m.policiesReady && !m.preparingPolicies {
		if assignmentError := m.assignmentError(); assignmentError != "" {
			b.WriteString("\n")
			b.WriteString(assignmentError)
		}
		b.WriteString("\n")
		b.WriteString(warningStyle.Render("Activation requirements are unavailable; press r to retry discovery."))
		b.WriteString("\n")
	}
	b.WriteString(m.footer([]keyHint{{"esc", "back to assignments"}, {"q", "quit"}}))
	return b.String()
}

func (m Model) viewActivation() string {
	var b strings.Builder
	b.WriteString(m.header("Activation request", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(3))
	b.WriteString("\n\n")
	selected := m.assignmentList.selected()
	b.WriteString(fmt.Sprintf("One request will be applied to %s.\n\n", accentStyle.Render(fmt.Sprintf("%d selected assignments", len(selected)))))

	required := m.form.requiredJustifications(selected)
	justificationLabel := "Justification — optional"
	justificationHelp := "Optional for all selected assignments."
	if required > 0 {
		justificationLabel = "Justification — REQUIRED"
		justificationHelp = fmt.Sprintf("Required by %d of %d selected assignments.", required, len(selected))
	}
	justificationStyle := fieldStyle
	if m.formField == formFieldJustification {
		justificationStyle = focusedFieldStyle
	}
	b.WriteString(violetStyle.Render(justificationLabel))
	b.WriteString("\n")
	b.WriteString(justificationStyle.Width(m.contentWidth() - 4).Render(m.justification.View()))
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(errorStyle.Render(m.err.Error()))
	} else {
		b.WriteString(mutedStyle.Render(justificationHelp))
	}
	b.WriteString("\n\n")
	b.WriteString(violetStyle.Render("Durations"))
	b.WriteString("\n")
	start, end := m.activationDurationWindow(len(selected))
	for index := start; index < end; index++ {
		assignment := selected[index]
		marker := "  "
		value := m.form.durations[assignment.ID]
		if m.formField == formFieldDuration && index == m.durationIndex {
			marker = "> "
			value = m.duration.View()
		}
		nameWidth := max(12, m.roleColumnWidth()-4)
		name := truncateText(displayName(assignment), nameWidth)
		b.WriteString(fmt.Sprintf("%s%-*s  %-10s  %s\n", marker, nameWidth, name, value, mutedStyle.Render("max "+assignment.ActivationPolicy.MaximumDurationISO)))
	}
	if start > 0 || end < len(selected) {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  Showing %d-%d of %d durations", start+1, end, len(selected))))
		b.WriteString("\n")
	}
	b.WriteString(m.footer([]keyHint{{"tab", "switch field"}, {"enter", "next / review"}, {"esc", "back"}}))
	return b.String()
}

func (m Model) viewConfirmation() string {
	var b strings.Builder
	b.WriteString(m.header("Review activation", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(4))
	b.WriteString("\n\n")
	selected := m.assignmentList.selected()
	b.WriteString(accentStyle.Render(fmt.Sprintf("%d assignments", len(selected))))
	b.WriteString("\n")
	start, end := m.activationDurationWindow(len(selected))
	for index := start; index < end; index++ {
		marker := "  "
		if index == m.durationIndex {
			marker = "> "
		}
		b.WriteString(fmt.Sprintf("%s%s  %s  %s\n", marker, displayName(selected[index]), accentStyle.Render(m.form.durations[selected[index].ID]), mutedStyle.Render(displayScope(selected[index]))))
	}
	if start > 0 || end < len(selected) {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  Showing %d-%d of %d assignments", start+1, end, len(selected))))
		b.WriteString("\n")
	}
	justification := strings.TrimSpace(m.form.justification)
	if justification == "" {
		justification = "(none)"
	}
	b.WriteString("\n")
	b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
		fmt.Sprintf("%s\n%s", violetStyle.Render("Justification"), justification),
	))
	b.WriteString("\n\n")
	authenticationContext, mfaRequired, authenticationErr := authenticationRequirement(selected)
	switch {
	case authenticationErr != nil:
		b.WriteString(warningStyle.Render(authenticationErr.Error()))
		b.WriteString("\n")
	case authenticationContext != "":
		b.WriteString(warningStyle.Render("Authentication context " + authenticationContext + " is required before activation. Azure CLI will prompt after confirmation."))
		b.WriteString("\n")
	case mfaRequired:
		b.WriteString(warningStyle.Render("MFA is required. Azure PIM will validate the current Azure CLI session during activation."))
		b.WriteString("\n")
	}
	b.WriteString(warningStyle.Render("Activation requests are submitted immediately and are never retried automatically."))
	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render(m.err.Error()))
	}
	b.WriteString(m.footer([]keyHint{{"up/down", "review"}, {"enter", "activate"}, {"e", "edit request"}, {"esc", "back"}}))
	return b.String()
}

func (m Model) viewProgress() string {
	var b strings.Builder
	b.WriteString(m.header("Activating access", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(5))
	b.WriteString("\n\n")
	b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
		fmt.Sprintf("%s  Submitting %d activation requests\n%s", m.spinner.View(), len(m.assignmentList.selected()), mutedStyle.Render("The batch continues if an individual assignment fails.")),
	))
	b.WriteString(m.footer([]keyHint{{"ctrl+c", "quit"}}))
	return b.String()
}

func (m Model) viewSummary() string {
	var b strings.Builder
	b.WriteString(m.header("Activation summary", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(5))
	b.WriteString("\n\n")
	counts := lipgloss.JoinHorizontal(lipgloss.Top,
		panelStyle.Render(successStyle.Render(fmt.Sprintf("%d activated", len(m.summary.activated)))),
		"  ",
		panelStyle.Render(warningStyle.Render(fmt.Sprintf("%d pending", len(m.summary.pendingApproval)))),
		"  ",
		panelStyle.Render(dangerStyle.Render(fmt.Sprintf("%d failed", len(m.summary.failed)))),
	)
	b.WriteString(counts)
	b.WriteString("\n\n")
	b.WriteString(m.summaryViewport.View())
	hints := []keyHint{{"up/down", "scroll"}, {"esc", "assignments"}, {"h", "home"}, {"q", "quit"}}
	if len(m.summary.retryableFailures()) > 0 {
		hints = append([]keyHint{{"r", "retry failures"}}, hints...)
	}
	b.WriteString(m.footer(hints))
	return b.String()
}

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.header("Keyboard guide", "Help"))
	b.WriteString("\n\n")
	sections := []struct {
		title string
		keys  []keyHint
	}{
		{"Navigate", []keyHint{{"up/down or j/k", "move"}, {"enter", "open or continue"}, {"esc", "go back"}}},
		{"Assignments", []keyHint{{"space", "toggle selection"}, {"/", "search"}, {"a", "select visible"}, {"i", "inspect"}}},
		{"Everywhere", []keyHint{{"?", "open this guide"}, {"q", "quit"}, {"ctrl+c", "quit immediately"}}},
	}
	for _, section := range sections {
		b.WriteString(violetStyle.Render(section.title))
		b.WriteString("\n")
		for _, hint := range section.keys {
			b.WriteString(fmt.Sprintf("  %-20s %s\n", keyStyle.Render(hint.key), hint.label))
		}
		b.WriteString("\n")
	}
	b.WriteString(mutedStyle.Render("Press ?, Esc, or q to close help."))
	return b.String()
}

func (m Model) header(title, route string) string {
	return fmt.Sprintf("%s\n%s  %s\n%s", eyebrowStyle.Render("PIM / ACCESS CONSOLE"), titleStyle.Render(title), routeStyle.Render("/ "+route), mutedStyle.Render(strings.Repeat("-", max(10, m.contentWidth()))))
}

func (m Model) stepLine(active int) string {
	labels := []string{"Account", "Type", "Select", "Request", "Review", "Result"}
	parts := make([]string, 0, len(labels))
	for index, label := range labels {
		marker := "o"
		style := mutedStyle
		if index < active {
			marker = "x"
			style = successStyle
		} else if index == active {
			marker = ">"
			style = accentStyle
		}
		parts = append(parts, style.Render(marker+" "+label))
	}
	return strings.Join(parts, mutedStyle.Render("  "))
}

func (m Model) tenantPanel() string {
	if m.selectedTenant.ID == "" {
		return panelStyle.Width(m.contentWidth() - 6).Render(mutedStyle.Render("No Azure tenant selected."))
	}
	identity := successStyle.Render("CONNECTED") + "  " + tenantLabel(m.selectedTenant)
	id := mutedStyle.Render(fmt.Sprintf("%-13s %s", "Tenant", m.selectedTenant.ID))
	return panelStyle.Width(m.contentWidth() - 6).Render(identity + "\n" + id)
}

func (m *Model) resizeComponents() {
	inputWidth := max(20, m.contentWidth()-8)
	m.justification.SetWidth(inputWidth)
	m.duration.Width = min(28, inputWidth)
	m.summaryViewport.Width = max(20, m.contentWidth()-2)
	m.summaryViewport.Height = max(4, m.height-18)
	if len(m.summary.results) > 0 {
		m.refreshSummaryViewport()
	}
}

func (m *Model) refreshSummaryViewport() {
	m.summaryViewport.SetContent(m.summaryContent())
	m.summaryViewport.GotoTop()
}

func (m Model) summaryContent() string {
	if len(m.summary.results) == 0 {
		return mutedStyle.Render("No activation results were returned.")
	}
	var b strings.Builder
	for _, result := range m.summary.results {
		name := displayName(result.Assignment)
		message := strings.TrimSpace(result.Message)
		status := string(result.Status)
		statusText := status
		switch {
		case result.Success():
			statusText = successStyle.Render(status)
		case result.PendingApproval():
			statusText = warningStyle.Render(status)
		case result.Failure():
			statusText = dangerStyle.Render(status)
		}
		fmt.Fprintf(&b, "- %s: %s", name, statusText)
		if message != "" {
			fmt.Fprintf(&b, " (%s)", message)
		}
		if result.CanRetry() {
			b.WriteString("  " + warningStyle.Render("retry available"))
		}
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(m.summaryViewport.Width).Render(b.String())
}

func (m Model) frameWidth() int {
	return max(42, m.width-6)
}

func (m Model) contentWidth() int {
	return max(36, m.frameWidth()-4)
}

func (m Model) tenantWindow(total int) (int, int) {
	visible := min(total, max(1, (m.height-13)/2))
	start := m.tenantIndex - visible + 1
	if start < 0 {
		start = 0
	}
	if start+visible > total {
		start = total - visible
	}
	return start, start + visible
}

func (m Model) policyLoadingBanner() string {
	return fmt.Sprintf("%s  Loading activation requirements...\n\n", m.spinner.View())
}

func (m Model) assignmentVisibleRows() int {
	footerExtraRows := max(0, lipgloss.Height(m.assignmentFooter())-2)
	errorRows := 0
	if assignmentError := m.assignmentError(); assignmentError != "" {
		errorRows = lipgloss.Height(assignmentError)
	}
	visibleRows := max(4, m.height-19-footerExtraRows-errorRows)
	if m.preparingPolicies || m.waitingForPolicies {
		visibleRows = max(1, visibleRows-lipgloss.Height(m.policyLoadingBanner()))
	}
	return visibleRows
}

func (m Model) assignmentWindow(total int) (int, int) {
	visible := min(total, m.assignmentVisibleRows())
	start := m.listCursor - visible + 1
	if start < 0 {
		start = 0
	}
	if start+visible > total {
		start = total - visible
	}
	return start, start + visible
}

func (m Model) activationDurationVisibleRows() int {
	return max(2, m.height-24)
}

func (m Model) activationDurationWindow(total int) (int, int) {
	visible := min(total, m.activationDurationVisibleRows())
	start := m.durationIndex - visible + 1
	if start < 0 {
		start = 0
	}
	if start+visible > total {
		start = total - visible
	}
	return start, start + visible
}

func (m Model) roleColumnWidth() int {
	return max(14, (m.contentWidth()-12)*2/5)
}

func (m Model) scopeColumnWidth() int {
	return max(10, m.contentWidth()-m.roleColumnWidth()-19)
}

func (m Model) assignmentFooter() string {
	if m.searchMode {
		return m.footer([]keyHint{{"type", "filter"}, {"enter", "apply"}, {"esc", "close search"}})
	}
	return m.footer([]keyHint{{"space", "select"}, {"a", "select all"}, {"/", "search"}, {"i", "details"}, {"enter", "continue"}, {"esc", "back"}})
}

func (m Model) assignmentError() string {
	if m.err == nil {
		return ""
	}
	return errorStyle.Width(m.contentWidth() - 4).Render("Could not continue: " + m.err.Error())
}

func (m Model) footer(hints []keyHint) string {
	lines := make([]string, 0, 2)
	current := ""
	for _, hint := range hints {
		chunk := keyStyle.Render(hint.key) + " " + hint.label
		candidate := chunk
		if current != "" {
			candidate = current + "   " + chunk
		}
		if current != "" && lipgloss.Width(candidate) > m.contentWidth() {
			lines = append(lines, current)
			current = chunk
			continue
		}
		current = candidate
	}
	if current != "" {
		lines = append(lines, current)
	}
	return footerStyle.Render(strings.Join(lines, "\n"))
}

func displayName(assignment pim.EligibleAssignment) string {
	if value := strings.TrimSpace(assignment.DisplayName); value != "" {
		return value
	}
	if value := strings.TrimSpace(assignment.ID); value != "" {
		return value
	}
	return "Unnamed assignment"
}

func displayScope(assignment pim.EligibleAssignment) string {
	name := strings.TrimSpace(assignment.Scope.DisplayName)
	scopeType := strings.TrimSpace(string(assignment.Scope.Type))
	switch assignment.Scope.Type {
	case pim.ScopeTypeManagementGroup:
		scopeType = "MG"
	case pim.ScopeTypeSubscription:
		scopeType = "Sub"
	case pim.ScopeTypeResourceGroup:
		scopeType = "RG"
	}
	switch {
	case name != "" && scopeType != "":
		return fmt.Sprintf("%s: %s", scopeType, name)
	case name != "":
		return name
	case scopeType != "":
		return scopeType
	case strings.TrimSpace(assignment.AzureScope) != "":
		return assignment.AzureScope
	default:
		return "Scope unavailable"
	}
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func truncateText(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}
