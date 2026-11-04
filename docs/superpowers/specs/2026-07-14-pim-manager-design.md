# pim-manager Design

## Overview

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities through an interactive terminal UI. It uses Cobra for command structure, Bubble Tea for the TUI runtime, and Bubbles for reusable UI components. Azure CLI supplies tenant selection and Azure Resource authentication; Entra Roles and Groups use a separate in-memory native MSAL browser session bound to the same tenant and principal.

The MVP provides a keyboard-first interactive workflow for eligible Azure Resource roles, Entra directory roles, and PIM group member/owner assignments.

## Goals

- Open the interactive TUI when `pim-manager` is run with no arguments.
- Reuse Azure CLI authentication for ARM and use explicit native MSAL browser PKCE authentication for Microsoft Graph.
- Discover eligible Azure Resource RBAC activations across management groups, subscriptions, and resource groups.
- Discover eligible Entra directory roles and PIM group member/owner assignments through supported Microsoft Graph v1.0 APIs.
- Let users select multiple assignments within one section.
- Collect shared justification and per-assignment durations within effective policy limits.
- Activate selected assignments and show a per-assignment result summary.
- Before submission, validate the exact token and identity against the selected tenant and effective policy; prompt for missing Graph policy claims or ARM authentication contexts only when required.
- Continue processing remaining assignments when one activation fails.

## Non-Goals

- Non-interactive scripting commands.
- Persistent config files.
- Device-code authentication, new app registrations, and persistent credential caches.
- Background scheduling or automatic activation.
- Approval management after a request is submitted.
- Cross-section mixed batches in the MVP.

## Native Microsoft Graph Connection

Azure CLI's fixed public client cannot obtain the delegated Graph PIM scopes. Entra Roles and Groups instead use the existing Microsoft Graph Command Line Tools public client through native MSAL browser authentication. The connection screen explains that branding before the user explicitly opens the browser. No PowerShell wrapper, device code, new registration, or persistent credential cache is used; tenant consent and Conditional Access remain authoritative.

The session is shared by both Graph-backed sections. Bind it to the selected tenant and the checked CLI ARM principal. Supply a checked username as a login hint and omit forced account selection when available, while allowing all required authentication and consent prompts. Validate the returned access-token, ID-token, and MSAL account identity; reject a different tenant or principal. Tenant/principal changes, cancellation, and session reset fence stale callbacks and clear credentials.

Read supported Graph v1.0 eligibility schedules and effective policy rules, including inherited requirements and pagination. Unknown or unsupported activation requirements fail closed. Before each batch or manual retry, validate Graph scopes, expiration, principal, and required MFA/authentication-context claims; use browser step-up for missing claims. Pin the exact checked Graph token to all batch requests. Silent token acquisition never opens the browser implicitly; interaction failures require explicit reconnection.

Deprecated `/beta/privilegedAccess` APIs and private `api.azrbac.mspim.azure.com` endpoints remain out of scope. The historical pause rationale is retained in `2026-07-15-pause-graph-pim-sections-design.md`, not as current product behavior.

## Product Flow

Running `pim-manager` with no arguments starts the Bubble Tea TUI. Startup lists the tenants available to the current Azure CLI user. One tenant is selected automatically; multiple tenants open a keyboard-driven selection screen that shows `Display Name (default.domain)` and the tenant ID before the home screen. The home screen shows the selected tenant and three selectable PIM sections:

| Section | What it lists |
| --- | --- |
| Entra Roles | Eligible Microsoft Entra directory roles. |
| Azure Resources | Eligible Azure RBAC activations across management groups, subscriptions, and resource groups. |
| Groups | Eligible PIM group member and owner assignments. |

Each section follows this flow; Graph-backed sections first require an explicit connection if the session is not ready:

1. Discover eligible assignments for that PIM area.
2. Render assignments in a searchable and filterable multi-select list.
3. Allow optional inspection of assignment details.
4. Collect shared justification and per-assignment durations.
5. Confirm selected assignments and shared inputs.
6. Submit activation requests as a batch.
7. Show a final summary grouped by activated, pending approval, pending provisioning, and failed results.

