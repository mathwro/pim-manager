# PIM MFA Activation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Authenticate through Azure CLI before activating a batch only when a selected Azure Resource assignment's effective PIM policy explicitly requires MFA.

**Architecture:** Extend the existing normalized activation policy with one boolean, map it from ARM effective rules, and gate confirmation in the Bubble Tea model. Azure CLI constructs the interactive MFA command; Bubble Tea's `ExecProcess` temporarily releases the terminal and resumes activation only after success.

**Tech Stack:** Go 1.24.2, Bubble Tea 1.3.10, Azure CLI 2.76 or later, standard library `encoding/base64`, `os/exec`, and `testing`.

## Global Constraints

- Work only on branch `feat/pim-mfa-activation` in `.worktrees/pim-mfa-activation`.
- Trigger MFA only when a selected assignment has an explicit `MultiFactorAuthentication` effective enablement rule.
- Support standard PIM MFA only; do not implement Conditional Access authentication contexts or inspect JWT claims.
- Continue using Azure CLI authentication; add no dependency and no custom login flow.
- A failed or canceled MFA command must submit zero activation requests and return to confirmation.
- A mixed batch performs at most one MFA command before any request is submitted.
- Preserve existing per-assignment activation and retry behavior.

---

### Task 1: Normalize the PIM MFA Policy

**Files:**
- Modify: `internal/pim/types.go:41-44`
- Modify: `internal/providers/azureresources/metadata.go:93-106`
- Test: `internal/providers/azureresources/metadata_test.go:67-104`

**Interfaces:**
- Consumes: ARM `Enablement_EndUser_Assignment.enabledRules`.
- Produces: `pim.ActivationPolicy.MFARequired bool` for TUI policy decisions.

- [ ] **Step 1: Extend the existing policy test and verify RED**

Update `TestPolicyForAssignmentUsesEffectiveEndUserRules` so its final assertion requires both flags:

```go
if policy.MaximumDurationISO != "PT8H" || !policy.JustificationRequired || !policy.MFARequired {
	t.Fatalf("unexpected policy: %#v", policy)
}
```

Update `TestPolicyForAssignmentAllowsOptionalJustification` to prove absent rules do not infer MFA:

```go
if err != nil || policy.JustificationRequired || policy.MFARequired || policy.MaximumDurationISO != "PT8H" {
	t.Fatalf("expected optional activation policy, got %#v, %v", policy, err)
}
```

Run:

```bash
go test ./internal/providers/azureresources -run 'TestPolicyForAssignment(UsesEffectiveEndUserRules|AllowsOptionalJustification)$' -count=1
```

Expected: FAIL to compile because `ActivationPolicy.MFARequired` does not exist.

- [ ] **Step 2: Add the minimal normalized field and mapping**

Add the field without changing request/result types:

```go
type ActivationPolicy struct {
	MaximumDurationISO    string
	JustificationRequired bool
	MFARequired           bool
}
```

In the existing `Enablement_EndUser_Assignment` loop, use the same case-insensitive rule mapping:

```go
for _, enabled := range rule.EnabledRules {
	switch {
	case strings.EqualFold(enabled, "Justification"):
		policy.JustificationRequired = true
	case strings.EqualFold(enabled, "MultiFactorAuthentication"):
		policy.MFARequired = true
	}
}
```

- [ ] **Step 3: Verify GREEN and package regressions**

Run:

```bash
gofmt -w internal/pim/types.go internal/providers/azureresources/metadata.go internal/providers/azureresources/metadata_test.go
go test ./internal/providers/azureresources -run 'TestPolicyForAssignment(UsesEffectiveEndUserRules|AllowsOptionalJustification)$' -count=1
go test ./internal/pim ./internal/providers/azureresources -count=1
```

Expected: all pass; a policy without the explicit rule leaves `MFARequired` false.

- [ ] **Step 4: Commit**

```bash
git add internal/pim/types.go internal/providers/azureresources/metadata.go internal/providers/azureresources/metadata_test.go
git commit -m "feat: detect MFA activation policies"
```

---

### Task 2: Construct the Interactive Azure CLI MFA Command

