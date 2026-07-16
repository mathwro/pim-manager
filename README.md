# pim-manager

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities from the terminal.

## Current MVP

Running `pim-manager` opens an interactive Bubble Tea TUI. Azure Resources is active for eligible Azure RBAC assignments across management groups, subscriptions, and resource groups. Entra Roles and Groups are shown as paused until Azure CLI can obtain their required Microsoft Graph PIM permissions.

## Authentication

The app uses your existing Azure CLI session. Sign in before running:

```bash
az login
```

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
