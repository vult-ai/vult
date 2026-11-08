# Forms

Version 1. This standard covers fields and the forms they make up: text,
numbers, dates, selects, comboboxes, checkboxes and file pickers, in dialogs,
panels and settings pages. Settings pages add their own layout in the
[settings standard](settings.md).

## Principle

**Every field has the same four parts, and only the shared components draw
them.** A field is a label, a control, an optional hint and an error. When
every form is built from the same field, a person learns one way to fill in
Vult and assistive technology reads every field the same way.

## Anatomy

| Part | Rule |
| -- | -- |
| Label | Always visible, above the control, and linked to it. A placeholder is never the label. |
| Control | The shared input, select, combobox, checkbox or file picker, 36px tall (32px in compact toolbars and panels). |
| Hint | Optional, under the control, in the metadata role. It says what to enter, not what the field is. |
| Error | Under the control, in words, saying what to fix. It replaces nothing else and is announced with the field. |

* Required fields are the default. Optional fields say "(optional)" in their
  label.
* A value that can't be changed is shown as text, never as a disabled or
  read-only field. The exception is a value meant to be copied whole, such as
  a new access token, which sits in a read-only field with a Copy button.
* Fields fill the width of their column. A field that only ever holds a short
  value, such as a code, may be narrower, but never wider than its column.

## Layouts

| Where | Layout |
| -- | -- |
| Dialogs and panels | One column: each field's label above its control |
| Settings pages | Rows: label and help at the start, the control at the end; stacked on phones |

Actions follow the [dialog standard](dialogs.md) in a dialog and the settings
standard on a settings page: one Save per section, enabled only once something
has changed.

## Choices

Selects list options in a meaningful order: the most common first, or
alphabetical when there is none. A list longer than about ten options is a
combobox you can type into.

## Saving

* Editing a field never saves by itself; the section or dialog's Save does
  (WCAG 2.2, 3.2.2).
* A failed save keeps everything that was typed and says what happened beside
  the form. A save whose answer was lost is never resent silently (see the
  [feedback standard](feedback.md)).

## Building one

Wrap each control in `Field` from `components/form.tsx`, which supplies the
label, hint and error and draws the control with the shared input or select.
On settings pages use `SettingsRow` with `NativeFieldControl`. Use `Select`
or `ComboBox` from `components/ui` for choices and `NativeCheckbox` for
checkboxes.

## Enforcement

`scripts/check-form-standard.mjs` runs with the design checks. It rejects a
native input, select or textarea outside `Field`, `NativeFieldControl` and the
shared controls.
