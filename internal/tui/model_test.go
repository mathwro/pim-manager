package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mathwro/pim-manager/internal/activation"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/pim"
	"github.com/muesli/termenv"
)

func sendRunes(model Model, text string) Model {
	for _, r := range text {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg = tea.KeyMsg{Type: tea.KeySpace}
		}
		next, _ := model.Update(msg)
		model = next.(Model)
	}
	return model
}

func runCommand(model Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return model
	}
	msg := cmd()
	switch typed := msg.(type) {
	case tea.BatchMsg:
		for _, child := range typed {
			model = runCommand(model, child)
		}
	case assignmentsDiscoveredMsg, assignmentsPreparedMsg, activationCompletedMsg, tenantsCheckedMsg:
		next, _ := model.Update(msg)
		model = next.(Model)
	}
	return model
}

type tenantProviderFunc func(context.Context) ([]azureauth.Tenant, error)

func (f tenantProviderFunc) Tenants(ctx context.Context) ([]azureauth.Tenant, error) {
	return f(ctx)
}

type fakeAssignmentProvider struct{}

func (fakeAssignmentProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
	return nil, nil
}

func (fakeAssignmentProvider) Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error) {
	return pim.ActivationResult{}, nil
}

func TestRuntimeAcceptsAssignmentProviders(t *testing.T) {
	var provider AssignmentProvider = fakeAssignmentProvider{}
	runtime := Runtime{
		Entra:          provider,
		AzureResources: provider,
		Groups:         provider,
	}

	model := NewModel(runtime)

	if model.runtime.Entra == nil {
		t.Fatal("expected Entra provider to be stored")
	}
	if model.runtime.AzureResources == nil {
		t.Fatal("expected Azure Resources provider to be stored")
	}
	if model.runtime.Groups == nil {
		t.Fatal("expected Groups provider to be stored")
	}
}

func TestHomeEnterOpensAzureResources(t *testing.T) {
	model := NewModel(Runtime{})

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)

	if got.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", got.screen)
	}
	if got.activeSection != SectionAzureResources {
		t.Fatalf("expected Azure Resources section, got %s", got.activeSection)
	}
}

type scriptedProvider struct {
	discoveries       [][]pim.EligibleAssignment
	discoverCalls     int
	results           []pim.ActivationResult
	activateErr       []error
	discoverErr       error
	activated         []pim.ActivationRequest
	tokens            []string
	discoveryTenants  []string
	activationTenants []string
}

func (p *scriptedProvider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	p.discoveryTenants = append(p.discoveryTenants, azureauth.TenantFromContext(ctx))
	p.discoverCalls++
	if p.discoverErr != nil {
		return nil, p.discoverErr
	}
	if len(p.discoveries) == 0 {
		return nil, nil
	}
	out := p.discoveries[0]
	p.discoveries = p.discoveries[1:]
	return out, nil
}

func (p *scriptedProvider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	p.tokens = append(p.tokens, arm.PinnedAccessToken(ctx))
	p.activationTenants = append(p.activationTenants, azureauth.TenantFromContext(ctx))
	p.activated = append(p.activated, request)
	if len(p.activateErr) > 0 {
		err := p.activateErr[0]
		p.activateErr = p.activateErr[1:]
		if err != nil {
			return pim.ActivationResult{}, err
		}
	}
	if len(p.results) == 0 {
		return pim.ActivationResult{}, nil
	}
	result := p.results[0]
	p.results = p.results[1:]
	result.Assignment = request.Assignment
	return result, nil
}

type progressiveProvider struct {
	*scriptedProvider
	prepared     chan []pim.EligibleAssignment
	prepareErr   error
	prepareCalls int
}

func (p *progressiveProvider) Prepare(context.Context, []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	p.prepareCalls++
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	return <-p.prepared, nil
}

func startProgressiveDiscovery(t *testing.T, model Model) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected discovery batch, got %T", cmd())
	}
	for _, child := range batch {
		msg := child()
		if _, ok := msg.(assignmentsDiscoveredMsg); !ok {
			continue
		}
		next, prepare := model.Update(msg)
		return next.(Model), prepare
	}
	t.Fatal("discovery batch did not contain a discovery command")
	return model, nil
}

type cancellableProgressiveProvider struct {
	*scriptedProvider
	prepareStarted  chan struct{}
	prepareCanceled chan struct{}
}

func (p *cancellableProgressiveProvider) Prepare(ctx context.Context, _ []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	close(p.prepareStarted)
	<-ctx.Done()
	close(p.prepareCanceled)
	return nil, ctx.Err()
}

func preparationCommand(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("expected preparation batch, got %T", cmd())
	}
	return batch[0]
}

type cancellableDiscoveryProvider struct {
	*scriptedProvider
	discoveryStarted  chan struct{}
	discoveryCanceled chan struct{}
}

func (p *cancellableDiscoveryProvider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	close(p.discoveryStarted)
	<-ctx.Done()
	close(p.discoveryCanceled)
	return nil, ctx.Err()
}

func TestRefreshAndTenantSwitchCancelDiscovery(t *testing.T) {
	for _, test := range []struct {
		name string
		act  func(Model)
	}{
		{
			name: "refresh",
			act: func(model Model) {
				model.screen = ScreenAssignments
				_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			},
		},
		{
			name: "tenant switch",
			act: func(model Model) {
				model.tenants = []azureauth.Tenant{{ID: "tenant-1"}, {ID: "tenant-2"}}
				model.tenantIndex = 1
				model.screen = ScreenTenants
				_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &cancellableDiscoveryProvider{
				scriptedProvider: &scriptedProvider{},
				discoveryStarted: make(chan struct{}), discoveryCanceled: make(chan struct{}),
			}
			model := NewModel(Runtime{AzureResources: provider})
			model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
			next, batch := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(Model)
			discovery := preparationCommand(t, batch)
			go discovery()
			<-provider.discoveryStarted

			test.act(model)
			select {
			case <-provider.discoveryCanceled:
			case <-time.After(time.Second):
				t.Fatal("superseded assignment discovery was not canceled")
			}
		})
	}
}

type contextRecordingProvider struct {
	*scriptedProvider
	contexts chan context.Context
}

func (p *contextRecordingProvider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	p.contexts <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestReplacingDiscoveryDoesNotOrphanEarlierRequest(t *testing.T) {
	provider := &contextRecordingProvider{scriptedProvider: &scriptedProvider{}, contexts: make(chan context.Context, 2)}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	go preparationCommand(t, cmd)()
	first := <-provider.contexts
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	go preparationCommand(t, cmd)()
	second := <-provider.contexts

	model.tenants = []azureauth.Tenant{{ID: "tenant-1"}, {ID: "tenant-2"}}
	model.tenantIndex = 1
	model.screen = ScreenTenants
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for index, ctx := range []context.Context{first, second} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatalf("discovery request %d was orphaned", index+1)
		}
	}
}

func TestRefreshAndTenantSwitchCancelPolicyPreparation(t *testing.T) {
	for _, test := range []struct {
		name string
		act  func(Model) (Model, tea.Cmd)
	}{
		{
			name: "refresh",
			act: func(model Model) (Model, tea.Cmd) {
				next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
				return next.(Model), cmd
			},
		},
		{
			name: "tenant switch",
			act: func(model Model) (Model, tea.Cmd) {
				model.tenants = []azureauth.Tenant{{ID: "tenant-1"}, {ID: "tenant-2"}}
				model.tenantIndex = 1
				model.screen = ScreenTenants
				next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
				return next.(Model), cmd
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &cancellableProgressiveProvider{
				scriptedProvider: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "old"}}, {{ID: "new"}}}},
				prepareStarted:   make(chan struct{}),
				prepareCanceled:  make(chan struct{}),
			}
			model := NewModel(Runtime{AzureResources: provider})
			model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
			model, prepareBatch := startProgressiveDiscovery(t, model)
			prepare := preparationCommand(t, prepareBatch)
			result := make(chan tea.Msg, 1)
			go func() { result <- prepare() }()
			<-provider.prepareStarted

			model, _ = test.act(model)
			select {
			case <-provider.prepareCanceled:
			case <-time.After(time.Second):
				t.Fatal("superseded policy preparation was not canceled")
			}
			msg := <-result
			next, _ := model.Update(msg)
			model = next.(Model)
			if len(model.assignmentList.items) > 0 && model.assignmentList.items[0].ID == "old" {
				t.Fatalf("canceled preparation mutated current list: %#v", model.assignmentList.items)
			}
		})
	}
}

func progressiveTestModel(assignments ...pim.EligibleAssignment) (Model, *progressiveProvider) {
	provider := &progressiveProvider{
		scriptedProvider: &scriptedProvider{discoveries: [][]pim.EligibleAssignment{assignments}},
		prepared:         make(chan []pim.EligibleAssignment, 1),
	}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	return model, provider
}

