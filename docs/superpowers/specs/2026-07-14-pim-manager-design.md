# pim-manager Design

## Overview

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities through an interactive terminal UI. It uses Cobra for command structure, Bubble Tea for the TUI runtime, and Bubbles for reusable UI components. Authentication is based on the user's existing Azure CLI session.

The current MVP focuses on a keyboard-first interactive workflow for selecting and activating eligible Azure Resource assignments. Entra roles and groups remain modeled as dormant reactivation targets after the Microsoft Graph PIM authentication limitation is resolved.

## Goals

- Open the interactive TUI when `pim-manager` is run with no arguments.
- Use Azure CLI authentication instead of implementing a custom login flow.
- Discover eligible Azure Resource RBAC activations across management groups, subscriptions, and resource groups.
- Preserve dormant Entra directory role and Privileged Access Group provider seams for future reactivation after supported Graph authentication is available.
- Let users select multiple Azure Resource assignments.
- Collect one justification and one duration for all selected assignments in the batch.
- Activate selected assignments and show a per-assignment result summary.
- Require Azure CLI MFA before submitting a batch only when a selected role's effective PIM policy explicitly requires it.
- Continue processing remaining assignments when one activation fails.

## Non-Goals

- Non-interactive scripting commands.
- Persistent config files.
- Custom browser/device-code login flows.
- Background scheduling or automatic activation.
- Approval management after a request is submitted.
- Cross-section mixed batches in the MVP.

## Temporary Graph PIM Limitation

Entra Roles and Groups are paused because Azure CLI's fixed client cannot obtain the delegated Microsoft Graph PIM permissions required for discovery and activation. Azure Resources remains available through ARM.

Track:

- [Azure CLI #22775](https://github.com/Azure/azure-cli/issues/22775)
- [Azure CLI #28854](https://github.com/Azure/azure-cli/issues/28854)
- [az-pim-cli #121](https://github.com/netr0m/az-pim-cli/issues/121)

Do not use the deprecated `/beta/privilegedAccess` APIs or private `api.azrbac.mspim.azure.com` endpoint. Re-enable these sections only when Azure CLI supports the required scopes or the product adopts a dedicated Graph application registration and login.

## Product Flow

Running `pim-manager` with no arguments starts the Bubble Tea TUI. The home screen shows Azure account context, the temporary Graph PIM pause explanation, and one selectable Azure Resources section. The complete product-area status is:

| Section | What it lists |
| --- | --- |
| Entra Roles | **Paused** — Azure CLI cannot obtain the required Microsoft Graph PIM permissions. |
| Azure Resources | Eligible Azure RBAC activations across management groups, subscriptions, and resource groups. |
| Groups | **Paused** — Azure CLI cannot obtain the required Microsoft Graph PIM permissions. |

The active Azure Resources section follows this flow:

1. Discover eligible assignments for that PIM area.
2. Render assignments in a searchable and filterable multi-select list.
3. Allow optional inspection of assignment details.
4. Collect shared activation inputs: justification and duration.
5. Confirm selected assignments and shared inputs.
6. Submit activation requests as a batch.
7. Show a final summary grouped by activated, pending approval, and failed results.

Cobra provides the root command and future extension points. The MVP command surface is intentionally small: `pim-manager` opens the app, while future scriptable commands such as `pim-manager list` or `pim-manager activate` can reuse the same service layer.

## Architecture

The application is split into focused packages with clear boundaries.

| Package | Responsibility |
| --- | --- |
| `cmd` | Cobra commands, root command setup, flags, and process exit behavior. |
| `internal/app` | Production wiring for Azure CLI auth, the ARM client, the active Azure Resources provider, and the TUI model. |
| `internal/azureauth` | Azure CLI login validation and token retrieval; the current app uses Azure Resource Manager credentials, while Graph token support is retained for future reactivation. |
| `internal/pim` | Shared domain types for eligibility, assignment source, scope metadata, activation requests, and activation results. |
| `internal/providers/entra` | Dormant Entra role discovery and activation implementation retained for reactivation after supported Graph authentication is available. |
| `internal/providers/azureresources` | Active discovery and activation for eligible Azure Resource roles across management groups, subscriptions, and resource groups. |
| `internal/providers/groups` | Dormant Privileged Access Group discovery and activation implementation retained for reactivation after supported Graph authentication is available. |
| `internal/activation` | Shared batch activation orchestration, partial success handling, and retry classification. |
| `internal/tui` | Bubble Tea screens, Bubbles components, navigation, selection state, forms, progress, and summaries. |

The TUI depends on provider interfaces rather than concrete Azure clients. The current production runtime supplies only the Azure Resources provider, which translates ARM responses into shared `pim.EligibleAssignment` values. Entra and Groups runtime seams remain dormant so their source-specific metadata and activation paths can be reactivated without changing the TUI contract.

## Authentication and Discovery

At startup, `pim-manager` validates Azure CLI login state. If the user is not signed in, the TUI shows a clear message with the exact `az login` command to run, provides a retry action, and exits only if the user chooses to quit.

After authentication, only Azure Resources discovery is active:

1. The Azure Resources provider uses Azure CLI-derived Azure Resource Manager tokens to list eligible role assignments across management groups, subscriptions, and resource groups visible to the user.
2. It returns normalized `pim.EligibleAssignment` records with source, display name, assignment type, scope name, scope type, principal details when available, eligibility identifiers, maximum duration, justification requirement, MFA requirement, and activation capability.
3. The dormant Entra and Groups providers are not constructed or invoked by production wiring. They may be reactivated only after the tracked Microsoft Graph PIM authentication limitation is resolved.

Azure Resources discovery failures remain inside the Azure Resources workflow and are shown without leaving the TUI.

## TUI Screen Model

The Bubble Tea app uses a simple stack-like screen flow:

| Screen | Purpose |
| --- | --- |
| Home | Choose Azure Resources; show auth and account context plus the Entra Roles and Groups pause explanation. |
| Assignment list | Fetch and render eligible assignments for the selected section; support search, filter, inspect, and multi-select. |
| Assignment details | Show source, scope, assignment identifiers, policy hints, and activation constraints for the focused item. |
| Activation form | Enter shared justification and duration for all selected assignments. |
| Confirmation | Review selected assignments and activation inputs before submitting. |
| Progress | Show batch activation progress with per-assignment updates. |
| Summary | Show activated, pending approval, and failed results; allow manual retry for retryable failures. |

Keyboard behavior:

| Key | Action |
| --- | --- |
| Arrow keys or `j`/`k` | Navigate. |
| `/` | Search. |
| Space | Toggle selection. |
| Enter | Advance or confirm. |
| Esc | Go back. |
| `?` | Show contextual help. |

Bubbles components should be used where they fit: list or table for assignments, text input or textarea for activation form fields, spinner or progress for activation, and viewport for long summaries.

## Activation Behavior

The activation wizard creates one activation request per selected assignment using the shared justification and duration. The activation service submits requests and records per-assignment results.

Before submitting a batch, the TUI checks the normalized policies of the selected assignments. If none explicitly require `MultiFactorAuthentication`, activation follows the normal path without an MFA step. If any selected assignment requires MFA, Bubble Tea temporarily releases the terminal and runs one interactive `az login` for the current tenant with the Azure Resource Manager scope and an MFA claims challenge. A successful login starts the complete batch; cancellation or failure returns to confirmation and submits no activation requests. Microsoft Entra controls the browser or device-code flow and the user's configured verification method.

Supported result states:

| State | Meaning |
| --- | --- |
| `activated` | The assignment became active successfully. |
| `pending_approval` | The request was submitted and is waiting for approval. |
| `failed` | The request failed and includes an actionable error message when available. |

Failures do not stop the batch. The summary preserves individual messages for policy failures, API failures, and transient errors.

Retry behavior is conservative. The MVP allows manual retry for failed activations classified as transient or input-related, but it does not automatically retry any activation. Failures that require user action, such as approval, incomplete MFA, permission changes, or policy changes, are shown with their message and are not offered as retry candidates.

## Error Handling

The app should surface Azure and PIM constraints clearly instead of collapsing them into generic errors. Important cases include:

- Azure CLI not installed or not signed in.
- Token retrieval failure.
- MFA login failure or an unresolved PIM `MfaRule`.
- Approval required.
- Invalid duration.
- Missing justification.
- No active eligibility.
- Expired eligibility.
- Insufficient permissions.
- API throttling.
- Tenant, subscription, management group, or resource group access failure.

Azure Resources provider errors are isolated to its workflow, and activation errors are isolated to the relevant assignment. Dormant Entra and Groups providers are not invoked. The UI should keep enough detail for users to know whether to retry, change input, sign in again, or resolve an Azure policy/access issue.

## Testing Strategy

Testing should focus on separable logic and avoid requiring live Azure access for normal test runs.

| Area | Test approach |
| --- | --- |
| Cobra command startup | Unit tests for default command behavior and error propagation. |
| Azure auth wrapper | Unit tests around command/token parsing and exact interactive MFA command construction without executing Azure CLI. |
| PIM domain normalization | Unit tests for active Azure Resource conversion and effective activation rules, including explicit MFA, plus retained Entra and Group conversion behavior needed for future reactivation. |
| Batch activation | Unit tests for partial success, pending approval, failures, and retry eligibility. |
| TUI models | Bubble Tea update/model tests for navigation, selection, form validation, conditional MFA gating, failed-login blocking, and summaries. |
| Provider integration | Optional manual integration tests gated behind environment variables or explicit commands. |

## Open Extension Points

The MVP should keep clean seams for later additions without implementing them now:

- Scriptable non-interactive commands.
- Persistent defaults for justification and duration.
- Cross-section mixed activation batches.
- Rich filtering by scope, assignment type, and role name.
- Exporting activation summaries.
- Approval status polling.
- Reactivating Entra Roles and Groups after a supported Microsoft Graph PIM authentication path is available.
