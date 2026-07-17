# Self-Update Command Design

## Overview

Add a non-interactive `pim-manager update` Cobra subcommand for users who installed the CLI with Go. The command delegates version resolution, module verification, compilation, and installation to the Go toolchain by running `go install github.com/mathwro/pim-manager@latest`.

## Goals

- Let users update without opening the TUI.
- Install the latest tagged module version using standard Go behavior.
- Stream Go command output and errors to the invoking terminal.
- Return a nonzero command result when the update fails.
- Keep the implementation dependency-free and testable without network access.

## Non-Goals

- Updating from the `main` branch.
- Downloading or replacing GitHub release binaries.
- Supporting release channels, version arguments, or prereleases.
- Comparing the running and latest versions before installation.
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

## Error Handling

- If `go` is unavailable, return an error stating that the Go toolchain is required and preserve the underlying executable-not-found error.
- If `go install` exits unsuccessfully, return `update pim-manager: <underlying error>` after streaming the child process output.
- Context cancellation terminates the child process through `exec.CommandContext` and returns the wrapped cancellation/process error.
- Do not fall back to `@main`, a cached binary, or a download path.

## Code Structure

- `cmd/root.go` registers the update subcommand and receives the updater function as a dependency for command-routing tests.
- `cmd/update.go` contains the fixed module target and the `os/exec` implementation.
- `cmd/root_test.go` proves root execution still opens the TUI and update execution bypasses it.
- `cmd/update_test.go` covers no-argument validation, success output, updater error propagation, and missing-Go guidance.
- `README.md` documents `pim-manager update`, its Go toolchain requirement, and latest-tag semantics.

No updater package or interface is introduced. A small function dependency is sufficient for Cobra routing tests.

## Release Requirement

`@latest` selects the highest non-retracted semantic module tag, not the newest commit on `main`. The update command only becomes useful after a tagged release containing it is published. The first intended release is `v0.1.1`; future merged changes require later tags before users receive them through `pim-manager update`.

## Testing Strategy

Normal tests do not execute a networked `go install`.

- Execute the root command without arguments and assert the configured TUI runner is called.
- Execute `update` with an injected updater and assert the updater runs while the TUI runner does not.
- Return an injected updater error and assert Cobra returns it.
- Pass a positional argument and assert Cobra rejects it without invoking the updater.
- Run the production updater with `PATH` restricted to an empty temporary directory and assert the error says the Go toolchain is required.
- Run `go test ./cmd` and `go test ./...`.
- Build the binary and smoke-test `pim-manager update --help`; do not run the live updater from the test suite.

## References

- [Go command documentation: install packages with version suffixes](https://pkg.go.dev/cmd/go#hdr-Compile_and_install_packages_and_dependencies)
- [Go module version queries](https://go.dev/ref/mod#version-queries)