func TestAssignmentsDisplayBeforePoliciesFinish(t *testing.T) {
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	model, prepare := startProgressiveDiscovery(t, model)

	view := model.View()
	if !strings.Contains(view, "Reader") || !strings.Contains(view, "Loading activation requirements") {
		t.Fatalf("expected list-ready assignment and policy loading state, got %q", view)
	}
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if details := next.(Model).View(); !strings.Contains(details, "Maximum duration") || !strings.Contains(details, "Loading...") {
		t.Fatalf("expected policy fields to remain loading in details, got %q", details)
	}

	provider.prepared <- []pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H"}}}
	model = runCommand(model, prepare)
	if !model.policiesReady || model.assignmentList.items[0].ActivationPolicy.MaximumDurationISO != "PT4H" {
		t.Fatalf("expected prepared policy, ready=%v assignments=%#v", model.policiesReady, model.assignmentList.items)
	}
}

func TestEnterWaitsForPoliciesThenOpensActivationForm(t *testing.T) {
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	model, prepare := startProgressiveDiscovery(t, model)
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenAssignments || !model.waitingForPolicies || !strings.Contains(model.View(), "Loading activation requirements") {
		t.Fatalf("expected activation to wait on assignments, screen=%s waiting=%v view=%q", model.screen, model.waitingForPolicies, model.View())
	}

	provider.prepared <- []pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT6H"}}}
	model = runCommand(model, prepare)
	if model.screen != ScreenActivation || model.form.durations["reader"] != "PT6H" {
		t.Fatalf("expected prepared activation form, screen=%s durations=%#v", model.screen, model.form.durations)
	}
}

func TestPreparedAssignmentsPreserveSelectionQueryAndCursor(t *testing.T) {
	model, provider := progressiveTestModel(
		pim.EligibleAssignment{ID: "one", DisplayName: "Role one"},
		pim.EligibleAssignment{ID: "two", DisplayName: "Role two"},
	)
	model, prepare := startProgressiveDiscovery(t, model)
	model.assignmentList.selectedIDs["two"] = true
	model.query = "Role"
	model.listCursor = 1
	provider.prepared <- []pim.EligibleAssignment{
		{ID: "one", DisplayName: "Role one", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H"}},
		{ID: "two", DisplayName: "Role two", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}},
	}
	model = runCommand(model, prepare)

	if !model.assignmentList.selectedIDs["two"] || model.query != "Role" || model.listCursor != 1 {
		t.Fatalf("prepared merge lost interaction state: selected=%#v query=%q cursor=%d", model.assignmentList.selectedIDs, model.query, model.listCursor)
	}
}

func TestDiscoveryCacheReusesPreparedAssignments(t *testing.T) {
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	model, prepare := startProgressiveDiscovery(t, model)
	provider.prepared <- []pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}}}
	model = runCommand(model, prepare)
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd := next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	if cmd != nil || provider.discoverCalls != 1 || provider.prepareCalls != 1 || !model.policiesReady {
		t.Fatalf("expected prepared cache hit, cmd=%v discover=%d prepare=%d ready=%v", cmd != nil, provider.discoverCalls, provider.prepareCalls, model.policiesReady)
	}
}

func TestRefreshInvalidatesDiscoveryCache(t *testing.T) {
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	provider.discoveries = append(provider.discoveries, []pim.EligibleAssignment{{ID: "owner", DisplayName: "Owner"}})
	model, prepare := startProgressiveDiscovery(t, model)
	provider.prepared <- []pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}}
	model = runCommand(model, prepare)
	oldGeneration := model.discoveryCheck

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model, _ = startDiscoveryBatch(t, next.(Model), cmd)
	if provider.discoverCalls != 2 || model.discoveryCheck <= oldGeneration || model.assignmentList.items[0].ID != "owner" {
		t.Fatalf("expected fresh generation, calls=%d generation=%d assignments=%#v", provider.discoverCalls, model.discoveryCheck, model.assignmentList.items)
	}
}

func startDiscoveryBatch(t *testing.T, model Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected discovery command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected discovery batch")
	}
	for _, child := range batch {
		msg := child()
		if _, ok := msg.(assignmentsDiscoveredMsg); ok {
			next, follow := model.Update(msg)
			return next.(Model), follow
		}
	}
	t.Fatal("discovery batch did not contain discovery result")
	return model, nil
}

func TestActivationCompletionInvalidatesDiscoveryCache(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	key := discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}
	model.discoveryCache[key] = discoveryEntry{assignments: []pim.EligibleAssignment{{ID: "reader"}}, policiesReady: true, generation: 1}

	next, _ := model.Update(activationCompletedMsg{})
	if _, ok := next.(Model).discoveryCache[key]; ok {
		t.Fatal("activation completion retained stale discovery cache")
	}
}

func TestSummaryEscStartsFreshDiscoveryAfterActivation(t *testing.T) {
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "fresh", DisplayName: "Fresh role"}}}}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.screen = ScreenProgress
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "activated", DisplayName: "Activated role"}})
	model.assignmentList.selectedIDs["activated"] = true

	next, _ := model.Update(activationCompletedMsg{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}})
	next, cmd := next.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if cmd == nil || model.screen != ScreenAssignments || !model.loading || len(model.assignmentList.items) != 0 || len(model.assignmentList.selectedIDs) != 0 {
		t.Fatalf("expected fresh discovery after summary, screen=%s loading=%v assignments=%#v selected=%#v cmd=%v", model.screen, model.loading, model.assignmentList.items, model.assignmentList.selectedIDs, cmd != nil)
	}
	model, _ = startDiscoveryBatch(t, model, cmd)
	if provider.discoverCalls != 1 || len(model.assignmentList.items) != 1 || model.assignmentList.items[0].ID != "fresh" {
		t.Fatalf("expected fresh provider result, calls=%d assignments=%#v", provider.discoverCalls, model.assignmentList.items)
	}
}

func TestCancelingSummaryAuthenticationRetryStartsFreshDiscovery(t *testing.T) {
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{ID: "fresh"}}}}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.screen = ScreenSummary
	model.checkingAuthentication = true
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "stale"}})
	model.assignmentList.selectedIDs["stale"] = true

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if cmd == nil || model.checkingAuthentication || !model.loading || len(model.assignmentList.items) != 0 || len(model.assignmentList.selectedIDs) != 0 {
		t.Fatalf("expected canceling summary retry to refresh assignments, checking=%v loading=%v assignments=%#v selected=%#v cmd=%v", model.checkingAuthentication, model.loading, model.assignmentList.items, model.assignmentList.selectedIDs, cmd != nil)
	}
}

func TestStalePreparationCannotOverwriteRefreshedDiscovery(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "current"}})
	key := discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}
	model.discoveryCache[key] = discoveryEntry{assignments: model.assignmentList.items, generation: 2}

	next, _ := model.Update(assignmentsPreparedMsg{key: key, generation: 1, assignments: []pim.EligibleAssignment{{ID: "stale"}}})
	got := next.(Model)
	if got.assignmentList.items[0].ID != "current" || got.discoveryCache[key].assignments[0].ID != "current" {
		t.Fatalf("stale preparation overwrote current assignments: list=%#v cache=%#v", got.assignmentList.items, got.discoveryCache[key])
	}
}

func TestCanceledPreparationCannotWarmInactiveTenantCache(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-2"}
	model.activeSection = SectionAzureResources
	model.discoveryCheck = 2
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "current"}})
	key := discoveryKey{tenantID: "tenant-1", section: SectionAzureResources}

	next, _ := model.Update(assignmentsPreparedMsg{key: key, generation: 1, assignments: []pim.EligibleAssignment{{ID: "prepared"}}})
	got := next.(Model)
	if _, ok := got.discoveryCache[key]; ok || got.assignmentList.items[0].ID != "current" {
		t.Fatalf("canceled inactive preparation warmed cache or changed list: cache=%#v list=%#v", got.discoveryCache, got.assignmentList.items)
	}
}

func TestPreparationFailureKeepsListAndBlocksActivation(t *testing.T) {
	sentinel := errors.New("policy lookup failed")
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	provider.scriptedProvider.discoveries = append(provider.scriptedProvider.discoveries, []pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}})
	provider.prepareErr = fmt.Errorf("prepare activation policies: %w", sentinel)
	model, prepare := startProgressiveDiscovery(t, model)
	model = runCommand(model, prepare)
	if len(model.assignmentList.items) != 1 || !errors.Is(model.err, sentinel) || !strings.Contains(model.View(), "prepare activation policies") {
		t.Fatalf("expected visible role and actionable wrapped error, assignments=%#v err=%v view=%q", model.assignmentList.items, model.err, model.View())
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenAssignments || !strings.Contains(model.err.Error(), "press r to retry discovery") {
		t.Fatalf("expected blocked activation with retry guidance, screen=%s err=%v", model.screen, model.err)
	}
	oldGeneration := model.discoveryCheck
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model, _ = startDiscoveryBatch(t, next.(Model), cmd)
	if provider.discoverCalls != 2 || model.discoveryCheck <= oldGeneration {
		t.Fatalf("expected retry discovery, calls=%d generation=%d", provider.discoverCalls, model.discoveryCheck)
	}
}

