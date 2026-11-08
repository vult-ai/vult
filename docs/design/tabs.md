# Tabs and view filters

Version 1. This standard covers the two controls that look alike but do
different jobs: tabs, which switch between separate panels (Settings, Your
settings), and view filters, which narrow one collection (All, Income and
Expenses above Activity).

## Principle

**Tabs change what you are looking at; filters change how much of it you
see.** If the content underneath is the same table or list, it is a filter.
If each choice shows a different panel with its own sections, it is a tab.
Using the right one tells assistive technology, and you, what will happen.

## Tabs

`TabStrip` in `components/ui/tab-strip.tsx`, inside a react-aria `Tabs` that
owns the panels.

| Rule | Value |
| -- | -- |
| Shape | A rounded strip, 4px inside, with the selected tab raised on a white surface that slides between tabs |
| Tab height | 32px, 12px padding at the sides |
| Labels | The label type role; a noun naming the panel ("General", "Your data") |
| Width | As wide as its tabs. When they don't fit, the strip scrolls sideways and keeps the selected tab in view |
| Keyboard | Arrow keys move between tabs; Home and End jump to the ends |

* Each panel keeps what you typed when you switch away and back.
* A panel loads its content the first time it is opened, not before.
* A link can open a tab by its id where the page supports it (Settings:
  `?tab=data`).
* Screens may place the strip (its margins) but never restyle the strip or
  its tabs.

## View filters

`CollectionFilters` in `design-system/collection-filters.tsx`, placed by the
[toolbar standard](toolbars.md).

* Each filter is a small button that stays pressed while selected: the
  selected one is secondary, the others tertiary (see the
  [button standard](buttons.md)).
* A filter shows its count after the label in the metadata role, and an
  optional colour dot when the filter is a kind of money (income green,
  expenses yellow).
* Exactly one filter is selected. The group has a label naming the
  collection.

## Enforcement

`scripts/check-tab-standard.mjs` runs with the design checks. It rejects a
tab list built outside `TabStrip`, and stylesheets that restyle the strip or
its tabs outside `components/ui/tab-strip.css`.
