# Discovery Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce tenant startup latency and make Azure Resource eligibilities usable before effective activation policies finish loading.

**Architecture:** Tenant metadata commands run concurrently. Azure Resource discovery becomes two phases: list-ready eligibility plus scope-specific active-state discovery, followed by background policy preparation. The Bubble Tea model caches normalized results per tenant and section, rejects stale generations, and invalidates cache entries on explicit refresh or activation.

**Tech Stack:** Go 1.24.2, standard-library concurrency, Bubble Tea, Azure CLI, ARM Authorization API 2020-10-01.

## Global Constraints

- Use only documented ARM query parameters; do not add `$filter` to `roleManagementPolicyAssignments`.
- Keep at most four ARM scope requests in flight per phase.
- Keep pagination serial within one scope.
- Cache only normalized assignments in memory; never cache bearer tokens, raw ARM payloads, or errors.
- Preserve current Azure CLI login errors and activation authentication, principal validation, and retry behavior.
- Add no dependency.
- Normal tests must not require live Azure access or wall-clock assertions.

## File Structure

- `internal/azureauth/auth.go`: concurrent tenant and metadata command execution.
- `internal/azureauth/auth_test.go`: concurrency and unchanged tenant behavior tests.
- `internal/app/lazy_providers_test.go`: thread-safe, order-independent command recording.
- `internal/arm/client.go`: explicit context token pinning for multi-request phases.
- `internal/arm/client_test.go`: one-token/multiple-request contract.
- `internal/providers/azureresources/provider.go`: list-ready discovery and policy preparation entry points.
- `internal/providers/azureresources/metadata.go`: normalized scope collection and bounded scope fan-out.
- `internal/providers/azureresources/provider_test.go`: scoped request shape, token count, pagination, errors, and concurrency.
- `internal/tui/model.go`: progressive preparation messages, cache generations, refresh, and invalidation.
- `internal/tui/view.go`: list and detail policy-loading states.
- `internal/tui/model_test.go`: progressive rendering, waiting, cache reuse, invalidation, and stale-result tests.

---

### Task 1: Run Tenant Metadata Commands Concurrently

**Files:**
- Modify: `internal/azureauth/auth.go:73-158`
- Modify: `internal/azureauth/auth_test.go:14-115,272-283`
- Modify: `internal/app/lazy_providers_test.go:96-147`

**Interfaces:**
- Preserves: `func (c CLI) Tenants(context.Context) ([]Tenant, error)`
- Produces no new exported API.

- [ ] **Step 1: Add a failing overlap test**

Add this behavioral test to `internal/azureauth/auth_test.go`. It proves both child commands start before either is released; it does not compare elapsed time.

```go
func TestTenantsRunsTenantAndMetadataLookupsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	client := NewCLI(func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		started <- command
		<-release
		switch command {
		case "az account tenant list --output json":
			return []byte(`[{"tenantId":"tenant-1"}]`), nil
		case "az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json":
			return []byte(`[{"tenantId":"tenant-1","displayName":"Contoso"}]`), nil
		default:
			return nil, fmt.Errorf("unexpected command %s", command)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := client.Tenants(context.Background())
		done <- err
	}()

	commands := []string{<-started, <-started}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Tenants returned error: %v", err)
	}
	slices.Sort(commands)
	want := []string{
		"az account list --all --query [].{tenantId:tenantId,displayName:tenantDisplayName,defaultDomain:tenantDefaultDomain} --output json",
		"az account tenant list --output json",
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("expected both lookups, got %#v", commands)
	}
}
```

Add `fmt` to the test imports.

- [ ] **Step 2: Run the focused test and confirm the serialized implementation blocks**

Run:

```bash
go test ./internal/azureauth -run TestTenantsRunsTenantAndMetadataLookupsConcurrently -count=1
```

Expected before implementation: the test blocks waiting for the second `started` value. Stop the test after confirming the block.

- [ ] **Step 3: Start both commands before parsing either result**

Add a private result type and replace the two serialized `c.run` calls in `Tenants` with buffered result channels:

```go
type commandResult struct {
	out []byte
	err error
}

func (c CLI) Tenants(ctx context.Context) ([]Tenant, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tenantResults := make(chan commandResult, 1)
	accountResults := make(chan commandResult, 1)
	go func() {
		out, err := c.run(ctx, "az", "account", "tenant", "list", "--output", "json")
		tenantResults <- commandResult{out: out, err: err}
	}()
	go func() {
		out, err := c.run(ctx, "az", "account", "list", "--all", "--query", tenantMetadataQuery, "--output", "json")
		accountResults <- commandResult{out: out, err: err}
	}()

	tenantResult := <-tenantResults
	out, err := tenantResult.out, tenantResult.err
```

Keep the existing tenant error classification, JSON parsing, deduplication, and `needsMetadata` calculation unchanged. If metadata is required, receive and process the second result using the existing error wording:

```go
	if !needsMetadata {
		return tenants, nil
	}
	accountResult := <-accountResults
	out, err = accountResult.out, accountResult.err
```

The deferred cancellation stops an unnecessary metadata lookup when tenant parsing fails or metadata is not needed.

- [ ] **Step 4: Make the app command-recording test concurrency-safe and order-independent**

In `internal/app/lazy_providers_test.go`, protect `commands` with `sync.Mutex`, copy it after `Run`, sort both slices with `slices.Sort`, and compare them. Add `slices` and `sync` imports.

```go
var commandMu sync.Mutex
var commands []string
// Inside the runner:
commandMu.Lock()
commands = append(commands, command)
commandMu.Unlock()
// Before comparison:
commandMu.Lock()
got := slices.Clone(commands)
commandMu.Unlock()
slices.Sort(got)
slices.Sort(want)
```

- [ ] **Step 5: Run tenant and app tests**

Run:

```bash
go test ./internal/azureauth ./internal/app -count=1
```

Expected: both packages pass, including login, invalid JSON, metadata error, and lazy-startup tests.

- [ ] **Step 6: Commit the tenant improvement**

```bash
git add internal/azureauth/auth.go internal/azureauth/auth_test.go internal/app/lazy_providers_test.go
git commit -m "perf: overlap tenant metadata lookup"
```

---

### Task 2: Pin Tokens and Scope Azure Resource Discovery

**Files:**
- Modify: `internal/arm/client.go:57-111`
- Modify: `internal/arm/client_test.go:41-108`
- Modify: `internal/providers/azureresources/provider.go:16-98`
- Modify: `internal/providers/azureresources/metadata.go:124-195`
- Modify: `internal/providers/azureresources/provider_test.go:62-322`

**Interfaces:**
- Produces: `func (c Client) PinAccessToken(context.Context) (context.Context, error)`
- Extends provider-local `ARMClient` with `PinAccessToken(context.Context) (context.Context, error)`.
- Preserves: `func (p Provider) Discover(context.Context) ([]pim.EligibleAssignment, error)`, now returning list-ready assignments without policies.
- Produces: `func (p Provider) Prepare(context.Context, []pim.EligibleAssignment) ([]pim.EligibleAssignment, error)`.

- [ ] **Step 1: Add failing token-pinning and phased-provider tests**

Add to `internal/arm/client_test.go`:

```go
func TestPinnedPhaseUsesOneTokenForMultipleRequests(t *testing.T) {
	tokenSource := &recordingTokenSource{}
	client := NewClient(&http.Client{Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}, tokenSource)

	ctx, err := client.PinAccessToken(context.Background())
	if err != nil {
		t.Fatalf("PinAccessToken returned error: %v", err)
	}
	if err := client.Get(ctx, "/one", nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(ctx, "/two", nil); err != nil {
		t.Fatal(err)
	}
	if tokenSource.calls != 1 {
		t.Fatalf("expected one token lookup, got %d", tokenSource.calls)
	}
}
```

Change `recordingTokenSource` to increment `calls`.

Replace the current combined provider discovery expectation with two tests:

```go
func TestDiscoverUsesScopedActiveLookupWithoutLoadingPolicies(t *testing.T) {
	scope := "/subscriptions/sub-1/resourceGroups/rg-prod"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := scope + "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	end := time.Now().UTC().Add(time.Hour)
	arm := newProviderFakeARM(map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{Properties: roleEligibilityProperties{
			Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner", RoleEligibilityScheduleID: "schedule-1",
		}}}},
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{Properties: roleAssignmentScheduleProperties{
			LinkedRoleEligibilityScheduleID: "schedule-1", AssignmentType: "Activated", Status: "Provisioned", EndDateTime: &end,
		}}}},
	})

	assignments, err := NewProvider(arm).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || !assignments[0].Active || assignments[0].ActivationPolicy.MaximumDurationISO != "" {
		t.Fatalf("expected list-ready active assignment, got %#v", assignments)
	}
	if arm.pinCalls != 1 || slices.Contains(arm.recordedPaths(), "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01") {
		t.Fatalf("expected one pin and scoped active path, pins=%d paths=%#v", arm.pinCalls, arm.recordedPaths())
	}
}
```