type scriptedTenantProvider struct {
	replies [][]azureauth.Tenant
	errs    []error
	calls   int
}

func (p *scriptedTenantProvider) Tenants(context.Context) ([]azureauth.Tenant, error) {
	p.calls++
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(p.replies) == 0 {
		return nil, nil
	}
	tenants := p.replies[0]
	p.replies = p.replies[1:]
	return tenants, nil
}

func TestModelDiscoversSelectedSectionAndActivatesSelection(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Global Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}},
			{ID: "two", DisplayName: "Privileged Role Administrator"},
		}},
		results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}},
	}
	model := NewModel(Runtime{AzureResources: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)
	if !strings.Contains(model.View(), "Eligible assignments") {
		t.Fatalf("expected assignments screen, got %q", model.View())
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenActivation || cmd == nil {
		t.Fatalf("expected focused activation form, got screen %s", model.screen)
	}
	model = sendRunes(model, "Need access now")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = next.(Model)
	model = sendRunes(model, "PT2H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenConfirmation {
		t.Fatalf("expected confirmation screen, got %s", model.screen)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenActivation {
		t.Fatalf("expected Esc to return to activation form, got %s", model.screen)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if model.screen != ScreenSummary {
		t.Fatalf("expected summary screen, got %s", model.screen)
	}
	if len(provider.activated) != 1 || provider.activated[0].Justification != "Need access now" || provider.activated[0].DurationISO != "PT2H" {
		t.Fatalf("unexpected activation requests: %#v", provider.activated)
	}
	if !strings.Contains(model.View(), "1 activated") {
		t.Fatalf("expected rendered summary, got %q", model.View())
	}
}

func TestActivationFormDefaultsAndSubmitsPerAssignmentDurations(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Contributor", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"}},
			{ID: "two", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H"}},
			{ID: "three", DisplayName: "Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}},
		}},
		results: []pim.ActivationResult{
			{Status: pim.ActivationStatusActivated},
			{Status: pim.ActivationStatusActivated},
		},
	}
	model := NewModel(Runtime{AzureResources: provider})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = sendRunes(next.(Model), "Need access")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = sendRunes(next.(Model), "PT7H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = sendRunes(next.(Model), "PT3H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.form.durations["one"] != "PT7H" || model.form.durations["three"] != "PT2H" {
		t.Fatalf("expected per-ID edit and policy default after changing selection, got %#v", model.form.durations)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.duration.Value() != "PT2H" {
		t.Fatalf("expected Reader policy default, got %q", model.duration.Value())
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = sendRunes(next.(Model), "PT1H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenConfirmation {
		t.Fatalf("expected confirmation, got %s with error %v", model.screen, model.err)
	}
	view := model.View()
	for _, want := range []string{"Contributor", "PT7H", "Reader", "PT1H", "Need access"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected confirmation to contain %q, got %q", want, view)
		}
	}

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)
	if model.screen != ScreenSummary {
		t.Fatalf("expected summary, got %s", model.screen)
	}
	if len(provider.activated) != 2 ||
		provider.activated[0].Assignment.ID != "one" || provider.activated[0].DurationISO != "PT7H" ||
		provider.activated[1].Assignment.ID != "three" || provider.activated[1].DurationISO != "PT1H" ||
		provider.activated[0].Justification != "Need access" || provider.activated[1].Justification != "Need access" {
		t.Fatalf("unexpected activation requests: %#v", provider.activated)
	}
}

func TestActivationFormShowsPolicyJustificationRequirement(t *testing.T) {
	for _, test := range []struct {
		name     string
		required bool
		want     string
	}{
		{name: "required", required: true, want: "Justification — REQUIRED"},
		{name: "optional", want: "Justification — optional"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Runtime{})
			model.screen = ScreenAssignments
			model.activeSection = SectionAzureResources
			model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
				ID: "one", DisplayName: "Owner",
				ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H", JustificationRequired: test.required},
			}})
			model.assignmentList.toggle("one")
			next, _ := model.openActivationForm()
			view := next.(Model).View()
			if !strings.Contains(view, test.want) {
				t.Fatalf("expected %q, got %q", test.want, view)
			}
		})
	}
}

func TestActivationFormRendersCleanEmptyJustification(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	previousDarkBackground := lipgloss.HasDarkBackground()
	lipgloss.SetColorProfile(termenv.ANSI)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(previousProfile)
	defer lipgloss.SetHasDarkBackground(previousDarkBackground)

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "one", DisplayName: "Reader",
		ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"},
	}})
	model.assignmentList.toggle("one")
	next, _ := model.openActivationForm()
	field := next.(Model).justification.View()

	if !strings.Contains(field, "hy is this access needed?") {
		t.Fatalf("expected placeholder, got %q", field)
	}
	if strings.Contains(field, "┃") {
		t.Fatalf("expected no internal prompt rail, got %q", field)
	}
	if strings.Contains(field, "\x1b[40m") {
		t.Fatalf("expected no focused-line background, got %q", field)
	}
}

func TestActivationFormKeepsDurationEditsWhileMovingFocus(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "one", DisplayName: "Contributor", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"}},
		{ID: "two", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H"}},
	})
	model.assignmentList.toggle("one")
	model.assignmentList.toggle("two")
	next, _ := model.openActivationForm()
	model = next.(Model)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = next.(Model)
	if model.formField != formFieldDuration || model.durationIndex != 0 || model.duration.Value() != "PT8H" {
		t.Fatalf("expected first duration focus, got field %d index %d value %q", model.formField, model.durationIndex, model.duration.Value())
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	model = sendRunes(next.(Model), "PT6H")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = next.(Model)
	if model.durationIndex != 1 || model.duration.Value() != "PT4H" || model.form.durations["one"] != "PT6H" {
		t.Fatalf("expected saved first duration and focused second, got index %d input %q values %#v", model.durationIndex, model.duration.Value(), model.form.durations)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	model = next.(Model)
	if model.durationIndex != 0 || model.duration.Value() != "PT6H" {
		t.Fatalf("expected edited first duration to be restored, got index %d value %q", model.durationIndex, model.duration.Value())
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	model = next.(Model)
	if model.formField != formFieldJustification {
		t.Fatalf("expected justification focus, got field %d", model.formField)
	}
}

func TestActivationConfirmationPreservesEveryDurationAndOptionalJustification(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "one", DisplayName: "Contributor", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"}},
		{ID: "two", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H"}},
	})
	model.assignmentList.toggle("one")
	model.assignmentList.toggle("two")
	next, _ := model.openActivationForm()
	model = next.(Model)
	model.form.durations["one"] = "PT6H"
	next, _ = model.focusDuration(1)
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	if model.screen != ScreenConfirmation {
		t.Fatalf("expected confirmation, got %s with error %v", model.screen, model.err)
	}
	view := model.View()
	for _, want := range []string{"Contributor", "PT6H", "Owner", "PT4H", "(none)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected confirmation to contain %q, got %q", want, view)
		}
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenActivation || model.durationIndex != 1 || model.duration.Value() != "PT4H" || model.form.durations["one"] != "PT6H" {
		t.Fatalf("expected duration focus and values to survive back navigation: %#v", model.form.durations)
	}
}

func TestConfirmationSkipsMFAForOrdinaryBatch(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	mfaCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
			mfaCalls++
			return exec.Command("true"), nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}})
	model.assignmentList.toggle("reader")
	model.form.durations = map[string]string{"reader": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if mfaCalls != 0 || len(provider.activated) != 1 || model.screen != ScreenSummary {
		t.Fatalf("expected direct activation, MFA calls=%d requests=%#v screen=%s", mfaCalls, provider.activated, model.screen)
	}
}

func TestOrdinaryAzureActivationUsesSignedInPrincipal(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{AccessToken: "checked-token", PrincipalID: "user-1", Satisfied: true}, nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader", PrincipalID: "group-1"}})
	model.assignmentList.toggle("reader")
	model.form.durations = map[string]string{"reader": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd()
	next, cmd = next.(Model).Update(msg)
	model = runCommand(next.(Model), cmd)

	if len(provider.activated) != 1 || provider.activated[0].Assignment.PrincipalID != "user-1" || model.screen != ScreenSummary {
		t.Fatalf("expected signed-in user to activate group-derived eligibility, requests=%#v screen=%s", provider.activated, model.screen)
	}
}

func TestConfirmationReusesSatisfiedMFA(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	stepUpCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{AccessToken: "checked-token", PrincipalID: "user-1", Satisfied: true}, nil
		},
		StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
			stepUpCalls++
			return exec.Command("true"), nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", PrincipalID: "group-1", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}})
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"owner": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd()
	next, cmd = next.(Model).Update(msg)
	model = runCommand(next.(Model), cmd)

	if stepUpCalls != 0 || len(provider.activated) != 1 || provider.activated[0].Assignment.PrincipalID != "user-1" || provider.tokens[0] != "checked-token" || model.screen != ScreenSummary {
		t.Fatalf("expected signed-in user and existing MFA token, step-up calls=%d tokens=%#v requests=%#v screen=%s", stepUpCalls, provider.tokens, provider.activated, model.screen)
	}
}

