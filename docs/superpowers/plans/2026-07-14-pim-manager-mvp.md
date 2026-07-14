# pim-manager MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the first interactive `pim-manager` MVP that discovers and activates PIM assignments for Entra Roles, Azure Resources, and Groups using Azure CLI authentication.

**Architecture:** Cobra starts a Bubble Tea TUI by default. Azure CLI authentication, PIM domain types, providers, activation orchestration, and TUI models live behind focused package boundaries so Azure API code is testable without a live tenant. The MVP uses provider interfaces and mockable HTTP/command executors for unit tests.

**Tech Stack:** Go 1.24.2, Cobra, Bubble Tea, Bubbles, Lip Gloss, Azure CLI, Microsoft Graph REST, Azure Resource Manager REST.

---

## Source Documents and API References

- Product design: `docs/superpowers/specs/2026-07-14-pim-manager-design.md`
- Agent guidance: `AGENTS.md`
- Microsoft Graph Entra eligibility discovery: `GET /roleManagement/directory/roleEligibilitySchedules/filterByCurrentUser(on='principal')`
- Microsoft Graph Entra activation: `POST /roleManagement/directory/roleAssignmentScheduleRequests` with `action: "selfActivate"`
- Microsoft Graph group eligibility discovery: `GET /identityGovernance/privilegedAccess/group/eligibilityScheduleInstances?$filter=principalId eq '{principalId}'`
- Microsoft Graph group activation: `POST /identityGovernance/privilegedAccess/group/assignmentScheduleRequests` with `action: "selfActivate"`
- Azure Resource eligibility discovery: `GET https://management.azure.com/{scope}/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=assignedTo('{principalId}')&api-version=2020-10-01`
- Azure Resource activation: `PUT https://management.azure.com/{scope}/providers/Microsoft.Authorization/roleAssignmentScheduleRequests/{guid}?api-version=2020-10-01` with `requestType: "SelfActivate"`

## File Structure

Create or modify these files during implementation:

| Path | Responsibility |
| --- | --- |
| `go.mod` | Add Bubble Tea, Bubbles, Lip Gloss, and UUID dependencies. |
| `main.go` | Keep minimal process entrypoint. |
| `cmd/root.go` | Cobra root command starts the app by default. |
| `cmd/root_test.go` | Root command behavior tests. |
| `internal/app/app.go` | Runtime wiring for auth, providers, activation service, and TUI. |
| `internal/pim/types.go` | Shared domain types and result statuses. |
| `internal/pim/types_test.go` | Domain helper tests. |
| `internal/azureauth/auth.go` | Azure CLI login/account/token wrapper. |
| `internal/azureauth/auth_test.go` | Azure CLI wrapper tests with mocked command execution. |
| `internal/graph/client.go` | Small Microsoft Graph REST client with pagination and error mapping. |
| `internal/arm/client.go` | Small Azure Resource Manager REST client with pagination and error mapping. |
| `internal/providers/entra/provider.go` | Entra role discovery and activation. |
| `internal/providers/entra/provider_test.go` | Entra provider normalization and request tests. |
| `internal/providers/groups/provider.go` | Privileged Access Group discovery and activation. |
| `internal/providers/groups/provider_test.go` | Group provider normalization and request tests. |
| `internal/providers/azureresources/scopes.go` | Discovers management group, subscription, and resource group scopes visible to the signed-in user. |
| `internal/providers/azureresources/scopes_test.go` | Azure Resource scope discovery tests. |
| `internal/providers/azureresources/provider.go` | Azure Resource PIM discovery and activation. |
| `internal/providers/azureresources/provider_test.go` | Azure Resource provider normalization and request tests. |
| `internal/activation/service.go` | Batch activation orchestration and retry classification. |
| `internal/activation/service_test.go` | Partial success, pending approval, failed, and retry tests. |
| `internal/tui/model.go` | Bubble Tea root model and screen routing. |
| `internal/tui/model_test.go` | Navigation, auth retry, and section selection tests. |
| `internal/tui/assignments.go` | Assignment list, filtering, selection, and details model. |
| `internal/tui/assignments_test.go` | Search/filter/multi-select tests. |
| `internal/tui/activation.go` | Activation form, confirmation, progress, and summary models. |
| `internal/tui/activation_test.go` | Form validation, summary grouping, and retry visibility tests. |

## Task 1: Add Runtime Dependencies and Cobra Startup Seam

**Files:**
- Modify: `go.mod`
- Modify: `cmd/root.go`
- Test: `cmd/root_test.go`

- [ ] **Step 1: Add dependencies**

Run:

```bash
go get github.com/charmbracelet/bubbletea@latest github.com/charmbracelet/bubbles@latest github.com/charmbracelet/lipgloss@latest github.com/google/uuid@latest
```

Expected: `go.mod` gains direct requirements for Bubble Tea, Bubbles, Lip Gloss, and UUID.

- [ ] **Step 2: Write the failing root command test**

Create `cmd/root_test.go`:

```go
package cmd

import (
	"bytes"
	"errors"
	"testing"
)

func TestRootCommandRunsConfiguredApp(t *testing.T) {
	var ran bool
	cmd := newRootCmd(func() error {
		ran = true
		return nil
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !ran {
		t.Fatal("expected root command to run the configured app")
	}
}

func TestRootCommandReturnsAppError(t *testing.T) {
	want := errors.New("app failed")
	cmd := newRootCmd(func() error {
		return want
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	got := cmd.Execute()
	if !errors.Is(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
```

- [ ] **Step 3: Run the focused test and confirm it fails**

Run:

```bash
go test ./cmd
```

Expected: FAIL because `newRootCmd` is undefined.

- [ ] **Step 4: Implement the root command seam**

Replace `cmd/root.go` with:

```go
package cmd

import (
	"os"

	"github.com/mathwro/pim-manager/internal/app"
	"github.com/spf13/cobra"
)

func newRootCmd(runApp func() error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pim-manager",
		Short: "Discover and activate Microsoft PIM eligibilities",
		Long:  "pim-manager opens an interactive TUI for activating eligible Entra, Azure Resource, and Group PIM assignments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp()
		},
	}
	return cmd
}

func Execute() {
	if err := newRootCmd(app.Run).Execute(); err != nil {
		os.Exit(1)
	}
}
```

Create a temporary compile stub in `internal/app/app.go`:

```go
package app

func Run() error {
	return nil
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./cmd
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/root.go cmd/root_test.go internal/app/app.go
git commit -m "feat: wire root command to app runner"
```

## Task 2: Define Shared PIM Domain Types

**Files:**
- Create: `internal/pim/types.go`
- Test: `internal/pim/types_test.go`

- [ ] **Step 1: Write failing domain tests**

Create `internal/pim/types_test.go`:

