# Self-Update and Notification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `pim-manager update` for latest-tag installation and a non-blocking home-screen notice when a newer tagged version exists.

**Architecture:** A small `internal/selfupdate` package owns the module target, build-version inspection, background `go list` lookup, and `go install`. Cobra injects installation into the root command. The application injects the checker into Bubble Tea, which stores only a successful available-version string and renders it on the home screen.

**Tech Stack:** Go 1.24.2, Cobra, Bubble Tea, standard-library `os/exec` and `runtime/debug`.

## Global Constraints

- Install and check only `github.com/mathwro/pim-manager@latest`.
- `pim-manager update` accepts no arguments or flags and never starts the TUI.
- The TUI check runs once in the background and never delays or fails Azure startup.
- Show the notice only on the home screen.
- Skip update checks for development, pseudo-version, and prerelease builds.
- Add no dependency, semantic-version parser, GitHub client, release channel, or self-replacement logic.
- Normal tests must not execute networked `go install` or `go list` commands.

---

### Task 1: Add Shared Self-Update Logic

**Files:**
- Create: `internal/selfupdate/selfupdate.go`
- Create: `internal/selfupdate/selfupdate_test.go`

**Interfaces:**
- Produce: `func InstallLatest(context.Context, io.Writer, io.Writer) error`
- Produce: `func CheckLatest(context.Context) (string, error)`
- Keep comparison and runner injection private to the package.

- [ ] **Step 1: Write failing version-check tests**

Create table tests around a private `checkLatest` function with an injected output runner. Cover:

```go
{
	name: "new stable tag",
	current: "v0.1.0",
	latest: "v0.1.1\n",
	want: "v0.1.1",
},
{
	name: "same stable tag",
	current: "v0.1.1",
	latest: "v0.1.1\n",
	want: "",
},
```

Add separate tests proving `(devel)`, `v0.1.1-0.20260717150525-6802e8aa589c`, and `v0.2.0-beta.1` return no update without invoking the runner. Assert empty command output is an error and runner errors remain discoverable with `errors.Is`.

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./internal/selfupdate -count=1
```

Expected: compile failure because the package implementation does not exist.

- [ ] **Step 3: Implement tagged-build gating and `go list`**

Create `internal/selfupdate/selfupdate.go` with:

```go
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime/debug"
	"strings"
)

const module = "github.com/mathwro/pim-manager"
const latestModule = module + "@latest"

type outputRunner func(context.Context, string, ...string) ([]byte, error)

func currentVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.Main.Version)
}

func stableTag(version string) bool {
	return strings.HasPrefix(version, "v") && !strings.Contains(version, "-")
}

func checkLatest(ctx context.Context, current string, run outputRunner) (string, error) {
	if !stableTag(current) {
		return "", nil
	}
	out, err := run(ctx, "go", "list", "-m", "-f={{.Version}}", latestModule)
	if err != nil {
		return "", fmt.Errorf("check latest pim-manager version: %w", err)
	}
	latest := strings.TrimSpace(string(out))
	if latest == "" {
		return "", errors.New("check latest pim-manager version: Go returned an empty version")
	}
	if latest == current {
		return "", nil
	}
	return latest, nil
}

func CheckLatest(ctx context.Context) (string, error) {
	return checkLatest(ctx, currentVersion(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	})
}
```

- [ ] **Step 4: Add installation behavior and missing-Go test**

Add `InstallLatest`:

```go
func InstallLatest(ctx context.Context, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, "go", "install", latestModule)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("update pim-manager: the Go toolchain is required: %w", err)
		}
		return fmt.Errorf("update pim-manager: %w", err)
	}
	_, err := fmt.Fprintln(stdout, "Installed the latest tagged pim-manager version. Restart pim-manager to use it.")
	return err
}
```

Test with `PATH` set to an empty temporary directory. Assert the error contains `Go toolchain is required` and wraps `exec.ErrNotFound`.

- [ ] **Step 5: Verify and commit shared logic**

```bash
go test ./internal/selfupdate -count=1
git add internal/selfupdate
git commit -m "feat: add self-update service"
```

---

### Task 2: Add the Cobra Update Command

**Files:**
- Modify: `cmd/root.go:1-27`
- Modify: `cmd/root_test.go:1-120`

**Interfaces:**
- Produce: `type updateFunc func(context.Context, io.Writer, io.Writer) error`
- Change: `newRootCmd(runApp func() error, update updateFunc) *cobra.Command`

- [ ] **Step 1: Complete the already-written failing routing tests**

Keep the tests that verify:

- `update` invokes the updater and not the app.
- updater errors propagate through Cobra.
- positional arguments invoke neither runner.
- existing root and help behavior remains unchanged.

Run:

```bash
go test ./cmd -run 'TestUpdate' -count=1
```

Expected: compile failure because `newRootCmd` still accepts one function.

- [ ] **Step 2: Register the subcommand**

In `cmd/root.go`, add `context`, `io`, and `internal/selfupdate` imports, define `updateFunc`, and change `newRootCmd`:

```go
type updateFunc func(context.Context, io.Writer, io.Writer) error

