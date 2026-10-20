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
- Require Azure CLI step-up authentication before submitting a batch only when a selected role's effective PIM policy explicitly requires standard MFA or an authentication context.
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

Running `pim-manager` with no arguments starts the Bubble Tea TUI. Startup lists the tenants available to the current Azure CLI user. One tenant is selected automatically; multiple tenants open a keyboard-driven selection screen that shows `Display Name (default.domain)` and the tenant ID before the home screen. The home screen shows the selected tenant with the same label, the temporary Graph PIM pause explanation, and one selectable Azure Resources section. The complete product-area status is:

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
| `internal/azureauth` | Azure CLI tenant listing, session-local tenant context, access token retrieval and claims inspection, and interactive authentication-context step-up command construction. |
| `internal/pim` | Shared domain types for eligibility, assignment source, scope metadata, activation requests, and activation results. |
| `internal/providers/entra` | Dormant Entra role discovery and activation implementation retained for reactivation after supported Graph authentication is available. |
| `internal/providers/azureresources` | Active discovery and activation for eligible Azure Resource roles across management groups, subscriptions, and resource groups. |
| `internal/providers/groups` | Dormant Privileged Access Group discovery and activation implementation retained for reactivation after supported Graph authentication is available. |
| `internal/activation` | Shared batch activation orchestration, partial success handling, and retry classification. |
| `internal/tui` | Bubble Tea screens, Bubbles components, navigation, selection state, forms, progress, and summaries. |

The TUI depends on provider interfaces rather than concrete Azure clients. The current production runtime supplies only the Azure Resources provider, which translates ARM responses into shared `pim.EligibleAssignment` values. Entra and Groups runtime seams remain dormant so their source-specific metadata and activation paths can be reactivated without changing the TUI contract.

## Authentication and Discovery

At startup, `pim-manager` starts its loading spinner, lists the tenants available through Azure CLI, and enriches missing tenant names from the Azure CLI subscription cache. If the user is not signed in, the TUI shows a clear message with the exact `az login` command to run, provides a retry action, and exits only if the user chooses to quit. One tenant is selected automatically. A tenant menu appears only when more than one tenant is available, and the choice does not run `az account set` or change Azure CLI configuration.

After selecting a tenant, start a background Microsoft Graph `/v1.0/me?$select=id,displayName,userPrincipalName` lookup using a tenant-scoped Azure CLI Graph token. Bound Azure CLI startup, token acquisition, and HTTP together to fifteen seconds. Keep navigation and ARM discovery usable while checking. Report `Checking account`, `Account found`, `Account unverified`, or `Account unavailable` instead of claiming connectivity from tenant selection alone; display the found account identity alongside the tenant. Inconclusive checks retain and display the failing stage (Graph token acquisition or Graph self lookup) and the original error, including deadline expiration. Self-profile lookup uses basic `User.Read` and does not reactivate the paused Graph PIM sections.

Retain conclusive account status for at most five minutes and coalesce concurrent checks. Inconclusive errors are not fresh results: the next assignment-list entry or empty-discovery result may check again. Tenant selection, returning to cached assignments with stale status, and empty discovery with stale status trigger a background lookup; explicit `r` refreshes status, tenant membership, and the current workflow. Cancel checks on tenant changes and reject results from older tenant, principal, or request generations. No continuous polling or automatic interactive login is added.

Definite missing/disabled-account or unusable-sign-in errors invalidate the affected tenant's cached workflow and prevent activation. Keep a persistent warning with `az login --tenant <tenant-id>` and an `r` recovery path. Timeout, network, consent, and permission errors leave health unverified and do not block otherwise-valid ARM activation. Before submission, compare a known account principal with the checked ARM principal and clear stale selections on drift. Preserve existing checked-token pinning and step-up protections. Discovery errors must not also render an empty-eligibility message; successful empty discovery includes the account-check status and tenant context.

After tenant selection, only Azure Resources discovery is active:

1. The Azure Resources provider uses an Azure Resource Manager token requested with the selected tenant ID to list eligible role assignments across management groups, subscriptions, and resource groups visible to the user.
2. It returns normalized `pim.EligibleAssignment` records with source, display name, assignment type, scope name, scope type, principal details when available, eligibility identifiers, maximum duration, justification requirement, MFA requirement, authentication context, and activation capability.
3. The dormant Entra and Groups providers are not constructed or invoked by production wiring. They may be reactivated only after the tracked Microsoft Graph PIM authentication limitation is resolved.

Azure Resources discovery failures remain inside the Azure Resources workflow and are shown without leaving the TUI.

## TUI Screen Model

The Bubble Tea app uses a simple stack-like screen flow:

| Screen | Purpose |
| --- | --- |
| Tenant selection | List Azure CLI tenants; auto-select one tenant or require a choice when multiple tenants are available. |
| Home | Choose Azure Resources; show the selected tenant plus the Entra Roles and Groups pause explanation. |
| Assignment list | Fetch and render eligible assignments for the selected section; support search, filter, inspect, and multi-select. |
| Assignment details | Scroll through source, scope, assignment identifiers, policy hints, and activation constraints without displacing the activity dock or controls. |
| Activation form | Enter shared justification and duration for all selected assignments. |
| Confirmation | Review selected assignments and activation inputs before submitting. |
| Progress | Show the current assignment, completed/total requests, and per-assignment outcomes as the batch runs. |
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

All workflow screens share a bottom activity dock above contextual keyboard controls. It retains a bounded, timestamped in-memory session history and shows named in-flight stages with elapsed time, including ARM authentication preflight and retry validation. On short terminals it collapses to one status row; assignment, duration, and summary windows reserve space for the dock and controls. Unknown discovery totals use indeterminate feedback, not estimated percentages.

Activation emits individual started/result updates before moving to the next assignment. Live counts and the final summary distinguish activated, pending approval, and failed outcomes; failures do not stop the remaining batch. Manual retry counts describe only the submitted retry subset. Intermediate results remain guarded by originating tenant and workflow generation. Step-up completion is similarly scoped before updating screen or activity state.

Activity messages use allowlisted operation names, counts, and sanitized assignment labels. They exclude tokens, claims, justification, full request/response payloads, and raw errors; detailed diagnostics remain on existing error and summary surfaces. Tenant or principal workflow resets clear activity and operation clocks. The history is neither persisted nor presented as an audit trail.

## Activation Behavior

The activation wizard creates one activation request per selected assignment using the shared justification and duration. The activation service submits requests and records per-assignment results.

Before submitting a batch, the TUI checks the normalized policies of the selected assignments. Batches without an authentication requirement follow the normal path. Standard MFA uses an `amr` claims challenge; an enabled Conditional Access authentication context uses its `acrs` claim. One shared context is supported per batch, while conflicting contexts are blocked with instructions to activate separately. Bubble Tea releases the terminal while Azure CLI performs the interactive step-up, and cancellation or failure submits no activation requests.

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
- Step-up login failure, conflicting authentication contexts, or an unresolved PIM authentication policy rule.
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
| Azure auth wrapper | Unit tests around tenant-list parsing, tenant-name enrichment, tenant-scoped token commands, token claims, and exact MFA and authentication-context command construction without executing Azure CLI. |
| PIM domain normalization | Unit tests for active Azure Resource conversion and effective activation rules, including explicit MFA and enabled authentication contexts, plus retained Entra and Group conversion behavior needed for future reactivation. |
| Batch activation | Unit tests for partial success, pending approval, failures, and retry eligibility. |
| TUI models | Bubble Tea update/model tests for one/multiple tenant startup, tenant switching and stale-result rejection, navigation, assignment selection, form validation, step-up gating, context conflicts, failed-login blocking, wrapped errors, and summaries. |
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
