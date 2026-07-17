# Tenant Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add session-local Microsoft Entra tenant selection before PIM-area selection and scope every Azure CLI token operation to that tenant.

**Architecture:** `internal/azureauth` lists tenants and carries the selected tenant through `context.Context`; the existing ARM client and provider interfaces remain unchanged. `internal/tui` owns tenant loading/selection, clears tenant-specific state on switches, and rejects stale async results by tenant and request generation.

**Tech Stack:** Go, Azure CLI, Cobra, Bubble Tea, Bubbles, standard `context` and `encoding/json` packages.

## Global Constraints

- Work on branch `feat/tenant-selection`.
- Never run `az account set` or mutate Azure CLI configuration.
- Keep policy-required step-up on the existing `az login --tenant` path.
- Preserve checked ARM token pinning and post-step-up principal drift validation.
- Do not add dependencies, persistence, tenant search, subscription selection, or paused provider behavior.
- Follow test-driven development: each production change follows a focused failing test.

---

### Task 1: Tenant-aware Azure CLI wrapper

**Files:**
- Modify: `internal/azureauth/auth_test.go`
- Modify: `internal/azureauth/auth.go`

**Interfaces:**
- Produces: `azureauth.Tenant { ID, DisplayName, DefaultDomain string }`.
- Produces: `CLI.Tenants(context.Context) ([]Tenant, error)`.
- Produces: `azureauth.WithTenant(context.Context, string) context.Context` and `azureauth.TenantFromContext(context.Context) string`.
- Changes: `CLI.AccessToken` adds `--tenant <id>` only when the context contains a tenant.

- [ ] **Step 1: Replace current-account tests with failing tenant-list tests**

Replace the `TestAccount...` tests at the start of `internal/azureauth/auth_test.go` with:

```go
func TestTenantsReturnsDistinctAzureTenants(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`[
			{"tenantId":"tenant-1","displayName":"Contoso","defaultDomain":"contoso.onmicrosoft.com"},
			{"tenantId":"tenant-2","defaultDomain":"fabrikam.onmicrosoft.com"},
			{"tenantId":"tenant-1","displayName":"duplicate"},
			{"tenantId":"  "}
		]`),
	}}

	tenants, err := NewCLI(runner.Run).Tenants(context.Background())
	if err != nil {
		t.Fatalf("Tenants returned error: %v", err)
	}
	want := []Tenant{
		{ID: "tenant-1", DisplayName: "Contoso", DefaultDomain: "contoso.onmicrosoft.com"},
		{ID: "tenant-2", DefaultDomain: "fabrikam.onmicrosoft.com"},
	}
	if !reflect.DeepEqual(tenants, want) {
		t.Fatalf("expected %#v, got %#v", want, tenants)
	}
}

func TestTenantsReturnsLoginHintWhenNoneAreUsable(t *testing.T) {
	client := NewCLI(fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`[{"tenantId":" "}]`),
	}}.Run)

	_, err := client.Tenants(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
}

func TestTenantsPreservesContextErrors(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
				return nil, contextErr
			})
			_, err := client.Tenants(context.Background())
			if !errors.Is(err, contextErr) || errors.Is(err, ErrNotLoggedIn) {
				t.Fatalf("expected unchanged %v, got %v", contextErr, err)
			}
		})
	}
}

func TestTenantsWrapsAzureCLIErrorWithLoginHint(t *testing.T) {
	commandErr := errors.New("az account tenant list failed")
	client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return nil, commandErr
	})

	_, err := client.Tenants(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), commandErr.Error()) {
		t.Fatalf("expected login hint with command details, got %v", err)
	}
}

func TestTenantsRejectsInvalidJSON(t *testing.T) {
	client := NewCLI(fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`not-json`),
	}}.Run)
	if _, err := client.Tenants(context.Background()); err == nil || !strings.Contains(err.Error(), "parse az account tenant list output") {
		t.Fatalf("expected parse error, got %v", err)
	}
}
```

Add this token-scoping test after `TestAccessTokenUsesRequestedResource`:

```go
func TestAccessTokenUsesTenantFromContext(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account get-access-token --resource https://management.core.windows.net/ --tenant tenant-1 --output json": []byte(`{"accessToken":"abc"}`),
	}}
	ctx := WithTenant(context.Background(), " tenant-1 ")

	token, err := NewCLI(runner.Run).AccessToken(ctx, "https://management.core.windows.net/")
	if err != nil || token != "abc" {
		t.Fatalf("expected tenant-scoped token abc, got %q, %v", token, err)
	}
	if got := TenantFromContext(ctx); got != "tenant-1" {
		t.Fatalf("expected tenant-1 in context, got %q", got)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/azureauth -run 'Test(Tenants|AccessTokenUsesTenant)' -count=1
```

Expected: compile failure because `Tenant`, `CLI.Tenants`, `WithTenant`, and `TenantFromContext` do not exist.

- [ ] **Step 3: Implement tenant listing and context scoping**

In `internal/azureauth/auth.go`, replace `Account` and `CLI.Account` with:

```go
type Tenant struct {
	ID            string
	DisplayName   string
	DefaultDomain string
}

type tenantContextKey struct{}

func WithTenant(ctx context.Context, tenantID string) context.Context {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantContextKey{}, tenantID)
}

func TenantFromContext(ctx context.Context) string {
	tenantID, _ := ctx.Value(tenantContextKey{}).(string)
	return tenantID
}

func (c CLI) Tenants(ctx context.Context) ([]Tenant, error) {
	out, err := c.run(ctx, "az", "account", "tenant", "list", "--output", "json")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrNotLoggedIn, err)
	}
	var payload []struct {
		TenantID     string `json:"tenantId"`
		DisplayName  string `json:"displayName"`
		DefaultDomain string `json:"defaultDomain"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse az account tenant list output: %w", err)
	}
	tenants := make([]Tenant, 0, len(payload))
	seen := make(map[string]struct{}, len(payload))
	for _, item := range payload {
		id := strings.TrimSpace(item.TenantID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		tenants = append(tenants, Tenant{
			ID: id,
			DisplayName: strings.TrimSpace(item.DisplayName),
			DefaultDomain: strings.TrimSpace(item.DefaultDomain),
		})
	}
	if len(tenants) == 0 {
		return nil, fmt.Errorf("%w: Azure CLI returned no tenants", ErrNotLoggedIn)
	}
	return tenants, nil
}
```

Change `AccessToken` argument construction to:

```go
func (c CLI) AccessToken(ctx context.Context, resource string) (string, error) {
	args := []string{"account", "get-access-token", "--resource", resource}
	if tenantID := TenantFromContext(ctx); tenantID != "" {
		args = append(args, "--tenant", tenantID)
	}
	args = append(args, "--output", "json")
	out, err := c.run(ctx, "az", args...)
	if err != nil {
		return "", fmt.Errorf("get Azure CLI access token for %s: %w", resource, err)
	}
	var payload struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("parse Azure CLI access token output: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("Azure CLI returned empty access token for %s", resource)
	}
	return payload.AccessToken, nil
}
```

- [ ] **Step 4: Verify GREEN and the complete auth package**

Run:

```bash
go test ./internal/azureauth -count=1
```

Expected: PASS, including existing no-tenant token and step-up tests.

- [ ] **Step 5: Commit the auth boundary**

```bash
git add internal/azureauth/auth.go internal/azureauth/auth_test.go
git commit -m "feat: add tenant-aware Azure CLI auth"
```

---

### Task 2: Tenant selection and stale-result protection in the TUI model

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/model.go`

**Interfaces:**
- Consumes: `azureauth.Tenant`, `azureauth.WithTenant`, and `azureauth.TenantFromContext` from Task 1.
- Produces: `ScreenTenants` and `TenantProvider.Tenants(context.Context) ([]azureauth.Tenant, error)`.
- Changes: `Runtime.Account` becomes `Runtime.Tenants TenantProvider`.
- Preserves: `AssignmentProvider`, `StepUpCommand`, and `ARMAuthentication` signatures.