func newRootCmd(runApp func() error, update updateFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pim-manager",
		Short: "Discover and activate Microsoft PIM eligibilities",
		Long:  "Discover and activate Microsoft PIM eligibilities through an interactive TUI for eligible Entra, Azure Resource, and Group PIM assignments.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApp()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "update",
		Short: "Install the latest tagged version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return update(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
	return cmd
}
```

Pass `selfupdate.InstallLatest` from `Execute`.

- [ ] **Step 3: Verify and commit command routing**

```bash
go test ./cmd -count=1
git add cmd/root.go cmd/root_test.go
git commit -m "feat: add update command"
```

---

### Task 3: Add the Background Home-Screen Notice

**Files:**
- Modify: `internal/app/app.go:1-30`
- Modify: `internal/app/lazy_providers_test.go`
- Modify: `internal/tui/model.go:42-216,219-423`
- Modify: `internal/tui/view.go:76-114`
- Modify: `internal/tui/model_test.go`

**Interfaces:**
- Add to `tui.Runtime`: `CheckUpdate func(context.Context) (string, error)`.
- Add private `updateCheckedMsg` and model `availableUpdate string`.

- [ ] **Step 1: Write failing TUI behavior tests**

Add tests proving:

1. `Init` starts both tenant and update checks before either is released, using channel barriers rather than elapsed time.
2. A successful `updateCheckedMsg{version: "v0.1.2"}` stores the version.
3. An errored update message leaves `model.err` and `availableUpdate` unchanged.
4. The exact home notice contains `Update v0.1.2 available` and `pim-manager update`.
5. The notice does not appear on the tenant or assignment screens.
6. At 80×26, the home screen with the notice retains the footer and has `lipgloss.Height(view) <= 26`.

Run:

```bash
go test ./internal/tui -run 'Test.*Update|TestHome.*Update' -count=1
```

Expected: compile failures for the missing runtime field, message, and model state.

- [ ] **Step 2: Start the optional check in the initial batch**

Add to `Runtime` and `Model`, then change `Init` to build a command slice:

```go
func (m Model) Init() tea.Cmd {
	var commands []tea.Cmd
	if m.runtime.Tenants != nil {
		commands = append(commands, m.checkTenants(m.tenantCheck), m.spinner.Tick)
	}
	if m.runtime.CheckUpdate != nil {
		commands = append(commands, m.checkUpdate())
	}
	if len(commands) == 0 {
		return nil
	}
	return tea.Batch(commands...)
}
```

`checkUpdate` calls the injected function with `context.Background()` and returns `updateCheckedMsg`. Its `Update` case stores the trimmed version only when `err == nil`; errors are ignored.

- [ ] **Step 3: Render a home-only compact notice**

After `tenantPanel()` in `viewHome`, render:

```go
if m.availableUpdate != "" {
	b.WriteString("\n")
	b.WriteString(warningStyle.Render(fmt.Sprintf("Update %s available — exit and run: pim-manager update", m.availableUpdate)))
}
```

Keep the existing final blank-line spacing and ensure the 80×26 test passes without hiding the footer.

- [ ] **Step 4: Wire the checker in production without live test calls**

In `internal/app/app.go`:

```go
var checkLatest = selfupdate.CheckLatest
```

Set `CheckUpdate: checkLatest` in the runtime. Update app tests that execute `Init` to replace `checkLatest` with a local no-update function and restore it through `t.Cleanup`, preventing networked `go list` calls.

- [ ] **Step 5: Verify and commit notification behavior**

```bash
go test ./internal/tui ./internal/app -count=1
go test -race ./internal/tui ./internal/app -count=1
git add internal/app internal/tui
git commit -m "feat: notify when updates are available"
```

---

### Task 4: Document and Verify the Combined Feature

**Files:**
- Modify: `README.md:9-21`

- [ ] **Step 1: Document command and notice**

Add after installation:

````markdown
Update to the latest tagged release without opening the TUI:

```bash
pim-manager update
```

The command requires the Go toolchain and follows Go's `@latest` module version. Tagged builds also check once in the background and show the external update command on the home screen when a newer tag is available.
````

- [ ] **Step 2: Format and verify**

```bash
gofmt -w cmd/root.go cmd/root_test.go internal/selfupdate/selfupdate.go internal/selfupdate/selfupdate_test.go internal/app/app.go internal/app/lazy_providers_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go
go test ./...
go test -race ./cmd ./internal/selfupdate ./internal/app ./internal/tui
go build -o /tmp/pim-manager-self-update .
/tmp/pim-manager-self-update update --help
PATH=/tmp /tmp/pim-manager-self-update update
```

Expected: all normal and race tests pass; help shows the command without the TUI; the missing-Go smoke exits nonzero with `the Go toolchain is required`.

- [ ] **Step 3: Commit documentation**

```bash
git add README.md
git commit -m "docs: document update workflow"
```

- [ ] **Step 4: Release after merge**

After merging to `main`, create tag `v0.1.1`. Until then, `@latest` remains `v0.1.0` and cannot contain the command or notification.