```go
package pim

import "testing"

func TestActivationResultStatusHelpers(t *testing.T) {
	tests := []struct {
		status   ActivationStatus
		pending  bool
		success  bool
		failure  bool
		retryable bool
	}{
		{ActivationStatusActivated, false, true, false, false},
		{ActivationStatusPendingApproval, true, false, false, false},
		{ActivationStatusFailed, false, false, true, true},
	}

	for _, tt := range tests {
		result := ActivationResult{Status: tt.status, Retryable: tt.retryable}
		if result.PendingApproval() != tt.pending {
			t.Fatalf("%s pending: expected %v", tt.status, tt.pending)
		}
		if result.Success() != tt.success {
			t.Fatalf("%s success: expected %v", tt.status, tt.success)
		}
		if result.Failure() != tt.failure {
			t.Fatalf("%s failure: expected %v", tt.status, tt.failure)
		}
		if result.CanRetry() != tt.retryable {
			t.Fatalf("%s retryable: expected %v", tt.status, tt.retryable)
		}
	}
}

func TestEligibleAssignmentDisplayScope(t *testing.T) {
	assignment := EligibleAssignment{
		DisplayName: "Contributor",
		Scope: Scope{
			DisplayName: "rg-prod",
			Type:        ScopeTypeResourceGroup,
		},
	}

	if got := assignment.DisplayScope(); got != "Resource Group: rg-prod" {
		t.Fatalf("expected display scope, got %q", got)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/pim
```

Expected: FAIL because the package types are undefined.

- [ ] **Step 3: Implement domain types**

Create `internal/pim/types.go`:

```go
package pim

import "fmt"

type AssignmentSource string

const (
	AssignmentSourceEntra          AssignmentSource = "entra"
	AssignmentSourceAzureResource  AssignmentSource = "azure_resource"
	AssignmentSourceGroup          AssignmentSource = "group"
)

type AssignmentKind string

const (
	AssignmentKindDirectoryRole AssignmentKind = "directory_role"
	AssignmentKindAzureRole     AssignmentKind = "azure_role"
	AssignmentKindGroupMember   AssignmentKind = "group_member"
	AssignmentKindGroupOwner    AssignmentKind = "group_owner"
)

type ScopeType string

const (
	ScopeTypeTenant          ScopeType = "Tenant"
	ScopeTypeManagementGroup ScopeType = "Management Group"
	ScopeTypeSubscription    ScopeType = "Subscription"
	ScopeTypeResourceGroup   ScopeType = "Resource Group"
	ScopeTypeGroup           ScopeType = "Group"
)

type Scope struct {
	ID          string
	DisplayName string
	Type        ScopeType
}

type EligibleAssignment struct {
	ID                    string
	Source                AssignmentSource
	Kind                  AssignmentKind
	DisplayName           string
	PrincipalID           string
	RoleDefinitionID      string
	DirectoryScopeID      string
	AppScopeID            string
	GroupID               string
	AccessID              string
	AzureScope            string
	EligibilityScheduleID string
	Scope                 Scope
	Condition             string
	ConditionVersion      string
}

func (a EligibleAssignment) DisplayScope() string {
	if a.Scope.DisplayName == "" {
		return string(a.Scope.Type)
	}
	return fmt.Sprintf("%s: %s", a.Scope.Type, a.Scope.DisplayName)
}

type ActivationRequest struct {
	Assignment    EligibleAssignment
	Justification string
	DurationISO   string
}

type ActivationStatus string

const (
	ActivationStatusActivated       ActivationStatus = "activated"
	ActivationStatusPendingApproval ActivationStatus = "pending_approval"
	ActivationStatusFailed          ActivationStatus = "failed"
)

type ActivationResult struct {
	Assignment EligibleAssignment
	Status     ActivationStatus
	Message    string
	Retryable  bool
}

func (r ActivationResult) Success() bool {
	return r.Status == ActivationStatusActivated
}

func (r ActivationResult) PendingApproval() bool {
	return r.Status == ActivationStatusPendingApproval
}

func (r ActivationResult) Failure() bool {
	return r.Status == ActivationStatusFailed
}

func (r ActivationResult) CanRetry() bool {
	return r.Failure() && r.Retryable
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/pim
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pim/types.go internal/pim/types_test.go
git commit -m "feat: add shared pim domain types"
```

## Task 3: Add Azure CLI Authentication Wrapper

**Files:**
- Create: `internal/azureauth/auth.go`
- Test: `internal/azureauth/auth_test.go`

- [ ] **Step 1: Write failing auth wrapper tests**

Create `internal/azureauth/auth_test.go`:

```go
package azureauth

import (
	"context"
	"errors"
	"testing"
)

func TestAccountReturnsCurrentAzureAccount(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account show --output json": []byte(`{"id":"sub-1","tenantId":"tenant-1","user":{"name":"user@example.com"}}`),
	}}
	client := NewCLI(runner.Run)

	account, err := client.Account(context.Background())
	if err != nil {
		t.Fatalf("Account returned error: %v", err)
	}
	if account.SubscriptionID != "sub-1" || account.TenantID != "tenant-1" || account.UserName != "user@example.com" {
		t.Fatalf("unexpected account: %#v", account)
	}
}

func TestAccountReturnsLoginHintOnAzFailure(t *testing.T) {
	client := NewCLI(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not logged in")
	})

	_, err := client.Account(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
}

func TestAccessTokenUsesRequestedResource(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az account get-access-token --resource https://graph.microsoft.com/ --output json": []byte(`{"accessToken":"abc","expiresOn":"2026-07-14 12:00:00.000000"}`),
	}}
	client := NewCLI(runner.Run)

	token, err := client.AccessToken(context.Background(), "https://graph.microsoft.com/")
	if err != nil {
		t.Fatalf("AccessToken returned error: %v", err)
	}
	if token != "abc" {
		t.Fatalf("expected token abc, got %q", token)
	}
}

func TestPrincipalIDUsesSignedInUser(t *testing.T) {
	runner := fakeRunner{outputs: map[string][]byte{
		"az ad signed-in-user show --query id --output tsv": []byte("principal-1\n"),
	}}
	client := NewCLI(runner.Run)

	principalID, err := client.PrincipalID(context.Background())
	if err != nil {
		t.Fatalf("PrincipalID returned error: %v", err)
	}
	if principalID != "principal-1" {
		t.Fatalf("expected principal-1, got %q", principalID)
	}
}

type fakeRunner struct {
	outputs map[string][]byte
}

func (f fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	out, ok := f.outputs[key]
	if !ok {
		return nil, errors.New("unexpected command: " + key)
	}
	return out, nil
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/azureauth
```

Expected: FAIL because the package implementation is missing.

- [ ] **Step 3: Implement Azure CLI wrapper**

Create `internal/azureauth/auth.go`:

```go
package azureauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var ErrNotLoggedIn = errors.New("azure cli is not logged in; run az login")

type Runner func(context.Context, string, ...string) ([]byte, error)

type CLI struct {
	run Runner
}

type Account struct {
	SubscriptionID string
	TenantID       string
	UserName       string
}

func NewCLI(run Runner) CLI {
	if run == nil {
		run = execCommand
	}
	return CLI{run: run}
}

func (c CLI) Account(ctx context.Context) (Account, error) {
	out, err := c.run(ctx, "az", "account", "show", "--output", "json")
	if err != nil {
		return Account{}, ErrNotLoggedIn
	}

	var payload struct {
		ID       string `json:"id"`
		TenantID string `json:"tenantId"`
		User     struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Account{}, fmt.Errorf("parse az account output: %w", err)
	}
	return Account{SubscriptionID: payload.ID, TenantID: payload.TenantID, UserName: payload.User.Name}, nil
}

func (c CLI) AccessToken(ctx context.Context, resource string) (string, error) {
	out, err := c.run(ctx, "az", "account", "get-access-token", "--resource", resource, "--output", "json")
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

func (c CLI) PrincipalID(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "az", "ad", "signed-in-user", "show", "--query", "id", "--output", "tsv")
	if err != nil {
		return "", fmt.Errorf("get signed-in user principal ID: %w", err)
	}
	principalID := strings.TrimSpace(string(out))
	if principalID == "" {
		return "", errors.New("Azure CLI returned empty signed-in user principal ID")
	}
	return principalID, nil
}

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/azureauth
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/azureauth/auth.go internal/azureauth/auth_test.go
git commit -m "feat: add azure cli auth wrapper"
```

## Task 4: Add REST Clients for Graph and ARM

**Files:**
- Create: `internal/graph/client.go`
- Create: `internal/arm/client.go`
- Test through provider tests in later tasks.

- [ ] **Step 1: Implement Microsoft Graph client**

Create `internal/graph/client.go`:

```go
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const BaseURL = "https://graph.microsoft.com/v1.0"
const Resource = "https://graph.microsoft.com/"

type TokenSource interface {
	AccessToken(context.Context, string) (string, error)
}

type Client struct {
	httpClient  *http.Client
	tokenSource TokenSource
	baseURL     string
}

func NewClient(httpClient *http.Client, tokenSource TokenSource) Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return Client{httpClient: httpClient, tokenSource: tokenSource, baseURL: BaseURL}
}

func (c Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c Client) Post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c Client) do(ctx context.Context, method string, path string, body any, out any) error {
	token, err := c.tokenSource.AccessToken(ctx, Resource)
	if err != nil {
		return err
	}
	u := c.baseURL + path
	if strings.HasPrefix(path, "https://") {
		u = path
	}
	var reader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encode Graph request: %w", err)
		}
		reader = buf
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Graph %s %s failed: %s: %s", method, u, resp.Status, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func EscapeFilterValue(value string) string {
	return url.QueryEscape(strings.ReplaceAll(value, "'", "''"))
}
```

- [ ] **Step 2: Implement Azure Resource Manager client**

Create `internal/arm/client.go`:

```go
package arm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const BaseURL = "https://management.azure.com"
const Resource = "https://management.azure.com/"
const AuthorizationAPIVersion = "2020-10-01"

type TokenSource interface {
	AccessToken(context.Context, string) (string, error)
}

type Client struct {
	httpClient  *http.Client
	tokenSource TokenSource
	baseURL     string
}

func NewClient(httpClient *http.Client, tokenSource TokenSource) Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return Client{httpClient: httpClient, tokenSource: tokenSource, baseURL: BaseURL}
}

func (c Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c Client) Put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

func (c Client) do(ctx context.Context, method string, path string, body any, out any) error {
	token, err := c.tokenSource.AccessToken(ctx, Resource)
	if err != nil {
		return err
	}
	u := c.baseURL + path
	if strings.HasPrefix(path, "https://") {
		u = path
	}
	var reader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encode ARM request: %w", err)
		}
		reader = buf
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ARM %s %s failed: %s: %s", method, u, resp.Status, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
```

- [ ] **Step 3: Run compile tests**

Run:

```bash
go test ./internal/graph ./internal/arm
```

Expected: PASS with no test files.

- [ ] **Step 4: Commit**

```bash
git add internal/graph/client.go internal/arm/client.go
git commit -m "feat: add graph and arm rest clients"
```

## Task 5: Implement Batch Activation Service

**Files:**
- Create: `internal/activation/service.go`
- Test: `internal/activation/service_test.go`

- [ ] **Step 1: Write failing batch activation tests**

Create `internal/activation/service_test.go`:

```go
package activation

import (
	"context"
	"errors"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivateBatchContinuesAfterFailure(t *testing.T) {
	provider := fakeProvider{
		results: map[string]pim.ActivationResult{
			"one": {Status: pim.ActivationStatusActivated},
			"two": {Status: pim.ActivationStatusFailed, Message: "throttled", Retryable: true},
		},
	}
	service := NewService(provider)

	results := service.ActivateBatch(context.Background(), []pim.ActivationRequest{
		{Assignment: pim.EligibleAssignment{ID: "one"}},
		{Assignment: pim.EligibleAssignment{ID: "two"}},
	})

	if len(results) != 2 {
		t.Fatalf("expected two results, got %d", len(results))
	}
	if !results[0].Success() {
		t.Fatalf("expected first result success: %#v", results[0])
	}
	if !results[1].CanRetry() {
		t.Fatalf("expected second result retryable failure: %#v", results[1])
	}
}

func TestActivateBatchMapsProviderErrorToFailedResult(t *testing.T) {
	service := NewService(fakeProvider{err: errors.New("network down")})

	results := service.ActivateBatch(context.Background(), []pim.ActivationRequest{
		{Assignment: pim.EligibleAssignment{ID: "one"}},
	})

	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Status != pim.ActivationStatusFailed || !results[0].Retryable {
		t.Fatalf("expected retryable failure, got %#v", results[0])
	}
}

type fakeProvider struct {
	results map[string]pim.ActivationResult
	err     error
}

func (f fakeProvider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	if f.err != nil {
		return pim.ActivationResult{}, f.err
	}
	result := f.results[request.Assignment.ID]
	result.Assignment = request.Assignment
	return result, nil
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/activation
```

Expected: FAIL because `NewService` and provider interface are undefined.

- [ ] **Step 3: Implement activation service**

Create `internal/activation/service.go`:

```go
package activation

import (
	"context"

	"github.com/mathwro/pim-manager/internal/pim"
)

type Provider interface {
	Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}

type Service struct {
	provider Provider
}

func NewService(provider Provider) Service {
	return Service{provider: provider}
}

func (s Service) ActivateBatch(ctx context.Context, requests []pim.ActivationRequest) []pim.ActivationResult {
	results := make([]pim.ActivationResult, 0, len(requests))
	for _, request := range requests {
		result, err := s.provider.Activate(ctx, request)
		if err != nil {
			result = pim.ActivationResult{
				Assignment: request.Assignment,
				Status:     pim.ActivationStatusFailed,
				Message:    err.Error(),
				Retryable:  true,
			}
		}
		results = append(results, result)
	}
	return results
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/activation
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/activation/service.go internal/activation/service_test.go
git commit -m "feat: add batch activation service"
```

## Task 6: Implement Entra Provider

**Files:**
- Create: `internal/providers/entra/provider.go`
- Test: `internal/providers/entra/provider_test.go`

- [ ] **Step 1: Write provider tests**

Create `internal/providers/entra/provider_test.go` with table tests that verify:

```go
package entra

import (
	"context"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestNormalizeEligibility(t *testing.T) {
	item := roleEligibilitySchedule{
		ID:               "eligibility-instance-1",
		PrincipalID:      "principal-1",
		RoleDefinitionID: "role-1",
		DirectoryScopeID: "/",
		RoleDefinition: roleDefinition{
			DisplayName: "Global Reader",
		},
	}

	got := normalizeEligibility(item)

	if got.Source != pim.AssignmentSourceEntra {
		t.Fatalf("expected Entra source, got %s", got.Source)
	}
	if got.Kind != pim.AssignmentKindDirectoryRole {
		t.Fatalf("expected directory role kind, got %s", got.Kind)
	}
	if got.DisplayName != "Global Reader" {
		t.Fatalf("expected role display name, got %q", got.DisplayName)
	}
	if got.Scope.Type != pim.ScopeTypeTenant {
		t.Fatalf("expected tenant scope, got %s", got.Scope.Type)
	}
}

func TestActivationRequestBody(t *testing.T) {
	request := pim.ActivationRequest{
		Assignment: pim.EligibleAssignment{
			PrincipalID:      "principal-1",
			RoleDefinitionID: "role-1",
			DirectoryScopeID: "/",
		},
		Justification: "Need access",
		DurationISO:   "PT2H",
	}

	body := activationBody(request)

	if body.Action != "selfActivate" {
		t.Fatalf("expected selfActivate, got %q", body.Action)
	}
	if body.ScheduleInfo.Expiration.Type != "AfterDuration" {
		t.Fatalf("expected AfterDuration, got %q", body.ScheduleInfo.Expiration.Type)
	}
	if body.ScheduleInfo.Expiration.Duration != "PT2H" {
		t.Fatalf("expected duration PT2H, got %q", body.ScheduleInfo.Expiration.Duration)
	}
}

func TestMapStatus(t *testing.T) {
	provider := Provider{}
	result := provider.mapStatus(pim.EligibleAssignment{ID: "one"}, "PendingApproval", "")
	if result.Status != pim.ActivationStatusPendingApproval {
		t.Fatalf("expected pending approval, got %#v", result)
	}
}

type fakeGraph struct{}

func (fakeGraph) Get(context.Context, string, any) error { return nil }
func (fakeGraph) Post(context.Context, string, any, any) error { return nil }
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/providers/entra
```

Expected: FAIL because provider types are undefined.

- [ ] **Step 3: Implement Entra provider**

Create `internal/providers/entra/provider.go`:

```go
package entra

import (
	"context"
	"time"

	"github.com/mathwro/pim-manager/internal/pim"
)

type GraphClient interface {
	Get(context.Context, string, any) error
	Post(context.Context, string, any, any) error
}

type Provider struct {
	graph GraphClient
}

func NewProvider(graph GraphClient) Provider {
	return Provider{graph: graph}
}

type roleEligibilityResponse struct {
	Value []roleEligibilitySchedule `json:"value"`
}

type roleEligibilitySchedule struct {
	ID               string         `json:"id"`
	PrincipalID      string         `json:"principalId"`
	RoleDefinitionID string         `json:"roleDefinitionId"`
	DirectoryScopeID string         `json:"directoryScopeId"`
	AppScopeID       string         `json:"appScopeId"`
	RoleDefinition   roleDefinition `json:"roleDefinition"`
}

type roleDefinition struct {
	DisplayName string `json:"displayName"`
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	var response roleEligibilityResponse
	path := "/roleManagement/directory/roleEligibilitySchedules/filterByCurrentUser(on='principal')?$expand=roleDefinition"
	if err := p.graph.Get(ctx, path, &response); err != nil {
		return nil, err
	}
	assignments := make([]pim.EligibleAssignment, 0, len(response.Value))
	for _, item := range response.Value {
		assignments = append(assignments, normalizeEligibility(item))
	}
	return assignments, nil
}

func normalizeEligibility(item roleEligibilitySchedule) pim.EligibleAssignment {
	scopeType := pim.ScopeTypeTenant
	scopeName := "Tenant"
	if item.DirectoryScopeID != "/" && item.DirectoryScopeID != "" {
		scopeName = item.DirectoryScopeID
	}
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceEntra,
		Kind:                  pim.AssignmentKindDirectoryRole,
		DisplayName:           item.RoleDefinition.DisplayName,
		PrincipalID:           item.PrincipalID,
		RoleDefinitionID:      item.RoleDefinitionID,
		DirectoryScopeID:      item.DirectoryScopeID,
		AppScopeID:            item.AppScopeID,
		EligibilityScheduleID: item.ID,
		Scope: pim.Scope{
			ID:          item.DirectoryScopeID,
			DisplayName: scopeName,
			Type:        scopeType,
		},
	}
}

type activationRequestBody struct {
	Action           string          `json:"action"`
	PrincipalID      string          `json:"principalId"`
	RoleDefinitionID string          `json:"roleDefinitionId"`
	DirectoryScopeID string          `json:"directoryScopeId,omitempty"`
	AppScopeID       string          `json:"appScopeId,omitempty"`
	Justification    string          `json:"justification"`
	ScheduleInfo     scheduleInfo    `json:"scheduleInfo"`
}

type scheduleInfo struct {
	StartDateTime string     `json:"startDateTime"`
	Expiration    expiration `json:"expiration"`
}

type expiration struct {
	Type     string `json:"type"`
	Duration string `json:"duration"`
}

type activationResponse struct {
	Status string `json:"status"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	var response activationResponse
	err := p.graph.Post(ctx, "/roleManagement/directory/roleAssignmentScheduleRequests", activationBody(request), &response)
	if err != nil {
		return pim.ActivationResult{}, err
	}
	return p.mapStatus(request.Assignment, response.Status, ""), nil
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		Action:           "selfActivate",
		PrincipalID:      request.Assignment.PrincipalID,
		RoleDefinitionID: request.Assignment.RoleDefinitionID,
		DirectoryScopeID: request.Assignment.DirectoryScopeID,
		AppScopeID:       request.Assignment.AppScopeID,
		Justification:    request.Justification,
		ScheduleInfo: scheduleInfo{
			StartDateTime: time.Now().UTC().Format(time.RFC3339),
			Expiration: expiration{
				Type:     "AfterDuration",
				Duration: request.DurationISO,
			},
		},
	}
}

func (p Provider) mapStatus(assignment pim.EligibleAssignment, status string, message string) pim.ActivationResult {
	switch status {
	case "Granted", "Provisioned":
		return pim.ActivationResult{Assignment: assignment, Status: pim.ActivationStatusActivated, Message: status}
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: assignment, Status: pim.ActivationStatusPendingApproval, Message: status}
	default:
		return pim.ActivationResult{Assignment: assignment, Status: pim.ActivationStatusFailed, Message: status + " " + message, Retryable: false}
	}
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/providers/entra
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/entra/provider.go internal/providers/entra/provider_test.go
git commit -m "feat: add entra pim provider"
```

## Task 7: Implement Groups Provider

**Files:**
- Create: `internal/providers/groups/provider.go`
- Test: `internal/providers/groups/provider_test.go`

- [ ] **Step 1: Write group provider tests**

Create `internal/providers/groups/provider_test.go`:

```go
package groups

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestNormalizeMemberEligibility(t *testing.T) {
	item := eligibilityScheduleInstance{
		ID:          "group-1_member_sched-1",
		AccessID:    "member",
		PrincipalID: "principal-1",
		GroupID:     "group-1",
	}

	got := normalizeEligibility(item)

	if got.Source != pim.AssignmentSourceGroup {
		t.Fatalf("expected group source, got %s", got.Source)
	}
	if got.Kind != pim.AssignmentKindGroupMember {
		t.Fatalf("expected group member kind, got %s", got.Kind)
	}
	if got.AccessID != "member" {
		t.Fatalf("expected member access ID, got %q", got.AccessID)
	}
}