- [ ] **Step 1: Add failing tenant-flow and context tests**

Replace `scriptedAccountProvider` in `internal/tui/model_test.go` with:

```go
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
```

Update `runCommand` to handle `tenantsCheckedMsg` instead of `accountCheckedMsg`, then replace the old login retry test and add selection/context tests:

```go
func TestOneTenantSkipsTenantSelection(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{{ID: "tenant-1", DefaultDomain: "contoso.com"}}}}
	model := NewModel(Runtime{Tenants: provider})
	if strings.Contains(model.View(), "Choose Azure tenant") {
		t.Fatalf("tenant choice menu must not render before multiple tenants are known: %q", model.View())
	}

	model = runCommand(model, model.Init())
	if model.screen != ScreenHome || model.selectedTenant.ID != "tenant-1" {
		t.Fatalf("expected tenant-1 home, screen=%s tenant=%#v", model.screen, model.selectedTenant)
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
		t.Fatalf("expected successful retry, model=%#v calls=%d", model.selectedTenant, provider.calls)
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

func TestStaleDiscoveryFromPreviousTenantIsIgnored(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedTenant = azureauth.Tenant{ID: "tenant-2"}
	model.discoveryCheck = 2
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "current"}})

	next, _ := model.Update(assignmentsDiscoveredMsg{
		assignments: []pim.EligibleAssignment{{ID: "stale"}},
		tenantID: "tenant-1",
		checkID: 1,
	})
	got := next.(Model)
	if len(got.assignmentList.items) != 1 || got.assignmentList.items[0].ID != "current" {
		t.Fatalf("stale discovery changed assignments: %#v", got.assignmentList.items)
	}
}
```

Add `discoveryTenants []string` to `scriptedProvider` and append `azureauth.TenantFromContext(ctx)` in `Discover`.

- [ ] **Step 2: Run focused model tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'Test(OneTenant|MultipleTenants|TenantLookup|DiscoveryAndAuthenticationUseSelectedTenant|StaleDiscovery)' -count=1
```

Expected: compile failure because tenant runtime/model fields, messages, and screen do not exist.

- [ ] **Step 3: Add tenant state and commands to the model**

In `internal/tui/model.go`:

1. Add `ScreenTenants Screen = "tenants"`.
2. Replace `Account AccountProvider` with `Tenants TenantProvider` and define:

```go
type TenantProvider interface {
	Tenants(context.Context) ([]azureauth.Tenant, error)
}
```

3. Replace account model fields with:

```go
tenants           []azureauth.Tenant
tenantIndex       int
selectedTenant    azureauth.Tenant
checkingTenants   bool
tenantCheck       int
tenantErr         error
discoveryCheck    int
```

4. Replace `accountCheckedMsg` and extend assignment messages:

```go
type tenantsCheckedMsg struct {
	tenants []azureauth.Tenant
	checkID int
	err     error
}

type assignmentsDiscoveredMsg struct {
	assignments []pim.EligibleAssignment
	tenantID    string
	checkID     int
	err         error
}
```

5. After constructing the existing `model := Model{...}` value, initialize tenant startup with:

```go
if runtime.Tenants != nil {
	model.screen = ScreenTenants
	model.checkingTenants = true
	model.tenantCheck = 1
}
```

Replace `Init` with:

```go
func (m Model) Init() tea.Cmd {
	if m.runtime.Tenants == nil {
		return nil
	}
	return m.checkTenants(m.tenantCheck)
}
```

Update spinner ticks to continue while `m.loading || m.checkingTenants`.

6. Handle tenant and stale-discovery messages before key handling:

```go
case tenantsCheckedMsg:
	if typed.checkID != m.tenantCheck {
		return m, nil
	}
	m.checkingTenants = false
	m.tenantErr = typed.err
	if typed.err != nil {
		m.screen = ScreenTenants
		return m, nil
	}
	m.tenants = typed.tenants
	m.clampTenantCursor()
	if len(m.tenants) == 1 {
		m.selectTenant(0)
		return m, nil
	}
	m.screen = ScreenTenants
	if m.selectedTenant.ID != "" {
		index := m.indexOfTenant(m.selectedTenant.ID)
		if index < 0 {
			m.clearTenantWorkflow()
			m.selectedTenant = azureauth.Tenant{}
		} else {
			m.selectedTenant = m.tenants[index]
			m.tenantIndex = index
		}
	}
	return m, nil
