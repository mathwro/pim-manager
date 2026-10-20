package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
)

func activityCommandMessage(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected pending workflow command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if child == nil {
				continue
			}
			msg := activityCommandMessage(t, child)
			if _, tick := msg.(spinner.TickMsg); !tick {
				return msg
			}
		}
		t.Fatal("expected workflow message, got spinner ticks only")
	}
	return msg
}

func advanceActivityCommand(t *testing.T, model Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	next, pending := model.Update(activityCommandMessage(t, cmd))
	return next.(Model), pending
}

func TestActivationPublishesEachOutcomeBeforeStartingNextAssignment(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusPendingApproval},
	}}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-one"}
	model.activeSection = SectionAzureResources
	selected := []pim.EligibleAssignment{{ID: "one", DisplayName: "Reader"}, {ID: "two", DisplayName: "Owner"}}
	model.form.durations = map[string]string{"one": "PT1H", "two": "PT2H"}
	next, cmd := model.startActivation(selected, azureauth.ARMAuthentication{AccessToken: "pinned-token", PrincipalID: "user"})
	model = next.(Model)
	model, cmd = advanceActivityCommand(t, model, cmd)
	if model.activationCurrent != "Reader" || len(provider.activated) != 0 {
		t.Fatalf("first assignment not visible before submission: current=%q requests=%d", model.activationCurrent, len(provider.activated))
	}
	model, cmd = advanceActivityCommand(t, model, cmd)
	if model.screen != ScreenProgress || model.activationDone != 1 || model.activationTotal != 2 || len(model.summary.activated) != 1 || len(provider.activated) != 1 {
		t.Fatalf("first result not visible during remaining work: done=%d total=%d summary=%#v requests=%d", model.activationDone, model.activationTotal, model.summary, len(provider.activated))
	}
	model, cmd = advanceActivityCommand(t, model, cmd)
	if model.activationCurrent != "Owner" || len(model.summary.activated) != 1 || len(provider.activated) != 1 {
		t.Fatalf("next assignment erased completed result: current=%q summary=%#v requests=%d", model.activationCurrent, model.summary, len(provider.activated))
	}
	for model.screen != ScreenSummary {
		model, cmd = advanceActivityCommand(t, model, cmd)
	}
	if len(model.summary.activated) != 1 || len(model.summary.pendingApproval) != 1 || model.activationDone != 2 {
		t.Fatalf("final summary lost outcomes: %#v", model.summary)
	}
	if len(provider.tokens) != 2 || provider.tokens[0] != "pinned-token" || provider.tokens[1] != "pinned-token" {
		t.Fatalf("batch did not preserve checked token: %#v", provider.tokens)
	}
}

func TestActivationAccountFailureDoesNotEraseLiveOutcomesOrStopBatch(t *testing.T) {
	provider := &scriptedProvider{
		activateErr: []error{azureauth.ErrAccountUnavailable, nil},
		results:     []pim.ActivationResult{{Status: pim.ActivationStatusActivated}},
	}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-one"}
	model.activeSection = SectionAzureResources
	next, cmd := model.startActivation([]pim.EligibleAssignment{{ID: "one", DisplayName: "Reader"}, {ID: "two", DisplayName: "Owner"}}, azureauth.ARMAuthentication{})
	model = next.(Model)
	model, cmd = advanceActivityCommand(t, model, cmd)
	model, cmd = advanceActivityCommand(t, model, cmd)
	if len(model.summary.failed) != 1 || model.screen != ScreenProgress || model.accountUnavailable() {
		t.Fatalf("early account error interrupted or erased progress: screen=%s summary=%#v unavailable=%v", model.screen, model.summary, model.accountUnavailable())
	}
	for model.screen != ScreenSummary {
		model, cmd = advanceActivityCommand(t, model, cmd)
	}
	if len(provider.activated) != 2 || len(model.summary.failed) != 1 || len(model.summary.activated) != 1 || !model.accountUnavailable() {
		t.Fatalf("account failure lost final outcomes: requests=%d summary=%#v unavailable=%v", len(provider.activated), model.summary, model.accountUnavailable())
	}
}

