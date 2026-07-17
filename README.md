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
