# Self-Update Command Design

## Overview

Add a non-interactive `pim-manager update` Cobra subcommand for users who installed the CLI with Go, plus a non-blocking home-screen notice when a newer tagged version is available. Installation and version checking both delegate module resolution to the Go toolchain so they agree on `@latest`.

## Goals

- Let users update without opening the TUI.
- Install the latest tagged module version using standard Go behavior.
- Check for a newer tagged version without delaying or breaking TUI startup.
- Show one compact update notice on the home screen with the external command to run.
- Stream Go command output and errors to the invoking terminal.
- Return a nonzero command result when the update command fails.
- Keep the implementation dependency-free and testable without network access.

## Non-Goals

- Updating from the `main` branch.
- Downloading or replacing GitHub release binaries.
- Supporting release channels, version arguments, or prereleases.
- Updating from inside the TUI.
- Showing update notices for development or pseudo-version builds.
- Restarting the running process automatically.
- Updating Azure CLI, Go, or other dependencies.

## Command Behavior

`pim-manager update` is registered directly under the root command. Cobra routes it without invoking `internal/app.Run` or starting Bubble Tea.

The command accepts no positional arguments and defines no flags. It runs:

```bash
go install github.com/mathwro/pim-manager@latest
```

The child process receives the Cobra command context. Its stdout and stderr are connected to Cobra's output streams so downloads, compiler diagnostics, and Go errors remain visible.

After a successful child process exit, the command prints:

```text
Installed the latest tagged pim-manager version. Restart pim-manager to use it.
```

The command does not claim that the currently running process changed. Go installs the executable to `GOBIN`, or to `GOPATH/bin` when `GOBIN` is unset.

## TUI Update Notification

The application reads its current module version with `runtime/debug.ReadBuildInfo`. Only canonical stable tag builds such as `v0.1.1` are checked. Development builds (`(devel)`), pseudo-versions, and prereleases skip the lookup and show no notice.

For a stable tagged build, startup runs this command in the background alongside tenant loading:

```bash
go list -m -f={{.Version}} github.com/mathwro/pim-manager@latest
```

If the returned version differs from the running stable tag, the TUI stores only the available version and renders this compact line on the home screen:

```text
Update v0.1.2 available — exit and run: pim-manager update
```

The notice appears only on the home screen. The check runs once per process and has no spinner, retry action, configuration, or blocking state. Missing Go, network failures, proxy failures, malformed output, and context cancellation suppress the notice without affecting tenant loading or Azure workflows.

## Error Handling

- If `go` is unavailable during `pim-manager update`, return an error stating that the Go toolchain is required and preserve the underlying executable-not-found error.
- If `go install` exits unsuccessfully, return `update pim-manager: <underlying error>` after streaming the child process output.
- Context cancellation terminates the child process through `exec.CommandContext` and returns the wrapped cancellation/process error.
- Update-check errors are returned by the checker but intentionally ignored by the TUI because update availability is optional startup information.
- Do not fall back to `@main`, a cached binary, or a download path.

## Code Structure

- `internal/selfupdate/selfupdate.go` owns the fixed module target, running-version inspection, `go list` check, and `go install` implementation.
- `internal/selfupdate/selfupdate_test.go` covers stable/development build gating, version equality/difference, malformed output, missing Go, and install errors without network access.
- `cmd/root.go` registers the update subcommand and receives the updater function as a dependency for command-routing tests.
- `cmd/root_test.go` proves root execution still opens the TUI and update execution bypasses it.
- `internal/app/app.go` wires the shared update checker into the TUI runtime.
- `internal/tui/model.go` starts the optional check in the initial Bubble Tea batch and stores successful availability messages.
- `internal/tui/view.go` renders the compact home-screen notice.
- `internal/tui/model_test.go` covers background command behavior, silent errors, home-only rendering, and the 80×26 layout.
- `README.md` documents `pim-manager update`, its Go toolchain requirement, latest-tag semantics, and the home-screen notice.

No version parser, GitHub client, or updater interface is introduced. Small function dependencies are sufficient for command and TUI tests.

## Release Requirement

`@latest` selects the highest non-retracted semantic module tag, not the newest commit on `main`. The update command only becomes useful after a tagged release containing it is published. The first intended release is `v0.1.1`; future merged changes require later tags before users receive them through `pim-manager update`.

## Testing Strategy

Normal tests do not execute networked `go install` or `go list` commands.

- Execute the root command without arguments and assert the configured TUI runner is called.
- Execute `update` with an injected updater and assert the updater runs while the TUI runner does not.
- Return an injected updater error and assert Cobra returns it.
- Pass a positional argument and assert Cobra rejects it without invoking the updater.
- Run the production updater with `PATH` restricted to an empty temporary directory and assert the error says the Go toolchain is required.
- Exercise update comparison with injected current versions and command output: equal stable tag, newer stable tag, development build, pseudo-version, malformed output, and runner errors.
- Assert the update check starts concurrently with tenant loading and check failures do not change the TUI error state.
- Assert the notice appears only on the home screen and fits at 80×26 with the footer visible.
- Run `go test ./...` and relevant race tests.
- Build the binary and smoke-test `pim-manager update --help`; do not run the live updater from the test suite.

## References

- [Go command documentation: install packages with version suffixes](https://pkg.go.dev/cmd/go#hdr-Compile_and_install_packages_and_dependencies)
- [Go module version queries](https://go.dev/ref/mod#version-queries)