func TestActivationMessagesCannotCrossTenantOrWorkflowGeneration(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	old := NewModel(Runtime{AzureResources: provider})
	old.selectedTenant = azureauth.Tenant{ID: "old-tenant"}
	old.activeSection = SectionAzureResources
	next, started := old.startActivation([]pim.EligibleAssignment{{ID: "old", DisplayName: "Old role"}}, azureauth.ARMAuthentication{})
	old = next.(Model)
	startedMsg := activityCommandMessage(t, started)
	next, resultCmd := old.Update(startedMsg)
	old = next.(Model)
	resultMsg := resultCmd()
	next, completedCmd := old.Update(resultMsg)
	old = next.(Model)
	completedMsg := completedCmd()
	for _, reset := range []string{"tenant", "generation"} {
		t.Run(reset, func(t *testing.T) {
			for _, stale := range []tea.Msg{startedMsg, resultMsg, completedMsg} {
				model := NewModel(Runtime{AzureResources: provider})
				model.selectedTenant = old.selectedTenant
				model.activeSection = SectionAzureResources
				model.activationGeneration = old.activationGeneration
				if reset == "tenant" {
					model.selectedTenant = azureauth.Tenant{ID: "new-tenant"}
				}
				next, _ := model.startActivation([]pim.EligibleAssignment{{ID: "new", DisplayName: "New role"}}, azureauth.ARMAuthentication{})
				model = next.(Model)
				if reset == "tenant" {
					model.activationGeneration = old.activationGeneration
				}
				switch stale.(type) {
				case activationItemResultMsg:
					model.activationCurrent = "New role"
				case activationCompletedMsg:
					model.activationDone = 1
				}
				entries, done, current := len(model.activityEntries), model.activationDone, model.activationCurrent
				next, pending := model.Update(stale)
				model = next.(Model)
				if pending != nil || len(model.activityEntries) != entries || len(model.summary.results) != 0 || model.activationDone != done || model.activationCurrent != current || model.screen != ScreenProgress {
					t.Fatalf("stale %T changed current workflow: activity=%#v summary=%#v done=%d current=%q screen=%s", stale, model.activityEntries, model.summary, model.activationDone, model.activationCurrent, model.screen)
				}
			}
		})
	}
}

func TestManualRetryProgressUsesOnlyRetryableSubset(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	model := NewModel(Runtime{AzureResources: provider})
	model.activeSection = SectionAzureResources
	model.screen = ScreenSummary
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "ok"}, {ID: "retry"}, {ID: "permanent"}})
	for _, item := range model.assignmentList.items {
		model.assignmentList.selectedIDs[item.ID] = true
	}
	model.summary = newSummary([]pim.ActivationResult{
		{Assignment: pim.EligibleAssignment{ID: "ok"}, Status: pim.ActivationStatusActivated},
		{Assignment: pim.EligibleAssignment{ID: "retry", DisplayName: "Retry role"}, Status: pim.ActivationStatusFailed, Retryable: true},
		{Assignment: pim.EligibleAssignment{ID: "permanent"}, Status: pim.ActivationStatusFailed},
	})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = next.(Model)
	if model.activationTotal != 1 || model.activationDone != 0 {
		t.Fatalf("retry retained original selection totals: %d/%d", model.activationDone, model.activationTotal)
	}
	for model.screen != ScreenSummary {
		model, cmd = advanceActivityCommand(t, model, cmd)
	}
	if len(provider.activated) != 1 || provider.activated[0].Assignment.ID != "retry" || len(model.summary.activated) != 1 {
		t.Fatalf("retry submitted more than retryable subset: %#v summary=%#v", provider.activated, model.summary)
	}
}

func TestActivityBufferIsBoundedAndTenantLocal(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "old"}
	model.tenants = []azureauth.Tenant{{ID: "old"}, {ID: "new"}}
	for range 1000 {
		model.recordActivity("Assignments ready", false)
	}
	if len(model.activityEntries) == 0 || len(model.activityEntries) >= 1000 {
		t.Fatalf("activity is empty or unbounded: %d", len(model.activityEntries))
	}
	model.recordActivity("Activation failed", true)
	last := model.activityEntries[len(model.activityEntries)-1]
	if !last.failed || last.at.IsZero() {
		t.Fatalf("latest outcome not retained: %#v", last)
	}
	model.beginOperation("discovery")
	model.selectTenant(1)
	if len(model.activityEntries) != 0 || len(model.operationStarted) != 0 {
		t.Fatalf("old tenant activity survived selection: entries=%#v operations=%#v", model.activityEntries, model.operationStarted)
	}
}

func TestActivityNeverRecordsRawErrorTokenClaimsOrJustification(t *testing.T) {
	secrets := []string{"access-token-secret", "claims-secret", "justification-secret", "https://sensitive.example/"}
	provider := &scriptedProvider{activateErr: []error{activation.NewRetryableError(errors.New(strings.Join(secrets, " ")))}}
	model := NewModel(Runtime{AzureResources: provider})
	model.activeSection = SectionAzureResources
	model.form.justification = secrets[2]
	next, cmd := model.startActivation([]pim.EligibleAssignment{{ID: "one", DisplayName: "Reader\x1b[31m\n\t\x1b]8;;https://evil.example\a role\x1b]8;;\a"}}, azureauth.ARMAuthentication{AccessToken: secrets[0]})
	model = next.(Model)
	for model.screen != ScreenSummary {
		model, cmd = advanceActivityCommand(t, model, cmd)
		if strings.ContainsAny(model.activationCurrent, "\x1b\n\t\a") {
			t.Fatalf("current assignment contains terminal controls: %q", model.activationCurrent)
		}
	}
	for _, entry := range model.activityEntries {
		for _, secret := range secrets {
			if strings.Contains(entry.message, secret) {
				t.Fatalf("activity leaked secret %q: %q", secret, entry.message)
			}
		}
		if strings.ContainsAny(entry.message, "\x1b\n\t\a") || strings.Contains(entry.message, "https://evil.example") {
			t.Fatalf("activity retained terminal escape payload: %q", entry.message)
		}
	}
}

