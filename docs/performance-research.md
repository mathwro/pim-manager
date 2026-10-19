# Performance research: startup and interactive flow

Research recorded 2026-09-24. This is an investigation, not a change to the product design or its authentication requirements. The existing [PIM manager design](superpowers/specs/2026-07-14-pim-manager-design.md) and [tenant selection design](superpowers/specs/2026-07-17-tenant-selection-design.md) remain authoritative.

## What was measured

Measurements used a Linux/arm64 WSL workstation invoking a Windows Azure CLI installation. They are component timings, **not** end-to-end startup timings or representative Azure service latencies.

| Operation | Three observed runs | Interpretation |
| --- | --- | --- |
| Compiled `pim-manager --version` | 161, 169, 177 ms | Go/Cobra process-start baseline; does not launch the TUI or authenticate. |
| `az account list --all --output none` | 801, 735, 762 ms | One Azure CLI command on this workstation; not a measurement of tenant REST or token acquisition. |

An isolated interactive smoke run used a fake `az` executable returning one tenant. The TUI displayed the home screen with that tenant and exited normally. Focused fake-auth and provider tests covered concurrent tenant lookups, progressive policy display, cache reuse, and the four-request active-lookup limit. No live tenant REST, token, or ARM discovery call was timed.

A temporary benchmark of `Model.View()` with a populated assignment list produced these two-run ranges:

| Assignments | Query | Time per render | Bytes allocated per render |
| ---: | --- | ---: | ---: |
| 1,000 | Empty | 0.271 ms | ~82 KB |
| 1,000 | `Reader` | 0.999–1.252 ms | ~1.27 MB |
| 10,000 | `Reader` | 5.98–6.14 ms | ~17 MB |

The benchmark fixture contained matching assignments and rendered a 96-column, 30-row view. It was removed after measurement; these numbers describe this workstation, not a latency target.

## In-process improvements (no Azure API changes)

The most concrete local improvement is assignment-list interaction, not startup. Additional disposable benchmarks isolated Go work from Azure CLI and HTTP. Each range is two runs on the workstation above; fixtures used matching search results and prebuilt policy responses.

| Local operation | Fixture | Time per operation | Allocated per operation |
| --- | --- | ---: | ---: |
| Construct model and render home | No provider; no terminal I/O | 0.296–0.330 ms | ~329 KB |
| Press `j` in a searched list, then render | 1,000 assignments | 1.59–1.81 ms | ~2.48 MB |
| Press `j` in a searched list, then render | 10,000 assignments | 11.54–12.88 ms | ~33.9 MB |
| Normalize policies without HTTP | 100 assignments and 100 distinct policies in one scope | 0.277–0.318 ms | ~9 KB |
| Normalize policies without HTTP | 1,000 assignments and 1,000 distinct policies in one scope | 22.24–23.36 ms | ~83.5 KB |
| Schedule no-op scoped work | 1,000 scopes | 1.73–1.95 ms | ~85 KB |
| Schedule no-op scoped work | 10,000 scopes | 29.7–34.3 ms | ~1.34 MB |

