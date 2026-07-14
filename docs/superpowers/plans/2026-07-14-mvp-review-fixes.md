# MVP Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the full-branch review gaps by making the TUI exercise discovery and activation, moving Azure discovery errors into the interactive flow, adding Graph pagination, and classifying retryable activation failures.

**Architecture:** Keep Cobra startup thin: construct Azure-backed provider adapters and start Bubble Tea immediately. The TUI owns section discovery, assignment selection, shared activation form data, batch activation, summaries, and retryable-failure retry state. Providers remain responsible for API normalization and pagination.

**Tech Stack:** Go 1.24.2, Cobra, Bubble Tea, Microsoft Graph REST, Azure Resource Manager REST, Azure CLI authentication.

## Global Constraints

- Do not add custom credential storage; use Azure CLI authentication through `az`.
- Keep top-level MVP sections as Entra Roles, Azure Resources, and Groups.
- Azure Resources include management groups, subscriptions, and resource groups.
- Batch activation uses one shared justification and ISO-8601 duration.
- Activation is sequential and continues on per-item failures.
- Final statuses are `activated`, `pending_approval`, and `failed`.
- Retry must be manual and target only retryable failures.
- Use TDD: write failing tests, watch them fail, implement, then verify passing tests.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/app/app.go` | Construct Azure CLI auth, Graph/ARM clients, lazy providers, and start Bubble Tea without preflight Graph/ARM discovery. |
| `internal/app/lazy_providers.go` | Provide `tui.AssignmentProvider` adapters that defer principal lookup and Azure Resource scope discovery until the selected section is discovered. |
| `internal/tui/model.go` | Bubble Tea screen state, section navigation, discovery command handling, activation form input, activation command handling, summary/retry transitions, and text rendering. |
| `internal/tui/model_test.go` | End-to-end model tests for discovery errors, section discovery, activation, summary, and retryable-failure retry. |
| `internal/providers/entra/provider.go` | Add Graph `@odata.nextLink` pagination for Entra eligibility discovery. |
| `internal/providers/entra/provider_test.go` | Test Entra paginated discovery. |
| `internal/providers/groups/provider.go` | Add Graph `@odata.nextLink` pagination for Groups eligibility discovery. |
| `internal/providers/groups/provider_test.go` | Test Groups paginated discovery. |
| `internal/activation/service.go` | Respect retryability from provider-classified errors instead of marking every provider error retryable. |
| `internal/activation/service_test.go` | Test retryable and non-retryable error classification. |
| `README.md` | Keep MVP wording accurate after real TUI flow is implemented. |

---

## Task 1: Start TUI Without Azure Preflight

**Files:**
- Modify: `internal/app/app.go`
- Create: `internal/app/lazy_providers.go`
- Test: `internal/app/lazy_providers_test.go`

**Interfaces:**
- Consumes: `tui.AssignmentProvider`, `azureauth.CLI`, `graph.Client`, `arm.Client`, `entra.NewProvider`, `groups.NewProvider`, `azureresources.NewScopeDiscoverer`, `azureresources.NewProvider`.
- Produces: `app.Run() error` that starts Bubble Tea without calling `PrincipalID` or ARM scope discovery first.

- [ ] **Step 1: Write failing lazy provider tests**

Create `internal/app/lazy_providers_test.go`:

```go
package app

import (
	"context"
	"testing"

	"github.com/mathwro/pim-manager/internal/pim"
)

type fakePrincipalSource struct {
	id    string
	calls int
	err   error
}

func (f *fakePrincipalSource) PrincipalID(context.Context) (string, error) {
	f.calls++
	return f.id, f.err
}

type fakeScopeDiscoverer struct {
	scopes []string
	calls  int
	err    error
}

func (f *fakeScopeDiscoverer) Discover(context.Context) ([]string, error) {
	f.calls++
	return f.scopes, f.err
}

type fakeProviderFactory struct {
	principalID string
	scopes      []string
	discovered  []pim.EligibleAssignment
}

func (f *fakeProviderFactory) newProvider(principalID string, scopes []string) lazyAssignmentProvider {
	f.principalID = principalID
	f.scopes = scopes
	return lazyAssignmentProvider{
		discover: func(context.Context) ([]pim.EligibleAssignment, error) {
			return f.discovered, nil
		},
		activate: func(context.Context, pim.ActivationRequest) (pim.ActivationResult, error) {
			return pim.ActivationResult{}, nil
		},
	}
}