```go
func TestPrepareLoadsPoliciesOncePerNormalizedScope(t *testing.T) {
	scope := "/subscriptions/SUB-1/"
	policyPath := "/subscriptions/SUB-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := newProviderFakeARM(map[string]any{policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{
		testPolicy("/subscriptions/sub-1", "reader", "PT8H"),
		testPolicy("/subscriptions/sub-1", "owner", "PT4H", "Justification"),
	}})
	assignments := []pim.EligibleAssignment{
		{ID: "reader", AzureScope: scope, RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader"},
		{ID: "owner", AzureScope: "/subscriptions/sub-1", RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/owner"},
	}

	prepared, err := NewProvider(arm).Prepare(context.Background(), assignments)
	if err != nil {
		t.Fatal(err)
	}
	if prepared[0].ActivationPolicy.MaximumDurationISO != "PT8H" || prepared[1].ActivationPolicy.MaximumDurationISO != "PT4H" {
		t.Fatalf("unexpected policies: %#v", prepared)
	}
	if assignments[0].ActivationPolicy.MaximumDurationISO != "" {
		t.Fatal("Prepare mutated the list-ready input slice")
	}
	if arm.pinCalls != 1 || countPath(arm.recordedPaths(), policyPath) != 1 {
		t.Fatalf("expected one token and one scope request, pins=%d paths=%#v", arm.pinCalls, arm.recordedPaths())
	}
}
```

Add `slices`, `sync`, and `github.com/mathwro/pim-manager/internal/arm` imports to the provider tests. Make `providerFakeARM` protect `paths` and `pinCalls` with a mutex, return `arm.WithAccessToken(ctx, "phase-token")`, and expose `recordedPaths` as a copied slice.

- [ ] **Step 2: Run the focused tests and verify they fail**

```bash
go test ./internal/arm -run TestPinnedPhaseUsesOneTokenForMultipleRequests -count=1
go test ./internal/providers/azureresources -run 'TestDiscoverUsesScopedActiveLookupWithoutLoadingPolicies|TestPrepareLoadsPoliciesOncePerNormalizedScope' -count=1
```

Expected: compile failures for missing `PinAccessToken`, missing `Prepare`, and the old root active path.

- [ ] **Step 3: Add explicit phase token pinning to the ARM client**

In `internal/arm/client.go`, add:

```go
func (c Client) PinAccessToken(ctx context.Context) (context.Context, error) {
	if PinnedAccessToken(ctx) != "" {
		return ctx, nil
	}
	token, err := c.tokenSource.AccessToken(ctx, Resource)
	if err != nil {
		return nil, err
	}
	return WithAccessToken(ctx, token), nil
}
```

At the start of `do`, replace the inline token-source branch with:

```go
	ctx, err := c.PinAccessToken(ctx)
	if err != nil {
		return err
	}
	token := PinnedAccessToken(ctx)
```

- [ ] **Step 4: Add bounded scope fan-out**

In `internal/providers/azureresources/metadata.go`, add `sort`, `sync`, and a package constant:

```go
const maxConcurrentScopeRequests = 4
```

Add deterministic scope collection and a standard-library concurrency helper:

```go
func assignmentScopes(assignments []pim.EligibleAssignment) []string {
	byKey := make(map[string]string)
	for _, assignment := range assignments {
		scope := strings.TrimRight(strings.TrimSpace(assignment.AzureScope), "/")
		if scope == "" {
			continue
		}
		key := strings.ToLower(scope)
		if _, exists := byKey[key]; !exists {
			byKey[key] = scope
		}
	}
	scopes := make([]string, 0, len(byKey))
	for _, scope := range byKey {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

func forEachScope(ctx context.Context, scopes []string, fn func(context.Context, int, string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, maxConcurrentScopeRequests)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for index, scope := range scopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			if err := fn(ctx, index, scope); err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}
```

Change active lookup to take a scope while retaining pagination and error context:

```go
func (p Provider) discoverActiveAssignments(ctx context.Context, scope string) ([]roleAssignmentScheduleInstance, error) {
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%%28%%29&api-version=%s", strings.TrimRight(scope, "/"), arm.AuthorizationAPIVersion)
	var out []roleAssignmentScheduleInstance
	for path != "" {
		var response activeAssignmentResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list active Azure role assignments at %s: %w", scope, err)
		}
		out = append(out, response.Value...)
		path = response.NextLink
	}
	return out, nil
}
```

Replace `applyPolicies` with bounded per-scope loading followed by single-threaded assignment normalization:

```go
func (p Provider) applyPolicies(ctx context.Context, assignments []pim.EligibleAssignment) error {
	byScope := make(map[string][]int)
	for index := range assignments {
		key := strings.ToLower(strings.TrimRight(strings.TrimSpace(assignments[index].AzureScope), "/"))
		byScope[key] = append(byScope[key], index)
	}
	scopes := assignmentScopes(assignments)
	policiesByScope := make([][]roleManagementPolicyAssignment, len(scopes))
	if err := forEachScope(ctx, scopes, func(ctx context.Context, index int, scope string) error {
		policies, err := p.policiesForScope(ctx, scope)
		policiesByScope[index] = policies
		return err
	}); err != nil {
		return err
	}
	for scopeIndex, scope := range scopes {
		for _, assignmentIndex := range byScope[strings.ToLower(scope)] {
			policy, err := policyForAssignment(policiesByScope[scopeIndex], assignments[assignmentIndex].RoleDefinitionID)
			if err != nil {
				return fmt.Errorf("activation policy for %s at %s: %w", assignments[assignmentIndex].DisplayName, scope, err)
			}
			assignments[assignmentIndex].ActivationPolicy = policy
		}
	}
	return nil
}
```

- [ ] **Step 5: Split list discovery from policy preparation**

Extend `ARMClient` in `provider.go`:

```go
type ARMClient interface {
	PinAccessToken(context.Context) (context.Context, error)
	Get(context.Context, string, any) error
	Put(context.Context, string, any, any) error
}
```

Implement list-ready discovery:

```go
func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure Resource discovery: %w", err)
	}
	assignments, err := p.discoverEligibilities(ctx)
	if err != nil || len(assignments) == 0 {
		return assignments, err
	}
	scopes := assignmentScopes(assignments)
	activeByScope := make([][]roleAssignmentScheduleInstance, len(scopes))
	if err := forEachScope(ctx, scopes, func(ctx context.Context, index int, scope string) error {
		active, err := p.discoverActiveAssignments(ctx, scope)
		activeByScope[index] = active
		return err
	}); err != nil {
		return nil, err
	}
	var active []roleAssignmentScheduleInstance
	for _, scoped := range activeByScope {
		active = append(active, scoped...)
	}
	applyActiveState(assignments, active, time.Now().UTC())
	return assignments, nil
}
```

Implement preparation without mutating the TUI-visible slice:

```go
func (p Provider) Prepare(ctx context.Context, assignments []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	prepared := slices.Clone(assignments)
	if len(prepared) == 0 {
		return prepared, nil
	}
	ctx, err := p.arm.PinAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate Azure activation policy discovery: %w", err)
	}
	if err := p.applyPolicies(ctx, prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}
```

Add `slices` to `provider.go` imports.

- [ ] **Step 6: Add bounded-concurrency and cancellation tests**

Extend the provider fake with blocking controls that count in-flight `Get` calls under its mutex. Create five unique eligibility scopes, block active responses, and assert exactly four calls start before release. Then release all calls and assert the fifth completes. Add an error variant where the first released request fails and queued requests observe cancellation. Keep assertions on request count and wrapped scope error; do not assert elapsed time.

Use this exact synchronization shape:

```go
started := make(chan string, 5)
release := make(chan struct{})
arm.onGet = func(ctx context.Context, path string) error {
	started <- path
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
done := make(chan error, 1)
go func() {
	_, err := NewProvider(arm).Discover(context.Background())
	done <- err
}()
for range 4 {
	<-started
}
select {
case path := <-started:
	t.Fatalf("fifth request started before capacity was released: %s", path)
default:
}
close(release)
if err := <-done; err != nil {
	t.Fatal(err)
}
```

- [ ] **Step 7: Run provider and ARM tests**