func TestOverlappingOperationClocksDoNotRestartExistingWork(t *testing.T) {
	model := NewModel(Runtime{})
	old := time.Now().Add(-time.Minute)
	model.operationStarted = map[string]time.Time{"discovery": old}
	model.beginOperation("account")
	model.beginOperation("discovery")
	if !model.operationStarted["discovery"].Equal(old) || model.operationStarted["account"].IsZero() {
		t.Fatalf("overlapping operation changed existing start: %#v", model.operationStarted)
	}
	model.endOperation("account")
	if _, ok := model.operationStarted["account"]; ok || !model.operationStarted["discovery"].Equal(old) {
		t.Fatalf("finishing account ended discovery: %#v", model.operationStarted)
	}
}

func TestAuthenticationPreflightKeepsSpinnerAdvancing(t *testing.T) {
	model := NewModel(Runtime{ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
		return azureauth.ARMAuthentication{}, nil
	}})
	next, _ := model.startAuthenticationCheck(nil, false, "", false, ScreenConfirmation, "")
	model = next.(Model)
	_, cmd := model.Update(model.spinner.Tick())
	if cmd == nil {
		t.Fatal("preflight stopped the active spinner")
	}
}

func TestActivityBoundsUntrustedDisplayLabelSize(t *testing.T) {
	model := NewModel(Runtime{})
	model.recordActivity("Activating "+strings.Repeat("role", 10000), false)
	if len(model.activityEntries) != 1 || len(model.activityEntries[0].message) > 1000 {
		t.Fatalf("activity retained an unbounded display label: entries=%d bytes=%d", len(model.activityEntries), len(model.activityEntries[0].message))
	}
}

func TestStaleBackgroundResultsCannotAppendActivity(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "current"}
	model.discoveryCheck = 2
	model.accountCheck = 2
	model.authenticationCheck = 2
	model.checkingAccount = true
	model.checkingAuthentication = true
	model.recordActivity("Current workflow", false)
	for _, stale := range []tea.Msg{
		assignmentsDiscoveredMsg{tenantID: "current", checkID: 1, err: errors.New("secret discovery error")},
		accountCheckedMsg{tenantID: "current", checkID: 1, err: errors.New("secret account error")},
		authenticationCheckedMsg{checkID: 1, err: errors.New("secret authentication error")},
	} {
		next, cmd := model.Update(stale)
		model = next.(Model)
		if cmd != nil || len(model.activityEntries) != 1 || model.activityEntries[0].failed {
			t.Fatalf("stale %T appended feedback: %#v", stale, model.activityEntries)
		}
	}
}

func TestDiscoveryAndPolicyClocksFollowActualStages(t *testing.T) {
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	next, cmd := model.beginDiscovery(SectionAzureResources, false)
	model = next.(Model)
	if model.operationStarted["discovery"].IsZero() || !model.operationStarted["policies"].IsZero() {
		t.Fatalf("policy clock started before discovery finished: %#v", model.operationStarted)
	}
	model, cmd = advanceActivityCommand(t, model, cmd)
	if model.operationStarted["policies"].IsZero() || !model.operationStarted["discovery"].IsZero() || len(model.assignmentList.items) != 1 {
		t.Fatalf("list-ready did not transfer to policy stage: operations=%#v assignments=%#v", model.operationStarted, model.assignmentList.items)
	}
	provider.prepared <- []pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}}
	model, _ = advanceActivityCommand(t, model, cmd)
	if !model.policiesReady || len(model.operationStarted) != 0 || len(model.activityEntries) < 4 {
		t.Fatalf("policy completion retained clock or omitted lifecycle feedback: operations=%#v activity=%#v", model.operationStarted, model.activityEntries)
	}
}

func TestStaleStepUpCompletionCannotChangeResetWorkflow(t *testing.T) {
	for _, reset := range []string{"tenant", "generation"} {
		t.Run(reset, func(t *testing.T) {
			model := NewModel(Runtime{StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
				return exec.Command("true"), nil
			}})
			model.selectedTenant = azureauth.Tenant{ID: "old-tenant"}
			model.screen = ScreenConfirmation
			next, _ := model.startStepUp(nil, true, "", ScreenConfirmation, "principal")
			model = next.(Model)
			stale := stepUpCompletedMsg{tenantID: "old-tenant", checkID: model.authenticationCheck, origin: ScreenConfirmation, err: errors.New("secret stale authentication error")}
			if reset == "tenant" {
				model.selectedTenant = azureauth.Tenant{ID: "new-tenant"}
			} else {
				model.clearTenantWorkflow()
			}
			model.screen = ScreenHome
			model.activityEntries = nil
			next, cmd := model.Update(stale)
			model = next.(Model)
			if cmd != nil || model.screen != ScreenHome || model.err != nil || len(model.activityEntries) != 0 {
				t.Fatalf("stale step-up changed %s workflow: screen=%s error=%v activity=%#v", reset, model.screen, model.err, model.activityEntries)
			}
		})
	}
}
