# Discovery Performance Design

## Overview

Tenant enumeration and Azure Resource eligibility discovery currently serialize Azure CLI processes and broad Azure Resource Manager (ARM) queries. In the measured signed-in environment, tenant enumeration took 0.763 seconds median and Azure Resource discovery took 39.944 seconds for six eligibilities. The application should preserve fresh initial results, activation safety, and Azure error detail while making the list usable as soon as eligibility and active state are known.

## Goals

- Reduce initial tenant and Azure Resource discovery latency using supported Azure CLI and ARM behavior.
- Display list-ready Azure Resource eligibilities before effective activation policies finish loading.
- Preserve correct active-state detection so an already-active eligibility cannot be selected.
- Reuse successful discovery results during the process lifetime until the user explicitly refreshes or activation makes them stale.
- Bound parallel ARM work to avoid unbounded requests and memory use.
- Preserve existing activation authentication, principal validation, token pinning, and retry behavior.

## Non-Goals

- Persistent or cross-process caches.
- Automatic activation retries.
- Undocumented ARM query parameters.
- Changes to dormant Entra Roles or Groups discovery.
- A hard latency SLA for Azure-controlled response times.

## Measured Baseline

Measurements used the signed-in Azure CLI session and read-only ARM requests on 2026-07-17. Identifiers and bearer tokens were not recorded.

| Path | Result |
| --- | ---: |
| Sequential tenant and metadata commands, median of three warm runs | 0.763 s |
| Concurrent tenant and metadata commands, median of three warm runs | 0.534 s |
| Current Azure Resource discovery | 39.944 s |
| Current Azure CLI token acquisition, six calls | 1.308 s |
| Current eligibility request | 0.843 s |
| Current tenant-root active-assignment request | 26.035 s |
| Current four policy requests | 11.730 s |
| Scope-specific active requests, four concurrent scopes | 2.191 s |
| Four concurrent unfiltered policy requests | 3.262 s |
| Two concurrent unfiltered policy requests | 7.874 s |
| Fully optimized probe using an undocumented policy filter | 3.676 s |

The four policy responses contained 3,675 records and 24.0 MB of decoded JSON for six eligibilities. The optimized probe demonstrated the lower bound but its policy `$filter` must not be implemented: the stable ARM specification does not define that parameter and Azure has an open feature request to add it.

Scope-specific active discovery returned the same two linked active eligibility IDs as the tenant-root query. This is the largest supported performance improvement found.

## Tenant Discovery

`azureauth.CLI.Tenants` runs these commands concurrently:

1. `az account tenant list --output json`, which remains authoritative for accessible tenant IDs.
2. `az account list --all --query <metadata-query> --output json`, which reads subscription-cache metadata used only to enrich tenant display names and default domains.

The results retain existing parsing, deduplication, login detection, and metadata error behavior. The current Bubble Tea model already retains successful tenant results for the process lifetime. Returning to the tenant screen uses that state immediately; pressing `r` performs a new concurrent lookup.

## Azure Resource Discovery Pipeline

### List-ready phase

1. Acquire one tenant-scoped ARM token and pin it to the phase context.
2. List all eligibility schedule instances with `asTarget()`, preserving pagination.
3. Normalize eligibilities and group them by normalized Azure scope.
4. Query role assignment schedule instances with `asTarget()` at only those scopes.
5. Run at most four scope requests concurrently. Pagination remains serial within each scope because each next request depends on `nextLink`.
6. Match current active instances by linked eligibility schedule ID.
7. Return list-ready assignments to the TUI.

The tenant-root active-assignment query is removed. An active-state failure blocks the list because displaying unknown assignments as inactive could permit duplicate activation.

### Policy preparation phase

After list-ready assignments are displayed:

1. Acquire and pin one tenant-scoped ARM token for policy preparation.
2. Group assignments by normalized scope.
3. Query the documented, unfiltered `roleManagementPolicyAssignments` endpoint once per unique scope.
4. Run at most four scope requests concurrently and preserve pagination.
5. Normalize each assignment's effective activation policy.
6. Return the fully prepared assignments to the TUI.

A second token acquisition costs approximately 0.23 seconds in the measured environment and avoids storing a bearer token in long-lived TUI cache state. No unsupported policy `$filter` is used.

## TUI State and Caching

Discovery state is held in memory by the Bubble Tea model:

```go
type discoveryKey struct {
    tenantID string
    section  Section
}

type discoveryEntry struct {
    assignments   []pim.EligibleAssignment
    policiesReady bool
    generation    int
}
```

The cache stores only normalized domain assignments. It never stores raw ARM responses, bearer tokens, or errors.