**Files:**
- Modify: `internal/azureauth/auth.go:3-10,84-86`
- Test: `internal/azureauth/auth_test.go:69-98`

**Interfaces:**
- Consumes: current Azure account tenant ID.
- Produces: `azureauth.MFALoginCommand(tenantID string) (*exec.Cmd, error)`.

- [ ] **Step 1: Write command-construction tests and verify RED**

Add imports for `encoding/base64` and `reflect`, then add:

```go
func TestMFALoginCommandUsesTenantARMAndMFAClaim(t *testing.T) {
	command, err := MFALoginCommand(" tenant-1 ")
	if err != nil {
		t.Fatalf("MFALoginCommand returned error: %v", err)
	}
	claims := base64.StdEncoding.EncodeToString([]byte(`{"access_token":{"amr":{"essential":true,"values":["mfa"]}}}`))
	want := []string{
		"az", "login",
		"--tenant", "tenant-1",
		"--scope", "https://management.core.windows.net//.default",
		"--claims-challenge", claims,
		"--output", "none",
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("expected args %#v, got %#v", want, command.Args)
	}
}

func TestMFALoginCommandRejectsMissingTenant(t *testing.T) {
	if _, err := MFALoginCommand("  "); err == nil {
		t.Fatal("expected missing tenant error")
	}
}
```

Run:

```bash
go test ./internal/azureauth -run TestMFALoginCommand -count=1
```

Expected: FAIL to compile because `MFALoginCommand` does not exist.

- [ ] **Step 2: Implement the command with standard library only**

Add `encoding/base64` to imports. Reuse the existing `os/exec` and `strings` imports:

```go
const mfaClaimsRequest = `{"access_token":{"amr":{"essential":true,"values":["mfa"]}}}`

func MFALoginCommand(tenantID string) (*exec.Cmd, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, errors.New("Azure tenant ID is required for MFA authentication")
	}
	claims := base64.StdEncoding.EncodeToString([]byte(mfaClaimsRequest))
	return exec.Command(
		"az", "login",
		"--tenant", tenantID,
		"--scope", "https://management.core.windows.net//.default",
		"--claims-challenge", claims,
		"--output", "none",
	), nil
}
```

Do not log out, mutate the existing token getter, or invoke the command from this package.

- [ ] **Step 3: Verify GREEN and auth regressions**

Run:

```bash
gofmt -w internal/azureauth/auth.go internal/azureauth/auth_test.go
go test ./internal/azureauth -run TestMFALoginCommand -count=1
go test ./internal/azureauth -count=1
```

Expected: all pass; tests do not execute Azure CLI.

- [ ] **Step 4: Commit**

```bash
git add internal/azureauth/auth.go internal/azureauth/auth_test.go
git commit -m "feat: build Azure CLI MFA login command"
```

---

### Task 3: Gate MFA-Required Batches in the TUI

**Files:**
- Modify: `internal/tui/model.go:3-16,38-43,91-103,151-187,406-438,647-685`
- Modify: `internal/tui/view.go:149-196,252-284`
- Modify: `internal/app/app.go:20-27`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: `pim.ActivationPolicy.MFARequired`, `azureauth.Account.TenantID`, and `azureauth.MFALoginCommand`.
- Produces: `tui.Runtime.MFACommand func(string) (*exec.Cmd, error)` and a confirmation gate that starts activation only after `mfaCompletedMsg` succeeds.

- [ ] **Step 1: Write the no-MFA and MFA-gate model tests**

Add `os/exec` to `model_test.go` imports. Add a no-MFA test proving ordinary assignments retain the current path:

```go
func TestConfirmationSkipsMFAForOrdinaryBatch(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{{Status: pim.ActivationStatusActivated}}}
	mfaCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		MFACommand: func(string) (*exec.Cmd, error) {
			mfaCalls++
			return exec.Command("true"), nil
		},
	})
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{ID: "reader", DisplayName: "Reader"}})
	model.assignmentList.toggle("reader")
	model.form.durations = map[string]string{"reader": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runCommand(next.(Model), cmd)

	if mfaCalls != 0 || len(provider.activated) != 1 || model.screen != ScreenSummary {
		t.Fatalf("expected direct activation, MFA calls=%d requests=%#v screen=%s", mfaCalls, provider.activated, model.screen)
	}
}
```

