# Policy-Aware Azure Activations Design

## Goal

Make Azure Resource PIM activation policy-aware before the user submits a request:

- Show which eligible assignments are already active.
- Prevent already-active assignments from being selected for activation.
- Default every selected assignment to its own maximum allowed activation duration.
- Require justification only when at least one selected assignment's effective policy requires it.
- Make active state, duration limits, and justification requirements explicit in the TUI.

This work is limited to the active Azure Resources workflow. Entra Roles and Groups remain paused.

## Official API Sources

The design uses the supported Azure Resource Manager Authorization API version `2020-10-01`:

- [Role Assignment Schedule Instances - List For Scope](https://learn.microsoft.com/en-us/rest/api/authorization/role-assignment-schedule-instances/list-for-scope?view=rest-authorization-2020-10-01)
- [Role Management Policy Assignments - List For Scope](https://learn.microsoft.com/en-us/rest/api/authorization/role-management-policy-assignments/list-for-scope?view=rest-authorization-2020-10-01)
- [Role Management Policies - List For Scope](https://learn.microsoft.com/en-us/rest/api/authorization/role-management-policies/list-for-scope?view=rest-authorization-2020-10-01)
- [Privileged Identity Management APIs](https://learn.microsoft.com/en-us/entra/id-governance/privileged-identity-management/pim-apis)

The policy-assignment response exposes `effectiveRules`, including the end-user assignment rules that govern self-activation:

- `Expiration_EndUser_Assignment.maximumDuration`
- `Enablement_EndUser_Assignment.enabledRules`, where `Justification` means a justification is required

## Architecture

The Azure Resources provider remains the single boundary that understands ARM PIM schemas. Its existing `Discover` method returns fully enriched `pim.EligibleAssignment` values. The TUI does not call ARM policy APIs or interpret policy rule IDs.

No provider interface expansion, authentication change, dependency, or new Azure context enumeration is required.

### Domain metadata

Add a small activation-policy value to the shared PIM domain:

```go
type ActivationPolicy struct {
    MaximumDurationISO    string
    JustificationRequired bool
}
```

`pim.EligibleAssignment` additionally records:

- `Active`: whether its eligibility currently has an active linked assignment.
- `ActiveUntil`: the active schedule's optional end time.
- `ActivationPolicy`: the effective self-activation policy for that assignment.

`ActivationRequest` remains assignment, justification, and duration. The TUI supplies the assignment-specific duration when constructing each request.

## Azure Discovery Flow

`internal/providers/azureresources.Provider.Discover` performs the following steps:

1. List current-user eligibilities once from the tenant-level ARM extension endpoint:

   ```text
   /providers/Microsoft.Authorization/roleEligibilityScheduleInstances?$filter=asTarget()
   ```

2. List current-user active assignment schedule instances once:

   ```text
   /providers/Microsoft.Authorization/roleAssignmentScheduleInstances?$filter=asTarget()
   ```

3. Match active instances to eligibilities by normalized, case-insensitive `linkedRoleEligibilityScheduleId` and `roleEligibilityScheduleId`.
4. Group the returned eligibilities by their actual ARM scope.
5. For each unique represented scope, list `roleManagementPolicyAssignments` once. Do not enumerate subscriptions, management groups, resource groups, or unrelated contexts.
6. Match policy assignments to eligibilities by normalized, case-insensitive role-definition ID.
7. Read `effectiveRules` for the end-user assignment expiration and enablement rules.
8. Return the eligibilities enriched with active state and effective policy metadata.

All list operations follow `nextLink` pagination.

### Active-state semantics

An eligibility is active when a linked schedule instance:

- has `assignmentType` equal to `Activated`;
- links to the eligibility schedule;
- has started or has no start time;
- has not ended or has no end time; and
- is not in a terminal inactive state such as denied, revoked, canceled, failed, timed out, or invalid.

The provider records `endDateTime` when present. Active schedule instances that are upcoming or expired do not mark an eligibility active.

### Policy semantics

For every eligibility:

- `MaximumDurationISO` comes from `Expiration_EndUser_Assignment.maximumDuration` in the matching policy assignment's `effectiveRules`.
- `JustificationRequired` is true only when `Enablement_EndUser_Assignment.enabledRules` contains `Justification`, compared case-insensitively.

Effective rules are authoritative because they include inherited/default settings. Raw policy `rules` are not used.

A missing policy assignment, missing maximum-duration rule, malformed required metadata, active-instance lookup failure, or policy lookup failure fails Azure Resources discovery with an error that identifies the affected role and scope where possible. The app must not guess `PT1H`, assume justification is optional, or permit duplicate activation when active state is unknown.

## Assignment List

Add a fixed-width state column while retaining role and scope columns:

```text
STATE    ROLE                  SCOPE
[ ]      Owner                 MG: Tenant Root Group
[✓]      Contributor           Sub: Development
ACTIVE   Reader                RG: Production
```

Behavior:

- `ACTIVE` is visually distinct from selectable and selected states.
- Active rows remain focusable and searchable.
- Space does not select an active row.
- Select-all skips active rows.
- Active rows remain available to the details screen.
- The details screen shows active state and active-until time when present.
- The count line distinguishes selected, eligible, and active assignments.
- Search includes the active state term.

The state column has a stable width so selected and active markers do not disturb row highlighting or column alignment at supported terminal widths.

## Activation Form

### Shared justification

Keep one justification textarea for the batch.

- If any selected assignment requires justification, label the field `Justification — REQUIRED` and state how many selected assignments require it.
- Otherwise label it `Justification — optional`.
- An empty justification is valid only when every selected assignment makes it optional.
- A non-empty optional justification is sent with every activation request.
- Returning from confirmation preserves the entered text.

### Per-assignment durations

Replace the single shared duration input with a compact, scrollable list of selected assignments:

```text
DURATIONS
> Contributor   PT8H   max PT8H
  Owner         PT4H   max PT4H
```

Behavior:

- Each selected assignment owns one editable duration value.
- A value is initialized to that assignment's `MaximumDurationISO` when the assignment first enters the form.
- Different assignments can therefore default to different full durations.
- Returning from confirmation preserves edits.
- Returning to the assignment list preserves values for assignments that remain selected, initializes newly selected assignments, and discards values for assignments no longer selected.
- Tab and Shift+Tab cycle between the justification textarea and duration rows.
- The focused duration row stays within a terminal-height-aware visible window.
- Every duration must be non-empty.
- Azure validates ISO-8601 syntax and maximum-policy violations; per-assignment failures retain the existing actionable ARM error details. No custom duration parser is added.

The activation form remains usable at the minimum supported terminal size. It renders only the duration rows that fit and shows the visible range when rows are omitted.

## Confirmation and Request Construction

The confirmation screen:

- lists each selected assignment with its chosen duration;
- shows the shared justification, or `(none)` when optional and empty;
- retains the existing warning that requests are submitted immediately and never auto-retried.

Batch request construction uses the shared justification and looks up the duration by assignment ID. Each `pim.ActivationRequest` therefore carries the correct assignment-specific duration.

## Error Handling

- Eligibility, active-state, or policy discovery errors remain isolated to Azure Resources.
- Metadata failures prevent the assignment list from presenting unsafe defaults.
- An active row ignores selection input rather than emitting an activation request.
- Required-justification validation names the field and the number of affected assignments.
- Missing duration validation identifies the affected assignment.
- Azure activation failures remain isolated per assignment and batch processing continues.
- Manual retry behavior remains unchanged.

## Testing

All normal tests remain offline and use mocked ARM calls.

### Azure provider

Cover:

- active linkage by eligibility schedule ID;
- current, upcoming, expired, and terminal active schedule instances;
- active-until normalization;
- effective maximum-duration extraction;
- required and optional justification rules;
- mixed roles and scopes with different policies;
- pagination for eligibilities, active instances, and policy assignments;
- one policy call per unique represented scope;
- case-insensitive ARM ID matching;
- actionable active-state and policy metadata failures.

### Domain and assignment state

Cover:

- active assignments cannot toggle;
- select-all skips active assignments;
- selected assignment ordering remains stable;
- policy metadata survives normalization.

### TUI

Cover:

- active state and expiry rendering;
- stable state-column alignment and minimum terminal fit;
- required and optional justification labels;
- empty optional justification accepted;
- empty required justification rejected;
- per-assignment durations initialized from different policy maximums;
- duration edits preserved across form and confirmation navigation;
- assignment-specific durations used in activation requests;
- duration focus and visible-window behavior.

## Non-Goals

- Reactivating Entra Roles or Groups.
- Ticketing, MFA, approval, or authentication-context UI.
- Changing Azure CLI authentication.
- Automatically extending or deactivating active assignments.
- Custom ISO-8601 duration parsing.
- Persistent form values across application restarts.
- Enumerating unrelated Azure scopes or contexts.

## Acceptance Criteria

- The assignment list clearly marks currently active eligibilities and cannot select them.
- Details show active expiry when Azure provides one.
- Every selectable assignment has effective maximum-duration and justification metadata before it is shown.
- The activation form visually distinguishes required and optional justification.
- Empty justification is accepted only when all selected assignments make it optional.
- Every selected assignment defaults to its own maximum duration and can be edited independently.
- Confirmation and submitted requests retain each assignment's chosen duration.
- Discovery uses one eligibility query, one active-instance query, and one policy-assignment query per unique represented scope, with pagination.
- Unit tests require no live Azure access.