func TestAzureBatchTargetsAuthenticatedUserWithoutMutatingEligibilities(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusActivated},
	}}
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{AccessToken: "checked-token", PrincipalID: "user-1", Satisfied: true}, nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "direct", PrincipalID: "user-1"},
		{ID: "group", PrincipalID: "group-1"},
	})
	model.assignmentList.toggle("direct")
	model.assignmentList.toggle("group")
	model.form.durations = map[string]string{"direct": "PT1H", "group": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd()
	next, cmd = next.(Model).Update(msg)
	model = runCommand(next.(Model), cmd)

	if len(provider.activated) != 2 || len(provider.tokens) != 2 {
		t.Fatalf("expected two activation requests, requests=%#v tokens=%#v", provider.activated, provider.tokens)
	}
	for index := range provider.activated {
		if provider.activated[index].Assignment.PrincipalID != "user-1" || provider.tokens[index] != "checked-token" {
			t.Fatalf("expected request %d to use authenticated user and token, request=%#v token=%q", index, provider.activated[index], provider.tokens[index])
		}
	}
	selected := model.assignmentList.selected()
	if selected[0].PrincipalID != "user-1" || selected[1].PrincipalID != "group-1" {
		t.Fatalf("expected eligibility principals to remain unchanged, got %#v", selected)
	}
}

func TestConfirmationIgnoresDuplicateAndCanceledAuthenticationChecks(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{AccessToken: "checked-token", PrincipalID: "principal-1", Satisfied: true}, nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", PrincipalID: "principal-1", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}})
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"owner": "PT1H"}

	next, authenticationCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, duplicateCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if duplicateCmd != nil {
		t.Fatal("expected duplicate Enter to be ignored while authentication is checked")
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	next, activationCmd := model.Update(authenticationCmd())
	model = next.(Model)
	if activationCmd != nil || len(provider.activated) != 0 || model.screen != ScreenActivation {
		t.Fatalf("expected canceled authentication result to be ignored, requests=%#v screen=%s", provider.activated, model.screen)
	}
}

func TestSummaryRetryRechecksAuthentication(t *testing.T) {
	assignment := pim.EligibleAssignment{
		ID: "owner", PrincipalID: "group-1", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			return azureauth.ARMAuthentication{AccessToken: "retry-token", PrincipalID: "user-1", Satisfied: true}, nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenSummary
	model.summary = newSummary([]pim.ActivationResult{{
		Assignment: assignment, Status: pim.ActivationStatusFailed, Retryable: true,
	}})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = next.(Model)
	msg := cmd()
	if _, ok := msg.(authenticationCheckedMsg); !ok {
		t.Fatalf("expected retry authentication check, got %T", msg)
	}
	next, cmd = model.Update(msg)
	model = runCommand(next.(Model), cmd)
	if len(provider.activated) != 1 || provider.activated[0].Assignment.PrincipalID != "user-1" || provider.tokens[0] != "retry-token" || model.screen != ScreenSummary {
		t.Fatalf("expected retry to retarget the authenticated user with its checked token, tokens=%#v requests=%#v screen=%s", provider.tokens, provider.activated, model.screen)
	}
}

func TestStepUpBlocksActivationWhenPrincipalChanges(t *testing.T) {
	provider := &scriptedProvider{}
	checks := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			checks++
			if checks == 1 {
				return azureauth.ARMAuthentication{AccessToken: "old-token", PrincipalID: "principal-1"}, nil
			}
			return azureauth.ARMAuthentication{AccessToken: "new-token", PrincipalID: "principal-2", Satisfied: true}, nil
		},
		StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
			return exec.Command("true"), nil
		},
	})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", PrincipalID: "group-1", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c1"},
	}})
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"owner": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := cmd()
	next, cmd = next.(Model).Update(msg)
	model = next.(Model)
	if cmd == nil {
		t.Fatal("expected step-up command after missing authentication context")
	}
	next, cmd = model.Update(stepUpCompletedMsg{selected: model.assignmentList.selected(), principalID: "principal-1"})
	msg = cmd()
	next, cmd = next.(Model).Update(msg)
	model = next.(Model)

	if cmd != nil || len(provider.activated) != 0 || model.screen != ScreenConfirmation {
		t.Fatalf("expected identity change to block activation, requests=%#v screen=%s", provider.activated, model.screen)
	}
	if model.err == nil || !strings.Contains(model.err.Error(), "principal changed") {
		t.Fatalf("expected actionable identity-change error, got %v", model.err)
	}
}

func TestAuthenticationContextStepUpPreservesPrincipalForGroupEligibility(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	checks := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
			checks++
			if checks == 1 {
				return azureauth.ARMAuthentication{AccessToken: "old-token", PrincipalID: "user-1"}, nil
			}
			return azureauth.ARMAuthentication{AccessToken: "step-up-token", PrincipalID: "user-1", Satisfied: true}, nil
		},
		StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
			return exec.Command("true"), nil
		},
	})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", PrincipalID: "group-1", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c1"},
	}})
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"owner": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, cmd = next.(Model).Update(cmd())
	model = next.(Model)
	if cmd == nil {
		t.Fatal("expected authentication-context step-up")
	}
	next, cmd = model.Update(stepUpCompletedMsg{selected: model.assignmentList.selected(), principalID: "user-1"})
	next, cmd = next.(Model).Update(cmd())
	model = runCommand(next.(Model), cmd)

	if len(provider.activated) != 1 || provider.activated[0].Assignment.PrincipalID != "user-1" || provider.tokens[0] != "step-up-token" || model.screen != ScreenSummary {
		t.Fatalf("expected stable user and step-up token, requests=%#v tokens=%#v screen=%s", provider.activated, provider.tokens, model.screen)
	}
}

func TestAuthenticationErrorsSubmitNoActivation(t *testing.T) {
	t.Run("token check failure", func(t *testing.T) {
		provider := &scriptedProvider{}
		stepUpCalls := 0
		model := NewModel(Runtime{
			AzureResources: provider,
			ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
				return azureauth.ARMAuthentication{}, errors.New("token unavailable")
			},
			StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
				stepUpCalls++
				return exec.Command("true"), nil
			},
		})
		model.activeSection = SectionAzureResources
		model.screen = ScreenConfirmation
		model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "owner", PrincipalID: "group-1"}})
		model.assignmentList.toggle("owner")

		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		next, cmd = next.(Model).Update(cmd())
		model = next.(Model)
		if cmd != nil || stepUpCalls != 0 || len(provider.activated) != 0 || model.err == nil || !strings.Contains(model.err.Error(), "token unavailable") {
			t.Fatalf("expected token error to block activation, step-ups=%d requests=%#v error=%v", stepUpCalls, provider.activated, model.err)
		}
	})

	t.Run("step-up claims remain unsatisfied", func(t *testing.T) {
		provider := &scriptedProvider{}
		model := NewModel(Runtime{
			AzureResources: provider,
			ARMAuthentication: func(context.Context, bool, string) (azureauth.ARMAuthentication, error) {
				return azureauth.ARMAuthentication{AccessToken: "token", PrincipalID: "user-1"}, nil
			},
			StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
				return exec.Command("true"), nil
			},
		})
		model.activeSection = SectionAzureResources
		model.screen = ScreenConfirmation
		model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
			ID: "owner", PrincipalID: "group-1", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c1"},
		}})
		model.assignmentList.toggle("owner")

		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		next, cmd = next.(Model).Update(cmd())
		model = next.(Model)
		next, cmd = model.Update(stepUpCompletedMsg{selected: model.assignmentList.selected(), principalID: "user-1"})
		next, cmd = next.(Model).Update(cmd())
		model = next.(Model)
		if cmd != nil || len(provider.activated) != 0 || model.err == nil || !strings.Contains(model.err.Error(), "does not satisfy") {
			t.Fatalf("expected unsatisfied step-up claims to block activation, requests=%#v error=%v", provider.activated, model.err)
		}
	})
}

func TestConfirmationGatesMixedBatchOnMFA(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusActivated},
	}}
	mfaCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		StepUpCommand: func(tenantID string, mfaRequired bool, authenticationContext string) (*exec.Cmd, error) {
			mfaCalls++
			if tenantID != "tenant-1" || !mfaRequired || authenticationContext != "" {
				t.Fatalf("expected tenant-1 with standard MFA, got %q, %v, and %q", tenantID, mfaRequired, authenticationContext)
			}
			return exec.Command("true"), nil
		},
	})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "reader", DisplayName: "Reader"},
		{ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true}},
	})
	model.assignmentList.toggle("reader")
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"reader": "PT1H", "owner": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd == nil || mfaCalls != 1 || len(provider.activated) != 0 || model.screen != ScreenConfirmation {
		t.Fatalf("expected pending MFA gate, calls=%d requests=%#v screen=%s", mfaCalls, provider.activated, model.screen)
	}

	selected := model.assignmentList.selected()
	next, cmd = model.Update(stepUpCompletedMsg{selected: selected})
	model = runCommand(next.(Model), cmd)
	if len(provider.activated) != 2 || model.screen != ScreenSummary {
		t.Fatalf("expected full batch after MFA, requests=%#v screen=%s", provider.activated, model.screen)
	}
}

