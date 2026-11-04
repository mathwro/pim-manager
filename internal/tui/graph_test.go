package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/pim"
	"github.com/mathwro/pim-manager/internal/providers/entra"
)

func driveGraphCommand(model Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return model
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			model = driveGraphCommand(model, child)
		}
		return model
	}
	if _, ok := msg.(spinner.TickMsg); ok {
		return model
	}
	next, follow := model.Update(msg)
	return driveGraphCommand(next.(Model), follow)
}

func TestGraphDiscoveryRequiresExplicitConnectionAndReusesSession(t *testing.T) {
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "entra-1"}}, {{ID: "group-1"}}}}
	connected, connections := false, 0
	model := NewModel(Runtime{Entra: provider, Groups: provider,
		GraphConnected: func(context.Context) bool { return connected },
		GraphConnect: func(ctx context.Context) error {
			if azureauth.TenantFromContext(ctx) != "tenant-1" {
				t.Fatal("connection lost selected tenant")
			}
			connections++
			connected = true
			return nil
		},
	})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	next, cmd := model.beginDiscovery(SectionEntra, false)
	model = next.(Model)
	if model.screen != ScreenGraphConnect || cmd != nil || provider.discoverCalls != 0 || connections != 0 {
		t.Fatalf("Graph discovery must first explain and request connection: screen=%s cmd=%v calls=%d", model.screen, cmd != nil, provider.discoverCalls)
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = driveGraphCommand(next.(Model), cmd)
	if model.screen != ScreenAssignments || len(model.assignmentList.items) != 1 || model.assignmentList.items[0].ID != "entra-1" {
		t.Fatalf("connection did not reach discovery: %#v", model.err)
	}
	next, cmd = model.beginDiscovery(SectionGroups, false)
	model = driveGraphCommand(next.(Model), cmd)
	if connections != 1 || model.assignmentList.items[0].ID != "group-1" {
		t.Fatal("second Graph section did not reuse connected session")
	}
}

func TestGraphConnectionFailureDoesNotDiscoverOrBlockARM(t *testing.T) {
	graph := &scriptedProvider{}
	arm := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "arm-1"}}}}
	model := NewModel(Runtime{Entra: graph, AzureResources: arm, GraphConnected: func(context.Context) bool { return false }, GraphConnect: func(context.Context) error { return errors.New("consent required") }})
	next, _ := model.beginDiscovery(SectionEntra, false)
	next, cmd := next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = driveGraphCommand(next.(Model), cmd)
	if model.screen != ScreenGraphConnect || model.err == nil || graph.discoverCalls != 0 {
		t.Fatal("failed connection reached Graph discovery")
	}
	next, cmd = model.beginDiscovery(SectionAzureResources, false)
	model = driveGraphCommand(next.(Model), cmd)
	if model.err != nil || model.assignmentList.items[0].ID != "arm-1" {
		t.Fatal("Graph error blocked ARM")
	}
}

// An expired Azure CLI login during Graph sign-in must use the same recovery path as ARM.
func TestGraphConnectionMarksSignedOutAccountUnavailable(t *testing.T) {
	model := NewModel(Runtime{Entra: &scriptedProvider{}, GraphConnected: func(context.Context) bool { return false }, GraphConnect: func(context.Context) error {
		return fmt.Errorf("check Azure CLI ARM identity: %w; run az login", azureauth.ErrAccountUnavailable)
	}})
	next, _ := model.beginDiscovery(SectionEntra, false)
	next, cmd := next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = driveGraphCommand(next.(Model), cmd)
	if !errors.Is(model.accountErr, azureauth.ErrAccountUnavailable) || !model.accountUnavailable() {
		t.Fatalf("signed-out CLI account not marked unavailable: %v", model.accountErr)
	}
}

func TestGraphConnectionCancelRejectsLateSuccess(t *testing.T) {
	var connectionContext context.Context
	resets := 0
	model := NewModel(Runtime{GraphConnected: func(context.Context) bool { return false }, GraphConnect: func(ctx context.Context) error { connectionContext = ctx; return nil }, ResetGraph: func() { resets++ }})
	next, _ := model.beginDiscovery(SectionEntra, false)
	next, cmd := next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = driveGraphCommand(next.(Model), cmd)
	if model.screen != ScreenHome || connectionContext == nil || connectionContext.Err() == nil || resets == 0 {
		t.Fatal("canceled Graph connection was not fenced and canceled")
	}
}

