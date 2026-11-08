# Dialogs

Version 1. This standard covers every modal dialog: forms that open over a
page, details and pickers, confirmations, and the document viewer. Menus and
popovers that stay attached to their control are covered by the
[menu standard](menus.md); the phone navigation drawer by the
[app shell standard](shell.md).

## Principle

**A dialog's height follows its content; its width is one of three shared
sizes.** No dialog sets its own size. The space a short dialog would waste and
the scroll a tall one would need both come from the same rules, so every
dialog looks like it belongs to the same product.

## Size

| Size | Width | Use it for |
| -- | -- | -- |
| Small | 480px | Confirmations, one or two fields, short settings |
| Medium | 640px | Forms and details; the default |
| Large | 800px | Working dialogs with a list or table: agent proposals, entry details |
| Viewer | Up to 1280px, the full window height | Reading a document |

| Rule | Value |
| -- | -- |
| Height | The content's height, up to the window less 16px above and below |
| Beyond that | The body scrolls; the title and the actions stay in place |
| Space from the window edge | At least 16px, or 8px on phones |
| Phones (under 768px) | Every dialog is the full width less 8px each side; the viewer fills the screen |

Pick the smallest size its content fits without wrapping a field label or a
table column. A dialog that seems to need a size in between needs a different
layout, not a new number.

## Anatomy

A dialog has three parts, in this order.

1. **Title**, with the close button at the end. A confirmation's title is the
   question ("Delete this file?"); any other title names the thing ("Agent
   proposals", "GL code").
2. **Body**, the content. It scrolls when the dialog reaches its maximum
   height.
3. **Actions**, in `DialogFooter`, aligned to the end: Cancel first, then the
   one primary action. A dialog has at most one primary action. On phones the
   actions stack, the primary action on top.

A single line of description under the title is optional. Leave it out when
the title says enough; a dialog never shows a generic description.

Destructive and irreversible steps use the shared confirmation; see the
[feedback standard](feedback.md).

## Behaviour

* Opening a dialog moves focus to its title. Closing it returns focus to the
  control that opened it.
* Escape, the close button and Cancel close the dialog. Escape and the close
  button do nothing while a save is in progress, so a change is never left
  half-sent.
* The page behind a dialog cannot be reached by keyboard or pointer.

These come from the shared `Dialog` component (WAI-ARIA dialog pattern). Nothing
is hand-built on top of it.

## Building one

Use `Dialog` from `components/ui/dialog.tsx` with `size` set to `small`,
`medium` (the default), `large` or `viewer`, and put the actions in
`DialogFooter`. The widths live in one place, the `--vult-dialog-*` tokens in
`components/ui/controls.css`.

## Enforcement

`scripts/check-dialog-standard.mjs` runs with the design checks. It rejects a
width or height set on a dialog outside the shared stylesheet, a modal built
by hand instead of from `Dialog`, and token values that drift from this
document.
