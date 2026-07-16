# PIM MFA Activation Design

## Overview

`pim-manager` will support Azure Resource role activations whose effective PIM policy explicitly enables `MultiFactorAuthentication`. It will continue to use the user's Azure CLI identity and will not implement a custom Microsoft Entra login flow.

MFA is conditional. The application must not request MFA unless at least one assignment selected for activation has the `MultiFactorAuthentication` enablement rule.

## Goals

- Detect the standard PIM `MultiFactorAuthentication` activation requirement from each Azure Resource role's effective policy.
- Show the requirement in assignment details and activation confirmation.
- Before submitting a batch that contains an MFA-required assignment, use an interactive Azure CLI login to let Microsoft Entra perform MFA through the user's configured method, including phone-based methods.
- Submit the batch only after MFA succeeds.
- Preserve the existing activation flow for batches that do not require MFA.

## Non-Goals

- Conditional Access authentication-context rules or authentication strengths.
- A custom browser, device-code, phone, or Microsoft Authenticator flow.
- Automatic retry after an MFA or activation failure.
- Entra Role or Privileged Access Group activation, which remain paused by the current product design.
- Inspecting or validating JWT `amr` claims inside `pim-manager`.

## Policy Model

`pim.ActivationPolicy` gains an `MFARequired` boolean. The Azure Resources provider sets it only when the matched role's `Enablement_EndUser_Assignment` effective rule contains `MultiFactorAuthentication`, using the same case-insensitive matching already used for `Justification`.

Absence of that explicit rule means `MFARequired` is false. No tenant-wide assumption or role-name heuristic may trigger MFA.

## User Flow

1. Discovery loads eligible Azure Resource assignments and their effective activation policies.
2. Assignment details identify MFA as `Required` or `Not required`.
3. Confirmation states when the selected batch requires MFA.
4. On confirmation:
   - If no selected assignment has `MFARequired`, activation starts immediately as it does today.
   - If one or more selected assignments have `MFARequired`, the TUI temporarily relinquishes the terminal and runs one interactive Azure CLI MFA command for the batch.
5. Azure CLI opens its normal browser-based flow or falls back to device-code login. Microsoft Entra controls whether and how the user's phone is challenged.
6. A successful login resumes the TUI and starts the unchanged activation batch.
7. A canceled or failed login returns to confirmation, displays an actionable error, and sends no activation requests.

A mixed batch containing ordinary and MFA-required assignments performs one MFA step before any assignment is submitted. Microsoft documents that users might not be prompted again when MFA was already satisfied in the session; that decision remains with Microsoft Entra.

## Azure CLI Command

The application uses the current account's tenant ID and Azure Resource Manager scope:

```text
az login --tenant <tenant-id> --scope https://management.core.windows.net//.default --claims-challenge <base64-encoded-MFA-claims-request> --output none
```

The claims request is exactly `{"access_token":{"amr":{"essential":true,"values":["mfa"]}}}` before standard base64 encoding. It asks Microsoft Entra for an ARM access token that records completed MFA, as required by Azure CLI's `--claims-challenge` argument. The application invokes the command interactively through Bubble Tea's external-process support so browser and device-code prompts remain usable.

The command does not log out first. Successful authentication refreshes Azure CLI's cached account tokens, which the existing `az account get-access-token` integration then uses for ARM requests.

## Components

### `internal/pim`

Add `MFARequired` to the shared activation policy. No activation request or result shape changes.

### `internal/providers/azureresources`

Map `MultiFactorAuthentication` from `Enablement_EndUser_Assignment` into `MFARequired`. The provider remains responsible for Azure policy response normalization.

### `internal/azureauth`

Construct the interactive Azure CLI command for the current tenant, ARM scope, and MFA claims request. Keep account discovery and access-token retrieval unchanged.

### `internal/app`

Wire the Azure CLI MFA command factory into the TUI runtime alongside the existing account and Azure Resources provider dependencies.

### `internal/tui`

Display MFA policy information, decide whether the selected batch requires MFA, execute the interactive command once, and gate activation on its result. Domain policy detection remains outside view rendering.

## Error Handling

- Azure CLI launch, cancellation, or nonzero exit: return to confirmation with the command error; do not submit any assignment.
- Missing tenant ID: treat MFA preparation as failed and do not submit any assignment.
- ARM still returns `RoleAssignmentRequestPolicyValidationFailed` with `MfaRule`: preserve the full Azure error in that assignment's failed result and do not mark it retryable.
- An MFA failure never stops or alters a batch because the batch has not started yet.
- Existing per-assignment isolation remains unchanged after successful MFA.

## Testing

Normal tests require no Azure access.

- Azure Resources policy tests prove only an explicit `MultiFactorAuthentication` enablement sets `MFARequired`.
- Azure auth tests prove the tenant, ARM scope, and encoded claims challenge passed to `az login`.
- TUI model tests prove:
  - ordinary batches start without MFA;
  - a mixed batch requests MFA exactly once before activation;
  - MFA success starts the full batch;
  - MFA failure returns to confirmation and calls no provider activation;
  - details and confirmation show the requirement.
- Existing provider and batch tests continue to cover activation result isolation and non-retryable policy failures.

## Source Constraints

- [Activate Azure resource roles in PIM](https://learn.microsoft.com/entra/id-governance/privileged-identity-management/pim-resource-roles-activate-your-roles#activate-a-role) documents identity verification before activating an MFA-required role.
- [Configure Azure resource role settings](https://learn.microsoft.com/entra/id-governance/privileged-identity-management/pim-resource-roles-configure-role-settings#role-settings) documents `MultiFactorAuthentication`, session reuse, and authentication-context behavior excluded from this change.
- [PIM REST common errors](https://learn.microsoft.com/rest/api/authorization/includes/privileged-role-common-errors) documents `MfaRule` when MFA has not been completed.
- [Azure CLI MFA troubleshooting](https://learn.microsoft.com/cli/azure/use-azure-cli-successfully-troubleshooting#troubleshooting-multifactor-authentication-mfa) documents interactive `az login` with ARM scope and `--claims-challenge`.
- [Claims challenge format](https://learn.microsoft.com/entra/identity-platform/claims-challenge#claims-challenge-header-format) defines the base64-encoded access-token claims request accepted by Azure CLI.
