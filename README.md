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

Release metadata is defined once in `release/metadata.json`. A maintainer release uses an annotated or signed stable SemVer tag from `main`:

```bash
git switch main
git pull --ff-only
go test ./...
git tag -a vX.Y.Z -m "pim-manager vX.Y.Z"
git push origin vX.Y.Z
```

The tag workflow rejects lightweight tags, non-stable tag names, commits outside `main`, an existing published release, test failures, missing targets, unsafe archive layouts, and checksum mismatches. It builds all six targets on macOS, ad-hoc signs the macOS binaries, executes every artifact natively on clean GitHub-hosted x86-64 and ARM64 runners, then creates a draft GitHub Release.

Review the draft and its seven assets. Publish it through the **Publish Release** workflow with the exact tag. The protected `release` environment is the signing/review gate. Publication re-downloads and independently verifies the draft, publishes it, then the protected `distribution` environment sends one `cli-release-published` repository dispatch to `mathwro/homebrew-tools`. Configure `DISTRIBUTION_DISPATCH_TOKEN` in that environment with access limited to dispatching that repository. A dispatch failure leaves the valid upstream release unchanged and fails visibly for manual workflow retry.

Before the first release, require the `CI` and `Workflow Lint` checks in `main` branch protection. Also create the `release` and `distribution` environments with required reviewers. Stable release automation must remain disabled until those controls and `mathwro/homebrew-tools` exist.

Dry-run the complete local build without creating a tag or GitHub Release:

```bash
go run ./release build \
  -tag v0.0.1 \
  -commit "$(git rev-parse HEAD)" \
  -source-date-epoch "$(git show -s --format=%ct HEAD)" \
  -output dist
go run ./release verify -version 0.0.1 -dir dist
go run ./release smoke -version 0.0.1 -dir dist
```

`v0.0.1` here is snapshot metadata only; this path never invokes GitHub publication. On macOS, add `-sign-darwin` to exercise ad-hoc signing.

Tags and published assets are immutable. If the tag workflow fails before publication, fix the source and create a new tag; an unpublished draft may be safely replaced by rerunning its tag workflow. If an artifact defect is found after publication, preserve the release and issue a new patch version. Never delete and recreate a consumed tag or overwrite a published asset. If package dispatch alone fails, rerun **Publish Release** for the same tag: it re-verifies the immutable published release, skips the already-completed publication edit, and retries notification.
