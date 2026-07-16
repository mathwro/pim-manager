# Policy-Aware Azure Activations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show active Azure Resource PIM eligibilities and build activation requests from each role's effective maximum-duration and justification policy.

**Architecture:** Enrich `pim.EligibleAssignment` inside the Azure Resources provider by joining eligibility schedule instances with active assignment schedule instances and effective role-management policy assignments. Keep the TUI ARM-agnostic: it consumes active/policy metadata, prevents selection of active rows, and manages one shared justification plus one duration per selected assignment.

**Tech Stack:** Go 1.24.2, Azure Resource Manager Authorization REST API `2020-10-01`, Cobra, Bubble Tea, Bubbles, Lip Gloss, standard `testing` package.

## Global Constraints

- Work only on branch `feat/policy-aware-activations` in `.worktrees/policy-aware-activations`.
- Azure Resources remains the only active top-level section; do not reactivate Entra Roles or Groups.
- Continue using the user's Azure CLI authentication; add no login flow or dependency.
- Use only supported ARM Authorization endpoints with API version `2020-10-01`.
- Discover eligibilities once, active schedule instances once, and policy assignments once per unique represented scope; follow every `nextLink`.
- Never guess active state, `PT1H`, maximum duration, or optional justification when required metadata is unavailable.
- Active assignments stay visible and focusable but cannot be selected.
- One shared justification applies to every request; it is required when any selected assignment requires it.
- Every selected assignment owns an editable duration defaulted to its own effective maximum.
- Do not add custom ISO-8601 parsing; preserve actionable per-assignment ARM activation errors.
- Normal tests must not require live Azure access.
- Product design: `docs/superpowers/specs/2026-07-16-policy-aware-azure-activations-design.md`.

---

### Task 1: Add Policy Metadata and Selection Invariants

**Files:**
- Modify: `internal/pim/types.go:1-54`
- Modify: `internal/tui/assignments.go:9-49`
- Modify: `internal/tui/assignments_test.go:9-29`
- Modify: `internal/tui/model.go:524-549`
- Modify: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: existing `pim.EligibleAssignment`, `assignmentList.toggle`, and `Model.toggleAllFiltered`.
- Produces: `pim.ActivationPolicy`, `EligibleAssignment.Active`, `EligibleAssignment.ActiveUntil`, `EligibleAssignment.ActivationPolicy`, active-aware filtering, and selection methods that never retain active IDs.

- [ ] **Step 1: Write failing assignment-state tests**

Append to `internal/tui/assignments_test.go`:

```go
func TestAssignmentListDoesNotSelectActiveAssignment(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "inactive", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true},
	})

	list.toggle("active")
	list.toggle("inactive")

	selected := list.selected()
	if len(selected) != 1 || selected[0].ID != "inactive" {
		t.Fatalf("expected only inactive assignment selected, got %#v", selected)
	}
}

func TestAssignmentListFiltersByActiveState(t *testing.T) {
	list := newAssignmentList([]pim.EligibleAssignment{
		{ID: "inactive", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true},
	})

	filtered := list.filtered("active")
	if len(filtered) != 1 || filtered[0].ID != "active" {
		t.Fatalf("expected active assignment, got %#v", filtered)
	}
}
```

Append to `internal/tui/model_test.go`:

```go
func TestToggleAllFilteredSkipsActiveAssignments(t *testing.T) {
	model := NewModel(Runtime{})
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "inactive", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true},
	})

	model.toggleAllFiltered()

	selected := model.assignmentList.selected()
	if len(selected) != 1 || selected[0].ID != "inactive" {
		t.Fatalf("expected select-all to skip active assignment, got %#v", selected)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestAssignmentListDoesNotSelectActiveAssignment|TestAssignmentListFiltersByActiveState|TestToggleAllFilteredSkipsActiveAssignments' -count=1
```

Expected: compile failure because `EligibleAssignment.Active` does not exist.

- [ ] **Step 3: Add domain metadata**

Update `internal/pim/types.go` imports and domain types:

```go
import (
	"fmt"
	"time"
)

type ActivationPolicy struct {
	MaximumDurationISO    string
	JustificationRequired bool
}
```

Add these fields to `EligibleAssignment` after `ConditionVersion`:

```go
	Active           bool
	ActiveUntil      *time.Time
	ActivationPolicy ActivationPolicy
```

No behavior method is needed; these are provider-normalized values.

- [ ] **Step 4: Make assignment selection active-aware**

Replace `assignmentList.filtered` and `assignmentList.toggle` behavior in `internal/tui/assignments.go` with:

```go
func (l assignmentList) filtered(query string) []pim.EligibleAssignment {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return l.items
	}
	var out []pim.EligibleAssignment
	for _, item := range l.items {
		state := "inactive"
		if item.Active {
			state = "active"
		}
		haystack := strings.ToLower(item.DisplayName + " " + item.Scope.DisplayName + " " + string(item.Kind) + " " + state)
		if strings.Contains(haystack, query) {
			out = append(out, item)
		}
	}
	return out
}

func (l assignmentList) toggle(id string) {
	for _, item := range l.items {
		if item.ID != id {
			continue
		}
		if item.Active {
			delete(l.selectedIDs, id)
			return
		}
		l.selectedIDs[id] = !l.selectedIDs[id]
		return
	}
}
```

Replace `Model.toggleAllFiltered` in `internal/tui/model.go` with:

```go
func (m *Model) toggleAllFiltered() {
	filtered := m.assignmentList.filtered(m.query)
	allSelected := true
	selectable := 0
	for _, assignment := range filtered {
		if assignment.Active {
			delete(m.assignmentList.selectedIDs, assignment.ID)
			continue
		}
		selectable++
		if !m.assignmentList.selectedIDs[assignment.ID] {
			allSelected = false
		}
	}
	if selectable == 0 {
		return
	}
	for _, assignment := range filtered {
		if !assignment.Active {
			m.assignmentList.selectedIDs[assignment.ID] = !allSelected
		}
	}
	m.err = nil
}
```