```bash
go test ./internal/arm ./internal/providers/azureresources -count=1
go test -race ./internal/providers/azureresources -count=1
```

Expected: all pass; the race run confirms concurrent fake recording and assignment writes are isolated.

- [ ] **Step 8: Commit the provider pipeline**

```bash
git add internal/arm/client.go internal/arm/client_test.go internal/providers/azureresources/provider.go internal/providers/azureresources/metadata.go internal/providers/azureresources/provider_test.go
git commit -m "perf: scope Azure resource discovery"
```

---

### Task 3: Add Progressive Policy Loading and Session Cache

**Files:**
- Modify: `internal/tui/model.go:42-138,188-290,417-480,649-702,776-805,901-981`
- Modify: `internal/tui/view.go:116-244`
- Modify: `internal/tui/model_test.go:34-49,96-163,165-219,1492-1520`

**Interfaces:**
- Adds private optional capability:
  `type assignmentPreparer interface { Prepare(context.Context, []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) }`
- Keeps `AssignmentProvider` unchanged, so dormant providers and existing fakes remain valid.
- Adds private cache keyed by tenant and section.

- [ ] **Step 1: Add failing progressive-loading tests**

Add a provider that supports the optional preparation capability:

```go
type progressiveProvider struct {
	*scriptedProvider
	prepared    chan []pim.EligibleAssignment
	prepareErr  error
	prepareCalls int
}

func (p *progressiveProvider) Prepare(context.Context, []pim.EligibleAssignment) ([]pim.EligibleAssignment, error) {
	p.prepareCalls++
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	return <-p.prepared, nil
}
```

Teach `runCommand` to forward `assignmentsPreparedMsg` through `Update`.

Add these contracts:

1. `TestAssignmentsDisplayBeforePoliciesFinish`: deliver `assignmentsDiscoveredMsg`, assert the role appears and the view contains `Loading activation requirements`, then release `Prepare` and assert the policy is installed.
2. `TestEnterWaitsForPoliciesThenOpensActivationForm`: select one list-ready role, press Enter, assert the screen remains assignments with the loading message, release preparation, and assert `ScreenActivation` plus the prepared maximum duration.
3. `TestPreparedAssignmentsPreserveSelectionQueryAndCursor`: prepare two ordered IDs and assert `selectedIDs`, `query`, and `listCursor` remain unchanged.
4. `TestDiscoveryCacheReusesPreparedAssignments`: complete both phases, return to home, press Enter, and assert `discoverCalls == 1`, `prepareCalls == 1`, and no discovery command is returned.
5. `TestRefreshInvalidatesDiscoveryCache`: after caching, send `r` on assignments and assert a second discovery starts with a higher generation.
6. `TestActivationCompletionInvalidatesDiscoveryCache`: cache an entry, deliver `activationCompletedMsg`, and assert its key is absent.
7. `TestStalePreparationCannotOverwriteRefreshedDiscovery`: cache generation two, deliver generation-one `assignmentsPreparedMsg`, and assert generation-two assignments remain.
8. `TestPreparationFailureKeepsListAndBlocksActivation`: return a wrapped error, assert the role remains visible, the error is shown, Enter does not open the form, and `r` starts fresh discovery.

Run:

```bash
go test ./internal/tui -run 'TestAssignmentsDisplayBeforePoliciesFinish|TestEnterWaitsForPoliciesThenOpensActivationForm|TestDiscoveryCacheReusesPreparedAssignments|TestRefreshInvalidatesDiscoveryCache|TestPreparationFailureKeepsListAndBlocksActivation' -count=1
```

Expected: compile failures for the missing preparation message and cache state.

- [ ] **Step 2: Add progressive and cache state types**

In `model.go`, add:

```go
type assignmentPreparer interface {
	Prepare(context.Context, []pim.EligibleAssignment) ([]pim.EligibleAssignment, error)
}

type discoveryKey struct {
	tenantID string
	section  Section
}

type discoveryEntry struct {
	assignments   []pim.EligibleAssignment
	policiesReady bool
	generation    int
}

type assignmentsPreparedMsg struct {
	assignments []pim.EligibleAssignment
	key         discoveryKey
	generation  int
	err         error
}
```

Add model fields:

```go
	discoveryCache   map[discoveryKey]discoveryEntry
	policiesReady    bool
	preparingPolicies bool
	waitingForPolicies bool
```

Initialize `discoveryCache` in `NewModel`.

