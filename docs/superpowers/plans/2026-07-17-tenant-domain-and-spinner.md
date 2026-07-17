# Tenant Domain Label and Startup Spinner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show tenant domains beside tenant names and animate the spinner during initial tenant loading.

**Architecture:** Keep presentation formatting in the existing shared `tenantLabel` helper so the menu and home panel stay consistent. Start the initial spinner with the same Bubble Tea batch pattern already used by tenant refresh, assignment discovery, and activation.

**Tech Stack:** Go, Bubble Tea, Bubbles spinner, existing TUI test package.

## Global Constraints

- Format both values exactly as `Display Name (default.domain)`.
- Preserve display-name-only, domain-only, and tenant-ID-only fallbacks.
- Keep tenant IDs on menu rows' second line.
- Change no authentication, tenant discovery, or Azure CLI behavior.
- Add no dependencies or new abstractions.

---

### Task 1: Format tenant name and domain

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`

**Interfaces:**
- Preserves: `tenantLabel(azureauth.Tenant) string`.
- Changes: tenants with both `DisplayName` and `DefaultDomain` render as `DisplayName (DefaultDomain)`.

- [ ] **Step 1: Write the failing label test**

Add to `internal/tui/model_test.go`:

```go
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
```

- [ ] **Step 2: Run the label test and verify RED**

Run:

```bash
go test ./internal/tui -run TestTenantLabel -count=1
```

Expected: `name and domain` fails with `expected "Contoso (contoso.onmicrosoft.com)", got "Contoso"`.

- [ ] **Step 3: Implement the minimal shared formatter**

Replace `tenantLabel` in `internal/tui/view.go` with:

```go
func tenantLabel(tenant azureauth.Tenant) string {
	if tenant.DisplayName != "" && tenant.DefaultDomain != "" {
		return fmt.Sprintf("%s (%s)", tenant.DisplayName, tenant.DefaultDomain)
	}
	if tenant.DisplayName != "" {
		return tenant.DisplayName
	}
	if tenant.DefaultDomain != "" {
		return tenant.DefaultDomain
	}
	return tenant.ID
}
```

- [ ] **Step 4: Verify GREEN**

Run:

```bash
gofmt -w internal/tui/model_test.go internal/tui/view.go
go test ./internal/tui -run 'TestTenant(Label|MenuOnlyRenders)' -count=1
```

Expected: PASS; the existing menu/home test also remains green.

---

### Task 2: Start spinner ticks during initial tenant loading

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/model.go`

**Interfaces:**
- Preserves: `Model.Init() tea.Cmd`.
- Changes: tenant-enabled startup returns a `tea.Batch` containing tenant lookup and `m.spinner.Tick` commands.

- [ ] **Step 1: Write the failing startup spinner test**

Add the spinner import to `internal/tui/model_test.go`:

```go
"github.com/charmbracelet/bubbles/spinner"
```

Add this test:

```go
func TestInitStartsTenantLookupSpinner(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{{ID: "tenant-1"}}}}
	model := NewModel(Runtime{Tenants: provider})

	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected startup command batch")
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
```

- [ ] **Step 2: Run the spinner test and verify RED**

Run:

```bash
go test ./internal/tui -run TestInitStartsTenantLookupSpinner -count=1
```

Expected: FAIL with `expected startup command batch` because `Init` currently returns only the tenant lookup command.

- [ ] **Step 3: Batch tenant lookup with the initial spinner tick**

Replace the return in `Model.Init` with:

```go
return tea.Batch(m.checkTenants(m.tenantCheck), m.spinner.Tick)
```

- [ ] **Step 4: Verify GREEN and full behavior**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go
go test ./internal/tui -count=1
go test ./... -count=1
go build ./...
```

Expected: all commands pass.

Smoke the app with a delayed fake `az account tenant list` command and confirm at least two distinct spinner frames render before tenant loading completes. Confirm the resulting tenant menu shows `Contoso (contoso.onmicrosoft.com)` with the tenant ID beneath it.

- [ ] **Step 5: Commit both fixes**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/view.go
git commit -m "fix: improve tenant loading UI"
```