func TestConfirmationUsesRequiredAuthenticationContext(t *testing.T) {
	provider := &scriptedProvider{}
	var gotContext string
	var gotMFA bool
	model := NewModel(Runtime{
		AzureResources: provider,
		StepUpCommand: func(_ string, mfaRequired bool, authenticationContext string) (*exec.Cmd, error) {
			gotMFA = mfaRequired
			gotContext = authenticationContext
			return exec.Command("true"), nil
		},
	})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c1"},
	}})
	model.assignmentList.toggle("owner")

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd == nil || gotMFA || gotContext != "c1" || len(provider.activated) != 0 {
		t.Fatalf("expected c1-only step-up before activation, MFA=%v context=%q requests=%#v", gotMFA, gotContext, provider.activated)
	}
}

func TestAuthenticationRequirementPreservesMFAWithContext(t *testing.T) {
	context, mfaRequired, err := authenticationRequirement([]pim.EligibleAssignment{{
		ActivationPolicy: pim.ActivationPolicy{MFARequired: true, AuthenticationContext: "c1"},
	}})
	if err != nil || !mfaRequired || context != "c1" {
		t.Fatalf("expected combined MFA and c1 requirement, MFA=%v context=%q error=%v", mfaRequired, context, err)
	}
}

func TestConfirmationRejectsConflictingAuthenticationContexts(t *testing.T) {
	provider := &scriptedProvider{}
	stepUpCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		StepUpCommand: func(string, bool, string) (*exec.Cmd, error) {
			stepUpCalls++
			return exec.Command("true"), nil
		},
	})
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "one", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c1"}},
		{ID: "two", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c2"}},
	})
	model.assignmentList.toggle("one")
	model.assignmentList.toggle("two")

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd != nil || stepUpCalls != 0 || len(provider.activated) != 0 {
		t.Fatalf("expected conflicting contexts to block activation, calls=%d requests=%#v", stepUpCalls, provider.activated)
	}
	if model.err == nil || !strings.Contains(model.err.Error(), "different authentication contexts") {
		t.Fatalf("expected conflicting-context error, got %v", model.err)
	}
}

func TestStepUpFailureReturnsToConfirmationWithoutActivation(t *testing.T) {
	provider := &scriptedProvider{}
	model := NewModel(Runtime{AzureResources: provider})
	model.screen = ScreenConfirmation

	next, cmd := model.Update(stepUpCompletedMsg{
		selected: []pim.EligibleAssignment{{ID: "owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true}}},
		err:      errors.New("login canceled"),
	})
	model = next.(Model)

	if cmd != nil || model.screen != ScreenConfirmation || len(provider.activated) != 0 {
		t.Fatalf("expected blocked activation, requests=%#v screen=%s", provider.activated, model.screen)
	}
	if !strings.Contains(model.View(), "step-up authentication failed: login canceled") {
		t.Fatalf("expected actionable step-up error, got %q", model.View())
	}
}

func TestAssignmentDetailsShowsAuthenticationRequirement(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenDetails
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{AuthenticationContext: "c1"},
	}})
	if view := model.View(); !strings.Contains(view, "Authentication context") || !strings.Contains(view, "c1") {
		t.Fatalf("expected authentication context in details, got %q", view)
	}
}

func TestAssignmentDetailsShowsPreparationFailureAndRetries(t *testing.T) {
	sentinel := errors.New("policy lookup failed")
	model, provider := progressiveTestModel(pim.EligibleAssignment{ID: "reader", DisplayName: "Reader"})
	provider.prepareErr = fmt.Errorf("prepare activation policies: %w", sentinel)
	model, prepare := startProgressiveDiscovery(t, model)
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	model = runCommand(next.(Model), prepare)

	view := model.View()
	if strings.Contains(view, "Loading...") || !strings.Contains(view, "Unavailable") || !strings.Contains(view, "prepare activation policies") || !strings.Contains(view, "press r to retry discovery") {
		t.Fatalf("expected actionable unavailable policy details, got %q", view)
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = next.(Model)
	if cmd == nil || model.screen != ScreenAssignments || !model.loading {
		t.Fatalf("expected details retry to start discovery, screen=%s loading=%v cmd=%v", model.screen, model.loading, cmd != nil)
	}
}

func TestAssignmentDetailsMultilinePolicyErrorFitsMinimumTerminal(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenDetails
	model.activeSection = SectionAzureResources
	model.policiesReady = false
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}})
	model.err = errors.New("prepare activation policies: list Azure activation policies at /subscriptions/00000000/resourceGroups/production-platform/providers/Microsoft.Authorization/roleManagementPolicies\nrequest failed: authorization denied")
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	view := next.(Model).View()

	if got := lipgloss.Height(view); got > 26 {
		t.Fatalf("expected compact details height at most 26, got %d for %q", got, view)
	}
	for _, want := range []string{"prepare activation policies", "authorization denied", "press r to retry discovery", "back to assignments"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected %q to remain visible, got %q", want, view)
		}
	}
}

func TestConfirmationShowsMFARequirement(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}})
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"owner": "PT1H"}
	view := model.View()
	if !strings.Contains(view, "Azure PIM will validate the current Azure CLI session") || strings.Contains(view, "will prompt") {
		t.Fatalf("expected server-validated MFA warning without a login prompt, got %q", view)
	}
}

func TestActivationViewFitsMinimumTerminalWithPerAssignmentDurations(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 6)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:               fmt.Sprintf("assignment-%d", index),
			DisplayName:      fmt.Sprintf("Role %d", index+1),
			ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: fmt.Sprintf("PT%dH", index+1)},
		}
	}
	model := NewModel(Runtime{})
	model.screen = ScreenActivation
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	for _, assignment := range assignments {
		model.assignmentList.toggle(assignment.ID)
	}
	model.prepareActivationForm()
	next, _ := model.focusDuration(len(assignments) - 1)
	model = next.(Model)
	next, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)

	view := model.View()
	if height := lipgloss.Height(view); height > 26 {
		t.Fatalf("expected height at most 26, got %d for %q", height, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("expected line width at most 80, got %d for %q", width, line)
		}
	}
	if !strings.Contains(view, "Role 6") || !strings.Contains(view, "Showing") {
		t.Fatalf("expected focused final duration and range, got %q", view)
	}
}

func TestConfirmationViewScrollsEveryDurationAtMinimumTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 8)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:               fmt.Sprintf("assignment-%d", index),
			DisplayName:      fmt.Sprintf("Role %d", index+1),
			ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: fmt.Sprintf("PT%dH", index+1)},
		}
	}
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.assignmentList = newAssignmentList(assignments)
	for _, assignment := range assignments {
		model.assignmentList.toggle(assignment.ID)
	}
	model.prepareActivationForm()
	model.screen = ScreenConfirmation
	model.durationIndex = 0
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)

	initial := model.View()
	if !strings.Contains(initial, "Role 1") || strings.Contains(initial, "Role 8") || !strings.Contains(initial, "Showing") {
		t.Fatalf("expected first confirmation window, got %q", initial)
	}
	for range 7 {
		next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = next.(Model)
	}
	view := model.View()
	if !strings.Contains(view, "Role 8") || strings.Contains(view, "Role 1") || !strings.Contains(view, "PT8H") {
		t.Fatalf("expected navigated final confirmation duration, got %q", view)
	}
	if height := lipgloss.Height(view); height > 26 {
		t.Fatalf("expected height at most 26, got %d for %q", height, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("expected line width at most 80, got %d for %q", width, line)
		}
	}
}

func TestActivationValidationErrorsFitMinimumTerminal(t *testing.T) {
	for _, test := range []struct {
		name     string
		required bool
		missing  bool
		want     string
	}{
		{name: "required justification", required: true, want: "justification is required"},
		{name: "missing duration", missing: true, want: "duration is required for Role 6"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assignments := make([]pim.EligibleAssignment, 6)
			for index := range assignments {
				maximum := "PT4H"
				if test.missing && index == len(assignments)-1 {
					maximum = ""
				}
				assignments[index] = pim.EligibleAssignment{
					ID:          fmt.Sprintf("assignment-%d", index),
					DisplayName: fmt.Sprintf("Role %d", index+1),
					ActivationPolicy: pim.ActivationPolicy{
						MaximumDurationISO:    maximum,
						JustificationRequired: test.required && index == 0,
					},
				}
			}
			model := NewModel(Runtime{})
			model.screen = ScreenAssignments
			model.assignmentList = newAssignmentList(assignments)
			for _, assignment := range assignments {
				model.assignmentList.toggle(assignment.ID)
			}
			next, _ := model.openActivationForm()
			model = next.(Model)
			next, _ = model.focusDuration(len(assignments) - 1)
			model = next.(Model)
			next, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
			model = next.(Model)
			next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(Model)

			view := model.View()
			if !strings.Contains(view, test.want) {
				t.Fatalf("expected %q, got %q", test.want, view)
			}
			if height := lipgloss.Height(view); height > 26 {
				t.Fatalf("expected height at most 26, got %d for %q", height, view)
			}
			for _, line := range strings.Split(view, "\n") {
				if width := lipgloss.Width(line); width > 80 {
					t.Fatalf("expected line width at most 80, got %d for %q", width, line)
				}
			}
		})
	}
}