Add an MFA test proving a mixed batch is held and the command is requested once:

```go
func TestConfirmationGatesMixedBatchOnMFA(t *testing.T) {
	provider := &scriptedProvider{results: []pim.ActivationResult{
		{Status: pim.ActivationStatusActivated},
		{Status: pim.ActivationStatusActivated},
	}}
	mfaCalls := 0
	model := NewModel(Runtime{
		AzureResources: provider,
		MFACommand: func(tenantID string) (*exec.Cmd, error) {
			mfaCalls++
			if tenantID != "tenant-1" {
				t.Fatalf("expected tenant-1, got %q", tenantID)
			}
			return exec.Command("true"), nil
		},
	})
	model.account = azureauth.Account{TenantID: "tenant-1"}
	model.activeSection = SectionAzureResources
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{
		{ID: "reader", DisplayName: "Reader"},
		{ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true}},
	})
	model.assignmentList.toggle("reader")
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"reader": "PT1H", "owner": "PT1H"}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if cmd == nil || mfaCalls != 1 || len(provider.activated) != 0 || model.screen != ScreenConfirmation {
		t.Fatalf("expected pending MFA gate, calls=%d requests=%#v screen=%s", mfaCalls, provider.activated, model.screen)
	}

	selected := model.assignmentList.selected()
	next, cmd = model.Update(mfaCompletedMsg{selected: selected})
	model = runCommand(next.(Model), cmd)
	if len(provider.activated) != 2 || model.screen != ScreenSummary {
		t.Fatalf("expected full batch after MFA, requests=%#v screen=%s", provider.activated, model.screen)
	}
}
```

Add a failure test:

```go
func TestMFAFailureReturnsToConfirmationWithoutActivation(t *testing.T) {
	provider := &scriptedProvider{}
	model := NewModel(Runtime{AzureResources: provider})
	model.screen = ScreenConfirmation

	next, cmd := model.Update(mfaCompletedMsg{
		selected: []pim.EligibleAssignment{{ID: "owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true}}},
		err:      errors.New("login canceled"),
	})
	model = next.(Model)

	if cmd != nil || model.screen != ScreenConfirmation || len(provider.activated) != 0 {
		t.Fatalf("expected blocked activation, requests=%#v screen=%s", provider.activated, model.screen)
	}
	if !strings.Contains(model.View(), "MFA authentication failed: login canceled") {
		t.Fatalf("expected actionable MFA error, got %q", model.View())
	}
}
```

Run:

```bash
go test ./internal/tui -run 'Test(ConfirmationSkipsMFAForOrdinaryBatch|ConfirmationGatesMixedBatchOnMFA|MFAFailureReturnsToConfirmationWithoutActivation)$' -count=1
```

Expected: FAIL to compile because `Runtime.MFACommand` and `mfaCompletedMsg` do not exist.

- [ ] **Step 2: Add the MFA gate and external-process handoff**

Add `os/exec` to `model.go`. Extend runtime and messages:

```go
type Runtime struct {
	Entra          AssignmentProvider
	AzureResources AssignmentProvider
	Groups         AssignmentProvider
	Account        AccountProvider
	MFACommand     func(string) (*exec.Cmd, error)
}

type mfaCompletedMsg struct {
	selected []pim.EligibleAssignment
	err      error
}
```

Handle `mfaCompletedMsg` before keyboard input:

```go
case mfaCompletedMsg:
	if typed.err != nil {
		m.screen = ScreenConfirmation
		m.err = fmt.Errorf("MFA authentication failed: %w", typed.err)
		return m, nil
	}
	return m.startActivation(typed.selected)
```

Add the minimal policy helper and centralize today's activation transition:

```go
func requiresMFA(assignments []pim.EligibleAssignment) bool {
	for _, assignment := range assignments {
		if assignment.ActivationPolicy.MFARequired {
			return true
		}
	}
	return false
}

func (m Model) startActivation(selected []pim.EligibleAssignment) (tea.Model, tea.Cmd) {
	m.screen = ScreenProgress
	m.loading = true
	m.err = nil
	return m, tea.Batch(m.activateSelected(selected), m.spinner.Tick)
}
```

