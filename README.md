# pim-manager

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities from the terminal.

## Current MVP

Running `pim-manager` opens an interactive Bubble Tea TUI. It validates the existing Azure CLI session and, when multiple tenants are available, asks which tenant to use before showing the PIM areas. Azure Resources handles eligible Azure RBAC assignments across management groups, subscriptions, and resource groups. Entra Roles handles eligible directory roles, and Groups handles eligible PIM group member and owner assignments through a separate session-only Microsoft Graph browser connection.

In the Azure Resources assignment list, press `/` to search by role, subscription or management group name or ID, or resource group scope. Search is case-insensitive and accepts part of a name or ID.

Press `i` to inspect an assignment. Details scroll with arrow keys or `j`/`k`, keeping long identifiers and policy diagnostics accessible above the activity dock; press Esc to return.

The bottom activity dock retains recent timestamped events while you navigate. Running tenant, account, discovery, policy, and activation-authentication checks show their stage and elapsed time; no estimated completion percentage is shown. During activation, the TUI displays the current assignment, completed-request count, and activated, pending-approval, pending-provisioning, and failed totals as results arrive. Manual retries show progress for the retryable subset only; accepted requests awaiting provisioning are not retryable failures.

Activity is bounded and kept only in memory for the current session, not written as an audit log. Tenant or principal workflow resets clear it. Activity messages omit credentials, claims, justification text, and raw Azure error payloads; actionable diagnostics remain in the existing error and result views. Short terminals collapse the dock to one status row, keeping contextual keyboard controls below it.

## Installation

Install the latest version directly from the Go module repository:

```bash
go install github.com/mathwro/pim-manager@latest
```

`go install` downloads the source, builds the CLI, and writes the `pim-manager` executable to `GOBIN`. If `GOBIN` is unset, Go uses `$(go env GOPATH)/bin` (normally `~/go/bin`). Ensure that directory is on your `PATH`, then start the TUI:

```bash
pim-manager
```

For `go install` module builds, update to the latest tagged release without opening the TUI:

```bash
pim-manager update
```

The command requires the Go toolchain and follows Go's `@latest` module version. Tagged `go install` builds also check once in the background and show the external update command on the home screen when a newer tag is available.

Prebuilt release archives are also published for Windows, macOS, and Linux on x86-64 and ARM64. They contain only the root `pim-manager` executable (`pim-manager.exe` on Windows) and require no Go runtime. Prebuilt release and package-manager builds skip the background `go list` check; running `pim-manager update` from one directs you to update via your package manager or GitHub Releases (`https://github.com/mathwro/pim-manager/releases/latest`) instead of writing a second binary to `GOBIN`. macOS archives are ad-hoc signed; they are not Developer ID signed or notarized. Linux archives use static pure-Go binaries with `CGO_ENABLED=0`, so they have no libc dependency.

## Authentication

The app requires Azure CLI on `PATH` and uses your existing session. Windows `az.cmd` installations are supported through the Windows command interpreter; macOS and Linux run Azure CLI directly. Sign in before running:

```bash
az login
```

If Azure CLI exposes more than one tenant, `pim-manager` shows a keyboard-driven tenant menu before the PIM areas. Rows use `Tenant Name (default.domain)` when available and retain the tenant ID beneath the label; name-only, domain-only, and ID-only fallbacks are supported. A single tenant is selected automatically. The choice applies only to the current `pim-manager` session: token acquisition, discovery, authentication checks, and activation use that tenant without running `az account set`.

After tenant selection, a non-blocking account check uses a tenant-scoped Azure CLI token and a live Microsoft Graph self-profile lookup. The check has a fifteen-second background deadline, including Azure CLI startup, token acquisition, and the Graph request; it does not delay navigation or role discovery. The UI distinguishes **Checking account**, **Account found**, **Account unverified**, and **Account unavailable**; finding an account does not guarantee its roles or sign-in remain active. An inconclusive check displays the failing stage and error, so a token-acquisition timeout can be distinguished from a Graph timeout or permission failure. Basic self-profile access uses `User.Read` and does not establish Graph PIM authorization.

Conclusive account status is retained for up to five minutes. An inconclusive check is not treated as fresh: the next assignment-list entry or empty-discovery result can check again, without repeating an in-flight check. Returning to cached assignments also refreshes stale status. Press `r` to refresh account status, tenants, and the current assignment workflow explicitly. There is no continuous polling or automatic login.

An explicit missing/disabled account or unusable sign-in clears affected cached assignments and blocks activation. Follow the displayed `az login --tenant <tenant-id>` command, then press `r` to reload. Network timeouts and Graph permission restrictions leave the account **unverified** rather than incorrectly reporting it as deleted; they do not block otherwise-valid ARM activation. Discovery failures are shown as errors, not as an empty list of eligible roles.

Azure Resources reuses the exact checked ARM token for standard MFA-only policies; ARM/PIM remains authoritative. When an enabled Conditional Access authentication context is missing, `pim-manager` temporarily hands the terminal to interactive Azure CLI login. Complete verification in the browser; Azure CLI returns directly to the TUI without asking you to select a subscription. Subscription-selector settings change only for that child process.

A batch can use one authentication context. If selected assignments require different contexts, activate them in separate batches.

### Entra Roles and Groups

Opening either Graph-backed section first shows a connection screen. Press Enter to open native MSAL browser sign-in with PKCE. Microsoft displays **Microsoft Graph Command Line Tools**, the existing public client used for this connection; no PowerShell installation, device code, or new app registration is involved. Tenant consent and Conditional Access still apply. If approval is required, the connection fails with a diagnostic rather than changing tenant permissions.

The checked Azure CLI username is supplied as a login hint when available, without forcing the browser's account chooser. Existing browser sessions may continue directly; Microsoft can still require credentials, MFA, consent, or account selection. The resulting tenant and principal must match the checked CLI identity before discovery or activation is allowed.

The connection is shared by Entra Roles and Groups and stays in memory until the process exits, the tenant or principal changes, or the connection is reset. It does not modify the Azure CLI context or persist a token cache. Esc cancels an in-progress connection; stale or canceled callbacks cannot reconnect a discarded workflow.

Discovery reads the effective activation policy, including inherited requirements. Unknown requirements, unavailable policies, and unsupported ticket requirements block submission rather than guessing defaults. Before every activation batch or manual retry, the app rechecks identity, scopes, and required MFA/authentication-context claims; a missing Graph policy claim triggers browser step-up. One exact validated Graph token is pinned to every request in that batch. Failed or canceled verification submits nothing, and quitting or changing tenants cancels queued submissions. Requests already accepted by Microsoft cannot be undone by closing the app.

## Development

Run tests:

```bash
go test ./...
```

CI checks formatting for every tracked Go file, runs `go vet ./...`, and runs uncached tests with the race detector:

```bash
go test -race ./... -count=1
```

Race detection requires a supported platform and, on Linux, a C compiler with cgo enabled.

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
