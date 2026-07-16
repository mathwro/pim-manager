# Clean Justification Textarea Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the Bubbles textarea's internal prompt rail and focused-line background from the empty justification field while retaining the existing outer border, muted placeholder, cursor, dimensions, validation, and keyboard behavior.

**Architecture:** Configure the existing `textarea.Model` once in `NewModel`; do not add a wrapper or custom renderer. Protect the visible contract with one focused rendering regression test.

**Tech Stack:** Go 1.24.2, Bubble Tea, Bubbles textarea, Lip Gloss, standard `testing`.

## Global Constraints

- Modify only `internal/tui/model.go` and `internal/tui/model_test.go`.
- Add no dependencies, custom textarea, or layout changes.
- Preserve the 80x26 minimum layout and all form behavior.

---

### Task 1: Remove Conflicting Textarea Defaults

**Files:**
- Modify: `internal/tui/model.go:105-111`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `textarea.New()`, existing `mutedStyle`, and `Model.openActivationForm()`.
- Produces: the same `Model` API with a clean empty focused justification field.

- [ ] **Step 1: Write the failing rendering test**

Add a focused activation-form test that creates one selected assignment, opens the form, renders `model.justification.View()`, and asserts that the empty field contains the placeholder but neither the Bubbles prompt rail (`┃`) nor the focused cursor-line background escape (`\x1b[40m`). Temporarily force a dark ANSI color profile and restore the prior Lip Gloss profile/background with `defer`.

```go
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

	if !strings.Contains(field, "Why is this access needed?") {
		t.Fatalf("expected placeholder, got %q", field)
	}
	if strings.Contains(field, "┃") {
		t.Fatalf("expected no internal prompt rail, got %q", field)
	}
	if strings.Contains(field, "\x1b[40m") {
		t.Fatalf("expected no focused-line background, got %q", field)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/tui -run TestActivationFormRendersCleanEmptyJustification -count=1
```

Expected: FAIL because the current Bubbles defaults render `┃` and the dark cursor-line background.

- [ ] **Step 3: Configure the existing textarea**

In `NewModel`, immediately after `textarea.New()`:

```go
justification.Prompt = ""
justification.FocusedStyle.CursorLine = justification.FocusedStyle.Base
justification.FocusedStyle.Placeholder = mutedStyle
justification.BlurredStyle.Placeholder = mutedStyle
```

Keep the existing placeholder, character limit, hidden line numbers, and height unchanged.

- [ ] **Step 4: Verify GREEN and regressions**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go
go test ./internal/tui -run TestActivationFormRendersCleanEmptyJustification -count=1
go test ./internal/tui -count=1
go test ./... -count=1
go build ./...
```

Expected: all commands pass; the focused field retains its placeholder without internal rails or a cursor-line background.

- [ ] **Step 5: Smoke the actual TUI**

Run `pim-manager`, open Azure Resources, select one inactive assignment, and open the activation request. Confirm the empty justification field has only the cyan outer border, muted placeholder, and cursor. Exit with Esc/q before confirmation; submit no activation.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "fix: clean justification textarea rendering"
```
