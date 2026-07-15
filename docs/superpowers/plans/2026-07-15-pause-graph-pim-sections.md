# Pause Graph PIM Sections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Azure Resources the only selectable PIM area until Azure CLI can obtain supported Microsoft Graph PIM permissions.

**Architecture:** Restrict the existing TUI section list to Azure Resources and explain the temporary pause on Home. Keep Entra and Groups provider packages dormant, remove their production runtime wiring, and preserve the existing Azure CLI/ARM path unchanged.

**Tech Stack:** Go, Bubble Tea, Lip Gloss, standard `testing` package.

## Global Constraints

- Azure Resources is the only selectable section and is selected by default.
- Home explains that Entra Roles and Groups are paused because Azure CLI lacks Microsoft Graph PIM permissions.
- Normal use must not call Graph PIM providers.
- Keep Entra and Groups provider packages for future reactivation.
- Do not call deprecated Graph beta or private `api.azrbac.mspim.azure.com` endpoints.
- Track Azure CLI issues `#22775` and `#28854`, plus `netr0m/az-pim-cli#121`.
- Add no dependencies or authentication flows.

---

### Task 1: Pause Entra Roles and Groups

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/model.go:121-138`
- Modify: `internal/tui/view.go:18-53`
- Modify: `internal/app/app.go:3-38`
- Modify: `docs/superpowers/specs/2026-07-14-pim-manager-design.md`

**Interfaces:**
- Consumes: existing `SectionAzureResources`, `Runtime.AzureResources`, `AssignmentProvider`, and Home `tea.KeyEnter` behavior.
- Produces: `NewModel(Runtime)` with `sections == []Section{SectionAzureResources}` and `selectedSection == SectionAzureResources`; Home copy describing the paused Graph sections.

- [ ] **Step 1: Write the failing paused-sections test**

Add a discovery counter to the existing `scriptedProvider` test helper in `internal/tui/model_test.go`:

```go
type scriptedProvider struct {
	discoveries  [][]pim.EligibleAssignment
	discoverCalls int
	results      []pim.ActivationResult
	activateErr  []error
	discoverErr  error
	activated    []pim.ActivationRequest
}

