# Pause Graph PIM Sections Design

## Goal

Prevent unusable Entra Roles and Groups workflows from exposing raw Microsoft Graph permission failures while retaining the working Azure Resources workflow.

## Root cause

Azure CLI authenticates users through Microsoft's fixed public client `04b07795-8ddb-461a-bbee-02f9e1bf7b46`. Its Graph token does not include the delegated PIM scopes required by the supported APIs:

- Entra discovery: `RoleEligibilitySchedule.Read.Directory`.
- Entra activation: `RoleAssignmentSchedule.ReadWrite.Directory`.
- Groups discovery: `PrivilegedEligibilitySchedule.Read.AzureADGroup`.
- Groups activation: `PrivilegedAssignmentSchedule.ReadWrite.AzureADGroup`.

Explicit scope acquisition fails with `AADSTS65002` because Microsoft first-party applications require Microsoft-managed preauthorization. Azure CLI does not support a custom client ID.

Older APIs do not provide a complete fallback:

- `/beta/privilegedAccess/aadroles` can still read eligibilities but activation lacks `PrivilegedAccess.ReadWrite.AzureAD` and the API is scheduled to stop returning data on October 28, 2026.
- `/beta/privilegedAccess/aadgroups` lacks the required Azure AD Group delegated permission.
- `api.azrbac.mspim.azure.com/api/v2` now returns `401 BadRequest` for Entra Roles and Groups. The older v1 path also fails.

## Product behavior

The home screen exposes only **Azure Resources** as a selectable PIM section. It includes a muted note explaining that Entra Roles and Groups are temporarily paused because Azure CLI cannot obtain the required Microsoft Graph PIM permissions.

Opening the only section continues through the existing Azure Resources discovery and activation flow. The app does not call Graph PIM endpoints during normal use.

The Entra and Groups provider packages remain in the repository but are not wired into the runtime. This is a temporary capability pause, not a provider deletion.

## Tracking and re-enable criteria

Track these upstream issues:

- [Azure CLI #22775](https://github.com/Azure/azure-cli/issues/22775): support custom client IDs and the fixed client's limited Graph permissions.
- [Azure CLI #28854](https://github.com/Azure/azure-cli/issues/28854): Azure CLI access to PIM Entra/Groups APIs.
- [az-pim-cli #121](https://github.com/netr0m/az-pim-cli/issues/121): May 2026 regression of the legacy private PIM endpoint.

Re-enable Entra Roles and Groups only when one of these conditions is met:

1. Azure CLI can obtain all four supported delegated Graph PIM permissions; or
2. The product design explicitly adopts a dedicated application registration and Graph login.

## Error handling

No raw Graph permission error should be reachable from the home workflow while the sections are paused. Azure Resources retains its existing section-specific errors.

## Testing

Unit tests verify:

- The model exposes only Azure Resources in its selectable section list.
- Azure Resources is selected by default.
- The home view explains why Entra Roles and Groups are paused.
- Pressing Enter from Home starts Azure Resources discovery and does not invoke Entra or Groups providers.

The full Go test suite and build remain free of live Azure dependencies.

## Non-goals

- Calling deprecated Graph beta PIM APIs.
- Calling the private `api.azrbac.mspim.azure.com` API.
- Adding a custom Graph login or app registration.
- Deleting dormant Entra and Groups provider implementations.
