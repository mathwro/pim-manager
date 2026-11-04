# AGENTS.md

Guidance for AI coding agents working on `pim-manager`.

## Project Purpose

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities through an interactive terminal UI. It uses Cobra for CLI structure, Bubble Tea for the TUI runtime, and Bubbles for reusable TUI components.

The product uses the user's existing Azure CLI authentication for tenant selection and Azure Resources. Entra Roles and Groups use an explicit, session-only native MSAL browser sign-in through the existing Microsoft Graph Command Line Tools public client. Graph authentication must match the selected tenant and checked CLI principal. ARM authentication-context step-up continues through Azure CLI; Graph policy step-up uses browser authentication. Device-code flow, new app registrations, and persistent credential caches are out of scope.

## Current Design Source

Use `docs/superpowers/specs/2026-07-14-pim-manager-design.md` as the current product source of truth and `docs/superpowers/specs/2026-07-17-tenant-selection-design.md` for tenant-selection behavior.

## MVP Scope

Running `pim-manager` with no arguments should validate the Azure CLI session, select an Azure tenant when multiple tenants are available, and open the interactive TUI.

The MVP supports three top-level PIM areas, matching the Azure portal model:

| Section | Scope |
| --- | --- |
| Entra Roles | Eligible Microsoft Entra directory role activations through Microsoft Graph. |
| Azure Resources | Active: eligible Azure RBAC activations across management groups, subscriptions, and resource groups. |
| Groups | Eligible PIM group member and owner activations through Microsoft Graph. |

All three sections support discovery, search/filtering, multi-select, optional detail inspection, shared justification and per-assignment duration input, confirmation, policy-required step-up authentication, batch activation, progress reporting, and a per-assignment summary. Unresolved or unsupported activation policy requirements block submission with actionable errors.

## Non-Goals for MVP

Do not implement these unless the design changes:

- Non-interactive scripting commands.
- Persistent config files.
- Device-code authentication, new app registrations, or persistent credential caches.
- Background scheduling or automatic activation.
- Approval management after a request is submitted.
- Cross-section mixed activation batches.

## Architecture Expectations

Keep packages small and purpose-specific:

| Package | Responsibility |
| --- | --- |
| `cmd` | Cobra commands, root command setup, flags, and process exit behavior. |
| `internal/app` | Application wiring for auth, providers, activation services, and the TUI model. |
| `internal/azureauth` | Azure CLI tenant discovery and display-name enrichment, session-local tenant context, access token retrieval and claims inspection, and interactive authentication-context step-up command construction. |
| `internal/graphauth` | Native Graph browser PKCE login, in-memory session, checked CLI/Graph identity binding, silent refresh, policy claims, and cancellation/reset fencing. |
| `internal/graph` | Graph HTTP transport, checked-token pinning, safe pagination, and effective activation-policy normalization shared by Graph providers. |
| `internal/pim` | Shared domain types for eligibility, activation policy requirements, scope metadata, activation requests, and activation results. |
| `internal/providers/entra` | Discovery and activation for eligible Microsoft Entra directory roles. |
| `internal/providers/azureresources` | Discovery, effective-policy normalization, and activation for eligible Azure Resource roles. |
| `internal/providers/groups` | Discovery and activation for eligible Privileged Access Group member and owner assignments. |
| `internal/activation` | Batch activation orchestration, partial success handling, and retry classification. |
| `internal/tui` | Bubble Tea tenant and PIM-area selection, screens, navigation, selection state, forms, policy-driven step-up gating, progress, and summaries. |

The TUI should depend on interfaces rather than concrete Azure clients. Provider packages should translate Azure API responses into shared `pim.EligibleAssignment` values.

## Behavior Requirements