- [ ] **Step 5: Run focused and package tests**

Run:

```bash
gofmt -w internal/pim/types.go internal/tui/assignments.go internal/tui/assignments_test.go internal/tui/model.go internal/tui/model_test.go
go test ./internal/tui ./internal/pim -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/pim/types.go internal/tui/assignments.go internal/tui/assignments_test.go internal/tui/model.go internal/tui/model_test.go
git commit -m "feat: model active assignment policies"
```

---

### Task 2: Enrich Azure Eligibilities from Active Instances and Policies

**Files:**
- Modify: `internal/providers/azureresources/provider.go:29-103`
- Create: `internal/providers/azureresources/metadata.go`
- Modify: `internal/providers/azureresources/provider_test.go:1-139`
- Create: `internal/providers/azureresources/metadata_test.go`

**Interfaces:**
- Consumes: `pim.ActivationPolicy`, `EligibleAssignment.Active`, `EligibleAssignment.ActiveUntil`, `EligibleAssignment.ActivationPolicy`, and `ARMClient.Get`.
- Produces: `Provider.Discover(context.Context) ([]pim.EligibleAssignment, error)` with every returned assignment enriched; `activeAssignmentResponse`, `policyAssignmentResponse`, `isCurrentActivation`, `resourceName`, and `policyForAssignment` inside the Azure provider package.

- [ ] **Step 1: Write failing active and policy normalization tests**

Create `internal/providers/azureresources/metadata_test.go`:

```go
package azureresources

import (
	"testing"
	"time"
)

func TestIsCurrentActivation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		item roleAssignmentScheduleInstance
		want bool
	}{
		{
			name: "current",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Provisioned", StartDateTime: &past, EndDateTime: &future,
			}},
			want: true,
		},
		{
			name: "upcoming",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Provisioned", StartDateTime: &future,
			}},
		},
		{
			name: "expired",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Provisioned", EndDateTime: &past,
			}},
		},
		{
			name: "revoked",
			item: roleAssignmentScheduleInstance{Properties: roleAssignmentScheduleProperties{
				AssignmentType: "Activated", Status: "Revoked", StartDateTime: &past, EndDateTime: &future,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCurrentActivation(test.item, now); got != test.want {
				t.Fatalf("expected %v, got %v", test.want, got)
			}
		})
	}
}

func TestPolicyForAssignmentUsesEffectiveEndUserRules(t *testing.T) {
	policy, err := policyForAssignment([]roleManagementPolicyAssignment{{
		Properties: roleManagementPolicyAssignmentProperties{
			RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/OWNER",
			EffectiveRules: []roleManagementPolicyRule{
				{ID: "Expiration_EndUser_Assignment", MaximumDuration: "PT8H"},
				{ID: "Enablement_EndUser_Assignment", EnabledRules: []string{"MultiFactorAuthentication", "Justification"}},
			},
		},
	}}, "/subscriptions/sub-1/providers/microsoft.authorization/roledefinitions/owner")
	if err != nil {
		t.Fatalf("policyForAssignment returned error: %v", err)
	}
	if policy.MaximumDurationISO != "PT8H" || !policy.JustificationRequired {
		t.Fatalf("unexpected policy: %#v", policy)
	}
}
```

- [ ] **Step 2: Run metadata tests and verify RED**

Run:

```bash
go test ./internal/providers/azureresources -run 'TestIsCurrentActivation|TestPolicyForAssignmentUsesEffectiveEndUserRules' -count=1
```

Expected: compile failure because metadata response types and helpers do not exist.

- [ ] **Step 3: Add active and policy response models**

Create `internal/providers/azureresources/metadata.go` with these package-private types and helpers:

```go
package azureresources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/pim"
)

type activeAssignmentResponse struct {
	Value    []roleAssignmentScheduleInstance `json:"value"`
	NextLink string                           `json:"nextLink"`
}

type roleAssignmentScheduleInstance struct {
	Properties roleAssignmentScheduleProperties `json:"properties"`
}

type roleAssignmentScheduleProperties struct {
	LinkedRoleEligibilityScheduleID string     `json:"linkedRoleEligibilityScheduleId"`
	AssignmentType                  string     `json:"assignmentType"`
	Status                          string     `json:"status"`
	StartDateTime                   *time.Time `json:"startDateTime"`
	EndDateTime                     *time.Time `json:"endDateTime"`
}

type policyAssignmentResponse struct {
	Value    []roleManagementPolicyAssignment `json:"value"`
	NextLink string                           `json:"nextLink"`
}

type roleManagementPolicyAssignment struct {
	Properties roleManagementPolicyAssignmentProperties `json:"properties"`
}

type roleManagementPolicyAssignmentProperties struct {
	Scope            string                     `json:"scope"`
	RoleDefinitionID string                     `json:"roleDefinitionId"`
	EffectiveRules   []roleManagementPolicyRule `json:"effectiveRules"`
}

type roleManagementPolicyRule struct {
	ID              string   `json:"id"`
	RuleType        string   `json:"ruleType"`
	MaximumDuration string   `json:"maximumDuration"`
	EnabledRules    []string `json:"enabledRules"`
}

func resourceName(id string) string {
	id = strings.TrimRight(strings.TrimSpace(id), "/")
	if index := strings.LastIndexByte(id, '/'); index >= 0 {
		id = id[index+1:]
	}
	return strings.ToLower(id)
}

func isCurrentActivation(item roleAssignmentScheduleInstance, now time.Time) bool {
	properties := item.Properties
	if !strings.EqualFold(properties.AssignmentType, "Activated") {
		return false
	}
	switch strings.ToLower(properties.Status) {
	case "denied", "revoked", "canceled", "failed", "timedout", "invalid", "admindenied", "failedasresourceislocked":
		return false
	}
	if properties.StartDateTime != nil && properties.StartDateTime.After(now) {
		return false
	}
	return properties.EndDateTime == nil || properties.EndDateTime.After(now)
}

func policyForAssignment(policies []roleManagementPolicyAssignment, roleDefinitionID string) (pim.ActivationPolicy, error) {
	roleName := resourceName(roleDefinitionID)
	for _, assignment := range policies {
		if resourceName(assignment.Properties.RoleDefinitionID) != roleName {
			continue
		}
		var policy pim.ActivationPolicy
		var enablementFound bool
		for _, rule := range assignment.Properties.EffectiveRules {
			switch {
			case strings.EqualFold(rule.ID, "Expiration_EndUser_Assignment"):
				policy.MaximumDurationISO = strings.TrimSpace(rule.MaximumDuration)
			case strings.EqualFold(rule.ID, "Enablement_EndUser_Assignment"):
				enablementFound = true
				for _, enabled := range rule.EnabledRules {
					if strings.EqualFold(enabled, "Justification") {
						policy.JustificationRequired = true
					}
				}
			}
		}
		if policy.MaximumDurationISO == "" || !enablementFound {
			return pim.ActivationPolicy{}, fmt.Errorf("incomplete end-user activation policy for role definition %s", roleDefinitionID)
		}
		return policy, nil
	}
	return pim.ActivationPolicy{}, fmt.Errorf("no activation policy for role definition %s", roleDefinitionID)
}
```

