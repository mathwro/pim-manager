package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// withActivity keeps the screen's controls last. History yields space to the
// primary workflow; the frame never clips selections, warnings, or hints.
func (m Model) withActivity(content string) string {
	content = lipgloss.NewStyle().Width(m.contentWidth()).Render(strings.TrimRight(content, "\n"))
	hints := strings.Trim(m.footer(m.screenHints()), "\n")
	room := m.height - 4 - lipgloss.Height(content) - lipgloss.Height(hints) - 1
	rows := min(m.activityHeight(), max(1, room))
	bottom := hints
	if activity := m.activityView(rows); activity != "" {
		bottom = activity + "\n" + hints
	}
	gap := max(2, m.height-4-lipgloss.Height(content)-lipgloss.Height(bottom)+1)
	return content + strings.Repeat("\n", gap) + bottom
}

func (m Model) activityHeight() int {
	rows := len(m.activityEntries)
	if m.checkingTenants || m.checkingAccount || m.loading || m.preparingPolicies || m.checkingAuthentication || m.screen == ScreenProgress {
		rows++
	}
	if m.height < 28 {
		return min(1, rows)
	}
	return min(4, rows)
}

func (m Model) activityView(rows int) string {
	if rows <= 0 {
		return ""
	}
	lines := make([]string, 0, rows)
	operation := m.currentOperation()
	historyRows := rows
	if operation != "" {
		historyRows--
	}
	start := max(0, len(m.activityEntries)-historyRows)
	for _, entry := range m.activityEntries[start:] {
		line := truncateText(entry.at.Local().Format("15:04:05")+"  "+entry.message, m.contentWidth())
		style := mutedStyle
		if entry.failed {
			style = dangerStyle
		}
		lines = append(lines, style.Render(line))
	}
	if operation != "" {
		lines = append(lines, accentStyle.Render(truncateText(operation, m.contentWidth())))
	}
	return strings.Join(lines, "\n")
}

func (m Model) currentOperation() string {
	stages := []struct {
		key    string
		label  string
		active bool
	}{
		{"tenants", "Checking tenants", m.checkingTenants},
		{"account", "Account check", m.checkingAccount},
		{"discovery", "Discovery", m.loading && m.screen != ScreenProgress},
		{"policies", "Activation requirements", m.preparingPolicies},
		{"authentication", "Authenticating", m.checkingAuthentication},
		{"activation", "Submitting activation requests", m.screen == ScreenProgress},
	}
	var labels []string
	for _, stage := range stages {
		if !stage.active {
			continue
		}
		label := stage.label
		if stage.key == "activation" && m.activationTotal > 0 {
			label = fmt.Sprintf("Activating %d/%d", m.activationDone, m.activationTotal)
		}
		if started := m.operationStarted[stage.key]; !started.IsZero() {
			elapsed := max(time.Duration(0), time.Since(started))
			label += " " + elapsed.Truncate(time.Second).String()
		}
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return ""
	}
	return m.spinner.View() + " " + strings.Join(labels, " · ")
}

// bodyHeight reserves the dock and the actual wrapped contextual controls.
func (m Model) bodyHeight() int {
	hints := strings.Trim(m.footer(m.screenHints()), "\n")
	return max(1, m.height-4-lipgloss.Height(hints)-m.activityHeight()-1)
}

func (m Model) screenHints() []keyHint {
	if m.helpVisible {
		return []keyHint{{"? / esc / q", "close help"}}
	}
	switch m.screen {
	case ScreenTenants:
		if m.checkingTenants {
			return []keyHint{{"q", "quit"}}
		}
		if m.tenantErr != nil {
			return []keyHint{{"r", "retry"}, {"?", "help"}, {"q", "quit"}}
		}
		return []keyHint{{"up/down", "move"}, {"enter", "select"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"}}
	case ScreenHome:
		hints := []keyHint{{"up/down", "move"}, {"enter", "open"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"}}
		if len(m.tenants) > 1 {
			hints = append([]keyHint{{"esc", "change account"}}, hints...)
		}
		return hints
	case ScreenAssignments:
		if m.loading {
			return []keyHint{{"esc", "back"}, {"q", "quit"}}
		}
		if m.accountUnavailable() || (m.err != nil && len(m.assignmentList.items) == 0) {
			return []keyHint{{"r", "refresh"}, {"esc", "back"}, {"q", "quit"}}
		}
		if m.searchMode {
			return []keyHint{{"type", "filter"}, {"enter", "apply"}, {"esc", "close search"}}
		}
		return []keyHint{{"space", "select"}, {"a", "select all"}, {"/", "search"}, {"i", "details"}, {"enter", "continue"}, {"r", "refresh"}, {"esc", "back"}}
	case ScreenDetails:
		if !m.policiesReady && !m.preparingPolicies {
			return []keyHint{{"up/down", "scroll"}, {"r", "retry discovery"}, {"esc", "back to assignments"}, {"q", "quit"}}
		}
		return []keyHint{{"up/down", "scroll"}, {"esc", "back to assignments"}, {"q", "quit"}}
	case ScreenActivation:
		return []keyHint{{"tab", "switch field"}, {"enter", "next / review"}, {"esc", "back"}}
	case ScreenConfirmation:
		if m.checkingAuthentication {
			return []keyHint{{"esc", "cancel"}, {"ctrl+c", "quit"}}
		}
		return []keyHint{{"up/down", "review"}, {"enter", "activate"}, {"e", "edit request"}, {"esc", "back"}}
	case ScreenProgress:
		return []keyHint{{"ctrl+c", "quit"}}
	case ScreenSummary:
		if m.checkingAuthentication {
			return []keyHint{{"esc", "cancel"}, {"ctrl+c", "quit"}}
		}
		hints := []keyHint{{"up/down", "scroll"}, {"esc", "assignments"}, {"h", "home"}, {"q", "quit"}}
		if m.accountUnavailable() {
			hints = append([]keyHint{{"r", "refresh account"}}, hints...)
		} else if len(m.summary.retryableFailures()) > 0 {
			hints = append([]keyHint{{"r", "retry failures"}}, hints...)
		}
		return hints
	}
	return nil
}
