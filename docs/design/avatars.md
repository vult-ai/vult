# Avatars

Version 2 for accounts, version 1 for counterparties. This standard covers
the round marks that identify accounts, institutions and the people and
businesses you pay or are paid by.

## Principle

**One frame, one fit, for every logo.** Every mark sits in the same circle
with the same clear space around it, measured from what the logo actually
paints rather than from its file. A bank's logo and a coffee shop's logo look
equally at home, and no page adjusts one to make it fit.

## Sizes

| Role | Circle | Safe area for the mark | Clear space | Use it for |
| -- | -- | -- | -- | -- |
| Account | 40px | 24px | 8px | Account lists and account pages |
| Compact | 24px | 14px | 5px | Dense tables: counterparties, compact account rows |

The compact role keeps at least the account role's proportion of clear space.
There are no other sizes, and neither role changes with the theme, a brand or
the window width.

## Marks

* **A known institution or brand** shows its own logo, fitted into the safe
  area by the shape it paints.
* **Anything else** shows initials on a colour from the data palette. The
  initials are the first letters of the first two words, or the first two
  characters of a single word. The colour comes from the identity itself, so
  the same account or counterparty always has the same colour.
* **A counterparty with no usable name** shows a neutral dash, never made-up
  initials.
* **A failed logo** falls back to the same initials and colour, so nothing
  jumps when an image is missing.

For a counterparty, the recorded brand or institution decides the mark when
there is one; otherwise its name does.

## Building one

Use `AccountAvatar` for accounts (`density="compact"` in dense tables),
`CounterpartyAvatar` for counterparties, or `ContactAvatar` when an entry may
carry a recorded brand. Avatars are decorative: the name next to them is what
assistive technology reads.

## Enforcement

`scripts/check-account-avatar-standard.mjs` runs with the asset checks. It
pins the geometry above, allows brand and theme rules to change colour only,
and rejects avatar styling outside `components/ui/account-avatar.css` or logos
rendered without the shared component.