- [ ] **Step 4: Run metadata tests and verify GREEN**

Run:

```bash
gofmt -w internal/providers/azureresources/metadata.go internal/providers/azureresources/metadata_test.go
go test ./internal/providers/azureresources -run 'TestIsCurrentActivation|TestPolicyForAssignmentUsesEffectiveEndUserRules' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write the failing enriched-discovery test**

Add a provider test using one eligibility, one matching active instance, and one policy assignment:

```go
func TestDiscoverEnrichesActiveStateAndActivationPolicy(t *testing.T) {
	scope := "/subscriptions/sub-1"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	end := time.Now().UTC().Add(time.Hour)
	arm := &providerFakeARM{responses: map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{{
			ID: "eligibility-1",
			Properties: roleEligibilityProperties{
				Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
				RoleEligibilityScheduleID: "schedule-1",
				ExpandedProperties: expandedProperties{RoleDefinition: expandedRoleDefinition{DisplayName: "Owner"}},
			},
		}}},
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{Properties: roleAssignmentScheduleProperties{
			LinkedRoleEligibilityScheduleID: "/providers/Microsoft.Authorization/roleEligibilitySchedules/SCHEDULE-1",
			AssignmentType: "Activated", Status: "Provisioned", EndDateTime: &end,
		}}}},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
			Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner",
			EffectiveRules: []roleManagementPolicyRule{
				{ID: "Expiration_EndUser_Assignment", MaximumDuration: "PT4H"},
				{ID: "Enablement_EndUser_Assignment", EnabledRules: []string{"Justification"}},
			},
		}}}},
	}}

	assignments, err := NewProvider(arm).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(assignments) != 1 || !assignments[0].Active || assignments[0].ActiveUntil == nil {
		t.Fatalf("expected active assignment, got %#v", assignments)
	}
	if assignments[0].ActivationPolicy.MaximumDurationISO != "PT4H" || !assignments[0].ActivationPolicy.JustificationRequired {
		t.Fatalf("unexpected policy: %#v", assignments[0].ActivationPolicy)
	}
}
```

Add `time` to `provider_test.go` imports.

- [ ] **Step 6: Run enriched discovery test and verify RED**

Run:

```bash
go test ./internal/providers/azureresources -run TestDiscoverEnrichesActiveStateAndActivationPolicy -count=1
```

Expected: FAIL because `Discover` only returns normalized eligibility data.

- [ ] **Step 7: Add paginated metadata retrieval and enrichment**

Add these methods to `metadata.go`:

```go
func (p Provider) discoverActiveAssignments(ctx context.Context) ([]roleAssignmentScheduleInstance, error) {
	path := fmt.Sprintf("/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%%28%%29&api-version=%s", arm.AuthorizationAPIVersion)
	var out []roleAssignmentScheduleInstance
	for path != "" {
		var response activeAssignmentResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list active Azure role assignments: %w", err)
		}
		out = append(out, response.Value...)
		path = response.NextLink
	}
	return out, nil
}

func (p Provider) policiesForScope(ctx context.Context, scope string) ([]roleManagementPolicyAssignment, error) {
	path := fmt.Sprintf("%s/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=%s", strings.TrimRight(scope, "/"), arm.AuthorizationAPIVersion)
	var out []roleManagementPolicyAssignment
	for path != "" {
		var response policyAssignmentResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list Azure activation policies at %s: %w", scope, err)
		}
		out = append(out, response.Value...)
		path = response.NextLink
	}
	return out, nil
}

func applyActiveState(assignments []pim.EligibleAssignment, active []roleAssignmentScheduleInstance, now time.Time) {
	bySchedule := make(map[string]roleAssignmentScheduleInstance, len(active))
	for _, instance := range active {
		key := resourceName(instance.Properties.LinkedRoleEligibilityScheduleID)
		if key != "" && isCurrentActivation(instance, now) {
			bySchedule[key] = instance
		}
	}
	for index := range assignments {
		key := resourceName(assignments[index].EligibilityScheduleID)
		if key == "" {
			continue
		}
		instance, ok := bySchedule[key]
		if !ok {
			continue
		}
		assignments[index].Active = true
		assignments[index].ActiveUntil = instance.Properties.EndDateTime
	}
}

