# PIM MFA Activation Design

## Overview

`pim-manager` supports Azure Resource role activations whose effective PIM policy requires either standard `MultiFactorAuthentication` or an enabled Conditional Access authentication context. It continues to use the user's Azure CLI identity and does not implement a custom Microsoft Entra login flow.

Step-up authentication is conditional. The application must not request it unless at least one selected assignment explicitly requires MFA or an authentication-context claim.

## Goals

- Detect standard PIM `MultiFactorAuthentication` and enabled `AuthenticationContext_EndUser_Assignment` requirements from each Azure Resource role's effective policy.
- Show the requirement in assignment details and activation confirmation.
- Before submitting a protected batch, use an interactive Azure CLI login with the required MFA or `acrs` claims challenge.
- Submit the batch only after step-up authentication succeeds.
- Preserve the existing activation flow for batches that do not require step-up authentication.

## Non-Goals

- Activating assignments with different authentication contexts in one batch; users must submit them as separate batches.
- A custom browser, device-code, phone, or Microsoft Authenticator flow.
- Automatic retry after an MFA or activation failure.
- Entra Role or Privileged Access Group activation, which remain paused by the current product design.
- Inspecting or validating JWT `amr` claims inside `pim-manager`.

## Policy Model

`pim.ActivationPolicy` records `MFARequired` and `AuthenticationContext`. The Azure Resources provider maps `MultiFactorAuthentication` from `Enablement_EndUser_Assignment` and maps the trimmed `claimValue` only from an enabled `AuthenticationContext_EndUser_Assignment` rule.

Absence of both explicit rules means no step-up is required. No tenant-wide assumption or role-name heuristic may trigger authentication.

## User Flow

1. Discovery loads eligible Azure Resource assignments and their effective activation policies.
2. Assignment details identify standard MFA or the required authentication-context claim.
3. Confirmation states when the selected batch requires step-up authentication.
4. On confirmation:
   - If no selected assignment has an authentication requirement, activation starts immediately.
   - If assignments require standard MFA, the TUI runs one Azure CLI login with an `amr: mfa` claims request.
   - If assignments require one shared authentication context, the TUI runs one Azure CLI login with the required `acrs` claim.
   - If selected assignments require different authentication contexts, activation is blocked with instructions to use separate batches.
5. Azure CLI opens its normal browser-based flow or falls back to device-code login. Microsoft Entra controls whether and how the user is challenged.
6. A successful login resumes the TUI and starts the unchanged batch.
7. A canceled or failed login returns to confirmation, displays an actionable error, and sends no activation requests.

A mixed batch containing ordinary assignments and assignments with one shared authentication requirement performs one step-up before any assignment is submitted. Microsoft Entra may reuse an already-satisfied session instead of prompting again.

## Azure CLI Command

The application uses the current account's tenant ID and Azure Resource Manager scope:

```text
az login --tenant <tenant-id> --scope https://management.core.windows.net//.default --claims-challenge <base64-encoded-claims-request> --output none
```

Standard MFA uses `{"access_token":{"amr":{"essential":true,"values":["mfa"]}}}`. An authentication context such as `c1` uses `{"access_token":{"acrs":{"essential":true,"value":"c1"}}}`. The selected claims request is standard-base64 encoded for Azure CLI. Bubble Tea runs the command as an interactive external process so browser and device-code prompts remain usable.

The command does not log out first. Successful authentication refreshes Azure CLI's cached token for `https://management.core.windows.net/`, and ARM requests retrieve that same resource token before calling `https://management.azure.com`.

## Components

### `internal/pim`

Store `MFARequired` and `AuthenticationContext` on the shared activation policy. No activation request or result shape changes.

### `internal/providers/azureresources`

Map standard MFA and enabled authentication-context effective rules into the shared policy. The provider remains responsible for Azure policy response normalization.

### `internal/azureauth`

Construct the interactive Azure CLI command for either an MFA or authentication-context claims request. Keep account discovery and access-token retrieval unchanged.

### `internal/app`

Wire the Azure CLI step-up command factory into the TUI runtime alongside the existing account and Azure Resources provider dependencies.

### `internal/tui`

Display authentication policy information, enforce one authentication context per batch, execute the interactive command once, and gate activation on its result.

## Error Handling

- Azure CLI launch, cancellation, or nonzero exit: return to confirmation with the command error; submit no assignment.
- Missing tenant ID: fail step-up preparation and submit no assignment.
- Different authentication contexts in one batch: remain on confirmation and require separate batches.
- ARM policy validation errors preserve the full wrapped Azure response and remain non-retryable.
- Existing per-assignment isolation remains unchanged after successful step-up authentication.

## Testing

Normal tests require no Azure access.

- Azure Resources policy tests cover explicit standard MFA, enabled authentication contexts, and absent requirements.
- Azure auth tests cover exact `amr` and `acrs` claims challenges.
- TUI model tests prove:
  - ordinary batches start without step-up;
  - standard MFA and one shared context each gate activation once;
  - conflicting contexts block the complete batch;
  - success starts the full batch;
  - failure returns to confirmation and calls no provider activation;
  - details and confirmation show the requirement.
- Summary tests ensure long Azure errors wrap without losing policy details.

## Source Constraints

- [Activate Azure resource roles in PIM](https://learn.microsoft.com/entra/id-governance/privileged-identity-management/pim-resource-roles-activate-your-roles#activate-a-role) documents identity verification before activating an MFA-required role.
- [Configure Azure resource role settings](https://learn.microsoft.com/entra/id-governance/privileged-identity-management/pim-resource-roles-configure-role-settings#role-settings) documents standard MFA, session reuse, and Conditional Access authentication contexts.
- [PIM REST common errors](https://learn.microsoft.com/rest/api/authorization/includes/privileged-role-common-errors) documents `MfaRule` when MFA has not been completed.
- [Azure CLI MFA troubleshooting](https://learn.microsoft.com/cli/azure/use-azure-cli-successfully-troubleshooting#troubleshooting-multifactor-authentication-mfa) documents interactive `az login` with ARM scope and `--claims-challenge`.
- [Claims challenge format](https://learn.microsoft.com/entra/identity-platform/claims-challenge#claims-challenge-header-format) defines the base64-encoded access-token claims request accepted by Azure CLI.
