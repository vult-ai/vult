# Toolbars

Version 1. This standard covers the collection toolbar: the band above a
records table or list, on Activity, Sources, Accounts, account ledgers,
reconciliation and imports. The page bar at the top of the window is covered
by the [app shell standard](shell.md).

## Principle

**Nothing in a toolbar is ever cut off or hidden.** When the band is too
narrow for everything, the actions move to a second row. The toolbar never
scrolls sideways, never truncates a view tab, and never lets one control sit
on top of another.

## Anatomy

A toolbar has two groups, in this order.

1. **Identity**, at the start. One of:
   * **Views**: counted tabs such as "All 68,350 · Income 8,026 · Expenses
     50,767". The selected view is the collection's current scope.
   * **Title**: the collection's name and its count, such as "Files 119" or
     "Accounts 212 · USD". The count follows the name in metadata text, with
     no parentheses. Metadata such as the currency follows after a "·".

   Active filters follow the identity as their own short labels, with "Clear
   filters" after them.
2. **Actions**, at the end, always in this order:
   1. Secondary actions (for example, Agent proposals).
   2. Filters and scope selects (Filter, Active, All types).
   3. The one primary action, if the collection has one (Add transaction).
   4. Manage, the collection's overflow menu.
   5. View controls (refresh, expand or collapse all).
   6. The Columns menu, last, when the table has resizable columns.

   A collection has no search field. It is searched from the finder (⌘K),
   and shows that search after its identity ([search standard](search.md)).

## Geometry

* The band is 40px tall on one row. Controls are the small size (32px).
* 8px between controls in a group, 16px between the two groups.
* Text controls keep their labels. A control is icon-only only when it is
  icon-only everywhere (refresh, Columns), and then it has a tooltip and an
  accessible name.

## Narrow widths

1. The identity group keeps its full width.
2. When the actions no longer fit beside it, the actions wrap to a second
   row, aligned to the end. Each row stays 40px tall.
3. Below 768px the actions take a row of their own. On
   phones the identity takes the full first row and view tabs drop their
   counts, so every tab fits.

## Building one

Use `CollectionToolbar` from `design-system/collection-toolbar.tsx`, with the
identity in `lead` and the actions in `actions`. Use `CollectionTitle` for a
title with a count. The records table adds the Columns menu itself.