func (p Provider) applyPolicies(ctx context.Context, assignments []pim.EligibleAssignment) error {
	byScope := make(map[string][]int)
	for index := range assignments {
		key := strings.ToLower(strings.TrimRight(assignments[index].AzureScope, "/"))
		byScope[key] = append(byScope[key], index)
	}
	for _, indexes := range byScope {
		scope := assignments[indexes[0]].AzureScope
		policies, err := p.policiesForScope(ctx, scope)
		if err != nil {
			return err
		}
		for _, index := range indexes {
			policy, err := policyForAssignment(policies, assignments[index].RoleDefinitionID)
			if err != nil {
				return fmt.Errorf("activation policy for %s at %s: %w", assignments[index].DisplayName, scope, err)
			}
			assignments[index].ActivationPolicy = policy
		}
	}
	return nil
}
```

Move the existing eligibility pagination into this method in `provider.go`:

```go
func (p Provider) discoverEligibilities(ctx context.Context) ([]pim.EligibleAssignment, error) {
	filter := url.QueryEscape("asTarget()")
	path := fmt.Sprintf("/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=%s&api-version=%s", filter, arm.AuthorizationAPIVersion)
	var assignments []pim.EligibleAssignment
	for path != "" {
		var response eligibilityResponse
		if err := p.arm.Get(ctx, path, &response); err != nil {
			return nil, fmt.Errorf("list eligible Azure role assignments: %w", err)
		}
		for _, item := range response.Value {
			assignments = append(assignments, normalizeEligibility(item))
		}
		path = response.NextLink
	}
	return assignments, nil
}

func (p Provider) Discover(ctx context.Context) ([]pim.EligibleAssignment, error) {
	assignments, err := p.discoverEligibilities(ctx)
	if err != nil {
		return nil, err
	}
	active, err := p.discoverActiveAssignments(ctx)
	if err != nil {
		return nil, err
	}
	applyActiveState(assignments, active, time.Now().UTC())
	if err := p.applyPolicies(ctx, assignments); err != nil {
		return nil, err
	}
	return assignments, nil
}
```

`provider.go` already imports `time` for activation request timestamps. Keep `normalizeEligibility` unchanged; metadata is filled after normalization.

- [ ] **Step 8: Generalize the fake ARM client and update existing discovery fixtures**

Update `providerFakeARM.Get` to support every response type and explicit errors:

```go
type providerFakeARM struct {
	responses map[string]any
	errs      map[string]error
	paths     []string
}

func (f *providerFakeARM) Get(_ context.Context, path string, out any) error {
	f.paths = append(f.paths, path)
	if err := f.errs[path]; err != nil {
		return err
	}
	response, ok := f.responses[path]
	if !ok {
		return fmt.Errorf("unexpected GET %s", path)
	}
	switch target := out.(type) {
	case *eligibilityResponse:
		*target = response.(eligibilityResponse)
	case *activeAssignmentResponse:
		*target = response.(activeAssignmentResponse)
	case *policyAssignmentResponse:
		*target = response.(policyAssignmentResponse)
	default:
		return fmt.Errorf("unsupported response target %T", out)
	}
	return nil
}
```

Add `fmt` to test imports. Replace `TestDiscoverFetchesCurrentUsersEligibilitiesOnceAtTenantScope` with the enriched-discovery test from Step 5 and assert `arm.paths` contains exactly the eligibility, active, and policy paths in that order. Update `TestDiscoverFollowsPaginatedEligibilities` so both eligibility pages use scope `/subscriptions/sub-1`, both roles have role-definition and eligibility-schedule IDs, and its fake responses also contain an empty `activeAssignmentResponse` plus one complete `policyAssignmentResponse` with policies for both role IDs.

- [ ] **Step 9: Add pagination, deduplication, optional-justification, and failure tests**

Add these test helpers and focused tests:

```go
func testPolicy(scope, role, maximum string, enabled ...string) roleManagementPolicyAssignment {
	return roleManagementPolicyAssignment{Properties: roleManagementPolicyAssignmentProperties{
		Scope: scope,
		RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/" + role,
		EffectiveRules: []roleManagementPolicyRule{
			{ID: "Expiration_EndUser_Assignment", MaximumDuration: maximum},
			{ID: "Enablement_EndUser_Assignment", EnabledRules: enabled},
		},
	}}
}

func TestPolicyForAssignmentAllowsOptionalJustification(t *testing.T) {
	policy, err := policyForAssignment(
		[]roleManagementPolicyAssignment{testPolicy("/subscriptions/sub-1", "reader", "PT8H")},
		"/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader",
	)
	if err != nil || policy.JustificationRequired {
		t.Fatalf("expected optional justification policy, got %#v, %v", policy, err)
	}
}

func TestPolicyForAssignmentRejectsMissingMaximum(t *testing.T) {
	_, err := policyForAssignment([]roleManagementPolicyAssignment{{Properties: roleManagementPolicyAssignmentProperties{
		RoleDefinitionID: "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader",
		EffectiveRules: []roleManagementPolicyRule{{ID: "Enablement_EndUser_Assignment"}},
	}}}, "/subscriptions/sub-1/providers/Microsoft.Authorization/roleDefinitions/reader")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete policy error, got %v", err)
	}
}

func TestMetadataLookupsFollowPagination(t *testing.T) {
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activeNext := "https://management.azure.com/active-next"
	policyPath := "/subscriptions/sub-1/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	policyNext := "https://management.azure.com/policy-next"
	arm := &providerFakeARM{responses: map[string]any{
		activePath: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{}}, NextLink: activeNext},
		activeNext: activeAssignmentResponse{Value: []roleAssignmentScheduleInstance{{}}},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{}}, NextLink: policyNext},
		policyNext: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{{}}},
	}}
	provider := NewProvider(arm)
	active, activeErr := provider.discoverActiveAssignments(context.Background())
	policies, policyErr := provider.policiesForScope(context.Background(), "/subscriptions/sub-1")
	if activeErr != nil || policyErr != nil || len(active) != 2 || len(policies) != 2 {
		t.Fatalf("expected paginated metadata, active=%d policies=%d errors=%v/%v", len(active), len(policies), activeErr, policyErr)
	}
}

