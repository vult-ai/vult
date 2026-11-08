# Dashboard

Version 1. Direction approved by the owner; the screen design follows from it.

## Model

Vult is to accounting what headless UI primitives are to interfaces. The
primitives carry the hard guarantees: double entry, standard classifications,
traceability and reconciliation. Agents compose views from them for each
owner's needs, and those needs change as a business or household changes.
Vult's own Dashboard is the first standard view built from the same
primitives, the way a default component set is built on headless components.

Unlike a UI library, Vult does not trust whoever composes a view. Every
primitive checks what is built from it (Ledger Standard LS-38).

## Purpose

The Dashboard is a summary of the Book's financial statements. It adds no
measures of its own. A CPA or CFO should recognise every figure, know its
definition, and be able to trace it to a statement line and from there to the
entries behind it.

Agents use the same statements to explain the Book and to show the effect of
a decision. Vult measures; the owner decides.

## Rules

1. **Statements first.** Every figure is a line of the balance sheet, the
   income statement or the statement of cash flows, or a ratio with a
   published textbook definition computed only from those lines.
2. **No invented measures.** A measure that is not a statement line or a
   standard ratio (for example runway or MRR) is a *key metric*. A key metric
   follows the same discipline as non-GAAP measures under SEC Regulation G and
   Regulation S-K Item 10(e): its definition is stated, it is reconciled to the
   nearest statement line, and it is never shown more prominently than that
   line.
3. **The basis is always visible.** The header states the framework the
   statements are prepared on (accrual, cash or tax basis), the period, the
   as-of date, and that the statements are unaudited. Vult never describes
   statements as GAAP or IFRS compliant unless every requirement is met.
4. **Completeness is shown, not assumed.** Source coverage, opening-balance
   status (confirmed, partial or unknown) and the report engine's findings and
   limitations appear with the figures they affect. Unknown is never shown as
   zero (Ledger Standard LS-35, LS-37).
5. **Comparatives.** Each figure shows the comparable prior period or prior
   date, as IAS 1 requires for presented statements: the change, and the
   change as a share of where it started ("+$7,098.88 (+0.4%)"), with no
   share when it started at zero. Dates every card in a panel shares are
   stated once, in the panel's header ("Dec 31, 2025 vs Dec 31, 2024").
6. **Traceable.** Every figure opens its statement line; every line opens its
   entries. The way in is the figure's label or the row's name, never the
   whole card or row: the figures themselves stay selectable, so a click on an
   amount copies it rather than leaving the page.
7. **No judgement.** No good/bad colours, verdicts, default thresholds or
   warnings Vult chose. An owner may record their own targets; they are shown
   as the owner's.

## Primitives

| Primitive | What it is | Who changes it |
| -- | -- | -- |
| Statements | Balance sheet, income statement, statement of cash flows, from the ledger | No one; fixed |
| Classifications | Standard account subtypes following the US GAAP and IFRS taxonomies, plus attributes such as fixed or variable cost and recurring or non-recurring revenue | An agent proposes with evidence; the owner corrects |
| Key metrics | A named formula over statement lines and classified accounts, with a window and a reconciliation to a statement line | An agent defines; the owner approves; every change is a new version |
| Targets | The owner's own reference marks on any figure | The owner, or an agent at the owner's request |
| Views | A saved selection of figures, key metrics and charts, drawn with standard components | An agent assembles; the owner keeps or discards |
| Pro forma | Draft entries for a decision, shown as Actual, Adjustments and Pro forma; never posted | An agent drafts; Vult computes |

What Vult enforces for every view, whoever builds it:

* Every figure traces to the ledger.
* Every key metric shows its definition and reconciliation.
* The basis and completeness status are always visible.
* A changed definition is a new version; history is never silently restated.
* No view can hide an unknown.

## Business Book (the default view)

### Header

Book · period · as of · basis (for example "Accrual basis, unaudited") ·
coverage and opening-balance status · findings count.

A Book opens on the last 30 days, compared with the 30 before. A period the
owner chooses is remembered for that Book and used instead.

### Period picker

The Dashboard, Activity and Accounts share one period, chosen with
`PeriodPicker` (`components/workspaces/period-picker.tsx`):

* **Arrows** beside the trigger step the period back or forward by its own
  length: a whole month to the month before, a quarter or year likewise, any
  other range by its number of days. Forward stops at today.
* **The trigger** names whole periods ("October 2026", "Q3 2026", "2025")
  and otherwise shows the preset and its dates.
* **Links** name a whole period in one short token: `?range=2025`,
  `?range=2025-Q3` or `?range=2025-10`. Any other range keeps
  `?from=…&to=…`, and older links of either form still open.
* **The panel** is one rounded shell (the 3xl radius) on the dropdown surface
  and elevation, growing from its trigger; nothing inside it draws a second
  edge. Quick windows sit beside one month of days, grouped as Rolling
  and Calendar, in two columns that pair "this" with "last" (This month |
  Last month), so the panel is as tall as the calendar. A range is one band,
  rounded where each week begins and ends. Months slide in from the side they were reached from.