case assignmentsDiscoveredMsg:
	if typed.checkID != m.discoveryCheck || typed.tenantID != m.selectedTenant.ID {
		return m, nil
	}
	m.loading = false
	m.err = typed.err
	if typed.err != nil {
		m.assignmentList = newAssignmentList(nil)
		return m, nil
	}
	m.assignmentList = newAssignmentList(typed.assignments)
	m.listCursor = 0
	return m, nil
```

7. Route `ScreenTenants` to `updateTenants`, and add:

```go
func (m Model) updateTenants(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.checkingTenants {
		return m, nil
	}
	switch key.Type {
	case tea.KeyUp:
		m.moveTenant(-1)
	case tea.KeyDown:
		m.moveTenant(1)
	case tea.KeyEnter:
		if m.tenantErr == nil && len(m.tenants) > 0 {
			m.selectTenant(m.tenantIndex)
		}
	case tea.KeyRunes:
		switch string(key.Runes) {
		case "j":
			m.moveTenant(1)
		case "k":
			m.moveTenant(-1)
		case "r":
			m.tenantCheck++
			m.checkingTenants = true
			m.tenantErr = nil
			return m, tea.Batch(m.checkTenants(m.tenantCheck), m.spinner.Tick)
		}
	}
	return m, nil
}

func (m *Model) selectTenant(index int) {
	if index < 0 || index >= len(m.tenants) {
		return
	}
	next := m.tenants[index]
	if m.selectedTenant.ID != next.ID {
		m.clearTenantWorkflow()
	}
	m.selectedTenant = next
	m.tenantIndex = index
	m.screen = ScreenHome
	m.tenantErr = nil
}

func (m *Model) clearTenantWorkflow() {
	m.discoveryCheck++
	m.authenticationCheck++
	m.checkingAuthentication = false
	m.loading = false
	m.activeSection = ""
	m.query = ""
	m.searchMode = false
	m.assignmentList = newAssignmentList(nil)
	m.form = activationForm{durations: map[string]string{}}
	m.justification.SetValue("")
	m.duration.SetValue("")
	m.durationIndex = 0
	m.summary = summary{}
	m.err = nil
}

func (m Model) tenantContext() context.Context {
	return azureauth.WithTenant(context.Background(), m.selectedTenant.ID)
}

func (m *Model) moveTenant(delta int) {
	m.tenantIndex += delta
	m.clampTenantCursor()
}

func (m *Model) clampTenantCursor() {
	if len(m.tenants) == 0 {
		m.tenantIndex = 0
		return
	}
	m.tenantIndex = min(max(m.tenantIndex, 0), len(m.tenants)-1)
}

func (m Model) indexOfTenant(tenantID string) int {
	for index, tenant := range m.tenants {
		if strings.EqualFold(tenant.ID, tenantID) {
			return index
		}
	}
	return -1
}
```

8. Add this case to `updateHome` so account switching is reachable only when useful:

```go
case tea.KeyEsc:
	if len(m.tenants) > 1 {
		m.screen = ScreenTenants
	}
```

9. Replace discovery command construction with tenant/request capture:

```go
func (m Model) beginDiscovery(section Section) (tea.Model, tea.Cmd) {
	m.activeSection = section
	m.screen = ScreenAssignments
	m.loading = true
	m.err = nil
	m.query = ""
	m.searchMode = false
	m.assignmentList = newAssignmentList(nil)
	m.listCursor = 0
	m.discoveryCheck++
	return m, tea.Batch(m.discoverAssignments(m.discoveryCheck, m.selectedTenant.ID), m.spinner.Tick)
}