func TestGraphActivationAlwaysPreflightsAndCancellationSubmitsNothing(t *testing.T) {
	provider := &scriptedProvider{}
	var preflightContext context.Context
	calls := 0
	model := NewModel(Runtime{Entra: provider, GraphAuthentication: func(ctx context.Context, _ bool, _ string) (azureauth.ARMAuthentication, error) {
		calls++
		preflightContext = ctx
		return azureauth.ARMAuthentication{PrincipalID: "user-1", AccessToken: "checked-graph", Satisfied: true}, nil
	}})
	assignment := pim.EligibleAssignment{ID: "entra-1", PrincipalID: "user-1"}
	model.activeSection = SectionEntra
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{assignment})
	model.assignmentList.toggle(assignment.ID)
	model.form.durations[assignment.ID] = "PT1H"
	next, cmd := model.beginAuthentication([]pim.EligibleAssignment{assignment})
	model = next.(Model)
	if !model.checkingAuthentication {
		t.Fatal("Graph activation bypassed preflight without MFA policy")
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = driveGraphCommand(next.(Model), cmd)
	if calls != 1 || preflightContext.Err() == nil || len(provider.activated) != 0 || model.screen == ScreenProgress {
		t.Fatal("canceled Graph preflight submitted an activation")
	}
}

func TestGraphTenantChangeDropsSession(t *testing.T) {
	resets := 0
	model := NewModel(Runtime{ResetGraph: func() { resets++ }})
	model.selectedTenant = azureauth.Tenant{ID: "old"}
	model.tenants = []azureauth.Tenant{{ID: "new"}}
	model.selectTenant(0)
	if resets != 1 {
		t.Fatal("tenant change retained Graph session")
	}
}

func TestGraphConnectionFitsSupportedTerminalSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{96, 30}, {80, 24}, {48, 24}} {
		for _, connecting := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/connecting=%v", size.width, size.height, connecting), func(t *testing.T) {
				model := NewModel(Runtime{})
				model.screen = ScreenGraphConnect
				model.activeSection = SectionEntra
				model.graphConnecting = connecting
				next, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				assertActivityFrameFits(t, next.(Model).View(), size.width, size.height)
			})
		}
	}
}

type graphRoundTripper func(*http.Request) (*http.Response, error)