func TestDiscoverLoadsPolicyOncePerScope(t *testing.T) {
	scope := "/subscriptions/sub-1"
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	policyPath := scope + "/providers/Microsoft.Authorization/roleManagementPolicyAssignments?api-version=2020-10-01"
	arm := &providerFakeARM{responses: map[string]any{
		eligibilityPath: eligibilityResponse{Value: []roleEligibilityScheduleInstance{
			{Properties: roleEligibilityProperties{Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/reader", RoleEligibilityScheduleID: "schedule-1"}},
			{Properties: roleEligibilityProperties{Scope: scope, RoleDefinitionID: scope + "/providers/Microsoft.Authorization/roleDefinitions/owner", RoleEligibilityScheduleID: "schedule-2"}},
		}},
		activePath: activeAssignmentResponse{},
		policyPath: policyAssignmentResponse{Value: []roleManagementPolicyAssignment{
			testPolicy(scope, "reader", "PT8H"),
			testPolicy(scope, "owner", "PT4H", "Justification"),
		}},
	}}
	if _, err := NewProvider(arm).Discover(context.Background()); err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	count := 0
	for _, path := range arm.paths {
		if path == policyPath {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one policy lookup for shared scope, got %d paths=%#v", count, arm.paths)
	}
}

func TestDiscoverPropagatesActiveLookupError(t *testing.T) {
	eligibilityPath := "/providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	activePath := "/providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget%28%29&api-version=2020-10-01"
	arm := &providerFakeARM{
		responses: map[string]any{eligibilityPath: eligibilityResponse{}},
		errs: map[string]error{activePath: errors.New("authorization denied")},
	}
	_, err := NewProvider(arm).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list active Azure role assignments") {
		t.Fatalf("expected active lookup context, got %v", err)
	}
}
```

Add `errors` and `strings` to test imports. These tests cover both metadata pagination, one policy call per unique scope, optional justification, malformed policy failure, and actionable active lookup failure without live Azure calls.

- [ ] **Step 10: Run provider and repository tests**

Run:

```bash
gofmt -w internal/providers/azureresources/provider.go internal/providers/azureresources/provider_test.go internal/providers/azureresources/metadata.go internal/providers/azureresources/metadata_test.go
go test ./internal/providers/azureresources -count=1
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/providers/azureresources/provider.go internal/providers/azureresources/provider_test.go internal/providers/azureresources/metadata.go internal/providers/azureresources/metadata_test.go
git commit -m "feat: enrich Azure PIM eligibility metadata"
```

---

### Task 3: Render and Disable Active Assignments

**Files:**
- Modify: `internal/tui/view.go:58-160,376-425`
- Modify: `internal/tui/model.go:524-549`
- Modify: `internal/tui/model_test.go:210-257,469-533`

**Interfaces:**
- Consumes: active-aware assignment selection from Task 1 and enriched `EligibleAssignment.Active` / `ActiveUntil` values from Task 2.
- Produces: stable `STATE` column, active count, active details, and responsive rendering at the existing minimum terminal size.

- [ ] **Step 1: Write failing active-row rendering tests**

Append to `internal/tui/model_test.go`:

```go
func TestAssignmentsViewShowsActiveStateAndCount(t *testing.T) {
	until := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	model := NewModel(Runtime{})
	model.screen = ScreenAssignments
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "available", DisplayName: "Contributor"},
		{ID: "active", DisplayName: "Owner", Active: true, ActiveUntil: &until},
	})

	view := model.View()
	if !strings.Contains(view, "STATE") || !strings.Contains(view, "ACTIVE") || !strings.Contains(view, "1 active") {
		t.Fatalf("expected active assignment state, got %q", view)
	}

	model.listCursor = 1
	model.screen = ScreenDetails
	view = model.View()
	if !strings.Contains(view, "Active") || !strings.Contains(view, "2026-07-16") {
		t.Fatalf("expected active details and expiry, got %q", view)
	}
}
```

Add `time` to the test imports.

In `TestAssignmentsViewFitsMinimumSupportedTerminal`, set `assignments[0].Active = true` after constructing the fixtures. After the existing height assertion, add:

```go
for _, line := range strings.Split(view, "\n") {
	if width := lipgloss.Width(line); width > 80 {
		t.Fatalf("expected line width at most 80, got %d for %q", width, line)
	}
}
```

- [ ] **Step 2: Run active-row tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestAssignmentsViewShowsActiveStateAndCount|TestAssignmentsViewFitsMinimumSupportedTerminal' -count=1
```

Expected: FAIL because the view has no `STATE` or `ACTIVE` rendering.

- [ ] **Step 3: Add stable state-column rendering**

In `viewAssignments`, replace the selected-count/header/row construction with this shape:

```go
	selectedCount := len(m.assignmentList.selected())
	activeCount := 0
	for _, assignment := range m.assignmentList.items {
		if assignment.Active {
			activeCount++
		}
	}
	b.WriteString(fmt.Sprintf("%s  %s  %s\n\n",
		accentStyle.Render(fmt.Sprintf("%d selected", selectedCount)),
		mutedStyle.Render(fmt.Sprintf("%d eligible", len(m.assignmentList.items))),
		successStyle.Render(fmt.Sprintf("%d active", activeCount)),
	))
```

Use a seven-cell state field for header and rows:

```go
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %-7s%-*s  %s", "STATE", m.roleColumnWidth(), "ROLE", "SCOPE")))
		b.WriteString("\n")
		for index := start; index < end; index++ {
			assignment := filtered[index]
			cursor := "  "
			if index == m.listCursor {
				cursor = "> "
			}
			state := "[ ]"
			if assignment.Active {
				state = "ACTIVE"
			} else if m.assignmentList.selectedIDs[assignment.ID] {
				state = "[✓]"
			}
			role := truncateText(displayName(assignment), m.roleColumnWidth())
			scope := truncateText(displayScope(assignment), m.scopeColumnWidth())
			row := fmt.Sprintf("%s%-7s%-*s  %s", cursor, state, m.roleColumnWidth(), role, scope)
			if index == m.listCursor {
				row = activeCardStyle.Width(m.contentWidth() - 4).Render(row)
			} else if assignment.Active {
				row = successStyle.Render(row)
			}
			b.WriteString(row)
			b.WriteString("\n")
		}
```