func (m Model) discoverAssignments(checkID int, tenantID string) tea.Cmd {
	provider := m.providerForSection(m.activeSection)
	if provider == nil {
		return func() tea.Msg {
			return assignmentsDiscoveredMsg{tenantID: tenantID, checkID: checkID, err: fmt.Errorf("provider for %s is unavailable", m.activeSection)}
		}
	}
	ctx := azureauth.WithTenant(context.Background(), tenantID)
	return func() tea.Msg {
		assignments, err := provider.Discover(ctx)
		return assignmentsDiscoveredMsg{assignments: assignments, tenantID: tenantID, checkID: checkID, err: err}
	}
}
```

Replace `context.Background()` with `m.tenantContext()` in `checkAuthentication`, replace `m.account.TenantID` with `m.selectedTenant.ID` in `startStepUp`, and change activation's base context exactly to:

```go
ctx := arm.WithAccessToken(m.tenantContext(), authentication.AccessToken)
```

These are the only provider/auth context changes; the checked token remains pinned.

10. Replace `checkAccount` with:

```go
func (m Model) checkTenants(checkID int) tea.Cmd {
	provider := m.runtime.Tenants
	if provider == nil {
		return nil
	}
	return func() tea.Msg {
		tenants, err := provider.Tenants(context.Background())
		return tenantsCheckedMsg{tenants: tenants, checkID: checkID, err: err}
	}
}
```

- [ ] **Step 4: Verify GREEN and all TUI behavior**

Run:

```bash
go test ./internal/tui -count=1
```

Expected: PASS. Existing runtimes without `TenantProvider` still begin on `ScreenHome`.

- [ ] **Step 5: Commit the model behavior**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat: add tenant selection state"
```

---