Cobra provides the root command and future extension points. The MVP command surface is intentionally small: `pim-manager` opens the app, while future scriptable commands such as `pim-manager list` or `pim-manager activate` can reuse the same service layer.

## Architecture

The application is split into focused packages with clear boundaries.

| Package | Responsibility |
| --- | --- |
| `cmd` | Cobra commands, root command setup, flags, and process exit behavior. |
| `internal/app` | Production wiring for Azure CLI and native Graph authentication, clients, providers, and the TUI model. |
| `internal/azureauth` | Azure CLI tenant listing, session-local tenant context, access token retrieval and claims inspection, and interactive authentication-context step-up command construction. |
| `internal/graphauth` | Native browser PKCE sign-in, in-memory MSAL session, tenant/principal validation, claims, silent acquisition, and reset fencing. |
| `internal/graph` | Graph HTTP transport, safe pagination, checked-token pinning, and shared effective activation-policy normalization. |
| `internal/pim` | Shared domain types for eligibility, assignment source, scope metadata, activation requests, and activation results. |
| `internal/providers/entra` | Eligible Entra directory role discovery, policy preparation, and activation. |
| `internal/providers/azureresources` | Active discovery and activation for eligible Azure Resource roles across management groups, subscriptions, and resource groups. |
| `internal/providers/groups` | Eligible PIM group member/owner discovery, policy preparation, and activation. |
| `internal/activation` | Shared batch activation orchestration, partial success handling, and retry classification. |
| `internal/tui` | Bubble Tea screens, Bubbles components, navigation, selection state, forms, progress, and summaries. |

The TUI depends on provider interfaces rather than concrete Azure clients. All three providers translate API responses into shared `pim.EligibleAssignment` records; Graph authentication is constructed once per application and shared by its providers without changing Azure CLI configuration.

## Authentication and Discovery

At startup, `pim-manager` starts its loading spinner, lists the tenants available through Azure CLI, and enriches missing tenant names from the Azure CLI subscription cache. If the user is not signed in, the TUI shows a clear message with the exact `az login` command to run, provides a retry action, and exits only if the user chooses to quit. One tenant is selected automatically. A tenant menu appears only when more than one tenant is available, and the choice does not run `az account set` or change Azure CLI configuration.

After selecting a tenant, start a background Microsoft Graph `/v1.0/me?$select=id,displayName,userPrincipalName` lookup using a tenant-scoped Azure CLI Graph token. Bound Azure CLI startup, token acquisition, and HTTP together to fifteen seconds. Keep navigation and ARM discovery usable while checking. Report `Checking account`, `Account found`, `Account unverified`, or `Account unavailable` rather than claiming connectivity from tenant selection alone. Display the found account identity and retain the failing stage for inconclusive checks. Self-profile access uses basic `User.Read` and does not establish Graph PIM authorization.

Retain conclusive account status for at most five minutes and coalesce concurrent checks. Inconclusive errors are not fresh results: the next assignment-list entry or empty-discovery result may check again. Tenant selection, returning to cached assignments with stale status, and empty discovery with stale status trigger a background lookup; explicit `r` refreshes status, tenant membership, and the current workflow. Cancel checks on tenant changes and reject results from older tenant, principal, or request generations. No continuous polling or automatic interactive login is added.

Definite missing/disabled-account or unusable-sign-in errors invalidate the affected tenant's cached workflow and prevent activation. Keep a persistent warning with `az login --tenant <tenant-id>` and an `r` recovery path. Timeout, network, consent, and permission errors leave health unverified and do not block otherwise-valid ARM activation. Before submission, compare a known account principal with the checked ARM principal and clear stale selections on drift. Preserve existing checked-token pinning and step-up protections. Discovery errors must not also render an empty-eligibility message; successful empty discovery includes the account-check status and tenant context.

