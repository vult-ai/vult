# Menus

Version 1. This standard covers every list that opens from a control: action
menus (the Manage, Book and profile menus), submenus, and the option lists of
selects and comboboxes.

## Principle

**A menu's size follows its content, within fixed limits.** Its width comes from
its longest label, never from a number chosen for one screen. The space after
that label is the same in every menu, because it comes from the spacing tokens
rather than from a width.

## Width

| Rule | Value |
| -- | -- |
| Width | The longest row, plus the spacing below |
| Minimum | 144px, so short menus (Edit, Copy) stay easy to hit |
| Maximum | 280px, or the viewport width less 16px if that is smaller |
| Beyond the maximum | The label truncates with "…" and the full text appears on hover |
| Select and combobox lists | At least as wide as their trigger; grow with content up to 320px, or the trigger's width if that is wider |

No menu sets its own width. A menu that seems to need one is either listing
content whose length can't be predicted, which the maximum already handles, or
needs a different control.

## Spacing

All values come from the control tokens.

| Part | Value | Source |
| -- | -- | -- |
| Row height | 32px | `--vult-control-height-sm` |
| Menu padding | 4px on every side | |
| Row padding | 8px at the start, 12px at the end | |
| Space after the longest label | 16px | menu padding + row end padding |
| Icon | 16px | `--vult-control-icon-size` |
| Icon to label | 8px | |
| Row corners | 4px | control radius less menu padding |
| Separator | 1px, with 4px above and below | |
| Height | Up to 360px, or the space available, then the list scrolls | |

Text in a row uses the interface type role (14px). A secondary value on the
right of a row, such as "System" beside Appearance, uses the metadata role and
sits 12px after the label.

## Structure

* **Icons are all or none.** Within a menu, either every row has an icon or
  none does. A selection check counts as an icon: its column is reserved on
  every row, shown only on the selected one.
* **Action labels start with a verb** ("Export transactions", "Create Book").
  Lists of things (Books, accounts, options) are named by the thing.
* **Unavailable actions stay in place**, greyed and not selectable, so the menu
  keeps its shape and the action stays discoverable.
* **Destructive actions come last**, after a separator, in the error colour, and
  ask for confirmation before acting.
* **Separators group related actions.** A menu with one or two actions should be
  buttons instead.
* **Submenus** show a chevron at the end of their row and follow the same
  rules.

## Behaviour

Menus are built on the accessible menu and listbox primitives, which provide
the keyboard behaviour: Enter, Space or Down opens and focuses the first row; Up
and Down move; Home and End jump; typing a letter jumps to the next matching
row; Enter chooses; Escape closes and returns focus to the trigger. Nothing is
hand-built on top of them.

## Enforcement

`scripts/check-menu-standard.mjs` runs with the design checks. It rejects a
width set on a menu or list popover outside the shared components, and a menu
built by hand instead of from them. The values above live in one place, the
`--vult-menu-*` tokens in `vult-controls.css`, and the check confirms they
still match this document.