### Task 3: Tenant UI and production wiring

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/lazy_providers_test.go`

**Interfaces:**
- Consumes: `ScreenTenants`, `Runtime.Tenants`, and `Model.selectedTenant` from Task 2.
- Produces: tenant selection view, selected-tenant panel, and six-step progress line.
- Changes: production startup invokes only `az account tenant list --output json` during `Init`.

- [ ] **Step 1: Add failing view and app-wiring assertions**

Add to `internal/tui/model_test.go`:

```go
func TestTenantViewAndProgressRenderSelectedContext(t *testing.T) {
	provider := &scriptedTenantProvider{replies: [][]azureauth.Tenant{{
		{ID: "tenant-1", DisplayName: "Contoso", DefaultDomain: "contoso.com"},
		{ID: "tenant-2", DefaultDomain: "fabrikam.com"},
	}}}
	model := NewModel(Runtime{Tenants: provider})
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
```

In `internal/app/lazy_providers_test.go`, change `TestRunInitChecksAccountWithoutSectionDiscovery` to return:

```go
if command == "az account tenant list --output json" {
	return []byte(`[{"tenantId":"tenant-1","defaultDomain":"contoso.com"}]`), nil
}
```

and assert that this is the only command.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
go test ./internal/tui ./internal/app -run 'Test(TenantView|RunInit)' -count=1
```

Expected: FAIL because the tenant view and production tenant provider wiring are absent.

- [ ] **Step 3: Render tenant selection and selected context**

In `internal/tui/view.go`:

1. Route `ScreenTenants` to `viewTenants` in `Model.View`.
2. Add a direct tenant view:

```go
func (m Model) viewTenants() string {
	var b strings.Builder
	title := "Azure CLI sign-in"
	if !m.checkingTenants && m.tenantErr == nil && len(m.tenants) > 1 {
		title = "Choose Azure tenant"
	}
	b.WriteString(m.header(title, "Account"))
	b.WriteString("\n")
	b.WriteString(m.stepLine(0))
	b.WriteString("\n\n")
	if m.checkingTenants {
		b.WriteString(panelStyle.Width(m.contentWidth()-6).Render(fmt.Sprintf("%s  Checking Azure CLI tenants...", m.spinner.View())))
		b.WriteString(m.footer([]keyHint{{"q", "quit"}}))
		return b.String()
	}
	if m.tenantErr != nil {
		guidance := "Resolve the Azure CLI error, then press r to retry."
		if errors.Is(m.tenantErr, azureauth.ErrNotLoggedIn) {
			guidance = "Run az login, then press r to retry."
		}
		b.WriteString(errorStyle.Width(m.contentWidth()-4).Render("Azure sign-in required\n" + guidance + "\n" + mutedStyle.Render(m.tenantErr.Error())))
		b.WriteString(m.footer([]keyHint{{"r", "retry"}, {"?", "help"}, {"q", "quit"}}))
		return b.String()
	}
	for index, tenant := range m.tenants {
		marker := "  "
		style := cardStyle
		if index == m.tenantIndex {
			marker = "> "
			style = activeCardStyle
		}
		body := fmt.Sprintf("%s%s\n  %s", marker, tenantLabel(tenant), mutedStyle.Render(tenant.ID))
		b.WriteString(style.Width(m.contentWidth()-4).Render(body))
		b.WriteString("\n")
	}
	b.WriteString(m.footer([]keyHint{{"up/down", "move"}, {"enter", "select"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"}}))
	return b.String()
}

func tenantLabel(tenant azureauth.Tenant) string {
	if tenant.DisplayName != "" {
		return tenant.DisplayName
	}
	if tenant.DefaultDomain != "" {
		return tenant.DefaultDomain
	}
	return tenant.ID
}
```

3. Replace `accountPanel` with a selected-tenant-only panel:

```go
func (m Model) tenantPanel() string {
	if m.selectedTenant.ID == "" {
		return panelStyle.Width(m.contentWidth()-6).Render(mutedStyle.Render("No Azure tenant selected."))
	}
	identity := successStyle.Render("CONNECTED") + "  " + tenantLabel(m.selectedTenant)
	id := mutedStyle.Render(fmt.Sprintf("%-13s %s", "Tenant", m.selectedTenant.ID))
	return panelStyle.Width(m.contentWidth()-6).Render(identity + "\n" + id)
}
```

Call `tenantPanel` from `viewHome`. Add an Esc/change-account footer hint when more than one tenant exists.

4. Change progress labels to `Account`, `Type`, `Select`, `Request`, `Review`, `Result`; update screen indices to tenants 0, home 1, assignments 2, request 3, confirmation 4, and progress/summary 5.

- [ ] **Step 4: Wire the production tenant provider**

Change `internal/app/app.go` runtime construction to:

```go
runtime := tui.Runtime{
	AzureResources:    azureresources.NewProvider(armClient),
	Tenants:           auth,
	StepUpCommand:     azureauth.StepUpLoginCommand,
	ARMAuthentication: auth.ARMAuthentication,
}
```

Update app test names and expected command text from account lookup to tenant listing. No provider discovery command may occur during `Init`.

- [ ] **Step 5: Verify focused packages and complete repository**

Run:

```bash
gofmt -w internal/azureauth/auth.go internal/azureauth/auth_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/app/app.go internal/app/lazy_providers_test.go
go test ./internal/azureauth ./internal/tui ./internal/app -count=1
go test ./... -count=1
go build ./...
```

Expected: all commands succeed with no test failures or build errors.

- [ ] **Step 6: Smoke the startup path**

Run `go run .` in a terminal. Verify one of these observed paths:

- Multiple Azure CLI tenants: tenant choices render before PIM areas; selecting one shows its tenant ID on the home screen; Esc returns to tenant choices.
- One Azure CLI tenant: the PIM-area home opens directly and shows that tenant ID.
- No Azure CLI login: exact `az login` guidance and the `r` retry hint render.

Quit with `q`. Do not run `az account set`.

- [ ] **Step 7: Commit the complete feature**

```bash
git add internal/tui/model_test.go internal/tui/view.go internal/app/app.go internal/app/lazy_providers_test.go
git commit -m "feat: add Azure tenant selection"
```
