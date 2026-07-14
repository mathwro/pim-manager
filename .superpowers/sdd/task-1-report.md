# Task 1 Report: Start TUI Without Azure Preflight

## What I implemented
- Removed Azure principal lookup and ARM scope discovery from `internal/app.Run`.
- Added lazy provider adapters in `internal/app/lazy_providers.go` so Entra stays eager, while Groups and Azure Resources resolve Azure-dependent data only when the TUI interacts with those sections.
- Wired Groups through a lazy principal source so it no longer needs a permanent empty principal ID.
- Wired Azure Resources through a lazy principal + scope discoverer wrapper so both principal ID and scope discovery are deferred.
- Added tests covering lazy deferral and cached resolution behavior, plus a `Run` smoke test that verifies no Azure CLI lookup happens before TUI startup.

## What I tested and test results
- `cd /mnt/c/Users/mwrobel/repo/pim-manager/.worktrees/pim-manager-mvp && go test ./internal/app`
  - PASS
- `cd /mnt/c/Users/mwrobel/repo/pim-manager/.worktrees/pim-manager-mvp && go run . --help`
  - PASS

## TDD Evidence
### RED
- Command: `cd /mnt/c/Users/mwrobel/repo/pim-manager/.worktrees/pim-manager-mvp && go test ./internal/app`
- Output:
  - `internal/app/lazy_providers_test.go:38:80: undefined: lazyAssignmentProvider`
  - `internal/app/lazy_providers_test.go:41:9: undefined: lazyAssignmentProvider`
  - `internal/app/lazy_providers_test.go:55:14: undefined: newLazyAzureResourcesProvider`

### GREEN
- Command: `cd /mnt/c/Users/mwrobel/repo/pim-manager/.worktrees/pim-manager-mvp && go test ./internal/app`
- Output:
  - `ok  	github.com/mathwro/pim-manager/internal/app	0.006s`

## Files changed
- `internal/app/app.go`
- `internal/app/lazy_providers.go`
- `internal/app/lazy_providers_test.go`

## Self-review findings
- `Run` no longer performs Azure CLI principal lookup or ARM scope discovery before Bubble Tea starts.
- Azure Resources and Groups now resolve Azure-specific inputs lazily instead of at app startup.
- `go run . --help` still works and does not trigger Azure preflight.

## Concerns
- The lazy wrappers cache the first resolved provider instance; this is good for startup behavior, but any later transient lookup error will be returned on first use and not retried automatically.
