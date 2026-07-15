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
- Consumes: `Model.width`, `Model.height`, `tea.WindowSizeMsg`, and the existing `contentWidth()` calculation.
- Produces: `frameWidth() int` without a maximum, `assignmentVisibleRows() int` without a maximum, and a role/scope split favoring scope text.

- [ ] **Step 1: Write failing resize and column tests**

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

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestWindowResizeUsesAvailableWidthAndHeight|TestAssignmentColumnsFavorScopeText' -count=1
```

Expected: FAIL with frame width `104`, visible rows `12`, and the role column wider than the scope column.

- [ ] **Step 3: Remove width and height caps and rebalance columns**

Replace the three helpers in `internal/tui/view.go`:

```go
func (m Model) frameWidth() int {
	return max(42, m.width-6)
}

func (m Model) assignmentVisibleRows() int {
	return max(4, m.height-19)
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

- [ ] **Step 4: Format and run focused tests**

Run:

```bash
gofmt -w internal/tui/model_test.go internal/tui/view.go
go test ./internal/tui -run 'TestWindowResizeUsesAvailableWidthAndHeight|TestAssignmentColumnsFavorScopeText' -count=1
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

1. At approximately `80x24`, rows stay one line and long role/scope values end with `...` inside the frame.
2. At approximately `160x50`, the frame expands beyond 104 columns, more than 12 assignments are visible when available, and the scope column is wider than the role column.
3. Scope prefixes render as `MG:`, `Sub:`, and `RG:`.

- [ ] **Step 7: Commit fluid sizing**

```bash
git add internal/tui/model_test.go internal/tui/view.go
git commit -m "feat: make TUI sizing fluid"
```
