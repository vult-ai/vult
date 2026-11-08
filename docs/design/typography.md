# Typography

Version 1. This standard covers type in the interface: the typeface, the type
roles every screen uses, and how numbers are set.

## Principle

**Text takes a role, never a size.** A screen says what a piece of text is (a
label, a section title, a value) and the role decides its size, line height
and weight. Six sizes cover the whole interface, so screens look related
without anyone comparing pixels.

## Typefaces

| Use | Typeface |
| -- | -- |
| Interface | Geist, then the system interface font |
| Logs and code | Geist Mono, then the system monospace font |

## Roles

| Role | Size / line height | Weight | Use it for |
| -- | -- | -- | -- |
| `metadata` | 12 / 16px | 500 | Dates, counts, secondary values, hints |
| `label` | 14 / 20px | 500 | Field labels, row labels, column headers |
| `ui` | 14 / 20px | 400 | Controls, table cells, interface copy |
| `body` | 16 / 28px | 400 | Reading text |
| `compact-title` | 16 / 24px | 600 | Panel and card titles |
| `section-title` | 20 / 28px | 600 | Section headings |
| `metric-value` | 20 / 28px | 600 | A prominent figure inside a card |
| `dashboard-value` | 24 / 32px | 400 | Dashboard headline figures |
| `page-title` | 30 / 36px | 600 | A page's own title, where a page has one |

Logs use their own three roles in Geist Mono: `log` (12px), `log-value`
(14px) and `log-metric` (20px). Document editing alone may use
`document-body` (15 / 22px).

Apply a role with its class, such as `vult-type-label`, or in a stylesheet
with its token, such as `font-size: var(--text-vult-label)`.

## Numbers

Amounts, counts and dates that line up in columns use tabular figures
(`vult-type-number`), so digits align. Amounts keep their full precision and
their sign; a missing value is shown as missing, never as zero.

A display-size amount, the one figure a headline, a Dashboard card or an
entry's details leads with, uses `DisplayAmount`
(`components/ui/display-amount.tsx`). Its fraction, the decimal separator and
the digits after it, takes the `ui` role (14px) in the secondary text colour,
on the whole part's baseline, so the whole amount reads first. The sign,
currency symbol and digit grouping stay full size. The split follows the
regional format's decimal separator and the currency's minor units: a
currency without them has no fraction. It is one string, so selecting,
copying and screen readers get the exact amount. Only exact amounts use it;
compact amounts ("$1.2M") and percentages do not. Amounts in tables and lists
keep one size, so their digits line up.

## Enforcement

`scripts/check-typography.mjs` runs with the design checks. It confirms each
role keeps the size in this document and rejects any new `font-size` that is
not a role. Older declarations are listed in `typography-legacy.json`; the
check allows them but never more of them, so the list only shrinks.