Replace the direct progress transition in `updateConfirmation` with:

```go
if !requiresMFA(selected) {
	return m.startActivation(selected)
}
if m.runtime.MFACommand == nil {
	m.err = errors.New("MFA authentication is unavailable")
	return m, nil
}
command, err := m.runtime.MFACommand(m.account.TenantID)
if err != nil {
	m.err = err
	return m, nil
}
m.err = nil
return m, tea.ExecProcess(command, func(err error) tea.Msg {
	return mfaCompletedMsg{selected: selected, err: err}
})
```

Import standard `errors`. Do not run MFA from retry handling because the summary only exposes transient/input retries and `MfaRule` remains non-retryable.

- [ ] **Step 3: Verify the gate tests GREEN**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go
go test ./internal/tui -run 'Test(ConfirmationSkipsMFAForOrdinaryBatch|ConfirmationGatesMixedBatchOnMFA|MFAFailureReturnsToConfirmationWithoutActivation)$' -count=1
```

Expected: all pass; the mixed batch has zero requests before the success message.

- [ ] **Step 4: Add failing view assertions**

Add one details test and one confirmation test:

```go
func TestAssignmentDetailsShowsMFARequirement(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenDetails
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}})
	if view := model.View(); !strings.Contains(view, "MFA") || !strings.Contains(view, "Required") {
		t.Fatalf("expected MFA requirement in details, got %q", view)
	}
}

func TestConfirmationShowsMFARequirement(t *testing.T) {
	model := NewModel(Runtime{})
	model.screen = ScreenConfirmation
	model.assignmentList = newAssignmentList([]pim.EligibleAssignment{{
		ID: "owner", DisplayName: "Owner", ActivationPolicy: pim.ActivationPolicy{MFARequired: true},
	}})
	model.assignmentList.toggle("owner")
	model.form.durations = map[string]string{"owner": "PT1H"}
	if view := model.View(); !strings.Contains(view, "MFA is required before activation") {
		t.Fatalf("expected MFA confirmation warning, got %q", view)
	}
}
```

Run:

```bash
go test ./internal/tui -run 'Test(AssignmentDetailsShowsMFARequirement|ConfirmationShowsMFARequirement)$' -count=1
```

Expected: FAIL because neither view renders MFA policy yet.

- [ ] **Step 5: Render the policy and MFA errors**

In `viewDetails`, derive and add the row:

```go
mfa := "Not required"
if assignment.ActivationPolicy.MFARequired {
	mfa = "Required"
}
```

```go
{"MFA", mfa},
```

In `viewConfirmation`, after the justification panel, render the conditional warning and any command error:

```go
if requiresMFA(selected) {
	b.WriteString("\n")
	b.WriteString(warningStyle.Render("MFA is required before activation. Azure CLI will prompt after confirmation."))
}
if m.err != nil {
	b.WriteString("\n")
	b.WriteString(errorStyle.Render(m.err.Error()))
}
```

Run the focused tests and the minimum-layout confirmation tests to ensure the extra lines remain bounded.

- [ ] **Step 6: Wire production Azure CLI command creation**

Extend `tui.Runtime` construction in `internal/app/app.go`:

```go
runtime := tui.Runtime{
	AzureResources: azureresources.NewProvider(armClient),
	Account:        auth,
	MFACommand:     azureauth.MFALoginCommand,
}
```

No new app abstraction or custom process runner is needed.

- [ ] **Step 7: Verify all TUI behavior and build**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/app/app.go
go test ./internal/tui -run 'Test(AssignmentDetailsShowsMFARequirement|ConfirmationShowsMFARequirement|ConfirmationSkipsMFAForOrdinaryBatch|ConfirmationGatesMixedBatchOnMFA|MFAFailureReturnsToConfirmationWithoutActivation)$' -count=1
go test ./internal/tui -count=1
go test ./... -count=1
go build ./...
```

Expected: all pass. The built application uses the Azure CLI command factory, ordinary roles do not request MFA, and MFA-required batches cannot activate before a successful MFA completion message.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/app/app.go
git commit -m "feat: require MFA before protected activations"
```
