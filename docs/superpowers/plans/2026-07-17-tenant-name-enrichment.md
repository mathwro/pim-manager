# Tenant Name Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Display Azure tenant names as the primary menu label while retaining tenant IDs beneath them.

**Architecture:** Keep `az account tenant list` authoritative for tenant membership. When any returned tenant lacks display metadata, enrich it from one filtered `az account list --all` response keyed by `tenantId`; the existing TUI label priority then works unchanged.

**Tech Stack:** Go standard library, Azure CLI, existing Bubble Tea TUI.

## Global Constraints

- Keep the tenant menu conditional on more than one tenant.
- Render display name first, default domain second, and tenant ID only as the final fallback.
- Always retain the tenant ID on the row's second line.
- Never run `az account set` or mutate Azure CLI configuration.
- Preserve tenants absent from the subscription cache.
- Add no dependencies or Graph calls.

---

### Task 1: Enrich tenant display metadata

**Files:**
- Modify: `internal/azureauth/auth_test.go`
- Modify: `internal/azureauth/auth.go`
- Modify: `internal/app/lazy_providers_test.go`

**Interfaces:**
- Preserves: `CLI.Tenants(context.Context) ([]Tenant, error)` and `Tenant { ID, DisplayName, DefaultDomain string }`.
- Adds no public API.
- Adds one conditional Azure CLI command: `az account list --all --query "[].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain}" --output json`.

- [ ] **Step 1: Write failing tenant enrichment tests**

Replace `TestTenantsReturnsDistinctAzureTenants` in `internal/azureauth/auth_test.go` with:

```go
func TestTenantsEnrichesNamesFromAzureAccounts(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account tenant list --output json": []byte(`[
			{"tenantId":"tenant-1"},
			{"tenantId":"tenant-2"},
			{"tenantId":"tenant-3"},
			{"tenantId":"tenant-1"},
			{"tenantId":" "}
		]`),
		`az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json`: []byte(`[
			{"tenantId":"tenant-1"},
			{"tenantId":"tenant-1","displayName":"Contoso","defaultDomain":"contoso.onmicrosoft.com"},
			{"tenantId":"tenant-2","defaultDomain":"fabrikam.onmicrosoft.com"}
		]`),
	}}

	tenants, err := NewCLI(runner.Run).Tenants(context.Background())
	if err != nil {
		t.Fatalf("Tenants returned error: %v", err)
	}
	want := []Tenant{
		{ID: "tenant-1", DisplayName: "Contoso", DefaultDomain: "contoso.onmicrosoft.com"},
		{ID: "tenant-2", DefaultDomain: "fabrikam.onmicrosoft.com"},
		{ID: "tenant-3"},
	}
	if !reflect.DeepEqual(tenants, want) {
		t.Fatalf("expected %#v, got %#v", want, tenants)
	}
}

func TestTenantsReturnsAccountEnrichmentErrors(t *testing.T) {
	commandErr := errors.New("account cache unavailable")
	client := NewCLI(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "account tenant list --output json" {
			return []byte(`[{"tenantId":"tenant-1"}]`), nil
		}
		return nil, commandErr
	})

	_, err := client.Tenants(context.Background())
	if !errors.Is(err, commandErr) || !strings.Contains(err.Error(), "list Azure CLI accounts") {
		t.Fatalf("expected account enrichment error, got %v", err)
	}
}
```

Update `TestRunInitListsTenantsWithoutSectionDiscovery` in `internal/app/lazy_providers_test.go` so its fake runner returns ID-only tenant discovery plus account metadata:

```go
switch command {
case "az account tenant list --output json":
	return []byte(`[{"tenantId":"tenant-1"}]`), nil
case "az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json":
	return []byte(`[{"tenantId":"tenant-1","displayName":"Contoso"}]`), nil
}
```

Change its command assertion to:

```go
want := []string{
	"az account tenant list --output json",
	"az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json",
}
if !reflect.DeepEqual(commands, want) {
	t.Fatalf("expected tenant discovery and name enrichment calls, got %#v", commands)
}
```

Add `reflect` to that test file's imports.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./internal/azureauth ./internal/app -run 'Test(TenantsEnriches|TenantsReturnsAccountEnrichment|RunInitListsTenants)' -count=1
```

Expected: `TestTenantsEnrichesNamesFromAzureAccounts` fails because `CLI.Tenants` never invokes `az account list`; the app test reports only one command.

- [ ] **Step 3: Implement conditional account metadata enrichment**

Add this constant near `ErrNotLoggedIn` in `internal/azureauth/auth.go`:

```go
const tenantMetadataQuery = "[].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain}"
```

After tenant parsing and the existing empty-list check in `CLI.Tenants`, add:

```go
needsMetadata := false
for _, tenant := range tenants {
	if tenant.DisplayName == "" && tenant.DefaultDomain == "" {
		needsMetadata = true
		break
	}
}
if !needsMetadata {
	return tenants, nil
}

out, err = c.run(ctx, "az", "account", "list", "--all", "--query", tenantMetadataQuery, "--output", "json")
if err != nil {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return nil, fmt.Errorf("list Azure CLI accounts: %w", azureCLIError(out, err))
}
var accounts []struct {
	TenantID     string `json:"tenantId"`
	DisplayName  string `json:"displayName"`
	DefaultDomain string `json:"defaultDomain"`
}
if err := json.Unmarshal(out, &accounts); err != nil {
	return nil, fmt.Errorf("parse az account list output: %w", err)
}
indexes := make(map[string]int, len(tenants))
for index, tenant := range tenants {
	indexes[strings.ToLower(tenant.ID)] = index
}
for _, account := range accounts {
	index, ok := indexes[strings.ToLower(strings.TrimSpace(account.TenantID))]
	if !ok {
		continue
	}
	if tenants[index].DisplayName == "" {
		tenants[index].DisplayName = strings.TrimSpace(account.DisplayName)
	}
	if tenants[index].DefaultDomain == "" {
		tenants[index].DefaultDomain = strings.TrimSpace(account.DefaultDomain)
	}
}
return tenants, nil
```

Remove the earlier unconditional `return tenants, nil` so the method returns after enrichment. Keep the existing TUI renderer unchanged: `tenantLabel` already uses `DisplayName`, then `DefaultDomain`, then `ID`, and the row already renders `tenant.ID` beneath it.

- [ ] **Step 4: Verify GREEN and full behavior**

Run:

```bash
gofmt -w internal/azureauth/auth.go internal/azureauth/auth_test.go internal/app/lazy_providers_test.go
go test ./internal/azureauth ./internal/app -count=1
go test ./... -count=1
go build ./...
```

Expected: all commands pass.

Smoke with a fake Azure CLI response where `account tenant list` returns IDs and `account list --all` returns `Contoso` and `Fabrikam`. Verify the multi-tenant TUI shows each name on the first row and its tenant ID beneath it.

- [ ] **Step 5: Commit**

```bash
git add internal/azureauth/auth.go internal/azureauth/auth_test.go internal/app/lazy_providers_test.go
git commit -m "fix: show Azure tenant names"
```
