# Side panels

Version 1. This standard covers the panel beside a collection that shows the
selected record or a summary: entry details beside Activity and account
ledgers, the Activity summary, source details beside Sources, and the
Statements work panel.

## Principle

**One panel, one width, one way to open and close it.** A side panel docks
beside its collection when there is room and takes over the space when there
isn't. Whichever page you are on, the details of a record appear in the same
place, at the same size, and leave the same way.

## Size and placement

| Rule | Value |
| -- | -- |
| Width | 304px, docked to the end of the collection with an 8px gap |
| Docked | When the workspace is at least 1120px wide: room for the panel and a table that keeps its columns |
| Compact | Below that, the panel replaces the collection while a record is open, with Back at its start |
| Height | The full height of the collection; the body scrolls |

The width is the `--vult-inspector-width` token in
`design-system/collection-inspector.css`. A panel that floats or pins beside
a report, like the Statements work panel, uses the same token.

## Anatomy

1. **Header**, 40px: the panel's title as a heading, and Close at the end
   (Back at the start in compact mode).
2. **Body**, which scrolls. Its scrollbar space is reserved so content never
   shifts when it appears, and the scrollbar sits 4px from the panel's edge,
   8px clear of the content (see [scrollbars](shell.md#scrollbars)).
3. **Footer**, optional: the panel's actions, aligned to the end, above a
   divider.

With nothing selected, the header shows the panel's default title and the
body shows an [empty state](feedback.md) in the region scope.

## Behaviour

* Selecting a row opens its details; the selected row stays highlighted.
* Escape or Close closes the panel, unless a change is being saved.
* On wide screens focus stays in the table, so you can move from row to row
  and watch the details follow. In compact mode focus moves to the panel's
  heading, and Back returns to the collection.
* Closing keeps the collection's scroll position.

## Building one

Use `CollectionInspector` with `InspectorCollection` for the table or list and
`InspectorPanel` for the panel, from `design-system/collection-inspector.tsx`.

## Enforcement

`scripts/check-side-panel-standard.mjs` runs with the design checks. It
confirms the width token matches this document and rejects a stylesheet that
sets a panel's width with a number instead of the token.
