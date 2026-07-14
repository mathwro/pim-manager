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

func (m Model) viewHome() string {
	var b strings.Builder
	b.WriteString(m.header("Access console", "Home"))
	b.WriteString("\n")
	b.WriteString(m.stepLine(0))
	b.WriteString("\n\n")
	b.WriteString(m.accountPanel())
	b.WriteString("\n\n")
	b.WriteString(violetStyle.Render("Choose where to request privileged access"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Assignments stay inside one Azure PIM area per activation batch."))
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
	if m.runtime.Account != nil && m.accountErr != nil {
		hints = append([]keyHint{{"r", "check sign-in"}}, hints...)
	}
	b.WriteString(m.footer(hints))
	return b.String()
}

func (m Model) viewAssignments() string {
	var b strings.Builder
	b.WriteString(m.header("Eligible assignments", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(1))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
			fmt.Sprintf("%s  Discovering %s...\n%s", m.spinner.View(), m.activeSection, mutedStyle.Render("Azure may take a moment to return every eligible assignment.")),
		))
		b.WriteString(m.footer([]keyHint{{"esc", "back"}, {"q", "quit"}}))
		return b.String()
	}
	if m.err != nil {
		b.WriteString(errorStyle.Width(m.contentWidth() - 4).Render("Could not continue\n" + m.err.Error()))
		b.WriteString("\n\n")
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
	selectedCount := len(m.assignmentList.selected())
	b.WriteString(fmt.Sprintf("%s  %s\n\n", accentStyle.Render(fmt.Sprintf("%d selected", selectedCount)), mutedStyle.Render(fmt.Sprintf("%d eligible", len(m.assignmentList.items)))))

	filtered := m.assignmentList.filtered(m.query)
	if len(filtered) == 0 {
		message := "No eligible assignments were returned."
		if m.query != "" {
			message = fmt.Sprintf("No assignments match %q.", m.query)
		}
		b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(message + "\n" + mutedStyle.Render("Clear the search or retry discovery.")))
	} else {
		start, end := m.assignmentWindow(len(filtered))
		b.WriteString(mutedStyle.Render(fmt.Sprintf("    ROLE%sSCOPE", strings.Repeat(" ", max(2, m.roleColumnWidth()-4)))))
		b.WriteString("\n")
		for index := start; index < end; index++ {
			assignment := filtered[index]
			cursor := "  "
			if index == m.listCursor {
				cursor = "> "
			}
			check := "[ ]"
			if m.assignmentList.selectedIDs[assignment.ID] {
				check = "[x]"
			}
			role := truncateText(displayName(assignment), m.roleColumnWidth())
			scope := truncateText(displayScope(assignment), m.scopeColumnWidth())
			row := fmt.Sprintf("%s%s %-*s  %s", cursor, check, m.roleColumnWidth(), role, scope)
			if index == m.listCursor {
				row = activeCardStyle.Width(m.contentWidth() - 4).Render(row)
			}
			b.WriteString(row)
			b.WriteString("\n")
		}
		if start > 0 || end < len(filtered) {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("    Showing %d-%d of %d", start+1, end, len(filtered))))
			b.WriteString("\n")
		}
	}

	if m.searchMode {
		b.WriteString(m.footer([]keyHint{{"type", "filter"}, {"enter", "apply"}, {"esc", "close search"}}))
		return b.String()
	}
	b.WriteString(m.footer([]keyHint{{"space", "select"}, {"a", "select all"}, {"/", "search"}, {"i", "details"}, {"enter", "continue"}, {"esc", "back"}}))
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
	rows := [][2]string{
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
	b.WriteString(m.footer([]keyHint{{"esc", "back to assignments"}, {"q", "quit"}}))
	return b.String()
}

func (m Model) viewActivation() string {
	var b strings.Builder
	b.WriteString(m.header("Activation request", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(2))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("One request will be applied to %s.\n\n", accentStyle.Render(fmt.Sprintf("%d selected assignments", len(m.assignmentList.selected())))))

	justificationStyle := fieldStyle
	durationStyle := fieldStyle
	if m.formField == formFieldJustification {
		justificationStyle = focusedFieldStyle
	} else {
		durationStyle = focusedFieldStyle
	}
	b.WriteString(violetStyle.Render("Justification"))
	b.WriteString("\n")
	b.WriteString(justificationStyle.Width(m.contentWidth() - 4).Render(m.justification.View()))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Describe the business reason Azure reviewers should see."))
	b.WriteString("\n\n")
	b.WriteString(violetStyle.Render("Duration"))
	b.WriteString("\n")
	b.WriteString(durationStyle.Width(min(30, m.contentWidth()-4)).Render(m.duration.View()))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("ISO 8601 duration, for example PT1H or PT4H."))
	if m.err != nil {
		b.WriteString("\n\n")
		b.WriteString(errorStyle.Render(m.err.Error()))
	}
	b.WriteString(m.footer([]keyHint{{"tab", "switch field"}, {"enter", "next / review"}, {"esc", "back"}}))
	return b.String()
}

