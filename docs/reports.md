# Recorded-ledger reports

Reports use accepted journal lines in one authorized database snapshot. Amounts
are exact integer strings in the Book currency and scale. Periods use the
accounting date, including a reversal's own accounting date. Native amounts,
denominations and later valuations do not replace recorded Book amounts.

The browser session can read:

- `GET /api/v1/financial-reports`
- `GET /api/v1/financial-reports/months`
- `GET /api/v1/financial-reports/contributions`
- `GET /api/v1/financial-reports/export-permission`

These routes belong to the bundled interface; they are not a public HTTP API.
They require an owner session and an explicit owned `book_id`. Bearer tokens
cannot use them. Responses have `schema_version: 1` and are never cached.

The supported calculations are trial balance, balance sheet, profit and loss,
general ledger, journal and a qualified changes-in-equity roll-forward. The
cash-flow engine requires an established cash scope and treatment policy; when
these are absent it reports `unsupported`, with no invented zero cash totals.
Reports make no claim of audited or accounting-framework compliance.

## Unknowns and treatment

Account identity is its permanent ID. Editable codes are optional. Classification
comes from the stored account class; a matching stored subtype can refine it.
Names, descriptions and account-code patterns never establish classification,
an opening position or an opening/closing reporting treatment.

The current journal adapter has no accepted special reporting treatment. It
therefore includes recorded effects and marks treatment unspecified. An expense
against equity stays an expense and carries an attributable review finding.
Descriptive economic purpose is separate from reporting treatment.

Source coverage, opening completeness, recognition basis and reporting framework
remain unknown until established. An accepted Book start date is not proof of a
zero opening balance. An unset fiscal calendar produces an accumulated result
without an invented January 1 boundary or fiscal-year split.

The balance sheet retains unclosed income and expense balances in equity. Typed
closing transfers, when supplied by a supported reporting adapter, cannot count
the same earned result twice. A report never infers these transfers from labels.

## Scope and inspection

Position reports use `as_of`; activity reports use `from` and `to`. Comparisons
accept `none`, `previous_period`, `previous_year` or an explicit comparison
period. General-ledger account filters contain permanent IDs. Unknown parameters,
duplicate scalar parameters, invalid dates and unsupported currencies fail.

A report binds its request, population, metadata, checks and values to a digest.
Contribution reads use that digest and the original knowledge cutoff and
generation time. A changed catalog or population causes a conflict instead of
silently showing different contributing records. The digest grants no access.
CSV is produced from that captured result, with spreadsheet formula protection.

Monthly columns support up to twelve months and reuse one captured Book state.
The month comparisons share population validation and common period totals.
A base Profit & Loss can include a comparison outside the monthly population.
Each column keeps a report digest that can reproduce its contributing postings.
The posting read checks a live owner lease before and after selecting the
explicitly scoped Book population.
Each read is bounded to 10,000 accounts and 100,000 posting lines; exceeding a
bound fails rather than truncating totals. CSV has a 32 MiB bound. These bounds
are operational limits, not assertions that a Book's history is complete.

## Calendar charts

The bundled Dashboard projection accepts `unit=day|week|month|quarter|year`
(default `day`). Its version 2 projection contract returns one calendar plan,
without capacity-based slices or account truncation. Weeks begin on Monday;
months, quarters and years are calendar units, not fiscal units.

Each bucket has exclusive `to` and `covered_to` dates. `from` and `to` identify
the full calendar interval; `covered_from` and `covered_to` identify the portion
inside the selected inclusive report period. Contributions never include dates
outside that selected portion. Dates are civil accounting dates in the Book's
stored IANA timezone, not observed instants or fixed elapsed-hour intervals.
An unset timezone makes the calendar unavailable, with no asserted calendar
totals; the recorded financial position and period totals remain available.

Income and expenses are separate, with exact per-account signed contributions.
Income credits and expense debits are positive; opposite sides remain negative.
Positive and negative subtotals and posting counts distinguish zero-net activity
from inactivity. Refunds are never clamped or combined into another account.
At most 1,000 buckets are returned; an oversized explicit unit fails instead of
silently changing interval boundaries.

## Recorded state

Live financial reads return `state_revision` and `state_captured_at`. The revision
identifies the accepted Book population, its recorded-time visibility and the
relevant Book definitions, schema, currency catalog and calculation model. It is
independent of the selected period or account filter. Responses with different
revisions must not be presented as one coherent state. Re-read the affected
figures; a revision grants no access and is not a request key or a result digest.

`state_captured_at` is when the observation was recorded, not when the Book last
changed. Aggregate responses also carry `state_knowledge_cutoff`, the recorded-time
cutoff used by their queries. Backdated postings, accepted definitions and a
retained future recording crossing the cutoff are distinguishable even when the
latest accounting date stays unchanged. Explicit immutable-entry reads include
all accepted selected records and do not assert a recorded-time cutoff.

The same marker is supplied on Dashboard, state, Activity, account and browser
journal reads, monthly reports, contributions, statement CSV and agent balance,
trial-balance and journal-page reads. The agent's exact `get_journal` record and
operation receipts retain their published immutable bytes and recorded time.
Captured-time metadata stays separate from financial-content digests and immutable
operation receipts. No recognition-basis, coverage or opening-balance claim is
created by observing a recorded state.
