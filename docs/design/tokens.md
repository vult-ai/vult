# Visual tokens

Version 1. This standard covers the small values every screen shares: corner
radii, colours and icons. Type has its own [typography standard](typography.md).

## Principle

**A value is chosen once and named; screens use the name.** When a corner or
a colour comes from a token, changing the token changes every screen at once,
and nothing drifts a pixel or a shade away from its neighbours.

## Corner radii

| Token | Value | Use it for |
| -- | -- | -- |
| `--radius-xs` | 2px | Focus rings and tiny marks |
| `--radius-sm` | 4px | Rows inside menus and lists, small chips |
| `--radius-md` | 6px | Compact controls |
| `--radius-lg` | 8px | Controls: buttons, fields, tabs |
| `--radius-xl` | 12px | Cards, tab strips, popovers, dialogs, the rail panel |
| `--radius-2xl` | 16px | Large surfaces: settings sections, the collection frame |
| `--radius-3xl` | 24px | The outer workspace frame |
| `--radius-full` | 9999px | Pills |

A circle uses `50%`. A surface inside another surface uses the outer radius
less the space between them, written with the tokens.

## Colours

Colours come from the theme tokens (`--color-text-*`, `--color-bg-*`,
`--color-border-*`, `--color-fg-*`), the control tokens (`--vult-control-*`)
and the chart palette (`--color-chart-*`), so light and dark mode change
together. Component stylesheets never write a raw colour; a colour that has
no token yet is added to the design system first.

Direction has its own tokens: `--color-direction-up` and
`--color-direction-down` for keys and swatches, and their `-text` forms for
figures. A change is coloured by its meaning, not its sign: good news in ink,
black in light mode and white in dark ("in the black"), and bad news in red.
More income or assets is good news; more expenses or liabilities is bad news,
so a fall in costs is shown in ink.

Money in and out are categories, not news, and never use the direction
colours. They are told by position, left column in and right column out, and
keyed by the ink pair glyph (`SeriesKey`). On Activity, where no account colours
share the view, the glyph and the summary columns use `--color-money-in` (ink)
and `--color-money-out` (a quieter gray). An ordinary cost is never drawn in red.

## Icons

Icons come from `src/design-system/icons.tsx` only, which alone imports
Phosphor (MIT) and keeps the names the components use, at 16px in controls
and 20px in state tiles. Icons use the bold weight; a selected state may
use `weight="fill"`. An icon beside text is decorative (`aria-hidden`); an icon-only
control has an accessible name.

## Enforcement

`scripts/check-visual-tokens.mjs` runs with the design checks. It rejects icon
imports from any other library and new literal radii or raw colours in
component stylesheets. Older literal values are listed in
`visual-legacy.json`; the check allows them but never more of them, so the
list only shrinks. The design system's own stylesheets define the tokens and
are exempt.
