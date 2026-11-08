# Buttons

Version 1. This standard covers buttons and button-styled links: actions in
forms, dialogs, toolbars, sections and rows, and the triggers that open menus,
popovers and dialogs.

## Principle

**One look, one vocabulary.** Every button takes one of five colours and one
of three sizes, named the same way everywhere. The colour says how important
the action is in its place, never which screen it is on.

## Colours

| Colour | Use it for |
| -- | -- |
| `primary` | The one main action in a dialog, section or empty state: "Save", "Create Book" |
| `secondary` | Other actions beside it, and Cancel |
| `tertiary` | Quiet actions in toolbars, rows and panels: filters, view options, "Try again" inside a panel |
| `link` | An action or destination that reads as text: "View all", "Open PDF" |
| `destructive` | The confirm action of the shared confirmation only |

* A dialog, section or empty state has at most one primary action.
* A destructive step never has its own red button: it opens the shared
  confirmation, whose confirm action is the only destructive button (see the
  [feedback standard](feedback.md)).

## Sizes

| Size | Height | Use it for |
| -- | -- | -- |
| `sm` | 32px | Toolbars, the page bar, section headers, table rows and panels |
| `md` | 36px | Forms and dialogs; the default |
| `lg` | 40px | A single prominent action on an otherwise empty screen |

Heights come from the control tokens (`--vult-control-height-*`), so buttons
line up with fields and selects of the same size.

## Labels and icons

* Labels start with a verb and use sentence case: "Export CSV", "Save".
* A button keeps its label while its action runs and shows progress instead
  (see the feedback standard).
* Icons are 16px. A button is icon-only only when it is icon-only everywhere
  (refresh, view options), and then it has an accessible name and a tooltip.

## Building one

There are two components with the same `color` and `size` props:

* **`Button`** from `components/ui/button.tsx` for actions that run: saving a
  form, submitting, retrying. It is a native button, tracks a running action
  and takes `busy`.
* **`Button`** from `design-system/catalog/components/vult-ui/button.tsx` when
  the button opens a menu, popover or dialog, when it is a link (`href`), or
  when it needs a leading or trailing icon component.

Never style another element with the button classes.

## Enforcement

`scripts/check-button-standard.mjs` runs with the design checks. It rejects
the retired `variant` prop and colour names on either Button, and any element
other than the two Buttons that uses the `vult-button` classes. The type
system rejects colours and sizes outside this document.
