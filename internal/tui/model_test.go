package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mathwro/pim-manager/internal/activation"
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
	case assignmentsDiscoveredMsg, activationCompletedMsg, accountCheckedMsg:
		next, _ := model.Update(msg)
		model = next.(Model)
	}
	return model
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
	discoveries   [][]pim.EligibleAssignment
	discoverCalls int
	results       []pim.ActivationResult
	activateErr   []error
	discoverErr   error
	activated     []pim.ActivationRequest
}

func (p *scriptedProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
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

func (p *scriptedProvider) Activate(_ context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
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

type scriptedAccountProvider struct {
	accounts []azureauth.Account
	errs     []error
	calls    int
}

func (p *scriptedAccountProvider) Account(context.Context) (azureauth.Account, error) {
	p.calls++
	var err error
	if len(p.errs) > 0 {
		err = p.errs[0]
		p.errs = p.errs[1:]
	}
	if err != nil {
		return azureauth.Account{}, err
	}
	var account azureauth.Account
	if len(p.accounts) > 0 {
		account = p.accounts[0]
		p.accounts = p.accounts[1:]
	}
	return account, err
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
		MFACommand: func(string) (*exec.Cmd, error) {
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

func TestConfirmationGatesMixedBatchOnMFA(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusActivated},
	}}
	mfaCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		MFACommand: func(tenantID string) (*exec.Cmd, error) {
			mfaCalls++
			if tenantID != "tenant-1" {
				t.Fatalf("expected tenant-1, got %q", tenantID)
			}
			return exec.Command("true"), nil
		},
	})
	model.account = azureauth.Account{TenantID: "tenant-1"}
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
	next, cmd = model.Update(mfaCompletedMsg{selected: selected})
	model = runCommand(next.(Model), cmd)
	if len(provider.activated) != 2 || model.screen != ScreenSummary {
		t.Fatalf("expected full batch after MFA, requests=%#v screen=%s", provider.activated, model.screen)
	}
}

func TestMFAFailureReturnsToConfirmationWithoutActivation(t *testing.T) {
	provider := &scriptedProvider{}
	model := NewModel(Runtime{AzureResources: provider})
	model.screen = ScreenConfirmation

	next, cmd := model.Update(mfaCompletedMsg{
		selected: []pim.EligibleAssignment{{ID: "owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true}}},
		err:      errors.New("login canceled"),
	})
	model = next.(Model)

	if cmd != nil || model.screen != ScreenConfirmation || len(provider.activated) != 0 {
		t.Fatalf("expected blocked activation, requests=%#v screen=%s", provider.activated, model.screen)
	}
	if !strings.Contains(model.View(), "MFA authentication failed: login canceled") {
		t.Fatalf("expected actionable MFA error, got %q", model.View())
	}
}

func TestAssignmentDetailsShowsMFARequirement(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenDetails
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}})
	if view := model.View(); !strings.Contains(view, "MFA") || !strings.Contains(view, "Required") {
		t.Fatalf("expected MFA requirement in details, got %q", view)
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
	if view := model.View(); !strings.Contains(view, "MFA is required before activation") {
		t.Fatalf("expected MFA confirmation warning, got %q", view)
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

func TestHomeShowsLoginGuidanceAndRetriesAccountLookup(t *testing.T) {
	account := &scriptedAccountProvider{
		accounts: []azureauth.Account{{SubscriptionID: "sub-1", TenantID: "tenant-1", UserName: "user@example.com"}},
		errs:     []error{azureauth.ErrNotLoggedIn, nil},
	}
	model := NewModel(Runtime{Account: account})

	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected Init command to check account")
	}
	model = runCommand(model, cmd)
	view := model.View()
	if !strings.Contains(view, "az login") || !strings.Contains(view, "check sign-in") {
		t.Fatalf("expected login guidance and retry hint, got %q", view)
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = runCommand(next.(Model), cmd)
	view = model.View()
	if !strings.Contains(view, "user@example.com") || !strings.Contains(view, "sub-1") {
		t.Fatalf("expected account context in view after retry, got %q", view)
	}
	if account.calls != 2 {
		t.Fatalf("expected two account calls, got %d", account.calls)
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