- Azure CLI login state is validated on startup.
- If the user is not signed in, show the exact `az login` command and provide a retry path.
- List tenants from the existing Azure CLI session on startup; auto-select one tenant and show a selection menu only when multiple tenants are available.
- Tenant rows show `Display Name (default.domain)` when both values exist and retain the tenant ID for disambiguation; preserve name-only, domain-only, and ID-only fallbacks.
- Tenant selection is session-local. Scope token acquisition, discovery, authentication checks, and activation to the selected tenant without running `az account set` or changing Azure CLI configuration.
- Changing tenants clears tenant-specific workflow state, and stale tenant/discovery results must not cross tenant boundaries.
- Animate the loading spinner during initial tenant discovery and refresh.
- Standard MFA-only Azure Resource activations reuse the exact checked ARM token from the current Azure CLI session; ARM/PIM remains authoritative for MFA enforcement.
- Azure Resource interactive step-up runs only when an enabled Conditional Access authentication context is required and the current ARM token does not contain it.
- A batch may contain at most one authentication context; conflicting contexts must be activated separately.
- Failed or canceled step-up authentication submits no activation requests.
- If authentication-context step-up changes the signed-in principal, submit no activation requests.
- Pin the checked service token (ARM or Graph) to every activation request in its batch; do not fetch a different token between validation and submission.
- For group-derived Azure role eligibilities, preserve the linked group eligibility schedule but use the authenticated user's principal ID as the `SelfActivate` target.
- Disable Azure CLI's subscription selector only for the child step-up process; do not mutate the user's global CLI configuration.
- Connect Graph only after explicit user action; explain Microsoft Graph Command Line Tools branding before opening the browser. Share the session across Entra Roles and Groups, clear it on tenant/principal changes, and reject stale callbacks. Use a known checked CLI username as a login hint without forcing account selection; returned identity checks remain authoritative.
- Before Graph submission and retries, check the selected tenant, CLI/Graph principal, required scopes, and policy claims. Cancel queued submissions on quit or workflow reset; never automatically replay activation requests.
- Discovery failures are isolated to the relevant top-level section.
- Activation failures are isolated to the relevant assignment.
- Batch activation continues after individual failures.
- Final summaries distinguish `activated`, `pending_approval`, `pending_provisioning`, and `failed`; accepted provisioning requests are not retryable failures.
- Manual retry is allowed only for retryable failures; do not auto-retry activations.
- Manual retries must repeat token, claims, and principal validation before submitting activation requests.
- Preserve actionable Azure/PIM error details where possible.

## TUI Requirements

The TUI should be keyboard-first.

| Key | Action |
| --- | --- |
| Arrow keys or `j`/`k` | Navigate. |
| `/` | Search. |
| Space | Toggle selection. |
| Enter | Advance or confirm. |
| Esc | Go back. |
| `?` | Show contextual help. |

Use Bubbles components where appropriate: list or table for assignments, text input or textarea for activation fields, spinner or progress for activation progress, and viewport for long summaries.

## Testing Expectations

Prefer unit tests around isolated logic. Normal test runs should not require live Azure access.

Cover:

- Cobra startup behavior and error propagation.
- Azure CLI tenant/account parsing and display-name enrichment, tenant-scoped token commands, ARM token claims inspection, MFA-only token reuse, `acrs` claims construction, child-process environment, and actionable command errors with no live login.
- Native Graph consent/session reuse, tenant/principal mismatch, browser claims, canceled and stale operations, silent refresh failure, secret-safe errors, and account chooser hints without live login.
- Provider response and effective activation-policy normalization into shared PIM domain records.
- Batch activation partial success, pending approval, failures, and retry eligibility.
- Bubble Tea model updates for one/multiple tenant startup, tenant label fallbacks, startup spinner ticks, tenant switching and stale-result rejection, navigation, selection, form validation, token pinning, direct and group-derived principal targeting, step-up principal drift, canceled or failed authentication, retry gating, context conflicts, wrapped Azure errors, and summaries.

Live Azure integration checks should be optional and gated behind explicit environment variables or commands.

## Code Style

- Follow idiomatic Go.
- Keep domain logic out of Bubble Tea view rendering.
- Keep Azure API-specific response handling inside provider packages.
- Prefer explicit errors over broad catch-all behavior.
- Avoid introducing global mutable state for auth, providers, or TUI models.
- Do not swallow Azure/PIM errors; surface them in a user-actionable way.
