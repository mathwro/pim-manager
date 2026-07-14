# pim-manager Design

## Overview

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities through an interactive terminal UI. It uses Cobra for command structure, Bubble Tea for the TUI runtime, and Bubbles for reusable UI components. Authentication is based on the user's existing Azure CLI session.

The MVP focuses on a keyboard-first interactive workflow for selecting and activating multiple eligible assignments. It supports three top-level PIM areas modeled after the Azure portal: Entra roles, Azure resource roles, and groups.

## Goals

- Open the interactive TUI when `pim-manager` is run with no arguments.
- Use Azure CLI authentication instead of implementing a custom login flow.
- Discover eligible PIM activations for:
  - Microsoft Entra directory roles.
  - Azure Resource RBAC roles across management groups, subscriptions, and resource groups.
  - Privileged Access Group member and owner assignments.
- Let users select multiple assignments within a section.
- Collect one justification and one duration for all selected assignments in the batch.
- Activate selected assignments and show a per-assignment result summary.
- Continue processing remaining assignments when one activation fails.

## Non-Goals

- Non-interactive scripting commands.
- Persistent config files.
- Custom browser/device-code login flows.
- Background scheduling or automatic activation.
- Approval management after a request is submitted.
- Cross-section mixed batches in the MVP.

## Product Flow

Running `pim-manager` with no arguments starts the Bubble Tea TUI. The home screen shows Azure account context and three primary sections:

| Section | What it lists |
| --- | --- |
| Entra Roles | Eligible Microsoft Entra directory role activations. |
| Azure Resources | Eligible Azure RBAC activations across management groups, subscriptions, and resource groups. |
| Groups | Eligible Privileged Access Group member and owner activations. |

Each section follows the same flow:

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
| `internal/app` | Application wiring for auth, providers, activation services, and the TUI model. |
| `internal/azureauth` | Azure CLI login validation and access token retrieval for Microsoft Graph and Azure Resource Manager. |
| `internal/pim` | Shared domain types for eligibility, assignment source, scope metadata, activation requests, and activation results. |
| `internal/providers/entra` | Discovery and activation for eligible Microsoft Entra directory roles. |
| `internal/providers/azureresources` | Discovery and activation for eligible Azure Resource roles across management groups, subscriptions, and resource groups. |
| `internal/providers/groups` | Discovery and activation for eligible Privileged Access Group member and owner assignments. |
| `internal/activation` | Shared batch activation orchestration, partial success handling, and retry classification. |
| `internal/tui` | Bubble Tea screens, Bubbles components, navigation, selection state, forms, progress, and summaries. |

The TUI depends on interfaces rather than concrete Azure clients. Provider packages translate Azure API responses into shared `pim.EligibleAssignment` values so the UI can render all sections consistently while preserving source-specific metadata for details and activation.

## Authentication and Discovery

At startup, `pim-manager` validates Azure CLI login state. If the user is not signed in, the TUI shows a clear message with the exact `az login` command to run, provides a retry action, and exits only if the user chooses to quit.

After authentication, discovery is section-specific:

1. The Entra provider uses Azure CLI-derived Microsoft Graph access tokens to list eligible directory role assignments and enrich them with display names and policy metadata.
2. The Azure Resources provider uses Azure CLI-derived Azure Resource Manager tokens to list eligible role assignments across management groups, subscriptions, and resource groups visible to the user.
3. The Groups provider uses Azure CLI-derived Microsoft Graph access tokens to list eligible Privileged Access Group member and owner assignments.
4. Each provider returns normalized `pim.EligibleAssignment` records with source, display name, assignment type, scope name, scope type, principal details when available, eligibility identifiers, policy hints, and activation capability.

Discovery failures are isolated by section. If Entra discovery fails, the Entra section shows that error; Azure Resources and Groups remain usable when their own discovery succeeds.

## TUI Screen Model

The Bubble Tea app uses a simple stack-like screen flow:

| Screen | Purpose |
| --- | --- |
| Home | Choose Entra Roles, Azure Resources, or Groups; show auth and account context. |
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

Supported result states:

| State | Meaning |
| --- | --- |
| `activated` | The assignment became active successfully. |
| `pending_approval` | The request was submitted and is waiting for approval. |
| `failed` | The request failed and includes an actionable error message when available. |

Failures do not stop the batch. The summary preserves individual messages for policy failures, API failures, and transient errors.

Retry behavior is conservative. The MVP allows manual retry for failed activations classified as transient or input-related, but it does not automatically retry any activation. Failures that require user action, such as approval, MFA, permission changes, or policy changes, are shown with their message and are not offered as retry candidates.

## Error Handling

The app should surface Azure and PIM constraints clearly instead of collapsing them into generic errors. Important cases include:

- Azure CLI not installed or not signed in.
- Token retrieval failure.
- MFA required.
- Approval required.
- Invalid duration.
- Missing justification.
- No active eligibility.
- Expired eligibility.
- Insufficient permissions.
- API throttling.
- Tenant, subscription, management group, or resource group access failure.

Provider-level errors are isolated to the relevant section. Activation errors are isolated to the relevant assignment. The UI should keep enough detail for users to know whether to retry, change input, sign in again, or resolve an Azure policy/access issue.

## Testing Strategy

Testing should focus on separable logic and avoid requiring live Azure access for normal test runs.

| Area | Test approach |
| --- | --- |
| Cobra command startup | Unit tests for default command behavior and error propagation. |
| Azure auth wrapper | Unit tests around command/token parsing with mocked Azure CLI execution. |
| PIM domain normalization | Unit tests for converting Entra, Azure Resource, and Group API responses into shared domain records. |
| Batch activation | Unit tests for partial success, pending approval, failures, and retry eligibility. |
| TUI models | Bubble Tea update/model tests for navigation, selection, form validation, and summaries. |
| Provider integration | Optional manual integration tests gated behind environment variables or explicit commands. |

## Open Extension Points

The MVP should keep clean seams for later additions without implementing them now:

- Scriptable non-interactive commands.
- Persistent defaults for justification and duration.
- Cross-section mixed activation batches.
- Rich filtering by scope, assignment type, and role name.
- Exporting activation summaries.
- Approval status polling.
