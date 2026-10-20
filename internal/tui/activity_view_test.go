package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mathwro/pim-manager/internal/pim"
)

// These boundaries catch a dock that displaces selection or keyboard controls,
// wraps long activity labels, or leaves a resized summary outside the frame.
func TestActivityLayoutKeepsSelectionAndControlsVisible(t *testing.T) {
	for _, size := range []struct{ width, height int }{{96, 30}, {80, 24}, {48, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			model := NewModel(Runtime{})
			model.screen = ScreenAssignments
			model.activeSection = SectionAzureResources
			model.policiesReady = true
			var assignments []pim.EligibleAssignment
			for index := range 30 {
				assignments = append(assignments, pim.EligibleAssignment{ID: fmt.Sprint(index), DisplayName: fmt.Sprintf("Role-%02d", index)})
			}
			model.assignmentList = newAssignmentList(assignments)
			model.listCursor = 29
			for index := range 9 {
				model.recordActivity(fmt.Sprintf("event-%d", index), false)
			}
			next, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			view := next.(Model).View()
			assertActivityFrameFits(t, view, size.width, size.height)
			for _, visible := range []string{"Role-29", "space", "enter", "esc", "event-8"} {
				if !strings.Contains(view, visible) {
					t.Fatalf("expected selection, recent activity, and controls to remain visible: missing %q in %q", visible, view)
				}
			}
			if strings.Contains(view, "event-0") {
				t.Fatalf("old activity should not crowd the primary list: %q", view)
			}
			if strings.Index(view, "event-8") > strings.LastIndex(view, "esc") {
				t.Fatalf("activity must stay above keyboard controls: %q", view)
			}
		})
	}
}

func TestActivityLayoutTruncatesLongLabelsWithoutWrapping(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenProgress
	model.activationTotal = 3
	model.activationDone = 1
	model.activationCurrent = strings.Repeat("長い役割", 40)
	model.operationStarted = map[string]time.Time{"activation": time.Now().Add(-5 * time.Second)}
	model.recordActivity(strings.Repeat("界", 150), false)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
	view := next.(Model).View()
	assertActivityFrameFits(t, view, 48, 24)
	if !strings.Contains(view, "ctrl+c") || !strings.Contains(view, "1/3") {
		t.Fatalf("bounded progress must preserve actual completion and quit control: %q", view)
	}
}

func TestActivityLayoutCollapsesHistoryOnShortTerminal(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenHome
	for index := range 8 {
		model.recordActivity(fmt.Sprintf("recent-%d", index), false)
	}
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := next.(Model).View()
	assertActivityFrameFits(t, view, 80, 24)
	if !strings.Contains(view, "recent-7") || strings.Contains(view, "recent-6") {
		t.Fatalf("short terminals should show one latest status row, not history: %q", view)
	}
}

func TestActivityLayoutResizesSummaryAroundDock(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenSummary
	results := make([]pim.ActivationResult, 20)
	for index := range results {
		results[index] = pim.ActivationResult{
			Assignment: pim.EligibleAssignment{ID: fmt.Sprint(index), DisplayName: fmt.Sprintf("Summary-%02d", index)},
			Status:     pim.ActivationStatusFailed,
			Message:    strings.Repeat("diagnostic ", 10),
		}
	}
	model.summary = newSummary(results)
	model.recordActivity("Latest-result", true)
	for _, size := range []struct{ width, height int }{{96, 30}, {80, 24}, {48, 24}} {
		next, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		model = next.(Model)
		view := model.View()
		assertActivityFrameFits(t, view, size.width, size.height)
		for _, visible := range []string{"Summary-00", "Latest-result", "up/down", "esc", "q"} {
			if !strings.Contains(view, visible) {
				t.Fatalf("summary content, activity, and controls must survive resize: missing %q in %q", visible, view)
			}
		}
	}
}

func TestActivityLayoutPreservesWorkflowScreens(t *testing.T) {
	for _, screen := range []Screen{ScreenTenants, ScreenHome, ScreenActivation, ScreenConfirmation} {
		for _, size := range []struct{ width, height int }{{96, 30}, {80, 24}, {48, 24}} {
			t.Run(fmt.Sprintf("%s/%dx%d", screen, size.width, size.height), func(t *testing.T) {
				model := NewModel(Runtime{})
				model.screen = screen
				model.policiesReady = true
				model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "selected", DisplayName: "Selected-role"}})
				model.assignmentList.toggle("selected")
				model.form.durations = map[string]string{"selected": "PT1H"}
				model.recordActivity("Workflow-event", false)
				next, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				view := next.(Model).View()
				assertActivityFrameFits(t, view, size.width, size.height)
				if !strings.Contains(view, "enter") || !strings.Contains(view, "Workflow-event") {
					t.Fatalf("workflow and its controls must remain usable: %q", view)
				}
			})
		}
	}
}

func TestActivityLayoutKeepsLongDetailsScrollable(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.policiesReady = true
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: strings.Repeat("a", 36), DisplayName: "Detailed role", Source: pim.AssignmentSourceAzureResource,
		Kind: pim.AssignmentKindAzureRole, PrincipalID: strings.Repeat("b", 36),
		RoleDefinitionID: strings.Repeat("c", 100), EligibilityScheduleID: strings.Repeat("d", 100),
		Scope:     pim.Scope{Type: pim.ScopeTypeSubscription, DisplayName: "Development"},
		Condition: "Final condition marker", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H", MFARequired: true},
	}})
	model.recordActivity("Details activity", false)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	model = next.(Model)
	assertActivityFrameFits(t, model.View(), 80, 24)
	for range 40 {
		next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = next.(Model)
	}
	view := model.View()
	assertActivityFrameFits(t, view, 80, 24)
	for _, text := range []string{"Final condition marker", "Details activity", "esc"} {
		if !strings.Contains(view, text) {
			t.Fatalf("details or controls inaccessible: missing %q in %q", text, view)
		}
	}
}

func assertActivityFrameFits(t *testing.T, view string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got > height {
		t.Fatalf("frame height %d exceeds terminal height %d: %q", got, height, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("frame width %d exceeds terminal width %d: %q", got, width, line)
		}
	}
}
