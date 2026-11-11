package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

type graphConnectedMsg struct {
	tenantID string
	section  Section
	checkID  int
	err      error
}

func isGraphSection(section Section) bool {
	return section == SectionEntra || section == SectionGroups
}

func (m Model) needsGraphConnection(section Section) bool {
	return isGraphSection(section) && m.runtime.GraphConnect != nil && (m.runtime.GraphConnected == nil || !m.runtime.GraphConnected(m.tenantContext()))
}

func (m Model) updateGraphConnect(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyEsc {
		m.cancelGraphConnection(true)
		m.screen = ScreenHome
		m.err = nil
		return m, nil
	}
	if m.graphConnecting {
		return m, nil
	}
	if key.Type != tea.KeyEnter && key.String() != "r" {
		m.syncGraphViewport()
		var cmd tea.Cmd
		m.detailsViewport, cmd = m.detailsViewport.Update(key)
		return m, cmd
	}
	if m.accountUnavailable() {
		// The account warning tells the user to press r after az login; recheck before allowing sign-in.
		return m.refreshAccountAndWorkflow()
	}
	m.graphConnectCheck++
	ctx, cancel := context.WithCancel(m.tenantContext())
	m.graphCancel = cancel
	m.graphConnecting = true
	m.err = nil
	m.detailsViewport.GotoTop()
	m.beginOperation("graph")
	m.recordActivity("Starting Microsoft Graph browser sign-in", false)
	tenantID, section, checkID := m.selectedTenant.ID, m.activeSection, m.graphConnectCheck
	connect := m.runtime.GraphConnect
	return m, tea.Batch(func() tea.Msg {
		err := connect(ctx)
		return graphConnectedMsg{tenantID: tenantID, section: section, checkID: checkID, err: err}
	}, m.spinner.Tick)
}

func (m Model) graphConnected(msg graphConnectedMsg) (tea.Model, tea.Cmd) {
	if !m.graphConnecting || msg.checkID != m.graphConnectCheck || msg.tenantID != m.selectedTenant.ID || msg.section != m.activeSection {
		return m, nil
	}
	m.cancelGraphConnection(false)
	if msg.err != nil {
		m.recordActivity("Microsoft Graph sign-in failed", true)
		if accountUnavailableError(msg.err) {
			m.markAccountUnavailable(msg.err)
			return m, nil
		}
		m.detailsViewport.GotoTop()
		m.err = fmt.Errorf("Microsoft Graph sign-in failed: %w", msg.err)
		return m, nil
	}
	m.recordActivity("Microsoft Graph connected", false)
	return m.beginDiscovery(msg.section, true)
}

func (m *Model) cancelGraphConnection(reset bool) {
	if m.graphCancel != nil {
		m.graphCancel()
		m.graphCancel = nil
	}
	m.graphConnecting = false
	m.graphConnectCheck++
	m.endOperation("graph")
	if reset && m.runtime.ResetGraph != nil {
		m.runtime.ResetGraph()
	}
}

func (m *Model) cancelAuthentication() {
	if m.authenticationCancel != nil {
		m.authenticationCancel()
		m.authenticationCancel = nil
	}
}

func (m *Model) cancelActivation() {
	if m.activationCancel != nil {
		m.activationCancel()
		m.activationCancel = nil
	}
	m.activationContext = nil
}

func (m *Model) cancelPendingWork() {
	m.cancelTenantCheck()
	m.cancelUpdateCheck()
	m.cancelGraphConnection(true)
	m.cancelAuthentication()
	m.cancelActivation()
	m.cancelDiscovery(true)
	m.cancelAccountCheck()
}

func (m Model) graphConnectionHeading() string {
	return m.header("Microsoft Graph connection", string(m.activeSection)) + "\n" + m.stepLine(1) + "\n\n" + m.tenantPanel() + "\n"
}

func (m Model) viewGraphConnect() string {
	m.syncGraphViewport()
	return m.graphConnectionHeading() + m.detailsViewport.View()
}

func (m *Model) syncGraphViewport() {
	body := "Entra Roles and Groups use a separate browser sign-in.\n\n" +
		"Microsoft identifies this application as Microsoft Graph Command Line Tools.\n" +
		"Use the same account as your Azure CLI session.\n\n" +
		"The session is shared by both sections. Credentials stay in memory; no device code or new app registration is required."
	if m.height < 28 {
		body = "Sign in with your Azure CLI account.\n\nApp: Microsoft Graph Command Line Tools\nCredentials stay in memory."
	}
	if m.graphConnecting {
		body = m.spinner.View() + "  Complete Microsoft Graph sign-in in your browser.\n\nReturn here when finished. Press Esc to cancel."
	}
	content := panelStyle.Width(m.contentWidth() - 6).Render(body)
	if m.err != nil {
		content = errorStyle.Width(m.contentWidth() - 4).Render(m.err.Error())
	}
	m.detailsViewport.Width = m.contentWidth()
	m.detailsViewport.Height = max(1, m.bodyHeight()-lipgloss.Height(strings.TrimRight(m.graphConnectionHeading(), "\n"))-1)
	m.detailsViewport.SetContent(content)
}