func TestLazyAzureResourcesProviderDefersPrincipalAndScopesUntilDiscover(t *testing.T) {
	principal := &fakePrincipalSource{id: "principal-1"}
	scopes := &fakeScopeDiscoverer{scopes: []string{"/subscriptions/sub-1"}}
	factory := &fakeProviderFactory{discovered: []pim.EligibleAssignment{{ID: "assignment-1"}}}
	provider := newLazyAzureResourcesProvider(principal, scopes, factory.newProvider)

	if principal.calls != 0 || scopes.calls != 0 {
		t.Fatal("expected constructor not to call Azure dependencies")
	}

	assignments, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 || assignments[0].ID != "assignment-1" {
		t.Fatalf("unexpected assignments: %#v", assignments)
	}
	if factory.principalID != "principal-1" || len(factory.scopes) != 1 {
		t.Fatalf("expected principal and scopes passed to factory, got %q %#v", factory.principalID, factory.scopes)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm it fails**

Run: `go test ./internal/app`

Expected: FAIL because lazy provider types and constructor are missing.

- [ ] **Step 3: Implement lazy providers and update app.Run**

Create `internal/app/lazy_providers.go` with `principalSource`, `scopeDiscoverer`, `lazyAssignmentProvider`, and `newLazyAzureResourcesProvider`. Modify `internal/app/app.go` so `Run` constructs auth, clients, and providers, then starts Bubble Tea immediately:

```go
runtime := tui.Runtime{
	Entra:          entra.NewProvider(graphClient),
	AzureResources: newLazyAzureResourcesProvider(auth, azureresources.NewScopeDiscoverer(armClient), func(principalID string, scopes []string) lazyAssignmentProvider {
		provider := azureresources.NewProvider(armClient, principalID, scopes)
		return lazyAssignmentProvider{discover: provider.Discover, activate: provider.Activate}
	}),
	Groups: groups.NewProvider(graphClient, ""),
}
```

For Groups, use a lazy principal wrapper too, because `groups.NewProvider` requires the principal ID for discovery:

```go
func newLazyPrincipalProvider(principal principalSource, factory func(string) lazyAssignmentProvider) lazyAssignmentProvider
```

- [ ] **Step 4: Run app tests**

Run: `go test ./internal/app`

Expected: PASS.

- [ ] **Step 5: Run smoke check**

Run: `go run . --help`

Expected: output includes `pim-manager` and does not require `az login`.

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/app/lazy_providers.go internal/app/lazy_providers_test.go
git commit -m "fix: defer azure discovery until tui interaction"
```

---

## Task 2: Implement End-to-End TUI Discovery and Activation Flow

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `Runtime`, `AssignmentProvider`, `assignmentList`, `activationForm`, `newSummary`, `pim.ActivationRequest`, `pim.ActivationResult`.
- Produces: Bubble Tea model behavior for section discovery, assignment selection, shared form entry, sequential activation, result summary, and retryable-failure retry.

- [ ] **Step 1: Write failing model flow tests**

Add tests to `internal/tui/model_test.go`:

```go
type scriptedProvider struct {
	discoveries [][]pim.EligibleAssignment
	results     []pim.ActivationResult
	discoverErr error
	activated   []pim.ActivationRequest
}

func (p *scriptedProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
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
	p.activated = append(p.activated, request)
	result := p.results[0]
	p.results = p.results[1:]
	result.Assignment = request.Assignment
	return result, nil
}

func TestModelDiscoversSelectedSectionAndActivatesSelection(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Global Reader"},
			{ID: "two", DisplayName: "Privileged Role Administrator"},
		}},
		results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}},
	}
	model := NewModel(Runtime{Entra: provider})

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	msg := cmd()
	next, _ = model.Update(msg)
	model = next.(Model)
	model.assignmentList.toggle("one")
	model.form.justification = "Need access"
	model.form.durationISO = "PT2H"

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	model = next.(Model)
	msg = cmd()
	next, _ = model.Update(msg)
	model = next.(Model)

	if model.screen != ScreenSummary {
		t.Fatalf("expected summary screen, got %s", model.screen)
	}
	if len(provider.activated) != 1 || provider.activated[0].Justification != "Need access" || provider.activated[0].DurationISO != "PT2H" {
		t.Fatalf("unexpected activation requests: %#v", provider.activated)
	}
	if !strings.Contains(model.View(), "activated") {
		t.Fatalf("expected rendered summary, got %q", model.View())
	}
}

func TestModelShowsDiscoveryErrorWithoutLeavingTUI(t *testing.T) {
	model := NewModel(Runtime{Entra: &scriptedProvider{discoverErr: errors.New("az login required")}})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	next, _ = model.Update(cmd())
	model = next.(Model)

	if model.screen != ScreenAssignments {
		t.Fatalf("expected assignments screen, got %s", model.screen)
	}
	if !strings.Contains(model.View(), "az login required") {
		t.Fatalf("expected discovery error in view, got %q", model.View())
	}
}
```

- [ ] **Step 2: Run focused tests and confirm they fail**

Run: `go test ./internal/tui`

Expected: FAIL because discovery commands, activation commands, and rendering are missing.

- [ ] **Step 3: Implement TUI commands and state**

Update `Model` with fields:

```go
assignmentList assignmentList
form           activationForm
summary        summary
loading        bool
err            error
```

Add message types:

```go
type assignmentsDiscoveredMsg struct {
	assignments []pim.EligibleAssignment
	err         error
}

