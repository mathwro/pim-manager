package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
)

type accountCheckedMsg struct {
	health      azureauth.AccountHealth
	tenantID    string
	principalID string
	checkID     int
	err         error
}

func (m *Model) cancelAccountCheck() {
	if m.accountCancel != nil {
		m.accountCancel()
		m.accountCancel = nil
	}
	m.accountCheck++
	m.checkingAccount = false
	m.endOperation("account")
}

func (m Model) accountFresh() bool {
	return (m.accountErr == nil || m.accountUnavailable()) && !m.accountCheckedAt.IsZero() && time.Since(m.accountCheckedAt) < 5*time.Minute
}

func (m *Model) startAccountCheck(force bool) tea.Cmd {
	if m.runtime.CheckAccount == nil || m.selectedTenant.ID == "" {
		return nil
	}
	if force {
		m.cancelAccountCheck()
	}
	if m.checkingAccount || (!force && m.accountFresh()) {
		return nil
	}
	m.accountCheck++
	m.checkingAccount = true
	m.beginOperation("account")
	m.recordActivity("Checking account", false)
	ctx, cancel := context.WithCancel(m.tenantContext())
	m.accountCancel = cancel
	check := m.runtime.CheckAccount
	tenantID, principalID, checkID := m.selectedTenant.ID, m.accountPrincipalID, m.accountCheck
	return tea.Batch(func() tea.Msg {
		health, err := check(ctx)
		return accountCheckedMsg{health: health, tenantID: tenantID, principalID: principalID, checkID: checkID, err: err}
	}, m.spinner.Tick)
}

func (m Model) accountChecked(msg accountCheckedMsg) (tea.Model, tea.Cmd) {
	if !m.checkingAccount || msg.checkID != m.accountCheck || msg.tenantID != m.selectedTenant.ID || !strings.EqualFold(msg.principalID, m.accountPrincipalID) {
		return m, nil
	}
	m.cancelAccountCheck()
	m.accountCheckedAt = time.Now()
	m.accountErr = msg.err
	if accountUnavailableError(msg.err) {
		m.markAccountUnavailable(msg.err)
		return m, nil
	}
	if msg.err != nil {
		m.recordActivity("Account check failed", true)
		return m, nil
	}
	if !strings.EqualFold(msg.health.TenantID, m.selectedTenant.ID) || strings.TrimSpace(msg.health.PrincipalID) == "" {
		m.accountErr = errors.New("account identity could not be verified for the selected tenant")
		m.recordActivity("Account identity could not be verified", true)
		return m, nil
	}
	if m.accountPrincipalID != "" && !strings.EqualFold(m.accountPrincipalID, msg.health.PrincipalID) {
		previous := m.accountPrincipalID
		m.clearAccountWorkflow()
		m.err = principalChangedError(previous, msg.health.PrincipalID)
		m.recordActivity("Account identity changed; assignments cleared", true)
	}
	m.accountPrincipalID = msg.health.PrincipalID
	m.accountHealth = msg.health
	m.recordActivity("Account check completed", false)
	return m, nil
}

func (m Model) accountUnavailable() bool {
	return accountUnavailableError(m.accountErr)
}

func accountUnavailableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, azureauth.ErrAccountUnavailable) || errors.Is(err, azureauth.ErrNotLoggedIn) {
		return true
	}
	var response arm.ResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusUnauthorized {
		return false
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(response.Body), &payload) != nil {
		return false
	}
	return strings.EqualFold(payload.Error.Code, "InvalidAuthenticationToken") || strings.EqualFold(payload.Error.Code, "ExpiredAuthenticationToken")
}

func (m *Model) markAccountUnavailable(err error) {
	m.cancelAccountCheck()
	m.accountErr = err
	m.accountCheckedAt = time.Now()
	m.accountPrincipalID = ""
	m.clearAccountWorkflow()
	m.recordActivity("Account unavailable; sign in to continue", true)
}

func (m *Model) clearAccountWorkflow() {
	section, screen := m.activeSection, m.screen
	if section != "" {
		m.accountRecoverySection = section
	}
	for key := range m.discoveryCache {
		if key.tenantID == m.selectedTenant.ID {
			delete(m.discoveryCache, key)
		}
	}
	m.clearTenantWorkflow()
	m.activeSection = section
	if screen != ScreenHome && screen != ScreenTenants {
		m.screen = ScreenAssignments
	}
}

func principalChangedError(previous, current string) error {
	return fmt.Errorf("Azure CLI principal changed from %s to %s; cached assignments were cleared, press r to reload before activating", previous, current)
}

func (m Model) refreshAccountAndWorkflow() (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	m.recordActivity("Refreshing account and assignments", false)
	if cmd := m.startAccountCheck(true); cmd != nil {
		commands = append(commands, cmd)
	}
	if m.runtime.Tenants != nil {
		m.tenantCheck++
		m.checkingTenants = true
		m.refreshingTenants = true
		m.tenantErr = nil
		m.endOperation("tenants")
		m.beginOperation("tenants")
		m.recordActivity("Refreshing tenants", false)
		commands = append(commands, m.checkTenants(m.tenantCheck), m.spinner.Tick)
	}
	section := m.activeSection
	if m.screen == ScreenHome && m.accountRecoverySection == "" {
		section = ""
	}
	if m.accountRecoverySection != "" {
		section = m.accountRecoverySection
	}
	if section != "" {
		next, cmd := m.beginDiscovery(section, true)
		m = next.(Model)
		m.accountRecoverySection = ""
		if len(commands) == 0 {
			return m, cmd
		}
		commands = append(commands, cmd)
	}
	return m, tea.Batch(commands...)
}