func (f graphRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type unavailableGraphTokenSource struct{}

func (unavailableGraphTokenSource) AccessToken(context.Context, string) (string, error) {
	return "", errors.New("submission fetched an unchecked token")
}

func TestGraphBatchPinsCheckedTokenTargetsUserAndRevalidatesRetry(t *testing.T) {
	var tokens []string
	httpClient := &http.Client{Transport: graphRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != "POST" || req.URL.Path != "/v1.0/roleManagement/directory/roleAssignmentScheduleRequests" {
			t.Fatalf("unexpected Graph submission: %s %s", req.Method, req.URL)
		}
		var body struct {
			PrincipalID string `json:"principalId"`
			Action      string `json:"action"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.PrincipalID != "signed-in-user" || body.Action != "selfActivate" {
			t.Fatalf("incorrect self-activation target: %#v", body)
		}
		tokens = append(tokens, req.Header.Get("Authorization"))
		status, payload := 503, `{"error":{"code":"ServiceUnavailable","message":"retry manually"}}`
		if len(tokens) > 1 {
			status, payload = 201, `{"status":"Provisioned"}`
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}
	checks := 0
	model := NewModel(Runtime{Entra: entra.NewProvider(graph.NewClient(httpClient, unavailableGraphTokenSource{})), GraphAuthentication: func(ctx context.Context, _ bool, _ string) (azureauth.ARMAuthentication, error) {
		checks++
		if azureauth.TenantFromContext(ctx) != "tenant-1" {
			t.Fatal("preflight lost tenant")
		}
		return azureauth.ARMAuthentication{PrincipalID: "signed-in-user", AccessToken: fmt.Sprintf("checked-%d", checks), Satisfied: true}, nil
	}})
	assignment := pim.EligibleAssignment{ID: "eligible", PrincipalID: "eligible-group", RoleDefinitionID: "role", DirectoryScopeID: "/"}
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionEntra
	model.screen = ScreenConfirmation
	model.form.durations[assignment.ID] = "PT1H"
	next, cmd := model.beginAuthentication([]pim.EligibleAssignment{assignment})
	model = driveGraphCommand(next.(Model), cmd)
	if model.screen != ScreenSummary || len(model.summary.retryableFailures()) != 1 || len(tokens) != 1 {
		t.Fatalf("expected isolated retryable failure: screen=%s summary=%#v", model.screen, model.summary)
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = driveGraphCommand(next.(Model), cmd)
	if checks != 2 || len(tokens) != 2 || tokens[0] != "Bearer checked-1" || tokens[1] != "Bearer checked-2" || len(model.summary.activated) != 1 {
		t.Fatalf("retry did not revalidate and pin fresh Graph token: checks=%d tokens=%v summary=%#v", checks, tokens, model.summary)
	}
}

func TestGraphUnusablePreflightSubmitsNothing(t *testing.T) {
	for _, tc := range []struct {
		name           string
		authentication azureauth.ARMAuthentication
		err            error
	}{
		{name: "login failed", err: errors.New("browser sign-in canceled")},
		{name: "claims missing", authentication: azureauth.ARMAuthentication{AccessToken: "token", PrincipalID: "user"}},
		{name: "token missing", authentication: azureauth.ARMAuthentication{PrincipalID: "user", Satisfied: true}},
		{name: "principal missing", authentication: azureauth.ARMAuthentication{AccessToken: "token", Satisfied: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedProvider{}
			model := NewModel(Runtime{Groups: provider, GraphAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
				return tc.authentication, tc.err
			}})
			model.activeSection = SectionGroups
			model.screen = ScreenConfirmation
			next, cmd := model.beginAuthentication([]pim.EligibleAssignment{{ID: "group"}})
			model = driveGraphCommand(next.(Model), cmd)
			if model.err == nil || len(provider.activated) != 0 || model.screen == ScreenProgress {
				t.Fatal("unusable Graph preflight submitted an activation")
			}
		})
	}
}

func TestGraphConnectionErrorRemainsScrollableOnSmallTerminal(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenGraphConnect
	model.activeSection = SectionGroups
	model.err = errors.New(strings.Repeat("Conditional Access diagnostic ", 40) + "diagnostic-end")
	next, _ := model.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
	model = next.(Model)
	assertActivityFrameFits(t, model.View(), 48, 24)
	for range 100 {
		next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = next.(Model)
	}
	if !strings.Contains(model.View(), "diagnostic-end") {
		t.Fatal("connection diagnostic cannot be read to the end")
	}
}

func TestQueuedGraphActivationStopsOnQuitOrTenantReset(t *testing.T) {
	for _, quit := range []bool{false, true} {
		t.Run(fmt.Sprintf("quit=%v", quit), func(t *testing.T) {
			provider := &scriptedProvider{}
			model := NewModel(Runtime{Entra: provider})
			model.activeSection = SectionEntra
			model.selectedTenant = azureauth.Tenant{ID: "tenant-old"}
			assignment := pim.EligibleAssignment{ID: "role"}
			model.form.durations[assignment.ID] = "PT1H"
			next, cmd := model.startActivation([]pim.EligibleAssignment{assignment}, azureauth.ARMAuthentication{AccessToken: "checked", PrincipalID: "user", Satisfied: true})
			model = next.(Model)
			batch := cmd().(tea.BatchMsg)
			started := batch[0]()
			next, submit := model.Update(started)
			model = next.(Model)
			if submit == nil {
				t.Fatal("activation command was not queued")
			}
			if quit {
				_, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			} else {
				model.tenants = []azureauth.Tenant{{ID: "tenant-new"}}
				model.selectTenant(0)
			}
			_ = submit()
			if len(provider.activated) != 0 {
				t.Fatal("queued activation submitted after cancellation")
			}
		})
	}
}