- [ ] **Step 3: Cache list-ready results and launch preparation**

Extend `assignmentsDiscoveredMsg` with `section Section`. In its `Update` case:

```go
	key := discoveryKey{tenantID: typed.tenantID, section: typed.section}
	if typed.checkID != m.discoveryCheck {
		return m, nil
	}
	m.loading = false
	m.err = typed.err
	if typed.err != nil {
		delete(m.discoveryCache, key)
		m.assignmentList = newAssignmentList(nil)
		return m, nil
	}
	entry := discoveryEntry{assignments: typed.assignments, policiesReady: true, generation: typed.checkID}
	m.assignmentList = newAssignmentList(typed.assignments)
	m.policiesReady = true
	m.preparingPolicies = false
	if preparer, ok := m.providerForSection(typed.section).(assignmentPreparer); ok {
		entry.policiesReady = false
		m.policiesReady = false
		m.preparingPolicies = true
		m.discoveryCache[key] = entry
		return m, tea.Batch(m.prepareAssignments(preparer, key, typed.checkID, typed.assignments), m.spinner.Tick)
	}
	m.discoveryCache[key] = entry
	return m, nil
```

The preparation command uses the message key rather than current model state:

```go
func (m Model) prepareAssignments(preparer assignmentPreparer, key discoveryKey, generation int, assignments []pim.EligibleAssignment) tea.Cmd {
	ctx := azureauth.WithTenant(context.Background(), key.tenantID)
	return func() tea.Msg {
		prepared, err := preparer.Prepare(ctx, assignments)
		return assignmentsPreparedMsg{assignments: prepared, key: key, generation: generation, err: err}
	}
}
```

Update `discoverAssignments` to include the section captured when the command is created.

- [ ] **Step 4: Merge prepared results without losing interaction state**

Handle `assignmentsPreparedMsg` before key processing:

```go
	entry, ok := m.discoveryCache[typed.key]
	if !ok || entry.generation != typed.generation {
		return m, nil
	}
	current := typed.key == (discoveryKey{tenantID: m.selectedTenant.ID, section: m.activeSection})
	if typed.err != nil {
		delete(m.discoveryCache, typed.key)
		if current {
			m.preparingPolicies = false
			m.waitingForPolicies = false
			m.err = typed.err
		}
		return m, nil
	}
	entry.assignments = typed.assignments
	entry.policiesReady = true
	m.discoveryCache[typed.key] = entry
	if !current {
		return m, nil
	}
	selected := m.assignmentList.selectedIDs
	m.assignmentList = newAssignmentList(typed.assignments)
	for id, enabled := range selected {
		if enabled {
			m.assignmentList.selectedIDs[id] = true
		}
	}
	m.clampCursor()
	m.policiesReady = true
	m.preparingPolicies = false
	m.err = nil
	if m.waitingForPolicies {
		m.waitingForPolicies = false
		return m.openActivationForm()
	}
	return m, nil
```

Update spinner handling so it continues while `preparingPolicies` or `waitingForPolicies` is true.

- [ ] **Step 5: Reuse and invalidate cache entries**

Change `beginDiscovery` to accept a refresh flag:

```go
func (m Model) beginDiscovery(section Section, refresh bool) (tea.Model, tea.Cmd) {
	m.activeSection = section
	m.screen = ScreenAssignments
	key := discoveryKey{tenantID: m.selectedTenant.ID, section: section}
	if refresh {
		delete(m.discoveryCache, key)
	}
	if entry, ok := m.discoveryCache[key]; ok {
		m.loading = false
		m.err = nil
		m.assignmentList = newAssignmentList(entry.assignments)
		m.policiesReady = entry.policiesReady
		m.preparingPolicies = !entry.policiesReady
		m.waitingForPolicies = false
		return m, nil
	}
	m.loading = true
	m.policiesReady = false
	m.preparingPolicies = false
	m.waitingForPolicies = false
	m.err = nil
	m.query = ""
	m.searchMode = false
	m.assignmentList = newAssignmentList(nil)
	m.listCursor = 0
	m.discoveryCheck++
	return m, tea.Batch(m.discoverAssignments(m.discoveryCheck, m.selectedTenant.ID, section), m.spinner.Tick)
}
```