func (m Model) viewConfirmation() string {
	var b strings.Builder
	b.WriteString(m.header("Review activation", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(3))
	b.WriteString("\n\n")
	selected := m.assignmentList.selected()
	b.WriteString(accentStyle.Render(fmt.Sprintf("%d assignments", len(selected))))
	b.WriteString("\n")
	limit := min(len(selected), 6)
	for index := range limit {
		b.WriteString(fmt.Sprintf("  - %s  %s\n", displayName(selected[index]), mutedStyle.Render(displayScope(selected[index]))))
	}
	if len(selected) > limit {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  ...and %d more\n", len(selected)-limit)))
	}
	b.WriteString("\n")
	b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
		fmt.Sprintf("%s\n%s\n\n%s  %s", violetStyle.Render("Justification"), strings.TrimSpace(m.form.justification), violetStyle.Render("Duration"), strings.TrimSpace(m.form.durationISO)),
	))
	b.WriteString("\n\n")
	b.WriteString(warningStyle.Render("Activation requests are submitted immediately and are never retried automatically."))
	b.WriteString(m.footer([]keyHint{{"enter", "activate"}, {"e", "edit request"}, {"esc", "back"}}))
	return b.String()
}

func (m Model) viewProgress() string {
	var b strings.Builder
	b.WriteString(m.header("Activating access", string(m.activeSection)))
	b.WriteString("\n")
	b.WriteString(m.stepLine(3))
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
	b.WriteString(m.stepLine(4))
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
	labels := []string{"Choose", "Select", "Request", "Review", "Result"}
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
	return strings.Join(parts, mutedStyle.Render("  --  "))
}

func (m Model) accountPanel() string {
	if m.runtime.Account == nil {
		return panelStyle.Width(m.contentWidth() - 6).Render(mutedStyle.Render("Azure CLI account validation is not configured."))
	}
	if m.checkingAccount {
		return panelStyle.Width(m.contentWidth() - 6).Render(fmt.Sprintf("%s  Checking Azure CLI sign-in...", m.spinner.View()))
	}
	if m.accountErr != nil {
		guidance := "Run az login, then press r to check again."
		if !errors.Is(m.accountErr, azureauth.ErrNotLoggedIn) {
			guidance = "Resolve the Azure CLI error, then press r to check again."
		}
		return errorStyle.Width(m.contentWidth() - 4).Render(fmt.Sprintf("Azure sign-in required\n%s\n%s", guidance, mutedStyle.Render(m.accountErr.Error())))
	}
	if !m.accountChecked {
		return panelStyle.Width(m.contentWidth() - 6).Render(mutedStyle.Render("Azure CLI account has not been checked yet."))
	}
	identity := successStyle.Render("CONNECTED") + "  " + valueOrUnknown(m.account.UserName)
	tenant := fmt.Sprintf("%-13s %s", "Tenant", valueOrUnknown(m.account.TenantID))
	subscription := fmt.Sprintf("%-13s %s", "Subscription", valueOrUnknown(m.account.SubscriptionID))
	context := mutedStyle.Render(tenant + "\n" + subscription)
	return panelStyle.Width(m.contentWidth() - 6).Render(identity + "\n" + context)
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
	return b.String()
}

func (m Model) frameWidth() int {
	return max(42, min(104, m.width-6))
}

func (m Model) contentWidth() int {
	return max(36, m.frameWidth()-4)
}

func (m Model) assignmentVisibleRows() int {
	return max(4, min(12, m.height-19))
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

func (m Model) roleColumnWidth() int {
	return max(14, (m.contentWidth()-12)*3/5)
}

func (m Model) scopeColumnWidth() int {
	return max(10, m.contentWidth()-m.roleColumnWidth()-10)
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