func TestAssignmentsViewClearlyMarksSelectedRole(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "one", DisplayName: "Contributor"}})

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	view := next.(Model).View()

	if !strings.Contains(view, "1 selected") || !strings.Contains(view, "[✓]") {
		t.Fatalf("expected an explicit selected marker, got %q", view)
	}
}

func TestSelectedFocusedAssignmentKeepsContinuousHighlight(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	previousDarkBackground := lipgloss.HasDarkBackground()
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(previousProfile)
	defer lipgloss.SetHasDarkBackground(previousDarkBackground)

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "one", DisplayName: "Contributor"}})
	model.assignmentList.toggle("one")

	for line := range strings.SplitSeq(model.View(), "\n") {
		if strings.Contains(line, "Contributor") && strings.Contains(line, "[✓]\x1b[0m") {
			t.Fatalf("selected marker reset the focused-row highlight: %q", line)
		}
	}
}

func TestAssignmentsViewShowsActiveStateAndCount(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("TASK3", 8*60*60)
	defer func() { time.Local = previousLocal }()

	until := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "available", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true, ActiveUntil: &until},
	})

	view := model.View()
	for _, want := range []string{"STATE", "[ACTIVE]", "0 selected", "1 selectable", "1 active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected %q in active assignment view, got %q", want, view)
		}
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	view = model.View()
	for _, want := range []string{"1 selected", "0 selectable", "1 active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected %q after selecting inactive assignment, got %q", want, view)
		}
	}

	model.listCursor = 1
	model.screen = ScreenDetails
	view = model.View()
	foundExpiry := false
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Active until") && strings.Contains(line, "2026-07-17 02:00 TASK3") {
			foundExpiry = true
			break
		}
	}
	if !strings.Contains(view, "Active") || !foundExpiry {
		t.Fatalf("expected active details and local expiry row, got %q", view)
	}
}

func TestActiveAssignmentStaysFocusableButCannotBeSelectedThroughUpdate(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("TASK3", 8*60*60)
	defer func() { time.Local = previousLocal }()

	until := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "available", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true, ActiveUntil: &until},
	})

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	if model.listCursor != 1 || len(model.assignmentList.selected()) != 0 {
		t.Fatalf("expected active row to retain focus without selection, cursor=%d selected=%#v", model.listCursor, model.assignmentList.selected())
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	model = next.(Model)
	view := model.View()
	if model.screen != ScreenDetails || !strings.Contains(view, "Active until") || !strings.Contains(view, "2026-07-17 02:00 TASK3") {
		t.Fatalf("expected focused active row details, screen=%s view=%q", model.screen, view)
	}
}

func TestActiveAssignmentDetailsOmitMissingExpiry(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenDetails
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "active", DisplayName: "Owner", Active: true}})

	view := model.View()
	if !strings.Contains(view, "Status") || !strings.Contains(view, "Active") {
		t.Fatalf("expected active status, got %q", view)
	}
	if strings.Contains(view, "Active until") {
		t.Fatalf("expected missing active expiry to be omitted, got %q", view)
	}
}

func TestActiveFocusedAssignmentKeepsContinuousHighlight(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	previousDarkBackground := lipgloss.HasDarkBackground()
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(previousProfile)
	defer lipgloss.SetHasDarkBackground(previousDarkBackground)

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "active", DisplayName: "Owner", Active: true}})

	found := false
	for line := range strings.SplitSeq(model.View(), "\n") {
		if strings.Contains(line, "[ACTIVE]") {
			found = true
			if strings.Contains(line, "[ACTIVE]\x1b[0m") {
				t.Fatalf("active marker reset the focused-row highlight: %q", line)
			}
		}
	}
	if !found {
		t.Fatal("expected focused active assignment marker")
	}
}

func TestModelShowsDiscoveryErrorWithoutLeavingTUI(t *testing.T) {
	model := NewModel(Runtime{AzureResources: &scriptedProvider{discoverErr: errors.New("az login required")}})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if model.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", model.screen)
	}
	if !strings.Contains(model.View(), "az login required") {
		t.Fatalf("expected discovery error in view, got %q", model.View())
	}
}

