# Tables

Version 1. This standard covers every records table: Activity, Sources,
account ledgers, account lists, reconciliation and imports.

## Principle

**Every column has its own width, and the person controls it.** This is the
model data grids share (spreadsheets, database table editors, issue
trackers). A column starts at the width its kind gives it. Resizing a column
changes that column only; no other column moves. When the columns are wider
than the table, it scrolls sideways. When they are narrower, the space after
the last column stays empty. Widths never change because the window did.

## Column kinds

Every column declares one kind. The kind sets where the column starts and how
narrow it can go.

| Kind | Starts at | Minimum | Resizable | Alignment | Holds |
| -- | -- | -- | -- | -- | -- |
| Text | 240px, or its own starting width | 120px | Yes | Start | Names, categories, descriptions, accounts |
| Date | 112px | 88px | Yes | Start | A date or a short period |
| Money | 160px | 96px | Yes | End, with tabular digits | Amounts, balances, debits, credits |
| Status | 128px | 96px | Yes | Start | A type, state or short label |
| Control | The control's own width | Same | No | Center | A checkbox, button or row action |

The starting widths hold the longest usual value at the interface type size:
"Sep 30, 2024" for dates and "$52,069,635.66" with its direction arrow for
money. The first column also holds the row selection checkbox when the table
has selection; it widens by 32px for it.

A text column may give its own starting width, and a larger minimum when its
content is unreadable below it.

## Resizing

* Drag the edge of a column heading. Only that column changes.
* Double-click the edge to return that column to its starting width.
* With the edge focused, the arrow keys resize in 8px steps (32px with Shift).
* A resized width is kept for that person, Book and table.
* The column menu offers "Fit to width", which shares the table's width
  across the columns once, and "Reset column widths", which returns every
  column to its starting width.
* When columns continue past the visible edge, a fade on the trailing edge
  shows it.

## Text in cells

* One line per cell. Text that doesn't fit ends with "…", and the full text
  appears on hover.
* Headers follow the same rule and share their column's alignment.
* A secondary column may hide when the table is narrow. The row's primary text
  column never hides.

## Enforcement

`scripts/check-table-standard.mjs` runs with the design checks. It requires a
kind on every column, rejects widths on date, money and status columns, and
rejects text minimums below 120px. The kind widths live in one place, the
records table's column kinds, and the check confirms they match this document.
