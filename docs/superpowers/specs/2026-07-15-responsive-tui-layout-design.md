# Responsive TUI Layout Design

## Goal

Make the terminal UI use available width and height while keeping assignment rows compact and readable.

## Scope labels

Azure resource scope types use compact labels wherever a scope is rendered:

| Scope type | Label |
| --- | --- |
| Management Group | `MG` |
| Subscription | `Sub` |
| Resource Group | `RG` |

The scope display name remains unchanged. Other scope types retain their current labels.

## Responsive sizing

The application frame uses the terminal width minus the existing outer margin, without the current 104-column maximum. The existing minimum width remains to preserve the one-line layout.

The assignment list uses the available terminal height minus its fixed content and the actual rendered assignment-footer height. It retains the existing four-row minimum but removes the current 12-row maximum. The minimum supported assignment screen is 80 columns by 26 rows; wrapped footer lines reduce visible assignments instead of overflowing the frame.

`tea.WindowSizeMsg` remains the single resize input. Existing component resizing continues to update text inputs and the summary viewport.

## Assignment columns

Assignment rows remain one line at all supported widths. The role column receives approximately 40 percent of the available table width; the scope column receives the remainder. Both columns retain minimum widths and truncate overflowing text with an ellipsis.

Column widths reserve the active card's border, padding, cursor, checkbox, and separators so fully occupied rows remain one line.

This favors the usually longer Azure scope names while preserving enough room for role names such as `User Access Administrator`.

## Non-goals

- Multi-line or stacked assignment rows.
- Content-measured column widths.
- A configurable layout or sizing policy.
- Provider or domain model changes.

## Verification

Unit tests cover:

- `MG`, `Sub`, and `RG` scope rendering.
- Frame width growth beyond 104 columns.
- Assignment row growth beyond 12 rows in tall terminals.
- Proportional role and scope column sizing.
- Live recalculation after `tea.WindowSizeMsg`.
- Assignment-view height at the minimum supported `80x26` terminal.

A terminal smoke check exercises the assignments screen at narrow and wide window sizes and confirms rows remain within the frame with correct truncation.
