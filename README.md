# pim-manager

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities from the terminal.

## Current MVP

Running `pim-manager` opens an interactive Bubble Tea TUI. It validates the existing Azure CLI session and, when multiple tenants are available, asks which tenant to use before showing the PIM areas. Azure Resources is active for eligible Azure RBAC assignments across management groups, subscriptions, and resource groups. Entra Roles and Groups are shown as paused until Azure CLI can obtain their required Microsoft Graph PIM permissions.

## Installation

Install the latest version directly from the Go module repository:

```bash
go install github.com/mathwro/pim-manager@latest
```

`go install` downloads the source, builds the CLI, and writes the `pim-manager` executable to `GOBIN`. If `GOBIN` is unset, Go uses `$(go env GOPATH)/bin` (normally `~/go/bin`). Ensure that directory is on your `PATH`, then start the TUI:

```bash
pim-manager
```

Update to the latest tagged release without opening the TUI:

```bash
pim-manager update
```

The command requires the Go toolchain and follows Go's `@latest` module version. Tagged builds also check once in the background and show the external update command on the home screen when a newer tag is available.

Prebuilt release archives are also published for Windows, macOS, and Linux on x86-64 and ARM64. They contain only the root `pim-manager` executable (`pim-manager.exe` on Windows) and require no Go runtime. macOS archives are ad-hoc signed; they are not Developer ID signed or notarized. Linux archives use static pure-Go binaries with `CGO_ENABLED=0`, so they have no libc dependency.

## Authentication

The app uses your existing Azure CLI session. Sign in before running:

```bash
az login
```

If Azure CLI exposes more than one tenant, `pim-manager` shows a keyboard-driven tenant menu before the PIM areas. Rows use `Tenant Name (default.domain)` when available and retain the tenant ID beneath the label; name-only, domain-only, and ID-only fallbacks are supported. A single tenant is selected automatically. The choice applies only to the current `pim-manager` session: token acquisition, discovery, authentication checks, and activation use that tenant without running `az account set`.

When a selected role requires standard MFA or a Conditional Access authentication context, `pim-manager` temporarily hands the terminal to an interactive Azure CLI login. Complete verification in the browser; Azure CLI then returns directly to the TUI without asking you to select a subscription. Activation requests are submitted only after verification succeeds.

A batch can use one authentication context. If selected assignments require different contexts, activate them in separate batches.

## Development

Run tests:

```bash
go test ./...
```

Run the CLI:

```bash
go run .
```

## Releases

`release.json` contains only the typed Go adapter settings: package and binary identity, ad-hoc macOS signing, and the linker symbols used for version provenance. The shared `mathwro/homebrew-tools/.github/workflows/release-tool.yml@main` workflow owns the release implementation.

Before the first release, merge the shared workflow into `mathwro/homebrew-tools`, require the `CI` and `Workflow Lint` checks in `main` branch protection, create the protected `release` environment with required reviewers, and configure the `DISTRIBUTION_DISPATCH_TOKEN` repository secret. The token must be limited to sending repository dispatches to `mathwro/homebrew-tools`.

Push an annotated prerelease tag such as `v0.1.0-rc.1` for a complete automatic dry run, or dispatch the **Release** workflow with `dry_run: true` for an existing tag. The central workflow validates tag ancestry, runs the Go tests, builds on all six native runners, injects reproducible version metadata, ad-hoc signs macOS binaries, executes version/help smoke tests, creates deterministic root-only archives, and verifies the complete checksum set. A dry run creates workflow artifacts but no GitHub Release or package update.

Publish a stable release from protected `main`:

```bash
git switch main
git pull --ff-only
go test ./...
git tag -a vX.Y.Z -m "pim-manager vX.Y.Z"
git push origin vX.Y.Z
```

The tag run repeats the dry-run checks and waits at the protected `release` environment before creating and publishing the immutable GitHub Release. Stable publication then calls the central notifier, which verifies the exact release and sends the canonical package update event.

Tags and published assets are immutable. If a run fails before publication, fix the source and create a new tag. If an artifact defect is found after publication, preserve the release and issue a new patch version. A notification failure does not invalidate the release; distribution reconciliation discovers it without republishing.
