# Settings

Draft, version 1. This standard covers settings and the other supporting pages
that hold a Book's configuration: Book settings, Manage Books, connections and
access, your data, and personal preferences.

## Principle

**A setting is a recorded fact about the Book, changed on purpose.** Settings
decide how every amount is measured and reported, so each one lives in exactly
one place, says what it means in standard terms, and changes only when the
person saves it.

## Requirements

These come from published standards. They are not design choices.

### Accessibility (WCAG 2.2, level AA)

| Criterion | Requirement |
| -- | -- |
| 2.4.2, 2.4.6 | The page has one title, in the page bar. Sections have headings that describe them; no heading repeats the page title. |
| 1.3.1, 3.3.2 | Every control has a visible label, linked to it. Related options sit in a labelled group. |
| 1.3.5 | Fields about a person or business (name, address, phone, email) declare their purpose so browsers can fill them. |
| 3.2.2 | Editing a field never saves, navigates or changes anything else by itself. |
| 3.3.1, 3.3.3 | An error appears on its field, in words, with how to fix it. |
| 3.3.4 | A change with legal or financial effect is confirmed before it is saved and can be reviewed after. |
| 3.3.7 | Nothing already recorded is asked for again. |
| 3.3.8 | Signing in and confirming identity never require remembering or retyping a code. Passkeys meet this. |
| 4.1.3 | "Saved" and other results are announced without moving focus. |
| 2.5.8, 1.4.10, 2.4.11 | Targets are at least 24px. The page reflows to one column on phones. Focus is never hidden behind a sticky bar. |

### Controls (WAI-ARIA Authoring Practices)

* Tabs follow the tabs pattern: arrow keys move between tabs, and each tab has
  its own address so a link opens it directly.
* On/off settings are switches; one choice from a few is a radio group; one
  choice from many is a select or combobox (see [menus](menus.md)).
* Confirmations are modal dialogs that return focus to the control that opened
  them.

### Values

Values are stored in their standard form and shown in the reader's locale
(Unicode CLDR).

| Value | Standard |
| -- | -- |
| Currency | ISO 4217 code, shown with its name: "US Dollar (USD)" |
| Country | ISO 3166-1 |
| Time zone | IANA time zone name, such as America/New_York |
| Date | ISO 8601 |
| Phone | E.164 |
| Legal entity identifier | ISO 17442 (LEI), when given |

### Accounting

Names follow the accounting standards (IAS 21 and IAS 1, ASC 830), so a
professional reads them as they expect.

* **Functional currency**: the currency the Book measures its transactions
  in. **Presentation currency**: the currency its statements are shown in.
  Both are named, even when they are the same.
* **Fiscal year start**: required for statements, so it is always settable.
  Once statements have been closed for a period, changing it asks for
  confirmation and says which periods it affects.
* **Opening balances**: the starting point of every statement, recorded as an
  opening entry dated the day before the Book's first period.

### Security and privacy

* Changing how you sign in, or anything with legal or financial effect, asks
  you to confirm with your passkey first (WebAuthn Level 3, NIST SP 800-63B).
* Every saved change records who made it and when, and is visible in the
  Book's history.
* Your data can be exported whole, in open, machine-readable formats, and a
  Book can be deleted (GDPR Articles 15, 17 and 20).

## Decisions

These are Vult's choices where the standards leave room.

### Where a setting lives

Each setting lives in one place.

| Scope | Opened from | Holds |
| -- | -- | -- |
| Book | Settings, at the bottom of the rail | The four tabs below |
| You | The profile menu | Your name, appearance, passkeys |
| Books | The Book switcher, "Manage Books" | Creating, archiving and deleting Books |

Book settings has four tabs, in this order:

| Tab | Sections |
| -- | -- |
| General | Name; Address (the Book URL and the Book ID) |
| Accounting | Book setup (type, currency, fiscal year, time zone) |
| Legal identity | The legal details printed on statements |
| Your data | Backup and export; Restore, which always creates a new Book |

A link can open any tab by its id, for example `?tab=data`.

### Layout

| Rule | Value |
| -- | -- |
| Page | The page bar names it; the page starts with its tabs |
| Column | One column, up to 720px, centred in the canvas |
| Section | A heading, one line saying what it holds, then its rows |
| Row | Label and help text at the start, the control at the end; stacked on phones |
| Row height | At least 48px; 16px padding |
| Space between sections | 32px |

### Saving

* **Each section saves on its own.** Its Save button sits at the end of the
  section and is enabled only when something in the section has changed.
  Leaving a section with unsaved changes asks first.
* **Switches are the exception**: a switch takes effect when flipped, and says
  so in its notice, because a switch is a single, reversible choice.
* **Consequential changes confirm first**: the Book address, currencies, the
  fiscal year start and legal identity. The confirmation says what will change
  and what it affects.
* After saving, a notice says what changed, in the past tense ("Book name
  saved.").

### Values that can't be changed

A value fixed by the Book's history is shown as text with the reason, never as
a disabled field: "US Dollar (USD) · Fixed once entries were recorded."

### Destructive actions

Archiving and deleting come last, in their own section, and use the shared
confirmation (see the [feedback standard](feedback.md)).

### Completeness

Every section on a settings page works. A section that can't be completed
isn't shown.

## Building one

Use `SettingsLayout` for the page and its tabs, `SettingsSection` for each
section, and `SettingsRow` for each row.

## Enforcement

`scripts/check-settings-standard.mjs` will run with the design checks. It
rejects:

* a settings page built without the shared layout, section and row
  components;
* more than one Save in a section, or a Save on a single row;
* a disabled input used to show a fixed value;
* a heading that repeats the page title.
