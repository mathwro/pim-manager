# Self-Update Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a non-interactive `pim-manager update` command that installs the latest tagged module version through Go.

**Architecture:** Cobra routes `update` without starting Bubble Tea. A small injected updater function keeps command routing testable, while production code delegates installation to `go install github.com/mathwro/pim-manager@latest` with inherited command context and Cobra streams.

**Tech Stack:** Go 1.24.2, Cobra, standard-library `os/exec`.

## Global Constraints

- Install only `github.com/mathwro/pim-manager@latest`.
- Accept no positional arguments and add no flags.
- Do not start the TUI for `update`.
- Stream child stdout and stderr to Cobra streams.
- Add no dependency, version parser, GitHub client, release channel, or self-replacement logic.
- Normal tests must not run a networked `go install`.

---

### Task 1: Add the Cobra Update Command

**Files:**
- Create: `cmd/update.go`
- Create: `cmd/update_test.go`
- Modify: `cmd/root.go:10-24`
- Modify: `cmd/root_test.go:10-57`

**Interfaces:**
- Produce: `type updateFunc func(context.Context, io.Writer, io.Writer) error`
- Produce: `func installLatest(context.Context, io.Writer, io.Writer) error`
- Change: `newRootCmd(runApp func() error, update updateFunc) *cobra.Command`

- [ ] **Step 1: Write failing command-routing tests**

Update existing `newRootCmd` calls with a no-op updater and add:

```go
func TestUpdateRunsUpdaterWithoutStartingApp(t *testing.T) {
	var appRan, updateRan bool
	cmd := newRootCmd(func() error {
		appRan = true
		return nil
	}, func(context.Context, io.Writer, io.Writer) error {
		updateRan = true
		return nil
	})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"update"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if appRan || !updateRan {
		t.Fatalf("expected updater only, app=%v update=%v", appRan, updateRan)
	}
}
```

Add tests that an updater error is returned with `errors.Is`, and that `update unexpected` is rejected without calling either runner.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
go test ./cmd -run 'TestUpdate' -count=1
```

Expected: compile failure because `newRootCmd` does not accept the updater and no update subcommand exists.

- [ ] **Step 3: Register the update subcommand**

In `cmd/root.go`, add context and io imports and change the constructor:

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

Pass `installLatest` from `Execute`.

- [ ] **Step 4: Implement Go delegation**

Create `cmd/update.go`:

```go
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

const latestModule = "github.com/mathwro/pim-manager@latest"

func installLatest(ctx context.Context, stdout, stderr io.Writer) error {
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

- [ ] **Step 5: Add production updater error coverage**

In `cmd/update_test.go`, restrict `PATH` to `t.TempDir()`, call `installLatest`, and assert the returned error contains `Go toolchain is required` and wraps `exec.ErrNotFound`.

- [ ] **Step 6: Verify the command package**

```bash
go test ./cmd -count=1
```

Expected: package passes without executing a networked install.

- [ ] **Step 7: Commit command implementation**

```bash
git add cmd/root.go cmd/root_test.go cmd/update.go cmd/update_test.go
git commit -m "feat: add self-update command"
```

---

### Task 2: Document and Verify Self-Update

**Files:**
- Modify: `README.md:9-21`

**Interfaces:**
- Documents the command and latest-tag release behavior.

- [ ] **Step 1: Document updates after installation**

After the initial install instructions, add:

````markdown
Update to the latest tagged release without opening the TUI:

```bash
pim-manager update
```

The update command requires the Go toolchain and installs the version selected by `@latest`. Restart `pim-manager` after it completes.
````

- [ ] **Step 2: Format and run complete verification**

```bash
gofmt -w cmd/root.go cmd/root_test.go cmd/update.go cmd/update_test.go
go test ./...
go build -o /tmp/pim-manager-self-update .
/tmp/pim-manager-self-update update --help
```

Expected: 11 tested packages pass, one package reports no tests, the build succeeds, and help identifies `update` without opening the TUI.

- [ ] **Step 3: Smoke-test updater failure without Go**

```bash
PATH=/tmp /tmp/pim-manager-self-update update
```

Expected: nonzero exit with `the Go toolchain is required`; the TUI does not open.

- [ ] **Step 4: Commit documentation**

```bash
git add README.md
git commit -m "docs: document self-update command"
```

- [ ] **Step 5: Release after merge**

After the feature branch is merged to `main`, create tag `v0.1.1`. Until that tag exists, `@latest` remains `v0.1.0` and cannot expose the new command.