After tenant selection, Azure Resources can discover immediately. Entra Roles and Groups require the shared Graph connection before discovery. Each provider returns normalized assignment metadata and prepares effective duration, justification, MFA, and authentication-context requirements before activation. Graph discovery uses current-user eligibility queries and preserves source-specific schedule identifiers and activation targets. Discovery errors remain inside the relevant section without leaving the TUI or being mislabeled as empty eligibility.

## TUI Screen Model

The Bubble Tea app uses a simple stack-like screen flow:

| Screen | Purpose |
| --- | --- |
| Tenant selection | List Azure CLI tenants; auto-select one tenant or require a choice when multiple tenants are available. |
| Home | Choose Entra Roles, Azure Resources, or Groups; show the selected tenant. |
| Graph connection | Explain the native browser login and public client branding; explicitly connect, cancel, or scroll actionable errors. |
| Assignment list | Fetch and render eligible assignments for the selected section; support search, filter, inspect, and multi-select. |
| Assignment details | Scroll through source, scope, assignment identifiers, policy hints, and activation constraints without displacing the activity dock or controls. |
| Activation form | Enter shared justification and per-assignment durations. |
| Confirmation | Review selected assignments and activation inputs before submitting. |
| Progress | Show the current assignment, completed/total requests, and per-assignment outcomes as the batch runs. |
| Summary | Show activated, pending approval, pending provisioning, and failed results; allow manual retry for retryable failures. |

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

Activation emits individual started/result updates before moving to the next assignment. Live counts and summaries distinguish activated, pending approval, pending provisioning, and failed outcomes; individual failures do not stop the remaining batch. Manual retry counts describe only the submitted retry subset. Intermediate results remain guarded by originating tenant and workflow generation. Step-up completion is similarly scoped before updating screen or activity state.

Activity messages use allowlisted operation names, counts, and sanitized assignment labels. They exclude tokens, claims, justification, full request/response payloads, and raw errors; detailed diagnostics remain on existing error and summary surfaces. Tenant or principal workflow resets clear activity and operation clocks. The history is neither persisted nor presented as an audit trail.

## Activation Behavior

The activation wizard creates one request per assignment using shared justification and that assignment's duration. Before submission or retry, check normalized effective policies and the selected tenant/principal. Azure Resources reuses the exact checked ARM token for MFA-only requirements and invokes Azure CLI step-up only for a missing enabled authentication context. Graph validates the required claims locally and uses native browser step-up when needed. Conflicting contexts must be activated separately; canceled or failed verification submits nothing.

Pin the exact checked token to the entire batch, and bind queued submissions to a cancelable workflow context. Quitting or resetting the tenant cancels work before submission; responses from older generations are ignored. Closing the app cannot undo a request already accepted by Microsoft.

Supported result states:

| State | Meaning |
| --- | --- |
| `activated` | The assignment became active successfully. |
| `pending_approval` | The request was submitted and is waiting for approval. |
| `pending_provisioning` | The request was accepted and awaits provisioning; do not retry it as a failure. |
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

Discovery and policy errors are isolated to the selected section; activation errors are isolated to the relevant assignment. The UI preserves enough detail to retry, change input, reconnect, sign in again, or resolve a tenant policy/access issue. Authentication errors expose actionable codes and correlation identifiers without credentials or raw token payloads.

## Testing Strategy

Testing should focus on separable logic and avoid requiring live Azure access for normal test runs.

| Area | Test approach |
| --- | --- |
| Cobra command startup | Unit tests for default command behavior and error propagation. |
| Azure auth wrapper | Unit tests around tenant-list parsing, tenant-name enrichment, tenant-scoped token commands, token claims, and exact MFA and authentication-context command construction without executing Azure CLI. |
| Native Graph authentication | Offline MSAL-boundary tests for consent scope/claims validation, session reuse, tenant/principal mismatch, canceled/stale callers, silent failure, safe diagnostics, and login hints that preserve PKCE/state. |
| PIM domain normalization | Unit tests for all providers, inherited effective rules, pagination, activation targeting, durations, MFA/authentication contexts, and fail-closed unsupported policies. |
| Batch activation | Unit tests for partial success, pending approval/provisioning, failures, cancellation, and retry eligibility. |
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