**Priority 1 — make search and navigation reuse results.** [`assignmentList.filtered`](../internal/tui/assignments.go#L18-L38) lowercases/concatenates search text and copies full `pim.EligibleAssignment` values into a new slice. One `j` key calls it in [`moveAssignment`](../internal/tui/model.go#L999-L1012), and the subsequent [`View`](../internal/tui/view.go#L162-L192) calls it again; other navigation paths repeat it too. Keep normalized search keys and matching assignment *indices* for the current query. Recompute on query or assignment changes, including refreshed discovery, policy preparation, active-state changes, and tenant switches. Use the indices for navigation and visible-row rendering, retaining selection and cursor behavior. This eliminates repeated full-list matching and large value-copy allocations without changing API traffic. The ~12 ms / 34 MB keystroke at 10,000 items makes this worth testing for large portfolios; the ~1.7 ms result at 1,000 items does not establish a typical-user problem.

**Priority 2 — index policies once per scope.** [`applyPolicies`](../internal/providers/azureresources/metadata.go#L240-L265) calls [`policyForAssignment`](../internal/providers/azureresources/metadata.go#L81-L125) for every assignment, and the latter scans every policy in that scope. Build a map by the existing normalized role-definition name, including duplicate counts, before matching assignments. This changes local matching from approximately `O(assignments × policies)` to `O(assignments + policies)` per scope. Preserve missing-policy, duplicate-policy, and incomplete-rule errors. The 1,000-by-1,000 fixture took ~23 ms; typical policy counts and user-visible impact remain unmeasured.

**Lower priority — bound local scheduler work only for very large scope counts.** [`forEachScope`](../internal/providers/azureresources/metadata.go#L148-L188) starts one goroutine per scope even though only four run requests concurrently. A fixed worker pool would bound scheduler allocations without changing HTTP concurrency, but no-op work at 1,000 scopes took under 2 ms; defer this unless real portfolios are much larger. Model construction and home rendering took under 0.4 ms in isolation, so neither is an evidenced startup hotspot. This does not measure Cobra or OS process startup; the optional background `go list` update process is separate.

## Azure-facing workflow (separate investigation)

1. **Home screen:** The model starts a spinner and tenant lookup, and independently starts a background update check ([`internal/tui/model.go`](../internal/tui/model.go#L219-L230)). Tenant listing launches both `az rest` and `az account list` concurrently, even if REST supplies complete names and the account result will not be used ([`internal/azureauth/auth.go`](../internal/azureauth/auth.go#L82-L183)). The update check in a stable build can launch `go list -m ...@latest` ([`internal/selfupdate/selfupdate.go`](../internal/selfupdate/selfupdate.go#L26-L47)); it is not required to show the tenant screen.
2. **First usable assignment list:** Discovery obtains an ARM token, requests eligibility pages in sequence, then checks active assignments at each distinct scope before returning the list ([`internal/providers/azureresources/provider.go`](../internal/providers/azureresources/provider.go#L69-L110)). Scoped lookups are capped at four simultaneous requests ([`internal/providers/azureresources/metadata.go`](../internal/providers/azureresources/metadata.go#L148-L188)). A user with many distinct scopes can therefore wait for multiple waves of lookups. No live timings establish how much of the wait is network latency.
3. **Activation-ready list:** Only after discovery returns does the model start policy preparation and show assignments with a loading banner ([`internal/tui/model.go`](../internal/tui/model.go#L252-L319)). Preparation starts another ARM token acquisition and fetches effective policies per distinct scope, also capped at four concurrent scopes ([`internal/providers/azureresources/provider.go`](../internal/providers/azureresources/provider.go#L112-L125), [`internal/providers/azureresources/metadata.go`](../internal/providers/azureresources/metadata.go#L240-L265)). `Discover` and `Prepare` each call `PinAccessToken` on separate contexts, so a nonempty discovery normally launches Azure CLI for an ARM token twice ([`internal/arm/client.go`](../internal/arm/client.go#L57-L66)).

Already useful: tenant and metadata commands overlap, scoped HTTP requests have a concurrency limit, assignments appear while policies load, and returning to an unrefreshed section reuses the in-session discovery cache ([`internal/tui/model.go`](../internal/tui/model.go#L819-L852)). Do not remove these behaviors to chase a benchmark.

## Azure-facing follow-up

1. **Measure the real critical path first.** In an explicitly opted-in session, record durations and request counts for tenant REST, metadata lookup, token launches, eligibility pages, active-state scopes, and policy scopes. Compare time to first home screen, usable list, and activation-ready list across one- and multi-tenant accounts. Record p50/p95 across runs and scope counts. Never log token contents, claims, tenant metadata, or assignment details in timing output. This establishes which changes are worth making.
2. **If scoped lookups dominate, overlap work without weakening correctness.** Evaluate starting policy lookups once eligibility scopes are known while active-state checks continue. Preserve the current ability to show assignments early, but do not enable activation until both active state and required policy are known. Keep tenant-switch cancellation, stale-result rejection, and actionable errors. An alternative progressive active-state display would also need to prevent selection/activation of assignments whose active state has not been verified. Measure 429 responses before changing the four-request limit; [Azure Resource Manager throttles reads](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/request-limits-and-throttling).
3. **If Azure CLI token launches dominate, share a token within one discovery operation.** `Discover` and `Prepare` could receive the same tenant-scoped, pinned token, with expiry accounted for if preparation runs long. Do not cache it across tenant changes, sessions, or activation attempts. Activation and manual retry must still perform their own fresh token, claims, and principal validation and pin the exact checked token to requests.
4. **If home-screen CPU or contention dominates, reassess speculative subprocesses.** Avoid starting `az account list` when REST metadata is complete only if measured savings outweigh making incomplete-metadata enrichment sequential. Preserve name/domain fallbacks and errors. Consider deferring the background update check until home is displayed and bounding its runtime; its result is not needed for tenant selection.

Do not substitute `az account tenant list` solely to remove a command: [Microsoft documents this command as an experimental, auto-installing extension](https://learn.microsoft.com/en-us/cli/azure/account/tenant?view=azure-cli-latest). Do not prefetch tenant-specific discovery before tenant selection or bypass checked-token pinning for apparent speed.

## Verification for a future optimization

Use a repeatable, explicitly authorized Azure session to compare the three user-visible milestones and 429/error counts before and after Azure-facing changes. Keep fake-runner tests for tenant enrichment and switching, partial discovery/policy results, cancellation, and token reuse boundaries. For local changes, benchmark `Update` plus `View` for searched navigation at 100, 1,000, and 10,000 assignments, and benchmark policy matching at realistic counts per scope. Preserve search/selection semantics and missing or duplicate policy errors; smoke the real TUI for spinner, search, selection, and activation gating. An offline fake-`az` TUI run can verify the startup surface but cannot establish real Azure latency.