Do not nest `successStyle` inside `activeCardStyle`; the focused active row must retain one continuous highlight.

Change `scopeColumnWidth` to account for the wider state field:

```go
func (m Model) scopeColumnWidth() int {
	return max(10, m.contentWidth()-m.roleColumnWidth()-17)
}
```

- [ ] **Step 4: Add active details**

Build the details rows before rendering:

```go
	status := "Available"
	activeUntil := ""
	if assignment.Active {
		status = "Active"
		if assignment.ActiveUntil != nil {
			activeUntil = assignment.ActiveUntil.Local().Format("2006-01-02 15:04 MST")
		}
	}
	justification := "Optional"
	if assignment.ActivationPolicy.JustificationRequired {
		justification = "Required"
	}
	rows := [][2]string{
		{"Status", status},
		{"Active until", activeUntil},
		{"Maximum duration", assignment.ActivationPolicy.MaximumDurationISO},
		{"Justification", justification},
		{"Source", string(assignment.Source)},
		{"Assignment type", string(assignment.Kind)},
		{"Scope type", string(assignment.Scope.Type)},
		{"Assignment ID", assignment.ID},
		{"Role definition", assignment.RoleDefinitionID},
		{"Eligibility schedule", assignment.EligibilityScheduleID},
		{"Principal", assignment.PrincipalID},
		{"Condition", assignment.Condition},
	}

- [ ] **Step 5: Run TUI tests**

Run:

```bash
gofmt -w internal/tui/view.go internal/tui/model_test.go
go test ./internal/tui -count=1
```

Expected: PASS, including minimum terminal layout and the continuous-highlight regression.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/view.go internal/tui/model_test.go
git commit -m "feat: show active Azure assignments"
```

---

### Task 4: Build the Policy-Aware Activation Form

**Files:**
- Modify: `internal/tui/activation.go:1-49`
- Modify: `internal/tui/activation_test.go:1-40`
- Modify: `internal/tui/model.go:54-88,104-140,358-495,585-623`
- Modify: `internal/tui/model_test.go:154-208,253-339,469-533`
- Modify: `internal/tui/view.go:163-220,376-425`

**Interfaces:**
- Consumes: `EligibleAssignment.ActivationPolicy`, active-aware selected assignments, existing Bubbles textarea/textinput components, and `pim.ActivationRequest`.
- Produces: `activationForm.durations map[string]string`, `activationForm.validate([]pim.EligibleAssignment) error`, `Model.durationIndex`, assignment-specific request durations, conditional shared justification, scrollable duration rows, and confirmation values.

- [ ] **Step 1: Replace the old form test with failing policy-aware validation tests**

Replace `TestActivationFormRequiresJustificationAndDuration` in `internal/tui/activation_test.go` with:

```go
func TestActivationFormRequiresJustificationOnlyWhenPolicyRequiresIt(t *testing.T) {
	optional := []pim.EligibleAssignment{{
		ID: "optional", DisplayName: "Reader",
		ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"},
	}}
	required := []pim.EligibleAssignment{{
		ID: "required", DisplayName: "Owner",
		ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H", JustificationRequired: true},
	}}
	form := activationForm{durations: map[string]string{"optional": "PT8H", "required": "PT4H"}}

	if err := form.validate(optional); err != nil {
		t.Fatalf("expected optional justification to be valid: %v", err)
	}
	if err := form.validate(required); err == nil || !strings.Contains(err.Error(), "justification") {
		t.Fatalf("expected required justification error, got %v", err)
	}
	form.justification = "Need access"
	if err := form.validate(required); err != nil {
		t.Fatalf("expected completed required form to be valid: %v", err)
	}
}

func TestActivationFormRequiresEveryAssignmentDuration(t *testing.T) {
	selected := []pim.EligibleAssignment{
		{ID: "one", DisplayName: "Contributor"},
		{ID: "two", DisplayName: "Owner"},
	}
	form := activationForm{durations: map[string]string{"one": "PT8H"}}

	err := form.validate(selected)
	if err == nil || !strings.Contains(err.Error(), "Owner") {
		t.Fatalf("expected Owner duration error, got %v", err)
	}
}
```

Add `strings` to imports.

- [ ] **Step 2: Run validation tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestActivationFormRequiresJustificationOnlyWhenPolicyRequiresIt|TestActivationFormRequiresEveryAssignmentDuration' -count=1
```

Expected: compile failure because `activationForm.durations` and `validate` do not exist.

- [ ] **Step 3: Implement policy-aware form validation**

Replace the form definition and `valid` method in `activation.go`:

```go
type activationForm struct {
	justification string
	durations     map[string]string
}

func (f activationForm) requiredJustifications(selected []pim.EligibleAssignment) int {
	count := 0
	for _, assignment := range selected {
		if assignment.ActivationPolicy.JustificationRequired {
			count++
		}
	}
	return count
}