* **The month's name** opens twelve months of its year: one click chooses a
  whole month and closes the panel. In that view the year takes the same
  place, and pressing it returns to days, so the switch is always in one spot.
  The months fill the same space as the days, so the panel never changes
  size. Days choose any range, applied with Apply.

### Financial position (balance sheet, as of the period end)

Classified into current and non-current under ASC 210 / IAS 1 (paras 60–76),
using stored account classes and subtypes, never names.

| Figure | Definition |
| -- | -- |
| Cash and cash equivalents | Balance sheet line |
| Current assets, current liabilities | Balance sheet subtotals |
| Working capital | Current assets − current liabilities |
| Current ratio | Current assets ÷ current liabilities |
| Quick ratio | (Cash and cash equivalents + marketable securities + receivables) ÷ current liabilities |
| Total assets, total liabilities, equity | Balance sheet totals |
| Debt to equity | Total liabilities ÷ total equity |

### Results (income statement, for the period)

Under ASC 220/225 and IAS 1: revenue, cost of revenue, gross profit,
operating expenses, operating income, other income and expense, net income.
Gross margin and net margin as percentages of revenue.

### Cash flows (statement of cash flows, for the period)

Under ASC 230 / IAS 7: net cash from operating, investing and financing
activities; net change in cash; beginning and ending cash. These reconcile to
the cash line on the balance sheet.

### Key metrics (optional, below the statements)

| Metric | Definition | Reconciled to |
| -- | -- | -- |
| Net burn | Net decrease in cash from operating and investing activities, monthly average over the stated window | Statement of cash flows |
| Runway | Cash and cash equivalents ÷ net burn; shown only when net burn is positive | Balance sheet cash line |

Recurring-revenue metrics (MRR, ARR) are added only with a stated definition
and a reconciliation to revenue.

### Headline

A card that answers one question leads with one figure: `Headline` from
`design-system/headline.tsx`. The figure, then a caption saying what it
covers ("net · Jan – Oct 2026"), then its parts with their series keys
("● $50m in ● $42m out"). Activity's summary uses the same headline, with net
as the figure, so a total reads the same on every page.

### Charts

Two, both standard: ending cash, and revenue and expenses. Same scale, same
period as the figures above. The chart is meant to be full, about 12 to 16
pairs of bars at full thickness whatever the period: one pair per day up to
16 days (a week draws 7), two days a pair up to a month (the last 30 days
draws 15), then one per week up to fourteen weeks, one per month up to
fourteen months (a year draws 12), then quarters or years. Periods are whole
days, weeks or months, counted back from the last day, so the latest is
whole; amounts are only added together, never split.

The scale is full too: the chart ends just above its tallest column (which
fills 96% of the height), never at a rounded-up ceiling, so no period opens
on empty headroom. Gridlines sit inside at two to four round steps (1, 2, 2.5
or 5 times a power of ten). Below zero uses the same scale and steps, and
reaches only as deep as the amounts do.

### Two views of a card

A card that has a natural reverse shows it with a small switch in its
header, beside its link. "Where the money went" (Out, the default) becomes
"Where the money came from" (In): the same share bar and rows over income
accounts. "What you owe" (You owe, the default) becomes "Owed to you": the
balances of accounts recorded with the receivable role on the same date. A
role is recorded on the account, as cash is; an account's name never makes
it a receivable. The title, the caption and the empty state follow the view;
the default view is the one the card opens on.

## Decisions: pro forma statements

A decision is shown as pro forma statements, the presentation professionals
already use (SEC Regulation S-X Article 11 is the reference model):

| Line | Actual | Adjustments | Pro forma |
| -- | -- | -- | -- |

1. The owner or an agent describes the transaction ("borrow $200,000 over
   36 months at 9%").
2. An agent drafts the journal entries. Vult computes the adjustments through
   the same operations and preview rules as any change (LS-38). Nothing is
   posted.
3. Vult shows the pro forma balance sheet, income statement and cash flow
   effect, each adjustment linked to its draft entry, labelled "Pro forma,
   not recorded".

Assumptions (rate, term, timing) are written on the adjustment, owned by
whoever stated them.

## Personal Book

Under ASC 274 (Personal Financial Statements):

- **Statement of financial condition:** assets and liabilities, and net worth.
  ASC 274 calls for estimated current values. Vult records cost; an asset is
  shown at current value only when a dated valuation is recorded, and is
  otherwise labelled "at cost".
- **Statement of changes in net worth** for the period.
- **Income and expenses** for the period, as a supporting schedule.

The same rules apply: comparatives, traceability, no invented measures.

## Removed

Invented measures and categories, verdict sentences, default reserves,
good/bad colours, gauges, hindsight panels and forecasts beyond pro forma
adjustments.

## Owner decisions

1. **Basis.** Books record on the accrual basis. Each statement view states
   its basis; a cash-basis view is derived from the accrual ledger.
2. **MRR and ARR.** Not built in. They are key metrics an agent defines for
   the Book, with a stated formula and a reconciliation to revenue.
3. **Classifications.** Receivables, marketable securities, inventory,
   payables, accrued liabilities and short-term debt use subtypes aligned to
   the US GAAP and IFRS taxonomies. This is part of the account
   classification contract.
4. **Personal asset values.** Shown at cost unless a dated valuation is
   recorded, and labelled accordingly.