type activationCompletedMsg struct {
	results []pim.ActivationResult
}
```

Add `providerForSection`, `discoverAssignments`, and `activateSelected` methods. Use `tea.KeyCtrlA` to activate selected assignments when the form is valid. Keep activation sequential by looping over selected assignments in the command.

- [ ] **Step 4: Render useful screens**

Update `View()` so it renders:
- Home: section list and enter hint.
- Assignments: selected section, loading/error state, filtered assignments, selected marker, and activation hint.
- Summary: activated/pending/failed counts plus retryable failure count.

- [ ] **Step 5: Run focused tests**

Run: `go test ./internal/tui`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat: implement tui discovery and activation flow"
```

---

## Task 3: Add Graph Discovery Pagination

**Files:**
- Modify: `internal/providers/entra/provider.go`
- Modify: `internal/providers/entra/provider_test.go`
- Modify: `internal/providers/groups/provider.go`
- Modify: `internal/providers/groups/provider_test.go`

**Interfaces:**
- Consumes: Graph clients that accept absolute `https://` nextLink paths.
- Produces: Entra and Groups discovery methods that collect every page until `@odata.nextLink` is empty.

- [ ] **Step 1: Write failing pagination tests**

In Entra and Groups provider tests, add fake Graph clients returning a first page with `NextLink: "https://graph.microsoft.com/v1.0/next"` and a second page with a different assignment. Assert both assignments are returned and both paths were requested.

- [ ] **Step 2: Run provider tests and confirm they fail**

Run: `go test ./internal/providers/entra ./internal/providers/groups`

Expected: FAIL because only one page is fetched.

- [ ] **Step 3: Implement nextLink loops**

Add `NextLink string `json:"@odata.nextLink"` to both response structs and change discovery to:

```go
path := firstPath
for path != "" {
	var response eligibilityResponse
	if err := p.graph.Get(ctx, path, &response); err != nil {
		return nil, err
	}
	for _, item := range response.Value {
		assignments = append(assignments, normalizeEligibility(item))
	}
	path = response.NextLink
}
```

- [ ] **Step 4: Run provider tests**

Run: `go test ./internal/providers/entra ./internal/providers/groups`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/providers/entra/provider.go internal/providers/entra/provider_test.go internal/providers/groups/provider.go internal/providers/groups/provider_test.go
git commit -m "fix: paginate graph pim discovery"
```

---

## Task 4: Classify Activation Error Retryability

**Files:**
- Modify: `internal/activation/service.go`
- Modify: `internal/activation/service_test.go`
- Verify: `README.md`

**Interfaces:**
- Consumes: provider errors from Entra, Groups, and Azure Resources.
- Produces: `activation.RetryableError`, `activation.NewRetryableError(error) error`, and `activation.IsRetryable(error) bool`.

- [ ] **Step 1: Write failing retryability tests**

Add to `internal/activation/service_test.go`:

```go
func TestActivateBatchMarksOnlyRetryableErrorsRetryable(t *testing.T) {
	service := NewService(&fakeProvider{err: NewRetryableError(errors.New("timeout"))})
	results := service.ActivateBatch(context.Background(), []pim.ActivationRequest{{Assignment: pim.EligibleAssignment{ID: "one"}}})
	if len(results) != 1 || !results[0].CanRetry() {
		t.Fatalf("expected retryable failure, got %#v", results)
	}

	service = NewService(&fakeProvider{err: errors.New("policy denied")})
	results = service.ActivateBatch(context.Background(), []pim.ActivationRequest{{Assignment: pim.EligibleAssignment{ID: "one"}}})
	if len(results) != 1 || results[0].CanRetry() {
		t.Fatalf("expected non-retryable failure, got %#v", results)
	}
}
```

- [ ] **Step 2: Run activation tests and confirm they fail**

Run: `go test ./internal/activation`

Expected: FAIL because `NewRetryableError` is missing and all errors are currently retryable.

- [ ] **Step 3: Implement retryable error wrapper**

Add:

```go
type RetryableError struct {
	err error
}

func NewRetryableError(err error) error {
	return RetryableError{err: err}
}

func (e RetryableError) Error() string { return e.err.Error() }
func (e RetryableError) Unwrap() error { return e.err }

func IsRetryable(err error) bool {
	var retryable RetryableError
	return errors.As(err, &retryable)
}
```

Change provider error mapping to `Retryable: IsRetryable(err)`.

- [ ] **Step 4: Update old retryability expectations**

Change existing tests that expected every provider error to be retryable so they wrap network-like errors with `NewRetryableError(errors.New("network down"))`.

- [ ] **Step 5: Run all tests and CLI smoke check**

Run: `go test ./... && go run . --help`

Expected: PASS and help output includes `pim-manager`.

- [ ] **Step 6: Commit**

```bash
git add internal/activation/service.go internal/activation/service_test.go README.md
git commit -m "fix: classify retryable activation failures"
```

---

## Self-Review

- Spec coverage: Task 1 fixes startup preflight and keeps Azure failures inside the TUI. Task 2 implements the missing interactive discovery, selection, activation, summary, and retry state. Task 3 fixes missing Graph pagination. Task 4 fixes retryability semantics.
- Placeholder scan: no TBD/TODO/implement-later placeholders are present.
- Type consistency: provider signatures match `tui.AssignmentProvider`; activation requests/results use `pim.ActivationRequest` and `pim.ActivationResult`; status helpers remain in `internal/pim`.