func (f activationForm) validate(selected []pim.EligibleAssignment) error {
	required := f.requiredJustifications(selected)
	if required > 0 && strings.TrimSpace(f.justification) == "" {
		return fmt.Errorf("justification is required by %d selected assignment(s)", required)
	}
	for _, assignment := range selected {
		if strings.TrimSpace(f.durations[assignment.ID]) == "" {
			return fmt.Errorf("duration is required for %s", assignment.DisplayName)
		}
	}
	return nil
}
```

Add `fmt` to imports. Run the two tests again; expected PASS.

- [ ] **Step 4: Write failing form-default and request-construction tests**

Add to `internal/tui/model_test.go`:

```go
func TestActivationFormDefaultsAndSubmitsPerAssignmentDurations(t *testing.T) {
	provider := &scriptedProvider{
		discoveries: [][]pim.EligibleAssignment{{
			{ID: "one", DisplayName: "Contributor", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT8H"}},
			{ID: "two", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H"}},
		}},
		results: []pim.ActivationResult{
			{Status: pim.ActivationStatusActivated},
			{Status: pim.ActivationStatusActivated},
		},
	}
	model := NewModel(Runtime{AzureResources: provider})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	_ = cmd

	if model.form.durations["one"] != "PT8H" || model.form.durations["two"] != "PT4H" {
		t.Fatalf("unexpected duration defaults: %#v", model.form.durations)
	}
	model.form.durations["one"] = "PT6H"
	msg := model.activateSelected(model.assignmentList.selected())()
	next, _ = model.Update(msg)
	model = next.(Model)

	if len(provider.activated) != 2 || provider.activated[0].DurationISO != "PT6H" || provider.activated[1].DurationISO != "PT4H" {
		t.Fatalf("unexpected activation requests: %#v", provider.activated)
	}
}
```

Add this second test:

```go
func TestActivationFormShowsPolicyJustificationRequirement(t *testing.T) {
	for _, test := range []struct {
		name     string
		required bool
		want     string
	}{
		{name: "required", required: true, want: "Justification — REQUIRED"},
		{name: "optional", want: "Justification — optional"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Runtime{})
			model.screen = ScreenAssignments
			model.activeSection = SectionAzureResources
			model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
				ID: "one", DisplayName: "Owner",
				ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT4H", JustificationRequired: test.required},
			}})
			model.assignmentList.toggle("one")
			next, _ := model.openActivationForm()
			view := next.(Model).View()
			if !strings.Contains(view, test.want) {
				t.Fatalf("expected %q, got %q", test.want, view)
			}
		})
	}
}
```

- [ ] **Step 5: Run form model tests and verify RED**

Run:

```bash
go test ./internal/tui -run 'TestActivationFormDefaultsAndSubmitsPerAssignmentDurations|TestActivationFormShowsPolicyJustificationRequirement' -count=1
```

Expected: compile failure because `activationForm` has no durations map and the view has no policy label.

- [ ] **Step 6: Replace shared duration state with per-assignment state**

In `Model`, add:

```go
	durationIndex int
```

Keep one `duration textinput.Model`; it edits the currently focused assignment and avoids allocating one Bubbles component per role.

In `NewModel`:

```go
	duration := textinput.New()
	duration.Prompt = ""
	duration.CharLimit = 20

	// inside Model literal
	form: activationForm{durations: map[string]string{}},
```

Remove the `PT1H` placeholder/value and old `durationISO` initialization.

Add these model helpers:

```go
func (m *Model) prepareActivationForm() {
	selected := m.assignmentList.selected()
	durations := make(map[string]string, len(selected))
	for _, assignment := range selected {
		value := strings.TrimSpace(m.form.durations[assignment.ID])
		if value == "" {
			value = assignment.ActivationPolicy.MaximumDurationISO
		}
		durations[assignment.ID] = value
	}
	m.form.durations = durations
	if m.durationIndex >= len(selected) {
		m.durationIndex = max(0, len(selected)-1)
	}
}

func (m Model) focusJustification() (tea.Model, tea.Cmd) {
	m.syncForm()
	m.formField = formFieldJustification
	m.duration.Blur()
	return m, m.justification.Focus()
}

func (m Model) focusDuration(index int) (tea.Model, tea.Cmd) {
	selected := m.assignmentList.selected()
	if len(selected) == 0 {
		return m.focusJustification()
	}
	m.syncForm()
	m.durationIndex = min(max(index, 0), len(selected)-1)
	m.formField = formFieldDuration
	m.justification.Blur()
	m.duration.SetValue(m.form.durations[selected[m.durationIndex].ID])
	return m, m.duration.Focus()
}

func (m Model) moveFormFocus(delta int) (tea.Model, tea.Cmd) {
	selected := m.assignmentList.selected()
	position := 0
	if m.formField == formFieldDuration {
		position = m.durationIndex + 1
	}
	position = (position + delta + len(selected) + 1) % (len(selected) + 1)
	if position == 0 {
		return m.focusJustification()
	}
	return m.focusDuration(position - 1)
}

func (m *Model) syncForm() {
	m.form.justification = m.justification.Value()
	if m.formField != formFieldDuration {
		return
	}
	selected := m.assignmentList.selected()
	if m.durationIndex < len(selected) {
		m.form.durations[selected[m.durationIndex].ID] = m.duration.Value()
	}
}
```

Update `openActivationForm`:

```go
func (m Model) openActivationForm() (tea.Model, tea.Cmd) {
	m.prepareActivationForm()
	m.screen = ScreenActivation
	m.err = nil
	return m.focusJustification()
}
```

Replace `updateActivation` key routing with these exact branches while retaining the existing textinput update at the end of the method:

```go
case tea.KeyEsc:
	m.syncForm()
	m.justification.Blur()
	m.duration.Blur()
	m.screen = ScreenAssignments
	m.err = nil
	return m, nil
case tea.KeyTab:
	return m.moveFormFocus(1)
case tea.KeyShiftTab:
	return m.moveFormFocus(-1)
case tea.KeyEnter:
	selected := m.assignmentList.selected()
	if m.formField == formFieldJustification {
		return m.focusDuration(0)
	}
	m.syncForm()
	if m.durationIndex < len(selected)-1 {
		return m.focusDuration(m.durationIndex + 1)
	}
	if err := m.form.validate(selected); err != nil {
		m.err = err
		return m, nil
	}
	m.err = nil
	m.duration.Blur()
	m.screen = ScreenConfirmation
	return m, nil
```

Delete the obsolete `toggleFormFocus` method. Update the confirmation-screen Esc branch to preserve the current duration focus with:

```go
case tea.KeyEsc:
	m.screen = ScreenActivation
	return m.focusDuration(m.durationIndex)