func TestNormalizeOwnerEligibility(t *testing.T) {
	item := eligibilityScheduleInstance{ID: "group-1_owner_sched-1", AccessID: "owner", PrincipalID: "principal-1", GroupID: "group-1"}
	got := normalizeEligibility(item)
	if got.Kind != pim.AssignmentKindGroupOwner {
		t.Fatalf("expected group owner kind, got %s", got.Kind)
	}
}

func TestActivationRequestBody(t *testing.T) {
	body := activationBody(pim.ActivationRequest{
		Assignment: pim.EligibleAssignment{PrincipalID: "principal-1", GroupID: "group-1", AccessID: "owner"},
		Justification: "Need ownership",
		DurationISO: "PT1H",
	})

	if body.Action != "selfActivate" || body.AccessID != "owner" || body.ScheduleInfo.Expiration.Duration != "PT1H" {
		t.Fatalf("unexpected body: %#v", body)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/providers/groups
```

Expected: FAIL because provider types are undefined.

- [ ] **Step 3: Implement Groups provider**

Create `internal/providers/groups/provider.go`:

```go
package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/pim"
)

type GraphClient interface {
	Get(context.Context, string, any) error
	Post(context.Context, string, any, any) error
}

type Provider struct {
	graph       GraphClient
	principalID string
}

func NewProvider(graph GraphClient, principalID string) Provider {
	return Provider{graph: graph, principalID: principalID}
}

type eligibilityResponse struct {
	Value []eligibilityScheduleInstance `json:"value"`
}

type eligibilityScheduleInstance struct {
	ID          string `json:"id"`
	AccessID    string `json:"accessId"`
	PrincipalID string `json:"principalId"`
	GroupID     string `json:"groupId"`
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	var response eligibilityResponse
	filter := graph.EscapeFilterValue(fmt.Sprintf("principalId eq '%s'", p.principalID))
	path := "/identityGovernance/privilegedAccess/group/eligibilityScheduleInstances?$filter=" + filter
	if err := p.graph.Get(ctx, path, &response); err != nil {
		return nil, err
	}
	assignments := make([]pim.EligibleAssignment, 0, len(response.Value))
	for _, item := range response.Value {
		assignments = append(assignments, normalizeEligibility(item))
	}
	return assignments, nil
}

func normalizeEligibility(item eligibilityScheduleInstance) pim.EligibleAssignment {
	kind := pim.AssignmentKindGroupMember
	if item.AccessID == "owner" {
		kind = pim.AssignmentKindGroupOwner
	}
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceGroup,
		Kind:                  kind,
		DisplayName:           "Group " + item.AccessID,
		PrincipalID:           item.PrincipalID,
		GroupID:               item.GroupID,
		AccessID:              item.AccessID,
		EligibilityScheduleID: item.ID,
		Scope: pim.Scope{
			ID:          item.GroupID,
			DisplayName: item.GroupID,
			Type:        pim.ScopeTypeGroup,
		},
	}
}

type activationRequestBody struct {
	AccessID      string       `json:"accessId"`
	PrincipalID   string       `json:"principalId"`
	GroupID       string       `json:"groupId"`
	Action        string       `json:"action"`
	ScheduleInfo  scheduleInfo `json:"scheduleInfo"`
	Justification string       `json:"justification"`
}

type scheduleInfo struct {
	StartDateTime string     `json:"startDateTime"`
	Expiration    expiration `json:"expiration"`
}

type expiration struct {
	Type     string `json:"type"`
	Duration string `json:"duration"`
}

