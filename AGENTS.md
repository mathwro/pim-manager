# AGENTS.md

Guidance for AI coding agents working on `pim-manager`.

## Project Purpose

`pim-manager` is a Go CLI for discovering and activating Microsoft PIM eligibilities through an interactive terminal UI. It uses Cobra for CLI structure, Bubble Tea for the TUI runtime, and Bubbles for reusable TUI components.

The product should use the user's existing Azure CLI authentication. Do not add a custom login flow unless the design is explicitly changed.

## Current Design Source

Use `docs/superpowers/specs/2026-07-14-pim-manager-design.md` as the current product/design source of truth.

## MVP Scope

Running `pim-manager` with no arguments should open the interactive TUI.

The MVP supports three top-level PIM areas, matching the Azure portal model:

| Section | Scope |
| --- | --- |
| Entra Roles | Eligible Microsoft Entra directory role activations. |
| Azure Resources | Eligible Azure RBAC activations across management groups, subscriptions, and resource groups. |
| Groups | Eligible Privileged Access Group member and owner activations. |

Each section should support discovery, search/filtering, multi-select, optional detail inspection, shared justification and duration input, confirmation, batch activation, progress reporting, and a per-assignment summary.

## Non-Goals for MVP

Do not implement these unless the design changes:

- Non-interactive scripting commands.
- Persistent config files.
- Custom browser/device-code login flows.
- Background scheduling or automatic activation.
- Approval management after a request is submitted.
- Cross-section mixed activation batches.

## Architecture Expectations

Keep packages small and purpose-specific:

| Package | Responsibility |
| --- | --- |
| `cmd` | Cobra commands, root command setup, flags, and process exit behavior. |
| `internal/app` | Application wiring for auth, providers, activation services, and the TUI model. |
| `internal/azureauth` | Azure CLI login validation and access token retrieval for Microsoft Graph and Azure Resource Manager. |
| `internal/pim` | Shared domain types for eligibility, assignment source, scope metadata, activation requests, and activation results. |
| `internal/providers/entra` | Discovery and activation for eligible Microsoft Entra directory roles. |
| `internal/providers/azureresources` | Discovery and activation for eligible Azure Resource roles. |
| `internal/providers/groups` | Discovery and activation for eligible Privileged Access Group member and owner assignments. |
| `internal/activation` | Batch activation orchestration, partial success handling, and retry classification. |
| `internal/tui` | Bubble Tea screens, Bubbles components, navigation, selection state, forms, progress, and summaries. |

The TUI should depend on interfaces rather than concrete Azure clients. Provider packages should translate Azure API responses into shared `pim.EligibleAssignment` values.

## Behavior Requirements

- Azure CLI login state is validated on startup.
- If the user is not signed in, show the exact `az login` command and provide a retry path.
- Discovery failures are isolated to the relevant top-level section.
- Activation failures are isolated to the relevant assignment.
- Batch activation continues after individual failures.
- Final summaries distinguish `activated`, `pending_approval`, and `failed`.
- Manual retry is allowed only for retryable failures; do not auto-retry activations.
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
- Azure CLI command/token parsing with mocked command execution.
- Provider response normalization into shared PIM domain records.
- Batch activation partial success, pending approval, failures, and retry eligibility.
- Bubble Tea model updates for navigation, selection, form validation, and summaries.

Live Azure integration checks should be optional and gated behind explicit environment variables or commands.

## Code Style

- Follow idiomatic Go.
- Keep domain logic out of Bubble Tea view rendering.
- Keep Azure API-specific response handling inside provider packages.
- Prefer explicit errors over broad catch-all behavior.
- Avoid introducing global mutable state for auth, providers, or TUI models.
- Do not swallow Azure/PIM errors; surface them in a user-actionable way.