Behavior:

- A successful list-ready result creates or replaces the entry for its tenant and section.
- A successful policy result replaces the assignments and marks the entry ready.
- Re-entering Azure Resources displays the cached assignments immediately.
- If cached policies are incomplete, policy preparation continues.
- Pressing `r` deletes the entry, increments its generation, and starts fresh discovery.
- Messages carry tenant, section, and generation. A late message cannot overwrite a refreshed entry.
- Completing an activation invalidates the current Azure Resources entry because active state may have changed.
- Exiting the process discards all cache state.

Selection and cursor state remain separate from the cached assignment data. Replacing list-ready assignments with prepared assignments preserves selections by assignment ID, the search query, and the focused assignment when it still exists.

## Progressive User Experience

The assignment screen distinguishes these states:

- `Discovering Azure Resources...`: eligibility and active-state discovery is incomplete; assignments are not yet safe to select.
- List visible with `Loading activation requirements...`: assignments are list-ready and selectable while policies load.
- List ready: policies are available and activation can advance normally.

If Enter is pressed while selected assignments are not policy-ready, the TUI waits on a loading state and opens the activation form automatically when preparation succeeds. Assignment details show policy requirements as loading until enrichment completes.

## Error Handling

- Tenant and metadata command errors retain their existing actionable Azure CLI context.
- Eligibility or active-state failure returns no assignment list.
- Policy failure leaves list-ready assignments browsable and preserves selection, but activation cannot proceed with incomplete policy data.
- Policy errors retain the wrapped scope and ARM response details.
- Pressing `r` retries discovery explicitly; no activation retry behavior changes.
- The first fatal phase error cancels sibling and queued requests.
- Stale results are ignored using tenant, section, and generation checks.

## Concurrency Rules

- Tenant discovery: two Azure CLI child processes maximum.
- Eligibility discovery: one paginated request chain.
- Active-state discovery: four scope workers maximum.
- Policy preparation: four scope workers maximum.
- Pagination for one scope is serial.
- Worker orchestration uses the Go standard library; no dependency is added.

The policy worker cap permits the measured 3.262-second completion while bounding simultaneous raw policy payloads to four responses. Normalized results replace and release raw response data after each worker completes.

## Testing Strategy

Unit tests use blocking fakes and request recording rather than wall-clock thresholds.

### Azure authentication

- Both tenant commands begin before either is released.
- Tenant-list remains authoritative.
- Metadata enrichment, deduplication, login errors, cancellation, and refresh behavior remain unchanged.

### Azure Resources provider

- One token is acquired per list-ready or policy-preparation phase.
- Active queries target each normalized eligible scope once and never use the tenant-root endpoint.
- Pagination remains correct for eligibility, active-state, and policy responses.
- Scoped active instances map to the same linked eligibility IDs.
- Policy lookup runs once per normalized scope.
- Concurrency never exceeds four workers.
- First fatal errors cancel queued work and retain scope context.

### TUI

- List-ready assignments display before policy preparation completes.
- Selection, query, and focus survive prepared-assignment replacement.
- Enter waits for policy readiness and then opens the form.
- Policy failure leaves the list available and blocks activation.
- Cache entries are isolated by tenant and section.
- Re-entry reuses cache without provider calls.
- `r`, activation completion, and generation changes invalidate stale data correctly.

### Live verification

After implementation, rerun the read-only live probe and the interactive application against the same signed-in environment. Verify:

- Eligibility count remains six in the measured tenant.
- Relevant active eligibility count remains two.
- Policy normalization succeeds for all six assignments.
- Initial list-ready wall time is materially below the 39.944-second baseline.
- Re-entering Azure Resources uses the session cache immediately.
- `r` performs fresh ARM requests.

Azure response time is externally controlled, so correctness and request shape are hard requirements; elapsed time is comparative evidence rather than a deterministic test assertion.

## References

- [Azure CLI `az account tenant`](https://learn.microsoft.com/cli/azure/account/tenant?view=azure-cli-latest)
- [Azure CLI `az account list`](https://learn.microsoft.com/cli/azure/account?view=azure-cli-latest)
- [Role Assignment Schedule Instances - List For Scope](https://learn.microsoft.com/rest/api/authorization/role-assignment-schedule-instances/list-for-scope?view=rest-authorization-2020-10-01)
- [Role Management Policy Assignments - List For Scope](https://learn.microsoft.com/rest/api/authorization/role-management-policy-assignments/list-for-scope?view=rest-authorization-2020-10-01)
- [Azure REST API Specs issue #35281: add `$filter` support](https://github.com/Azure/azure-rest-api-specs/issues/35281)