Use `beginDiscovery(section, false)` from Home and `beginDiscovery(section, true)` for `r`. In the `activationCompletedMsg` case, delete the current key before opening the summary. Reset the three policy-state booleans in `clearTenantWorkflow` but retain the per-tenant cache map.

- [ ] **Step 6: Gate form entry on policy readiness**

In `updateAssignments`, after validating that at least one assignment is selected:

```go
	if !m.policiesReady {
		if m.preparingPolicies {
			m.waitingForPolicies = true
			m.err = nil
			return m, m.spinner.Tick
		}
		m.err = errors.New("activation requirements are unavailable; press r to retry discovery")
		return m, nil
	}
	return m.openActivationForm()
```

While `waitingForPolicies` is true, ignore assignment keys except Esc; Esc clears only the wait state and leaves background preparation running.

- [ ] **Step 7: Render progressive states**

In `viewAssignments`, keep the existing full-screen discovery panel for `m.loading`. When list-ready assignments are visible and `m.preparingPolicies` is true, render:

```go
b.WriteString(mutedStyle.Render(fmt.Sprintf("%s  Loading activation requirements...\n\n", m.spinner.View())))
```

When `m.waitingForPolicies` is true, use warning styling and the same exact text. In `viewDetails`, replace maximum duration, justification, and authentication values with `Loading...` while `!m.policiesReady`; keep identity, scope, assignment type, and active state visible.

- [ ] **Step 8: Run focused and full TUI tests**

```bash
go test ./internal/tui -run 'TestAssignmentsDisplayBeforePoliciesFinish|TestEnterWaitsForPoliciesThenOpensActivationForm|TestPreparedAssignmentsPreserveSelectionQueryAndCursor|TestDiscoveryCacheReusesPreparedAssignments|TestRefreshInvalidatesDiscoveryCache|TestActivationCompletionInvalidatesDiscoveryCache|TestStalePreparationCannotOverwriteRefreshedDiscovery|TestPreparationFailureKeepsListAndBlocksActivation' -count=1
go test ./internal/tui -count=1
```

Expected: all pass; existing selection, tenant switching, activation, principal-drift, and summary tests remain unchanged.

- [ ] **Step 9: Commit progressive discovery**

```bash
git add internal/tui/model.go internal/tui/view.go internal/tui/model_test.go
git commit -m "perf: progressively load activation policies"
```

---

### Task 4: Format, Verify, and Smoke-Test

**Files:**
- Verify all modified Go files.
- No new production files.

**Interfaces:**
- Confirms the complete tenant, provider, TUI, and activation behavior.

- [ ] **Step 1: Format once after implementation**

```bash
gofmt -w internal/azureauth/auth.go internal/azureauth/auth_test.go internal/app/lazy_providers_test.go internal/arm/client.go internal/arm/client_test.go internal/providers/azureresources/provider.go internal/providers/azureresources/metadata.go internal/providers/azureresources/provider_test.go internal/tui/model.go internal/tui/view.go internal/tui/model_test.go
```

- [ ] **Step 2: Run package tests and race-sensitive packages**

```bash
go test ./...
go test -race ./internal/azureauth ./internal/providers/azureresources ./internal/tui
```

Expected: 11 tested packages pass, one package reports no tests, and the race detector reports no races.

- [ ] **Step 3: Build the executable**

```bash
go build -o /tmp/pim-manager-discovery .
```

Expected: exit status zero and `/tmp/pim-manager-discovery` is created.

- [ ] **Step 4: Exercise the interactive flow against Azure**

Run:

```bash
/tmp/pim-manager-discovery
```

Use the same signed-in account as the baseline. Verify:

1. Tenant selection contains the same two tenants and labels.
2. The measured tenant shows six eligibilities.
3. Two linked eligibilities show active.
4. The list becomes interactive while `Loading activation requirements...` is visible.
5. Policy loading completes for all six assignments without changing selection or cursor.
6. Leaving and re-entering Azure Resources performs no visible reload.
7. Pressing `r` performs a fresh discovery.
8. No activation request is submitted during this read-only smoke test.

Record tenant startup, list-ready, and policy-ready elapsed times. Compare list-ready time with the 39.944-second baseline; do not fail solely on an Azure-controlled timing outlier if counts and request shape are correct.

- [ ] **Step 5: Commit any formatter-only changes if present**

If `gofmt` changed files after the task commits:

```bash
git add internal
git commit -m "style: format discovery changes"
```

If it changed nothing, do not create an empty commit.