func TestModelMarksRetryableProviderFailuresAndShowsRetryAction(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{{ID: "one", DisplayName: "Global Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}}}},
		activateErr: []error{activation.NewRetryableError(errors.New("temporary Azure error")), nil},
		results:     []pim.ActivationResult{{Status: pim.ActivationStatusActivated}},
	}
	model := NewModel(Runtime{AzureResources: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	model = sendRunes(model, "Need access")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if len(model.summary.retryableFailures()) != 1 || len(provider.activated) != 1 {
		t.Fatalf("expected one failure without automatic retry, got results %#v requests %#v", model.summary.results, provider.activated)
	}
	first := provider.activated[0]
	if first.Assignment.ID != "one" || first.DurationISO != "PT2H" || first.Justification != "Need access" {
		t.Fatalf("unexpected first request: %#v", first)
	}
	view := model.View()
	if !strings.Contains(view, "1 failed") || !strings.Contains(view, "retry failures") {
		t.Fatalf("expected retryable summary action, got %q", view)
	}

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = runCommand(next.(Model), cmd)
	if len(provider.activated) != 2 {
		t.Fatalf("expected one explicit retry, got %#v", provider.activated)
	}
	second := provider.activated[1]
	if second.Assignment.ID != first.Assignment.ID || second.DurationISO != first.DurationISO || second.Justification != first.Justification {
		t.Fatalf("expected retry to preserve request values, first %#v second %#v", first, second)
	}
	if len(model.summary.activated) != 1 || len(model.summary.failed) != 0 {
		t.Fatalf("expected successful retry summary, got %#v", model.summary)
	}
}

func TestAssignmentsSearchModeFiltersAndSelection(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Global Reader", Scope: pim.Scope{DisplayName: "Tenant"}},
			{ID: "two", DisplayName: "Contributor", Scope: pim.Scope{DisplayName: "rg-prod"}},
		}},
	}
	model := NewModel(Runtime{AzureResources: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	model = next.(Model)
	if !model.searchMode {
		t.Fatal("expected search mode to be enabled")
	}
	model = sendRunes(model, "globalx")
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = next.(Model)
	if model.query != "global" {
		t.Fatalf("expected query to be edited, got %q", model.query)
	}
	view := model.View()
	if !strings.Contains(view, "/  global_") {
		t.Fatalf("expected search query in view, got %q", view)
	}
	if !strings.Contains(view, "Global Reader") || strings.Contains(view, "Contributor") {
		t.Fatalf("expected filtered assignments in view, got %q", view)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.searchMode {
		t.Fatal("expected search mode to exit on Enter")
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	selected := model.assignmentList.selected()
	if len(selected) != 1 || selected[0].ID != "one" {
		t.Fatalf("expected filtered assignment selected, got %#v", selected)
	}
}

func TestTenantLabel(t *testing.T) {
	tests := []struct {
		name   string
		tenant azureauth.Tenant
		want   string
	}{
		{name: "name and domain", tenant: azureauth.Tenant{ID: "tenant-1", DisplayName: "Contoso", DefaultDomain: "contoso.onmicrosoft.com"}, want: "Contoso (contoso.onmicrosoft.com)"},
		{name: "name only", tenant: azureauth.Tenant{ID: "tenant-1", DisplayName: "Contoso"}, want: "Contoso"},
		{name: "domain only", tenant: azureauth.Tenant{ID: "tenant-1", DefaultDomain: "contoso.onmicrosoft.com"}, want: "contoso.onmicrosoft.com"},
		{name: "ID only", tenant: azureauth.Tenant{ID: "tenant-1"}, want: "tenant-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tenantLabel(test.tenant); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestInitStartsTenantLookupSpinner(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{{ID: "tenant-1"}}}}
	model := NewModel(Runtime{Tenants: provider})

	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected startup command batch")
	}
	var tickFound bool
	for _, cmd := range batch {
		if _, ok := cmd().(spinner.TickMsg); ok {
			tickFound = true
		}
	}
	if !tickFound {
		t.Fatal("expected startup spinner tick command")
	}
}

func TestOneTenantSkipsTenantSelection(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{{ID: "tenant-1", DefaultDomain: "contoso.com"}}}}
	model := NewModel(Runtime{Tenants: provider})

	model = runCommand(model, model.Init())
	if model.screen != ScreenHome || model.selectedTenant.ID != "tenant-1" {
		t.Fatalf("expected tenant-1 home, screen=%s tenant=%#v", model.screen, model.selectedTenant)
	}
}

func TestTenantMenuOnlyRendersForMultipleTenants(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{
		{ID: "tenant-1", DisplayName: "Contoso", DefaultDomain: "contoso.com"},
		{ID: "tenant-2", DefaultDomain: "fabrikam.com"},
	}}}
	model := NewModel(Runtime{Tenants: provider})
	if strings.Contains(model.View(), "Choose Azure tenant") {
		t.Fatalf("tenant choice menu must not render before multiple tenants are known: %q", model.View())
	}

	model = runCommand(model, model.Init())
	view := model.View()
	if !strings.Contains(view, "Choose Azure tenant") || !strings.Contains(view, "Contoso") || !strings.Contains(view, "tenant-2") {
		t.Fatalf("expected tenant choices, got %q", view)
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	view = model.View()
	if !strings.Contains(view, "CONNECTED") || !strings.Contains(view, "Contoso") || !strings.Contains(view, "tenant-1") {
		t.Fatalf("expected selected tenant panel, got %q", view)
	}
	for _, label := range []string{"Account", "Type", "Select", "Request", "Review", "Result"} {
		if !strings.Contains(view, label) {
			t.Fatalf("expected progress label %q in %q", label, view)
		}
	}
}

func TestTenantMenuFitsMinimumTerminalAndKeepsFocusVisible(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenTenants
	model.width = 80
	model.height = 26
	for index := range 20 {
		model.tenants = append(model.tenants, azureauth.Tenant{ID: fmt.Sprintf("tenant-%02d", index), DisplayName: "A Very Long Tenant Display Name", DefaultDomain: "a-very-long-tenant-domain.onmicrosoft.com"})
	}
	model.tenantIndex = len(model.tenants) - 1

	view := model.View()
	if height := lipgloss.Height(view); height > model.height {
		t.Fatalf("expected tenant view height at most %d, got %d", model.height, height)
	}
	if !strings.Contains(view, "tenant-19") {
		t.Fatalf("expected focused tenant to be visible, got %q", view)
	}
}

func TestMultipleTenantsRequireSelectionAndSupportBack(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{
		{ID: "tenant-1", DefaultDomain: "contoso.com"},
		{ID: "tenant-2", DefaultDomain: "fabrikam.com"},
	}}}
	model := NewModel(Runtime{Tenants: provider})
	model = runCommand(model, model.Init())
	if model.screen != ScreenTenants {
		t.Fatalf("expected tenant screen, got %s", model.screen)
	}
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if model.screen != ScreenHome || model.selectedTenant.ID != "tenant-2" {
		t.Fatalf("expected tenant-2 home, screen=%s tenant=%#v", model.screen, model.selectedTenant)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(Model).screen != ScreenTenants {
		t.Fatalf("expected Esc to reopen tenant selection, got %s", next.(Model).screen)
	}
}

func TestSelectingDifferentTenantClearsWorkflowState(t *testing.T) {
	model := NewModel(Runtime{})
	model.tenants = []azureauth.Tenant{{ID: "tenant-1"}, {ID: "tenant-2"}}
	model.selectedTenant = model.tenants[0]
	model.tenantIndex = 1
	model.screen = ScreenTenants
	model.loading = true
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader"}})
	model.assignmentList.toggle("reader")
	model.form.justification = "old tenant request"
	model.form.durations = map[string]string{"reader": "PT1H"}
	model.summary = newSummary([]pim.ActivationResult{{Assignment: pim.EligibleAssignment{ID: "reader"}, Status: pim.ActivationStatusActivated}})

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if got.selectedTenant.ID != "tenant-2" || got.loading || len(got.assignmentList.items) != 0 || got.form.justification != "" || len(got.form.durations) != 0 || len(got.summary.results) != 0 {
		t.Fatalf("tenant switch retained workflow state: %#v", got)
	}
}

func TestTenantLookupFailureCanRetry(t *testing.T) {
	provider := &scriptedTenantProvider{
		replies: [][]azureauth.Tenant{{{ID: "tenant-1"}}},
		errs:    []error{azureauth.ErrNotLoggedIn, nil},
	}
	model := NewModel(Runtime{Tenants: provider})
	model = runCommand(model, model.Init())
	if model.tenantErr == nil {
		t.Fatal("expected tenant lookup error")
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = runCommand(next.(Model), cmd)
	if model.screen != ScreenHome || model.selectedTenant.ID != "tenant-1" || provider.calls != 2 {
		t.Fatalf("expected successful retry, tenant=%#v calls=%d", model.selectedTenant, provider.calls)
	}
}

func TestDiscoveryAndAuthenticationUseSelectedTenant(t *testing.T) {
	provider := &scriptedProvider{discoveries: [][]pim.EligibleAssignment{{{
		ID: "reader", DisplayName: "Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT1H"},
	}}}}
	var authenticationTenant string
	model := NewModel(Runtime{
		AzureResources: provider,
		ARMAuthentication: func(ctx context.Context, _ bool, _ string) (azureauth.ARMAuthentication, error) {
			authenticationTenant = azureauth.TenantFromContext(ctx)
			return azureauth.ARMAuthentication{AccessToken: "token", PrincipalID: "principal-1", Satisfied: true}, nil
		},
	})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)
	if got := provider.discoveryTenants[0]; got != "tenant-1" {
		t.Fatalf("expected tenant-scoped discovery, got %q", got)
	}
	model.assignmentList.toggle("reader")
	model.screen = ScreenConfirmation
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = runCommand(next.(Model), cmd)
	if authenticationTenant != "tenant-1" {
		t.Fatalf("expected tenant-scoped authentication, got %q", authenticationTenant)
	}
}

func TestActivationUsesSelectedTenantContext(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	model := NewModel(Runtime{AzureResources: provider})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.form.durations = map[string]string{"reader": "PT1H"}
	assignment := pim.EligibleAssignment{ID: "reader"}

	_ = model.activateSelected([]pim.EligibleAssignment{assignment}, azureauth.ARMAuthentication{AccessToken: "checked-token"})()
	if len(provider.activationTenants) != 1 || provider.activationTenants[0] != "tenant-1" {
		t.Fatalf("expected tenant-scoped activation, got %#v", provider.activationTenants)
	}
	if len(provider.tokens) != 1 || provider.tokens[0] != "checked-token" {
		t.Fatalf("expected pinned checked token, got %#v", provider.tokens)
	}
}

func TestStaleDiscoveryFromPreviousTenantIsIgnored(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-2"}
	model.discoveryCheck = 2
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "current"}})

	next, _ := model.Update(assignmentsDiscoveredMsg{
		assignments: []pim.EligibleAssignment{{ID: "stale"}},
		tenantID:    "tenant-1",
		checkID:     1,
	})
	got := next.(Model)
	if len(got.assignmentList.items) != 1 || got.assignmentList.items[0].ID != "current" {
		t.Fatalf("stale discovery changed assignments: %#v", got.assignmentList.items)
	}
}

func TestStaleTenantLookupIsIgnored(t *testing.T) {
	model := NewModel(Runtime{})
	model.tenantCheck = 2
	model.tenants = []azureauth.Tenant{{ID: "tenant-current"}}
	model.selectedTenant = model.tenants[0]

	next, _ := model.Update(tenantsCheckedMsg{tenants: []azureauth.Tenant{{ID: "tenant-stale"}}, checkID: 1})
	got := next.(Model)
	if len(got.tenants) != 1 || got.tenants[0].ID != "tenant-current" || got.selectedTenant.ID != "tenant-current" {
		t.Fatalf("stale tenant lookup changed selection: tenants=%#v selected=%#v", got.tenants, got.selectedTenant)
	}
}

func TestSummaryViewListsPerAssignmentStatuses(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenSummary
	model.activeSection = SectionEntra
	model.summary = newSummary([]pim.ActivationResult{
		{
			Assignment: pim.EligibleAssignment{DisplayName: "Global Reader"},
			Status:     pim.ActivationStatusActivated,
			Message:    "Granted",
		},
		{
			Assignment: pim.EligibleAssignment{DisplayName: "Privileged Role Administrator"},
			Status:     pim.ActivationStatusPendingApproval,
			Message:    "PendingApproval",
		},
		{
			Assignment: pim.EligibleAssignment{DisplayName: "Billing Reader"},
			Status:     pim.ActivationStatusFailed,
			Message:    "PolicyBlocked",
		},
	})
	model.refreshSummaryViewport()

	view := model.View()
	if !strings.Contains(view, "- Global Reader: activated (Granted)") {
		t.Fatalf("expected activated row in summary, got %q", view)
	}
	if !strings.Contains(view, "- Privileged Role Administrator: pending_approval (PendingApproval)") {
		t.Fatalf("expected pending_approval row in summary, got %q", view)
	}
	if !strings.Contains(view, "- Billing Reader: failed (PolicyBlocked)") {
		t.Fatalf("expected failed row in summary, got %q", view)
	}
}