```

- [ ] **Step 7: Use assignment-specific durations in requests**

Remove the shared `duration` local from `activateSelected`. Construct each request with:

```go
request := pim.ActivationRequest{
	Assignment:    assignment,
	Justification: justification,
	DurationISO:   strings.TrimSpace(m.form.durations[assignment.ID]),
}
```

This same map remains available for retryable failures.

- [ ] **Step 8: Render required/optional justification and duration rows**

In `viewActivation`, derive selected values first:

```go
	selected := m.assignmentList.selected()
	required := m.form.requiredJustifications(selected)
	justificationLabel := "Justification — optional"
	justificationHelp := "Optional for all selected assignments."
	if required > 0 {
		justificationLabel = "Justification — REQUIRED"
		justificationHelp = fmt.Sprintf("Required by %d of %d selected assignments.", required, len(selected))
	}
```

Render that label, the existing textarea, and the helper. Replace the single duration field with a windowed list:

```go
	b.WriteString(violetStyle.Render("Durations"))
	b.WriteString("\n")
	start, end := m.activationDurationWindow(len(selected))
	for index := start; index < end; index++ {
		assignment := selected[index]
		marker := "  "
		value := m.form.durations[assignment.ID]
		if m.formField == formFieldDuration && index == m.durationIndex {
			marker = "> "
			value = m.duration.View()
		}
		name := truncateText(displayName(assignment), max(12, m.roleColumnWidth()-4))
		b.WriteString(fmt.Sprintf("%s%-*s  %-10s  %s\n", marker, max(12, m.roleColumnWidth()-4), name, value, mutedStyle.Render("max "+assignment.ActivationPolicy.MaximumDurationISO)))
	}
	if start > 0 || end < len(selected) {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  Showing %d-%d of %d durations", start+1, end, len(selected))))
		b.WriteString("\n")
	}
```

Add helpers beside `assignmentWindow`:

```go
func (m Model) activationDurationVisibleRows() int {
	return max(2, m.height-22)
}

func (m Model) activationDurationWindow(total int) (int, int) {
	visible := min(total, m.activationDurationVisibleRows())
	start := m.durationIndex - visible + 1
	if start < 0 {
		start = 0
	}
	if start+visible > total {
		start = total - visible
	}
	return start, start + visible
}
```

Set the justification textarea height to three rows in `NewModel`:

```go
justification.SetHeight(3)
```

- [ ] **Step 9: Render assignment durations during confirmation**

Change each confirmation assignment row to include the chosen duration:

```go
b.WriteString(fmt.Sprintf("  - %s  %s  %s\n",
	displayName(selected[index]),
	accentStyle.Render(m.form.durations[selected[index].ID]),
	mutedStyle.Render(displayScope(selected[index])),
))
```

Replace the old confirmation panel, including its `m.form.durationISO` reference, with:

```go
justification := strings.TrimSpace(m.form.justification)
if justification == "" {
	justification = "(none)"
}
b.WriteString("\n")
b.WriteString(panelStyle.Width(m.contentWidth() - 6).Render(
	fmt.Sprintf("%s\n%s", violetStyle.Render("Justification"), justification),
))
b.WriteString("\n\n")
```

Keep the immediate-submission warning and existing footer behavior.

- [ ] **Step 10: Update existing workflow fixtures and add responsive tests**

Update these existing Home-to-activation fixtures to carry a non-empty maximum duration: `TestModelDiscoversSelectedSectionAndActivatesSelection`, `TestModelMarksRetryableProviderFailuresAndShowsRetryAction`, and `TestModelSupportsHelpBackNavigationAndQuit`. Add this field to each assignment that those tests select:

```go
ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: "PT2H"},
```

Replace assertions that read or write `form.durationISO` with `form.durations[assignment.ID]`.

Add the responsive duration-list test:

```go
func TestActivationViewFitsMinimumTerminalWithPerAssignmentDurations(t *testing.T) {
	assignments := make([]pim.EligibleAssignment, 6)
	for index := range assignments {
		assignments[index] = pim.EligibleAssignment{
			ID: fmt.Sprintf("assignment-%d", index),
			DisplayName: fmt.Sprintf("Role %d", index+1),
			ActivationPolicy: pim.ActivationPolicy{MaximumDurationISO: fmt.Sprintf("PT%dH", index+1)},
		}
	}
	model := NewModel(Runtime{})
	model.screen = ScreenActivation
	model.activeSection = SectionAzureResources
	model.assignmentList = newAssignmentList(assignments)
	for _, assignment := range assignments {
		model.assignmentList.toggle(assignment.ID)
	}
	model.prepareActivationForm()
	next, _ := model.focusDuration(len(assignments) - 1)
	model = next.(Model)
	next, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	model = next.(Model)

	view := model.View()
	if height := lipgloss.Height(view); height > 26 {
		t.Fatalf("expected height at most 26, got %d", height)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("expected line width at most 80, got %d for %q", width, line)
		}
	}
	if !strings.Contains(view, "Role 6") || !strings.Contains(view, "Showing") {
		t.Fatalf("expected focused final duration and range, got %q", view)
	}
}
```

- [ ] **Step 11: Run focused TUI tests and verify GREEN**

Run:

```bash
gofmt -w internal/tui/activation.go internal/tui/activation_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go
go test ./internal/tui -count=1
```

Expected: PASS.

- [ ] **Step 12: Run full verification**

Run:

```bash
go test ./... -count=1
go build ./...
```

Expected: all packages pass and the build exits successfully.

- [ ] **Step 13: Smoke-check the complete workflow**

Run:

```bash
go run .
```

With an Azure CLI-authenticated account that has Azure Resource PIM eligibilities, verify:

1. Discovery makes no unrelated subscription or management-group enumeration.
2. Active eligibilities show `ACTIVE`, expose expiry in details, and ignore Space.
3. Select-all skips active rows.
4. Mixed-policy selections show per-assignment maximum defaults.
5. Justification is labeled required only when at least one selected policy requires it.
6. Optional empty justification reaches confirmation as `(none)`.
7. Confirmation shows every assignment's chosen duration.

Do not submit a real activation unless the user explicitly authorizes it; stop at confirmation for the smoke check.

- [ ] **Step 14: Commit**

```bash
git add internal/tui/activation.go internal/tui/activation_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go
git commit -m "feat: apply activation policies in TUI"
```
