# Tenant Selection Design

## Overview

`pim-manager` will select a Microsoft Entra tenant before selecting a PIM area. This supports Azure CLI sessions authorized for multiple tenants without changing the user's global Azure CLI default.

The flow becomes:

1. Select tenant when more than one is available.
2. Select PIM area.
3. Discover and select eligible assignments.
4. Enter activation inputs, confirm, authenticate when required, and activate.

## Goals

- List the tenants available to the current Azure CLI user.
- Let the user choose one tenant for the `pim-manager` session when multiple tenants are available.
- Skip the tenant-selection screen when Azure CLI returns exactly one tenant.
- Keep discovery, authentication checks, step-up, and activation inside the selected tenant.
- Preserve the existing checked-token pinning and principal-drift protections.
- Keep tenant selection session-local; policy-required interactive step-up retains its existing Azure CLI login behavior.

## Non-Goals

- Selecting an Azure subscription.
- Persisting a preferred tenant.
- Running `az account set` or changing Azure CLI configuration.
- Signing into an additional account from inside `pim-manager`.
- Adding tenant search or filtering.
- Reactivating the paused Entra Roles or Groups providers.

## Azure CLI Behavior

Tenant discovery uses:

```text
az account tenant list --output json
```

Each usable record must have a non-empty `tenantId`. The UI displays the tenant's domain or display name when Azure CLI provides one and always includes its tenant ID. Missing optional display metadata falls back to the tenant ID. Duplicate tenant IDs are collapsed.

Azure CLI failures preserve their details. A failure that indicates no usable login shows the exact `az login` guidance and a retry action.

The selected tenant is session-local. Token retrieval uses:

```text
az account get-access-token --resource <resource> --tenant <tenant-id> --output json
```

This follows Azure CLI's supported per-command tenant selection and does not mutate the active Azure CLI account. Interactive step-up continues to use `az login --tenant <tenant-id>` with the existing claims challenge and child-process-only subscription-selector override.

## Product Flow

### Startup

The Bubble Tea model starts on a tenant-loading state and requests the tenant list without starting PIM discovery.

- Zero usable tenants or a command error shows an actionable Azure CLI error and `az login` guidance. Pressing `r` retries tenant discovery.
- One usable tenant is selected automatically and the PIM-area home opens.
- Multiple usable tenants open the tenant-selection screen.

### Tenant Selection

The screen is keyboard-first:

| Key | Action |
| --- | --- |
| Arrow keys or `j`/`k` | Move between tenants. |
| Enter | Select the focused tenant and open PIM-area selection. |
| `r` | Refresh tenants from Azure CLI. |
| `?` | Show contextual help. |
| `q` or Ctrl-C | Quit. |

The PIM-area home shows the selected tenant rather than the current subscription. If multiple tenants are available, Esc returns to tenant selection. Selecting another tenant clears assignment discovery, selections, form inputs, authentication state, progress, errors, and summaries before any new discovery can begin.

The progress labels become:

```text
Account -> Type -> Select -> Request -> Review -> Result
```

`Account` names the user-facing first step while the implementation and displayed fields make clear that the selected boundary is a tenant.

## Architecture

### Azure Authentication

`internal/azureauth` owns Azure CLI tenant parsing and tenant context propagation.

- Replace the single-current-account lookup with a tenant-list operation.
- Represent a tenant with its ID and optional display metadata.
- Provide a small context helper for attaching and retrieving the selected tenant ID.
- Make access-token retrieval add `--tenant` when the context contains a selected tenant.
- Keep context cancellation and deadline errors unchanged.

The context helper is preferred over a mutable tenant field on the shared CLI value. Concurrent or delayed operations therefore retain the tenant that started them, and selecting another tenant cannot retarget an in-flight operation.

### TUI

`internal/tui` owns tenant-list loading, selection, and navigation.

- Add a tenant-selection screen and focused-tenant cursor.
- Change the runtime account seam from one current account to a list of tenants.
- Store the selected tenant in the model.
- Create every discovery and authentication command with a context carrying that tenant.
- Continue passing the selected tenant ID directly to the step-up command.

Discovery result messages include the originating tenant ID and a request generation. The model ignores results whose tenant or generation no longer matches the active request. This prevents delayed responses from a previous tenant from populating the current tenant's assignment list.

### Providers and Activation

Provider interfaces remain unchanged. Providers receive the tenant-scoped context through their existing `Discover` and `Activate` methods. The ARM client obtains discovery tokens through that context.

Before activation, the TUI obtains and inspects an ARM token using the selected tenant context. The exact checked token remains pinned in the activation context and is reused for every request in the batch. No token is fetched between validation and submission.

A failed or canceled token check or step-up submits no activation requests. The existing principal-ID comparison after step-up remains authoritative for detecting account drift.

## State and Error Handling

- Refresh atomically replaces the available tenant list.
- If refresh removes the selected tenant, the model returns to tenant selection and clears tenant-specific workflow state.
- Invalid JSON and records without tenant IDs produce an actionable parsing error when no usable tenant remains.
- Tenant-list errors remain on the tenant screen and can be retried.
- Assignment discovery errors remain inside the selected PIM area.
- Authentication and activation errors retain the existing per-batch and per-assignment isolation.
- Stale tenant-list or discovery messages are ignored by request generation.

## Testing Strategy

Normal tests use fake Azure CLI runners and providers; they require no live Azure access.

### Azure Authentication Tests

- Parse one and multiple tenant records.
- Use optional display metadata and tenant-ID fallback.
- Collapse duplicate tenant IDs.
- Reject an empty or unusable tenant response with login guidance.
- Preserve context cancellation and command details.
- Add the exact `--tenant <tenant-id>` arguments to token retrieval when a selected tenant is present.

### TUI Tests

- Zero/error tenants show retry guidance.
- One tenant skips the selection screen.
- Multiple tenants support arrow and `j`/`k` navigation and Enter selection.
- Esc from PIM-area selection returns to the tenant screen only when another tenant can be chosen.
- Switching tenants clears all tenant-specific workflow state.
- Discovery and ARM authentication receive the selected tenant context.
- Delayed results from an earlier tenant or request generation are ignored.
- Failed or canceled authentication after a tenant switch submits nothing.
- Views render selected-tenant context and the six-step progress line.

### App Wiring and Verification

- App startup lists tenants but does not discover PIM assignments.
- Existing activation, token pinning, principal targeting, and step-up tests continue to pass.
- Run focused package tests, `go test ./...`, and `go build ./...`.
- Smoke the interactive TUI through multi-tenant selection when the local Azure CLI session exposes multiple tenants; otherwise exercise the one-tenant startup path and retain deterministic multi-tenant model coverage.

## Acceptance Criteria

- A user with multiple Azure CLI tenants must choose a tenant before choosing Azure Resources.
- A user with one Azure CLI tenant reaches PIM-area selection without an extra confirmation screen.
- Every discovery and authentication token request is scoped to the selected tenant.
- Changing tenants cannot display or activate assignments returned for the previous tenant.
- `pim-manager` never runs `az account set` and never changes Azure CLI global configuration.
- Existing activation security invariants remain intact: failed step-up submits nothing, principal drift blocks submission, and the checked ARM token is pinned across the batch.
