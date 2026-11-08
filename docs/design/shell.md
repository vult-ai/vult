# App shell

Version 1. This standard covers the frame around every page: the sidebar rail,
the page bar at the top of the canvas, the canvas itself and the navigation
drawer on phones. What sits inside the canvas is covered by the page standards
(tables, toolbars, dashboard, menus).

## Principle

**The frame never changes between pages.** Every page sits in the same rail,
page bar and canvas, at the same sizes. A page owns what is inside the canvas
and the page actions slot in the page bar, nothing else. When a page seems to
need a different frame, the page is wrong, not the frame.

## Rail

The rail is one floating panel at the start of the window. It shows the Book's
pages, with Settings at the bottom.

| Rule | Value |
| -- | -- |
| Width, expanded | 256px, including an 8px inset around the panel |
| Width, collapsed | 64px: icons only |
| Header | 56px, the same height as the page bar, holding the Vult mark |
| Groups | Two, without labels, 16px apart |
| Rows | 32px tall, 4px apart, the full width of the panel less 8px padding on each side |
| Icon | 16px, 8px before the label |
| Settings | Pinned to the bottom of the panel, the same row as every other page |

* **What needs you comes first.** Dashboard and Approvals lead, and Approvals
  shows how many items are waiting: at the end of its row when the rail is
  open, on the icon's corner when it is closed. It stays in place when nothing
  is waiting.
* **Then the records, most visited first**: Activity, Accounts, Statements,
  Sources.
* **Pages are named by the thing.** The current page is marked, not just
  highlighted.
* **Collapsing keeps every page reachable.** Labels fold away and each icon
  shows its label in a tooltip on hover or focus. Every row keeps its
  accessible name.
* **The choice is remembered** in this browser. Collapsing and expanding
  animate the rail and the canvas together; with reduced motion they change at
  once.

## Page bar

The page bar is 56px tall on every page, at every width. From the start:

1. **The sidebar toggle** (on phones, the navigation button).
2. **The Book switcher**: the Book's name with a menu to change Books. It is as
   wide as the name, up to 180px (148px on phones), then the name truncates and
   shows in full on hover.
3. **The page**: its icon and name, after a slanted divider. The name truncates
   before anything else does.
4. **Page actions**, at the end, when the page has any (for example, the
   Dashboard's period). The page puts them in the shell's actions slot; it
   never builds its own bar.
5. **Search**, labelled with its shortcut (⌘K, or Ctrl K). On phones it is an
   icon.
6. **The profile menu**, last, after a divider.

Controls in the page bar are the small size (32px). The bar's side padding is
24px, or 16px on phones.

## Canvas

| Rule | Value |
| -- | -- |
| Content width | Up to 1440px, centred |
| Side gutter | 16px |
| Space below the page bar | Pages that lead with a panel (collection pages and the Dashboard) start it at the bar's edge, 12px below the bar's controls, matching the 12px above them. Pages that lead with a title (Approvals, Settings) leave 24px |
| Scrolling | Collection pages scroll inside their table; other pages scroll the canvas |

The canvas is the main landmark. "Skip to content" is the first thing a
keyboard reaches and moves focus into it. When the page changes, focus moves
to the new page's heading.

## Scrollbars

One scrollbar everywhere: the canvas, tables, side panels, menus and dialogs.

| Rule | Value |
| -- | -- |
| Look | Thin, with a quiet thumb (`--vult-scrollbar-thumb`, the primary border colour) on a transparent track; set once in the global styles |
| Space | A container that scrolls vertically reserves the scrollbar's space, so content never shifts when it appears |
| Position | 4px from the edge of the surface it scrolls, and 8px clear of the content |
| Hidden | Only where the same content scrolls another way, such as a row of tabs |

## Phones

Below 768px the rail is hidden. The navigation button in the page bar opens a
drawer that mirrors the rail: the Book's name as its title, the same pages,
Settings at the bottom. The drawer is up to 320px wide, inset 8px from the
edges of the screen. Escape, the close button or choosing a page closes it and
returns focus to the navigation button. Under 380px the page name leaves the
bar so the Book switcher, search and profile menu still fit.

## Building a page

Render the page inside `AppShell` and pass `collection` when the page is a
toolbar and a table. Put page-level controls in the actions slot with
`HeaderActions`. Don't style the rail, the page bar or the canvas from a page:
the shell's sizes live in the `--vult-shell-*` tokens in
`design-system/styles/shell-rail.css`, and only the shell's own stylesheets
use them.

## Enforcement

`scripts/check-shell-standard.mjs` runs with the design checks. It confirms
the tokens still match this document, rejects page stylesheets that style the
rail, the page bar or the canvas, and rejects markup for deployment modes that
this release doesn't have. Print styles may hide the frame.