type activationResponse struct {
	Status string `json:"status"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	var response activationResponse
	err := p.graph.Post(ctx, "/identityGovernance/privilegedAccess/group/assignmentScheduleRequests", activationBody(request), &response)
	if err != nil {
		return pim.ActivationResult{}, err
	}
	switch response.Status {
	case "Granted", "Provisioned":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusActivated, Message: response.Status}, nil
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusPendingApproval, Message: response.Status}, nil
	default:
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusFailed, Message: response.Status}, nil
	}
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		AccessID:      request.Assignment.AccessID,
		PrincipalID:   request.Assignment.PrincipalID,
		GroupID:       request.Assignment.GroupID,
		Action:        "selfActivate",
		Justification: request.Justification,
		ScheduleInfo: scheduleInfo{
			StartDateTime: time.Now().UTC().Format(time.RFC3339),
			Expiration: expiration{
				Type:     "afterDuration",
				Duration: request.DurationISO,
			},
		},
	}
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/providers/groups
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/groups/provider.go internal/providers/groups/provider_test.go
git commit -m "feat: add group pim provider"
```

## Task 8: Discover Azure Resource PIM Scopes

**Files:**
- Create: `internal/providers/azureresources/scopes.go`
- Test: `internal/providers/azureresources/scopes_test.go`

- [ ] **Step 1: Write scope discovery tests**

Create `internal/providers/azureresources/scopes_test.go`:

```go
package azureresources

import (
	"context"
	"testing"
)

func TestScopeDiscovererReturnsManagementGroupsSubscriptionsAndResourceGroups(t *testing.T) {
	arm := &fakeARM{
		responses: map[string]any{
			"/providers/Microsoft.Management/managementGroups?api-version=2020-05-01": managementGroupsResponse{Value: []managementGroup{{Name: "mg-root"}}},
			"/subscriptions?api-version=2020-01-01": subscriptionsResponse{Value: []subscription{{SubscriptionID: "sub-1"}}},
			"/subscriptions/sub-1/resourcegroups?api-version=2021-04-01": resourceGroupsResponse{Value: []resourceGroup{{ID: "/subscriptions/sub-1/resourceGroups/rg-prod"}}},
		},
	}
	discoverer := NewScopeDiscoverer(arm)

	scopes, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	want := []string{
		"/providers/Microsoft.Management/managementGroups/mg-root",
		"/subscriptions/sub-1",
		"/subscriptions/sub-1/resourceGroups/rg-prod",
	}
	if len(scopes) != len(want) {
		t.Fatalf("expected %d scopes, got %#v", len(want), scopes)
	}
	for i := range want {
		if scopes[i] != want[i] {
			t.Fatalf("scope %d: expected %q, got %q", i, want[i], scopes[i])
		}
	}
}

type fakeARM struct {
	responses map[string]any
}

func (f *fakeARM) Get(_ context.Context, path string, out any) error {
	switch target := out.(type) {
	case *managementGroupsResponse:
		*target = f.responses[path].(managementGroupsResponse)
	case *subscriptionsResponse:
		*target = f.responses[path].(subscriptionsResponse)
	case *resourceGroupsResponse:
		*target = f.responses[path].(resourceGroupsResponse)
	}
	return nil
}

func (f *fakeARM) Put(context.Context, string, any, any) error {
	return nil
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/providers/azureresources
```

Expected: FAIL because `NewScopeDiscoverer` is undefined.

- [ ] **Step 3: Implement scope discovery**

Create `internal/providers/azureresources/scopes.go`:

```go
package azureresources

import (
	"context"
	"fmt"
)

type ScopeDiscoverer struct {
	arm ARMClient
}

func NewScopeDiscoverer(arm ARMClient) ScopeDiscoverer {
	return ScopeDiscoverer{arm: arm}
}

type managementGroupsResponse struct {
	Value []managementGroup `json:"value"`
}

type managementGroup struct {
	Name string `json:"name"`
}

type subscriptionsResponse struct {
	Value []subscription `json:"value"`
}

type subscription struct {
	SubscriptionID string `json:"subscriptionId"`
}

type resourceGroupsResponse struct {
	Value []resourceGroup `json:"value"`
}

type resourceGroup struct {
	ID string `json:"id"`
}

func (d ScopeDiscoverer) Discover(ctx context.Context) ([]string, error) {
	var scopes []string

	var managementGroups managementGroupsResponse
	if err := d.arm.Get(ctx, "/providers/Microsoft.Management/managementGroups?api-version=2020-05-01", &managementGroups); err != nil {
		return nil, err
	}
	for _, group := range managementGroups.Value {
		scopes = append(scopes, "/providers/Microsoft.Management/managementGroups/"+group.Name)
	}

	var subscriptions subscriptionsResponse
	if err := d.arm.Get(ctx, "/subscriptions?api-version=2020-01-01", &subscriptions); err != nil {
		return nil, err
	}
	for _, sub := range subscriptions.Value {
		subScope := "/subscriptions/" + sub.SubscriptionID
		scopes = append(scopes, subScope)

		var resourceGroups resourceGroupsResponse
		path := fmt.Sprintf("/subscriptions/%s/resourcegroups?api-version=2021-04-01", sub.SubscriptionID)
		if err := d.arm.Get(ctx, path, &resourceGroups); err != nil {
			return nil, err
		}
		for _, rg := range resourceGroups.Value {
			scopes = append(scopes, rg.ID)
		}
	}

	return scopes, nil
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/providers/azureresources
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/azureresources/scopes.go internal/providers/azureresources/scopes_test.go
git commit -m "feat: discover azure resource pim scopes"
```

## Task 9: Implement Azure Resources Provider

**Files:**
- Create: `internal/providers/azureresources/provider.go`
- Test: `internal/providers/azureresources/provider_test.go`

- [ ] **Step 1: Write Azure Resources provider tests**

Create `internal/providers/azureresources/provider_test.go`:

```go
package azureresources

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestNormalizeEligibility(t *testing.T) {
	item := roleEligibilityScheduleInstance{
		ID: "eligibility-1",
		Name: "eligibility-name",
		Properties: roleEligibilityProperties{
			Scope: "subscriptions/sub-1/resourceGroups/rg-prod",
			RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/contributor",
			PrincipalID: "principal-1",
			RoleEligibilityScheduleID: "schedule-1",
			ExpandedProperties: expandedProperties{
				Scope: expandedScope{DisplayName: "rg-prod", Type: "resourcegroup"},
				RoleDefinition: expandedRoleDefinition{DisplayName: "Contributor"},
			},
		},
	}

	got := normalizeEligibility(item)

	if got.Source != pim.AssignmentSourceAzureResource {
		t.Fatalf("expected Azure Resource source, got %s", got.Source)
	}
	if got.DisplayName != "Contributor" {
		t.Fatalf("expected Contributor, got %q", got.DisplayName)
	}
	if got.Scope.Type != pim.ScopeTypeResourceGroup {
		t.Fatalf("expected resource group scope, got %s", got.Scope.Type)
	}
}

func TestActivationRequestBody(t *testing.T) {
	body := activationBody(pim.ActivationRequest{
		Assignment: pim.EligibleAssignment{
			PrincipalID: "principal-1",
			RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/contributor",
			EligibilityScheduleID: "schedule-1",
		},
		Justification: "Need resource access",
		DurationISO: "PT3H",
	})

	if body.Properties.RequestType != "SelfActivate" {
		t.Fatalf("expected SelfActivate, got %q", body.Properties.RequestType)
	}
	if body.Properties.ScheduleInfo.Expiration.Duration != "PT3H" {
		t.Fatalf("expected PT3H, got %q", body.Properties.ScheduleInfo.Expiration.Duration)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/providers/azureresources
```

Expected: FAIL because provider types are undefined.

- [ ] **Step 3: Implement Azure Resources provider**

Create `internal/providers/azureresources/provider.go`:

```go
package azureresources

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

type ARMClient interface {
	Get(context.Context, string, any) error
	Put(context.Context, string, any, any) error
}

type Provider struct {
	arm         ARMClient
	principalID string
	scopes      []string
}

func NewProvider(arm ARMClient, principalID string, scopes []string) Provider {
	return Provider{arm: arm, principalID: principalID, scopes: scopes}
}

type eligibilityResponse struct {
	Value []roleEligibilityScheduleInstance `json:"value"`
}

type roleEligibilityScheduleInstance struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Properties roleEligibilityProperties `json:"properties"`
}

type roleEligibilityProperties struct {
	Scope                     string             `json:"scope"`
	RoleDefinitionID          string             `json:"roleDefinitionId"`
	PrincipalID               string             `json:"principalId"`
	RoleEligibilityScheduleID string             `json:"roleEligibilityScheduleId"`
	Condition                 string             `json:"condition"`
	ConditionVersion          string             `json:"conditionVersion"`
	ExpandedProperties        expandedProperties `json:"expandedProperties"`
}

type expandedProperties struct {
	Scope          expandedScope          `json:"scope"`
	RoleDefinition expandedRoleDefinition `json:"roleDefinition"`
}

type expandedScope struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type expandedRoleDefinition struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	var assignments []pim.EligibleAssignment
	for _, scope := range p.scopes {
		filter := url.QueryEscape(fmt.Sprintf("assignedTo('%s')", p.principalID))
		path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=%s&api-version=%s", scope, filter, arm.AuthorizationAPIVersion)
		var response eligibilityResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Value {
			assignments = append(assignments, normalizeEligibility(item))
		}
	}
	return assignments, nil
}

func normalizeEligibility(item roleEligibilityScheduleInstance) pim.EligibleAssignment {
	scopeType := mapScopeType(item.Properties.ExpandedProperties.Scope.Type, item.Properties.Scope)
	return pim.EligibleAssignment{
		ID:                    item.ID,
		Source:                pim.AssignmentSourceAzureResource,
		Kind:                  pim.AssignmentKindAzureRole,
		DisplayName:           item.Properties.ExpandedProperties.RoleDefinition.DisplayName,
		PrincipalID:           item.Properties.PrincipalID,
		RoleDefinitionID:      item.Properties.RoleDefinitionID,
		AzureScope:            item.Properties.Scope,
		EligibilityScheduleID: item.Properties.RoleEligibilityScheduleID,
		Condition:             item.Properties.Condition,
		ConditionVersion:      item.Properties.ConditionVersion,
		Scope: pim.Scope{
			ID:          item.Properties.Scope,
			DisplayName: item.Properties.ExpandedProperties.Scope.DisplayName,
			Type:        scopeType,
		},
	}
}

func mapScopeType(apiType string, scope string) pim.ScopeType {
	switch strings.ToLower(apiType) {
	case "managementgroup", "management group":
		return pim.ScopeTypeManagementGroup
	case "subscription":
		return pim.ScopeTypeSubscription
	case "resourcegroup", "resource group":
		return pim.ScopeTypeResourceGroup
	}
	if strings.Contains(strings.ToLower(scope), "/resourcegroups/") {
		return pim.ScopeTypeResourceGroup
	}
	if strings.Contains(strings.ToLower(scope), "/subscriptions/") {
		return pim.ScopeTypeSubscription
	}
	return pim.ScopeTypeManagementGroup
}

type activationRequestBody struct {
	Properties activationProperties `json:"properties"`
}

type activationProperties struct {
	PrincipalID                       string       `json:"principalId"`
	RequestType                       string       `json:"requestType"`
	RoleDefinitionID                  string       `json:"roleDefinitionId"`
	LinkedRoleEligibilityScheduleID   string       `json:"linkedRoleEligibilityScheduleId"`
	Justification                     string       `json:"justification"`
	Condition                         string       `json:"condition,omitempty"`
	ConditionVersion                  string       `json:"conditionVersion,omitempty"`
	ScheduleInfo                      scheduleInfo `json:"scheduleInfo"`
}

type scheduleInfo struct {
	StartDateTime string     `json:"startDateTime"`
	Expiration    expiration `json:"expiration"`
}

type expiration struct {
	Type        string  `json:"type"`
	EndDateTime *string `json:"endDateTime"`
	Duration    string  `json:"duration"`
}

type activationResponse struct {
	Properties struct {
		Status string `json:"status"`
	} `json:"properties"`
}

func (p Provider) Activate(ctx context.Context, request pim.ActivationRequest) (pim.ActivationResult, error) {
	requestID := uuid.NewString()
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleAssignmentScheduleRequests/%s?api-version=%s", request.Assignment.AzureScope, requestID, arm.AuthorizationAPIVersion)
	var response activationResponse
	if err := p.arm.Put(ctx, path, activationBody(request), &response); err != nil {
		return pim.ActivationResult{}, err
	}
	switch response.Properties.Status {
	case "Granted", "Provisioned":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusActivated, Message: response.Properties.Status}, nil
	case "PendingApproval", "PendingApprovalProvisioning", "PendingAdminDecision":
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusPendingApproval, Message: response.Properties.Status}, nil
	default:
		return pim.ActivationResult{Assignment: request.Assignment, Status: pim.ActivationStatusFailed, Message: response.Properties.Status}, nil
	}
}

func activationBody(request pim.ActivationRequest) activationRequestBody {
	return activationRequestBody{
		Properties: activationProperties{
			PrincipalID:                     request.Assignment.PrincipalID,
			RequestType:                     "SelfActivate",
			RoleDefinitionID:                request.Assignment.RoleDefinitionID,
			LinkedRoleEligibilityScheduleID: request.Assignment.EligibilityScheduleID,
			Justification:                   request.Justification,
			Condition:                       request.Assignment.Condition,
			ConditionVersion:                request.Assignment.ConditionVersion,
			ScheduleInfo: scheduleInfo{
				StartDateTime: time.Now().UTC().Format(time.RFC3339),
				Expiration: expiration{
					Type:     "AfterDuration",
					Duration: request.DurationISO,
				},
			},
		},
	}
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/providers/azureresources
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/azureresources/provider.go internal/providers/azureresources/provider_test.go
git commit -m "feat: add azure resource pim provider"
```

## Task 10: Build TUI Navigation and Assignment Selection

**Files:**
- Create: `internal/tui/model.go`
- Create: `internal/tui/assignments.go`
- Test: `internal/tui/model_test.go`
- Test: `internal/tui/assignments_test.go`

- [ ] **Step 1: Write navigation and selection tests**

Create `internal/tui/model_test.go`:

```go
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHomeEnterMovesToSelectedSection(t *testing.T) {
	model := NewModel(Runtime{})
	model.selectedSection = SectionGroups

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)

	if got.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", got.screen)
	}
	if got.activeSection != SectionGroups {
		t.Fatalf("expected groups section, got %s", got.activeSection)
	}
}
```

Create `internal/tui/assignments_test.go`:

```go
package tui

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestAssignmentListFiltersByRoleAndScope(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "one", DisplayName: "Contributor", Scope: pim.Scope{DisplayName: "rg-prod"}},
		{ID: "two", DisplayName: "Global Reader", Scope: pim.Scope{DisplayName: "Tenant"}},
	})

	filtered := list.filtered("prod")
	if len(filtered) != 1 || filtered[0].ID != "one" {
		t.Fatalf("expected rg-prod assignment, got %#v", filtered)
	}
}

func TestAssignmentListTogglesSelection(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{{ID: "one", DisplayName: "Contributor"}})
	list.toggle("one")

	selected := list.selected()
	if len(selected) != 1 || selected[0].ID != "one" {
		t.Fatalf("expected selected assignment, got %#v", selected)
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm they fail**

Run:

```bash
go test ./internal/tui
```

Expected: FAIL because the TUI model is missing.

- [ ] **Step 3: Implement TUI model and assignment list**

Create `internal/tui/model.go`:

```go
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

type Screen string

const (
	ScreenHome        Screen = "home"
	ScreenAssignments Screen = "assignments"
	ScreenActivation  Screen = "activation"
	ScreenSummary     Screen = "summary"
)

type Section string

const (
	SectionEntra          Section = "Entra Roles"
	SectionAzureResources Section = "Azure Resources"
	SectionGroups         Section = "Groups"
)

type Runtime struct{}

type Model struct {
	runtime         Runtime
	screen          Screen
	selectedSection Section
	activeSection   Section
	sections        []Section
}

func NewModel(runtime Runtime) Model {
	return Model{
		runtime:         runtime,
		screen:          ScreenHome,
		selectedSection: SectionEntra,
		sections:        []Section{SectionEntra, SectionAzureResources, SectionGroups},
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.screen == ScreenHome && key.Type == tea.KeyEnter {
		m.activeSection = m.selectedSection
		m.screen = ScreenAssignments
	}
	return m, nil
}

func (m Model) View() string {
	return "pim-manager"
}
```

Create `internal/tui/assignments.go`:

```go
package tui

import (
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

type assignmentList struct {
	items    []pim.EligibleAssignment
	selected map[string]bool
}

func newAssignmentList(items []pim.EligibleAssignment) assignmentList {
	return assignmentList{items: items, selected: map[string]bool{}}
}

func (l assignmentList) filtered(query string) []pim.EligibleAssignment {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return l.items
	}
	var out []pim.EligibleAssignment
	for _, item := range l.items {
		haystack := strings.ToLower(item.DisplayName + " " + item.Scope.DisplayName + " " + string(item.Kind))
		if strings.Contains(haystack, query) {
			out = append(out, item)
		}
	}
	return out
}

func (l assignmentList) toggle(id string) {
	l.selected[id] = !l.selected[id]
}

func (l assignmentList) selectedAssignments() []pim.EligibleAssignment {
	return l.selected()
}

func (l assignmentList) selected() []pim.EligibleAssignment {
	var out []pim.EligibleAssignment
	for _, item := range l.items {
		if l.selected[item.ID] {
			out = append(out, item)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/tui
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/assignments.go internal/tui/model_test.go internal/tui/assignments_test.go
git commit -m "feat: add tui navigation and assignment selection"
```

## Task 11: Add Activation Form and Summary Models

**Files:**
- Create: `internal/tui/activation.go`
- Test: `internal/tui/activation_test.go`

- [ ] **Step 1: Write activation form and summary tests**

Create `internal/tui/activation_test.go`:

```go
package tui

import (
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

func TestActivationFormRequiresJustificationAndDuration(t *testing.T) {
	form := activationForm{justification: "", durationISO: "PT2H"}
	if form.valid() {
		t.Fatal("expected empty justification to be invalid")
	}

	form.justification = "Need access"
	form.durationISO = ""
	if form.valid() {
		t.Fatal("expected empty duration to be invalid")
	}

	form.durationISO = "PT2H"
	if !form.valid() {
		t.Fatal("expected complete form to be valid")
	}
}

func TestSummaryGroupsResults(t *testing.T) {
	summary := newSummary([]pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusPendingApproval},
		{Status: pim.ActivationStatusFailed, Retryable: true},
	})

	if len(summary.activated) != 1 || len(summary.pendingApproval) != 1 || len(summary.failed) != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.retryableFailures()) != 1 {
		t.Fatalf("expected one retryable failure")
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run:

```bash
go test ./internal/tui
```

Expected: FAIL because activation models are missing.

- [ ] **Step 3: Implement activation models**

Create `internal/tui/activation.go`:

```go
package tui

import (
	"strings"

	"github.com/mathwro/pim-manager/internal/pim"
)

type activationForm struct {
	justification string
	durationISO   string
}

func (f activationForm) valid() bool {
	return strings.TrimSpace(f.justification) != "" && strings.TrimSpace(f.durationISO) != ""
}

type summary struct {
	activated       []pim.ActivationResult
	pendingApproval []pim.ActivationResult
	failed          []pim.ActivationResult
}

func newSummary(results []pim.ActivationResult) summary {
	var s summary
	for _, result := range results {
		switch {
		case result.Success():
			s.activated = append(s.activated, result)
		case result.PendingApproval():
			s.pendingApproval = append(s.pendingApproval, result)
		case result.Failure():
			s.failed = append(s.failed, result)
		}
	}
	return s
}

func (s summary) retryableFailures() []pim.ActivationResult {
	var out []pim.ActivationResult
	for _, result := range s.failed {
		if result.CanRetry() {
			out = append(out, result)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/tui
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/activation.go internal/tui/activation_test.go
git commit -m "feat: add activation form and summary models"
```

## Task 12: Wire App Runtime

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/tui/model.go`
- Test: `go test ./...`

- [ ] **Step 1: Replace app stub with Bubble Tea runtime wiring**

Modify `internal/app/app.go`:

```go
package app

import (
	"context"
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/graph"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
	"github.com/mathwro/pim-manager/internal/providers/entra"
	"github.com/mathwro/pim-manager/internal/providers/groups"
	"github.com/mathwro/pim-manager/internal/tui"
)

func Run() error {
	auth := azureauth.NewCLI(nil)
	principalID, err := auth.PrincipalID(context.Background())
	if err != nil {
		return err
	}
	httpClient := http.DefaultClient
	graphClient := graph.NewClient(httpClient, auth)
	armClient := arm.NewClient(httpClient, auth)
	scopes, err := azureresources.NewScopeDiscoverer(armClient).Discover(context.Background())
	if err != nil {
		return err
	}
	runtime := tui.Runtime{
		Entra:          entra.NewProvider(graphClient),
		AzureResources: azureresources.NewProvider(armClient, principalID, scopes),
		Groups:         groups.NewProvider(graphClient, principalID),
	}
	_, err := tea.NewProgram(tui.NewModel(runtime), tea.WithAltScreen()).Run()
	return err
}
```

- [ ] **Step 2: Expand TUI runtime type to accept dependencies**

Modify `internal/tui/model.go`:

```go
type Runtime struct {
	Entra          AssignmentProvider
	AzureResources AssignmentProvider
	Groups          AssignmentProvider
}

type AssignmentProvider interface {
	Discover(context.Context) ([]pim.EligibleAssignment, error)
	Activate(context.Context, pim.ActivationRequest) (pim.ActivationResult, error)
}
```

Add `context` and `github.com/mathwro/pim-manager/internal/pim` imports to `model.go`, then keep the rest of `model.go` behavior from Task 10 unchanged.

- [ ] **Step 3: Run full tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Run the CLI smoke check**

Run:

```bash
go run . --help
```

Expected: Output includes `Discover and activate Microsoft PIM eligibilities`.

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/tui/model.go
git commit -m "feat: wire bubble tea app runtime"
```

## Task 13: Finish Documentation and Validation

**Files:**
- Modify: `README.md`
- Verify: `AGENTS.md`
- Verify: `docs/superpowers/specs/2026-07-14-pim-manager-design.md`

- [ ] **Step 1: Create or update README**

Create `README.md`:

````markdown
# pim-manager

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities from the terminal.

## MVP

Running `pim-manager` opens an interactive Bubble Tea TUI with three top-level areas:

- Entra Roles
- Azure Resources
- Groups

The app uses Azure CLI authentication. Sign in before running:

```bash
az login
```

## Development

Run tests:

```bash
go test ./...
```

Run the CLI:

```bash
go run .
```
````

- [ ] **Step 2: Run final validation**

Run:

```bash
go test ./... && go run . --help
```

Expected: tests pass and help output includes `pim-manager`.

- [ ] **Step 3: Commit**

```bash
git add README.md AGENTS.md docs/superpowers/specs/2026-07-14-pim-manager-design.md docs/superpowers/plans/2026-07-14-pim-manager-mvp.md
git commit -m "docs: add pim-manager mvp guidance"
```

## Self-Review

Spec coverage:

- Default `pim-manager` interactive startup: Tasks 1 and 12.
- Azure CLI authentication: Task 3 and Task 12.
- Entra discovery and activation: Task 6.
- Azure Resource scope discovery across management groups, subscriptions, and resource groups: Task 8.
- Azure Resource discovery and activation: Task 9.
- Groups member and owner discovery and activation: Task 7.
- Multi-select and search/filtering: Task 10.
- Shared justification and duration: Task 11.
- Batch activation and partial failure behavior: Task 5.
- Result summary states: Tasks 2, 5, and 11.
- Testing expectations: every implementation task starts with focused tests.

The plan was scanned for filler terms, incomplete instructions, and type-name drift. API endpoint choices are grounded in the Microsoft Learn references listed at the top.