func (p *scriptedProvider) Discover(context.Context) ([]pim.EligibleAssignment, error) {
	p.discoverCalls++
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
```

Append this behavioral test:

```go
func TestNewModelPausesGraphPIMSections(t *testing.T) {
	entra := &scriptedProvider{}
	azureResources := &scriptedProvider{}
	groups := &scriptedProvider{}
	model := NewModel(Runtime{
		Entra:          entra,
		AzureResources: azureResources,
		Groups:         groups,
	})

	if len(model.sections) != 1 || model.sections[0] != SectionAzureResources {
		t.Fatalf("expected only Azure Resources, got %#v", model.sections)
	}
	if model.selectedSection != SectionAzureResources {
		t.Fatalf("expected Azure Resources selected, got %s", model.selectedSection)
	}
	view := model.View()
	if !strings.Contains(view, "Entra Roles and Groups are paused") || !strings.Contains(view, "Microsoft Graph PIM permissions") {
		t.Fatalf("expected paused-section guidance, got %q", view)
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = runCommand(next.(Model), cmd)
	if azureResources.discoverCalls != 1 || entra.discoverCalls != 0 || groups.discoverCalls != 0 {
		t.Fatalf("expected only Azure Resources discovery, got entra=%d azure=%d groups=%d", entra.discoverCalls, azureResources.discoverCalls, groups.discoverCalls)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./internal/tui -run TestNewModelPausesGraphPIMSections -count=1
```

Expected: FAIL because the model exposes all three sections, selects Entra Roles, and has no pause guidance.

- [ ] **Step 3: Restrict the TUI to Azure Resources**

Update the `Model` initializer in `internal/tui/model.go`:

```go
model := Model{
	runtime:         runtime,
	screen:          ScreenHome,
	selectedSection: SectionAzureResources,
	sections:        []Section{SectionAzureResources},
	assignmentList:  newAssignmentList(nil),
	form: activationForm{
		durationISO: "PT1H",
	},
	formField:       formFieldJustification,
	justification:   justification,
	duration:        duration,
	spinner:         activity,
	summaryViewport: viewport.New(0, 0),
	checkingAccount: runtime.Account != nil,
	width:           96,
	height:          30,
}
```

In `viewHome` after the existing assignment-batch explanation, render the pause note before the section cards:

```go
b.WriteString(mutedStyle.Render("Entra Roles and Groups are paused until Azure CLI can request the required Microsoft Graph PIM permissions."))
b.WriteString("\n\n")
```

- [ ] **Step 4: Update existing default-section tests**

In `internal/tui/model_test.go`:

1. Rename `TestHomeEnterMovesToSelectedSection` to `TestHomeEnterOpensAzureResources`, remove the manual `SectionGroups` assignment, and assert `SectionAzureResources`.
2. Replace `Runtime{Entra: provider}` with `Runtime{AzureResources: provider}` in tests that enter Home and exercise discovery, activation, search, retry, or navigation.
3. Leave `TestRuntimeAcceptsAssignmentProviders` unchanged so dormant providers remain supported by the runtime type.
4. Leave summary tests that directly assign `activeSection` unchanged; they do not exercise Home selection.

- [ ] **Step 5: Run focused TUI tests and verify GREEN**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go internal/tui/view.go
go test ./internal/tui -count=1
```

Expected: PASS.

- [ ] **Step 6: Remove dormant Graph providers from production wiring**

Update `internal/app/app.go` to construct only the working ARM provider:

```go
package app

import (
	"net/http"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/azureauth"
	"github.com/mathwro/pim-manager/internal/providers/azureresources"
	"github.com/mathwro/pim-manager/internal/tui"
)

var newCLI = azureauth.NewCLI

var runProgram = func(model tea.Model) error {
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func Run() error {
	auth := newCLI(nil)
	armClient := arm.NewClient(http.DefaultClient, auth)
	runtime := tui.Runtime{
		AzureResources: azureresources.NewProvider(armClient),
		Account:          auth,
	}
	return runProgram(tui.NewModel(runtime))
}
```

This is a green refactor after Home selection prevents Graph access. Keep provider packages and the lazy-principal helper unchanged for future reactivation.

- [ ] **Step 7: Update the product design source**

In `docs/superpowers/specs/2026-07-14-pim-manager-design.md`, add a temporary limitation section after **Non-Goals**:

```markdown
## Temporary Graph PIM Limitation

Entra Roles and Groups are paused because Azure CLI's fixed client cannot obtain the delegated Microsoft Graph PIM permissions required for discovery and activation. Azure Resources remains available through ARM.

Track:

- [Azure CLI #22775](https://github.com/Azure/azure-cli/issues/22775)
- [Azure CLI #28854](https://github.com/Azure/azure-cli/issues/28854)
- [az-pim-cli #121](https://github.com/netr0m/az-pim-cli/issues/121)

Do not use the deprecated `/beta/privilegedAccess` APIs or private `api.azrbac.mspim.azure.com` endpoint. Re-enable these sections only when Azure CLI supports the required scopes or the product adopts a dedicated Graph application registration and login.
```

Replace the Product Flow section table with:

```markdown
| Section | What it lists |
| --- | --- |
| Entra Roles | **Paused** — Azure CLI cannot obtain the required Microsoft Graph PIM permissions. |
| Azure Resources | Eligible Azure RBAC activations across management groups, subscriptions, and resource groups. |
| Groups | **Paused** — Azure CLI cannot obtain the required Microsoft Graph PIM permissions. |
```

- [ ] **Step 8: Run full verification**

Run:

```bash
go test ./... -count=1
go build ./...
```

Expected: all packages pass and the build exits successfully.

- [ ] **Step 9: Smoke-check Home behavior**

Launch the application and verify Home shows one selectable **Azure Resources** card plus the pause explanation. Press Enter and confirm Azure Resources discovery begins without a Microsoft Graph request.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/app/app.go docs/superpowers/specs/2026-07-14-pim-manager-design.md
git commit -m "fix: pause unsupported Graph PIM sections"
```
