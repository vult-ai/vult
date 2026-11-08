# Recorded Activity

The bundled interface reads a transaction page through
`GET /api/v1/state?view=transactions` and its full filtered totals through
`GET /api/v1/transactions/summary`. Both require an owner session and an explicit
`book_id`. They use one shared selection rule for `search`, `kind`, `from`, `to`
and `account`. Responses are version 1 and are never cached.

Dates filter the accounting date. Search is a literal, case-insensitive substring
of the description, line memos or recorded account names/codes; wildcard
characters remain literal. `account` is a permanent account ID. Kind accepts
`journal`, `reversal`, `income` and `expense`, or an empty selection. Income and
expense filters select entries containing that stored account class, including
zero-Book postings; they are not classifications supplied by a caller.

The list retains a journal's actual kind, all three dates, reversal links and its
Book-currency debit total. `amount_role` states that meaning explicitly. Original
denominations, native movements and valuation inputs remain in the complete
entry read; the list never invents a flat exchange-rate object. Permanent account
IDs occupy identity fields, with optional editable GL codes separately preserved.

Summary income and expense use the same recorded contribution rules as financial
reports. Labels, memos and equity counterparts never exclude effects. Totals are
exact integer strings with explicit currency and scale. Missing counterparties
remain `not_recorded`; descriptions are not guessed to be payees. Source coverage
remains unknown. Category keys are permanent account IDs. Monthly or yearly
series include every requested bucket, including zero recorded activity.

Pages accept `limit` 1–100, `offset` and an optional captured `revision`. Pass the
same revision to the summary and later pages. A change returns 409 so the
interface can refresh the table and summary together. Revisions bind the selected
membership, visible reversal links, filters and Book write epoch. A changing read
clock alone does not manufacture a new revision.

Selection is bounded to 100,000 matching entries and 100,000 lines. Exceeding
either fails, without truncated totals. Dense summary series are bounded to
1,000 buckets. Page completeness describes selected recorded journals, not
historical source completeness or an established opening balance.
