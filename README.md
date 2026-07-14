# pim-manager

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities from the terminal.

## MVP

Running `pim-manager` opens an interactive Bubble Tea TUI with three top-level areas:

- Entra Roles
- Azure Resources
- Groups

The app uses Azure CLI authentication. Sign in before running:

```bash
az login
```

## Development

Run tests:

```bash
go test ./...
```

Run the CLI:

```bash
go run .
```