func TestSummaryWrapsLongAzureErrors(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenSummary
	model.summary = newSummary([]pim.ActivationResult{{
		Assignment: pim.EligibleAssignment{DisplayName: "Owner"},
		Status:     pim.ActivationStatusFailed,
		Message:    strings.Repeat("ARM response detail ", 12) + "MfaRule",
	}})
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)
	model.refreshSummaryViewport()

	if view := model.View(); !strings.Contains(view, "MfaRule") {
		t.Fatalf("expected complete wrapped Azure error, got %q", view)
	}
}

func TestModelSupportsHelpBackNavigationAndQuit(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{{ID: "one", DisplayName: "Global Reader", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"}}}},
	}
	model := NewModel(Runtime{AzureResources: provider})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenAssignments {
		t.Fatalf("expected activation Esc to return to assignments, got %s", model.screen)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.screen != ScreenHome {
		t.Fatalf("expected assignments Esc to return home, got %s", model.screen)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model = next.(Model)
	if !model.helpVisible || !strings.Contains(model.View(), "Keyboard guide") {
		t.Fatalf("expected contextual help overlay, got %q", model.View())
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(Model)
	if model.helpVisible {
		t.Fatal("expected Esc to close help")
	}

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected q to quit outside text input")
	}
}

func TestDisplayScopeUsesCompactAzureScopeLabels(t *testing.T) {
	tests := []struct {
		name      string
		scopeType pim.ScopeType
		want      string
	}{
		{name: "management group", scopeType: pim.ScopeTypeManagementGroup, want: "MG: scope-name"},
		{name: "subscription", scopeType: pim.ScopeTypeSubscription, want: "Sub: scope-name"},
		{name: "resource group", scopeType: pim.ScopeTypeResourceGroup, want: "RG: scope-name"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assignment := pim.EligibleAssignment{Scope: pim.Scope{DisplayName: "scope-name", Type: test.scopeType}}
			if got := displayScope(assignment); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestWindowResizeUsesAvailableWidthAndHeight(t *testing.T) {
	model := NewModel(Runtime{})
	next, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	model = next.(Model)

	if got, want := model.frameWidth(), 154; got != want {
		t.Fatalf("expected frame width %d, got %d", want, got)
	}
	if got, want := model.assignmentVisibleRows(), 31; got != want {
		t.Fatalf("expected %d visible assignment rows, got %d", want, got)
	}
}

func TestAssignmentColumnsFavorScopeText(t *testing.T) {
	model := NewModel(Runtime{})
	next, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	model = next.(Model)

	if model.scopeColumnWidth() <= model.roleColumnWidth() {
		t.Fatalf("expected scope column wider than role column, got role=%d scope=%d", model.roleColumnWidth(), model.scopeColumnWidth())
	}
}

func TestAssignmentsViewFitsMinimumSupportedTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 20)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:          "assignment",
			DisplayName: "Privileged Role Administrator",
			Scope: pim.Scope{
				DisplayName: "production-management-group-with-long-name",
				Type:        pim.ScopeTypeManagementGroup,
			},
		}
	}
	assignments[0].Active = true

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)

	view := model.View()
	if got, want := lipgloss.Height(view), 26; got > want {
		t.Fatalf("expected assignments view height at most %d, got %d", want, got)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("expected line width at most 80, got %d for %q", width, line)
		}
	}
}

func TestAssignmentsPolicyLoadingFitsMinimumSupportedTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 20)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:          fmt.Sprintf("assignment-%d", index),
			DisplayName: "Privileged Role Administrator",
			Scope: pim.Scope{
				DisplayName: "production-management-group-with-long-name",
				Type:        pim.ScopeTypeManagementGroup,
			},
		}
	}

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	model.policiesReady = false
	model.preparingPolicies = true
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)
	view := model.View()

	if got, want := lipgloss.Height(view), 26; got > want {
		t.Fatalf("expected policy-loading assignments height at most %d, got %d for %q", want, got, view)
	}
	if !strings.Contains(view, "Loading activation requirements") || !strings.Contains(view, "select all") {
		t.Fatalf("expected loading state and footer to remain visible, got %q", view)
	}
}

func TestAssignmentsValidationErrorFitsMinimumSupportedTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 20)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID:          "assignment",
			DisplayName: "Privileged Role Administrator",
			Scope: pim.Scope{
				DisplayName: "production-management-group-with-long-name",
				Type:        pim.ScopeTypeManagementGroup,
			},
		}
	}

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	view := model.View()
	if want := "select at least one assignment to continue"; !strings.Contains(view, want) {
		t.Fatalf("expected validation error %q to remain visible, got %q", want, view)
	}
	if got, want := lipgloss.Height(view), 26; got > want {
		t.Fatalf("expected assignments view height at most %d, got %d", want, got)
	}
}

func TestNewModelPausesGraphPIMSections(t *testing.T) {
	entra := &scriptedProvider{}
	azureResources := &scriptedProvider{}
	groups := &scriptedProvider{}
	model := NewModel(Runtime{
		Entra:          entra,
		AzureResources: azureResources,
		Groups:         groups,
	})

	if len(model.sections) != 1 || model.sections[0] != SectionAzureResources {
		t.Fatalf("expected only Azure Resources, got %#v", model.sections)
	}
	if model.selectedSection != SectionAzureResources {
		t.Fatalf("expected Azure Resources selected, got %s", model.selectedSection)
	}
	view := model.View()
	if !strings.Contains(view, "Entra Roles and Groups are paused") || !strings.Contains(view, "Microsoft Graph PIM permissions") {
		t.Fatalf("expected paused-section guidance, got %q", view)
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = runCommand(next.(Model), cmd)
	if azureResources.discoverCalls != 1 || entra.discoverCalls != 0 || groups.discoverCalls != 0 {
		t.Fatalf("expected only Azure Resources discovery, got entra=%d azure=%d groups=%d", entra.discoverCalls, azureResources.discoverCalls, groups.discoverCalls)
	}
}

func TestToggleAllFilteredSkipsActiveAssignmentsThroughUpdate(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "inactive", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true},
	})
	model.listCursor = 1

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = next.(Model)

	selected := model.assignmentList.selected()
	if len(selected) != 1 || selected[0].ID != "inactive" || model.listCursor != 1 {
		t.Fatalf("expected select-all to skip active assignment and retain focus, cursor=%d selected=%#v", model.listCursor, selected)
	}
}

func TestInitStartsTenantAndUpdateChecksTogether(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	model := NewModel(Runtime{
		Tenants: tenantProviderFunc(func(context.Context) ([]azureauth.Tenant, error) {
			started <- "tenants"
			<-release
			return []azureauth.Tenant{{ID: "tenant-1"}}, nil
		}),
		CheckUpdate: func(context.Context) (string, error) {
			started <- "update"
			<-release
			return "v0.1.2", nil
		},
	})
	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected Init batch, got %T", model.Init()())
	}
	done := make(chan struct{}, len(batch))
	for _, command := range batch {
		go func() {
			_ = command()
			done <- struct{}{}
		}()
	}
	seen := map[string]bool{<-started: true, <-started: true}
	close(release)
	for range batch {
		<-done
	}
	if !seen["tenants"] || !seen["update"] {
		t.Fatalf("expected both checks to start, got %#v", seen)
	}
}

func TestUpdateCheckStoresOnlySuccessfulAvailability(t *testing.T) {
	model := NewModel(Runtime{})
	next, _ := model.Update(updateCheckedMsg{version: " v0.1.2 "})
	model = next.(Model)
	if model.availableUpdate != "v0.1.2" {
		t.Fatalf("expected available version, got %q", model.availableUpdate)
	}
	want := errors.New("existing workflow error")
	model.err = want
	next, _ = model.Update(updateCheckedMsg{version: "v0.1.3", err: errors.New("proxy unavailable")})
	model = next.(Model)
	if model.availableUpdate != "v0.1.2" || !errors.Is(model.err, want) {
		t.Fatalf("update failure changed model state: version=%q err=%v", model.availableUpdate, model.err)
	}
}

func TestUpdateNoticeAppearsOnlyOnHome(t *testing.T) {
	model := NewModel(Runtime{})
	model.availableUpdate = "v0.1.2"
	model.screen = ScreenHome
	home := model.View()
	for _, want := range []string{"Update v0.1.2 available", "pim-manager update"} {
		if !strings.Contains(home, want) {
			t.Fatalf("expected home notice %q, got %q", want, home)
		}
	}
	model.screen = ScreenTenants
	if view := model.View(); strings.Contains(view, "pim-manager update") {
		t.Fatalf("tenant screen included update notice: %q", view)
	}
	model.screen = ScreenAssignments
	if view := model.View(); strings.Contains(view, "pim-manager update") {
		t.Fatalf("assignment screen included update notice: %q", view)
	}
}

func TestHomeUpdateNoticeFitsMinimumTerminal(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenHome
	model.availableUpdate = "v0.1.2"
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	view := next.(Model).View()
	if got := lipgloss.Height(view); got > 26 {
		t.Fatalf("expected home height at most 26, got %d for %q", got, view)
	}
	if !strings.Contains(view, "enter  open") {
		t.Fatalf("expected home footer to remain visible, got %q", view)
	}
}
