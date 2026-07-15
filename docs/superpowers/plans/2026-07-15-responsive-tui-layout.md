# Responsive TUI Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make assignment scope labels compact and let the TUI consume available terminal width and height.

**Architecture:** Keep `tea.WindowSizeMsg` as the only resize input and adjust the existing pure layout helpers in `internal/tui/view.go`. No new components or dependencies are needed; rendering continues through the current `Model.View` flow.

**Tech Stack:** Go, Bubble Tea, Lip Gloss, standard `testing` package.

## Global Constraints

- Scope labels are `MG`, `Sub`, and `RG`; scope display names remain unchanged.
- Assignment rows remain one line and truncate overflowing text with an ellipsis.
- The frame has no maximum width; assignment rows have no maximum visible-row count.
- Keep the existing frame and row minimums.
- Support the assignments screen from `80x26` upward; wrapped footer lines reduce visible rows without lowering the four-row minimum.
- Give the scope column approximately 60 percent of assignment table width.
- Do not change providers, domain models, or add dependencies.

---

### Task 1: Compact Azure scope labels

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go:442-457`

**Interfaces:**
- Consumes: `pim.EligibleAssignment`, `pim.ScopeTypeManagementGroup`, `pim.ScopeTypeSubscription`, `pim.ScopeTypeResourceGroup`.
- Produces: `displayScope(pim.EligibleAssignment) string` rendering `MG`, `Sub`, or `RG` before the unchanged scope name.

- [ ] **Step 1: Write the failing scope-label test**

Append to `internal/tui/model_test.go`:

```go
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
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./internal/tui -run TestDisplayScopeUsesCompactAzureScopeLabels -count=1
```

Expected: FAIL because `displayScope` returns `Management Group: scope-name`, `Subscription: scope-name`, or `Resource Group: scope-name`.

- [ ] **Step 3: Implement compact scope labels**

In `displayScope` in `internal/tui/view.go`, map only the three Azure resource scope types before the existing empty-value handling:

```go
func displayScope(assignment pim.EligibleAssignment) string {
	name := strings.TrimSpace(assignment.Scope.DisplayName)
	scopeType := strings.TrimSpace(string(assignment.Scope.Type))
	switch assignment.Scope.Type {
	case pim.ScopeTypeManagementGroup:
		scopeType = "MG"
	case pim.ScopeTypeSubscription:
		scopeType = "Sub"
	case pim.ScopeTypeResourceGroup:
		scopeType = "RG"
	}
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
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
go test ./internal/tui -run TestDisplayScopeUsesCompactAzureScopeLabels -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit compact labels**

```bash
git add internal/tui/model_test.go internal/tui/view.go
git commit -m "feat: abbreviate Azure scope labels"
```

---

### Task 2: Fluid terminal sizing

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go:378-408`

**Interfaces:**
- Consumes: `Model.width`, `Model.height`, `tea.WindowSizeMsg`, the existing `contentWidth()` calculation, and the rendered assignment footer.
- Produces: `frameWidth() int` without a maximum, footer-aware `assignmentVisibleRows() int` without a maximum, and a role/scope split favoring scope text.

- [ ] **Step 1: Write failing resize, column, and minimum-terminal tests**

Append to `internal/tui/model_test.go`:

```go
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
```

Add `github.com/charmbracelet/lipgloss` to the test imports, then append:

```go
func TestAssignmentsViewFitsMinimumSupportedTerminal(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 20)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{ID: "assignment", DisplayName: "Global Reader"}
	}

	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	next, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)

	if got, want := lipgloss.Height(model.View()), 26; got > want {
		t.Fatalf("expected assignments view height at most %d, got %d", want, got)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestWindowResizeUsesAvailableWidthAndHeight|TestAssignmentColumnsFavorScopeText|TestAssignmentsViewFitsMinimumSupportedTerminal' -count=1
```

Expected: FAIL with frame width `104`, the role column wider than the scope column, and the `80x26` rendered view exceeding 26 rows.

- [ ] **Step 3: Remove width and height caps and rebalance columns**

Replace the three helpers in `internal/tui/view.go`:

```go
func (m Model) frameWidth() int {
	return max(42, m.width-6)
}

func (m Model) assignmentVisibleRows() int {
	footerExtraRows := max(0, lipgloss.Height(m.assignmentFooter())-2)
	return max(4, m.height-19-footerExtraRows)
}

func (m Model) roleColumnWidth() int {
	return max(14, (m.contentWidth()-12)*2/5)
}
```

Keep `contentWidth()` and `scopeColumnWidth()` unchanged. The scope helper receives the remaining table width:

```go
func (m Model) scopeColumnWidth() int {
	return max(10, m.contentWidth()-m.roleColumnWidth()-10)
}
```

Use one footer source for rendering and sizing. Replace the assignment screen's normal/search footer branches with `b.WriteString(m.assignmentFooter())`, then add:

```go
func (m Model) assignmentFooter() string {
	if m.searchMode {
		return m.footer([]keyHint{{"type", "filter"}, {"enter", "apply"}, {"esc", "close search"}})
	}
	return m.footer([]keyHint{{"space", "select"}, {"a", "select all"}, {"/", "search"}, {"i", "details"}, {"enter", "continue"}, {"esc", "back"}})
}
```

- [ ] **Step 4: Format and run focused tests**

Run:

```bash
gofmt -w internal/tui/model_test.go internal/tui/view.go
go test ./internal/tui -run 'TestWindowResizeUsesAvailableWidthAndHeight|TestAssignmentColumnsFavorScopeText|TestAssignmentsViewFitsMinimumSupportedTerminal' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run full automated verification**

Run:

```bash
go test ./... -count=1
go build ./...
```

Expected: all packages pass and the build exits successfully.

- [ ] **Step 6: Smoke-check narrow and wide terminals**

Build and launch the real application:

```bash
go build -o /tmp/pim-manager-responsive .
/tmp/pim-manager-responsive
```

Open **Azure Resources** and verify:

1. At approximately `80x26`, rows stay one line, the footer wraps without overflowing the frame, and long role/scope values end with `...`.
2. At approximately `160x50`, the frame expands beyond 104 columns, more than 12 assignments are visible when available, and the scope column is wider than the role column.
3. Scope prefixes render as `MG:`, `Sub:`, and `RG:`.

- [ ] **Step 7: Commit fluid sizing**

```bash
git add internal/tui/model_test.go internal/tui/view.go
git commit -m "feat: make TUI sizing fluid"
```
