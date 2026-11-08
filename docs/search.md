# Book search

The Accounts workspace can request only the complete matching account IDs with
`GET /api/v1/search?book_id=…&q=…&types=accounts&account_ids=1`. The response
carries `account_ids` when there are matches, an exact `accounts.total`, and no
account cards. The finder keeps its limited, ranked results.

Source matches include ready originals that are still available at the read's
expiry cutoff. An exact word in an available original prevents typo fallback.
The finder and `GET /api/v1/documents?book_id=…&q=…` use this same population.
The filtered documents response returns the full matching `total` and a page
of matching documents in sequence order. Continue with its `next_after` and
the same query and response `revision` while `has_more` is true. If the Book
changes or an original expires, the stale continuation returns 409. Start
again without a cursor to capture the current matches.


The header finder, Activity filters and `search_book` use the same matching
operation. Every search belongs to one Book and the caller's current access.
Browser routes require an owner session; agent connections use MCP.

Words match in any order. Every term must match, using account names or codes,
entry descriptions and line memos, accounting dates, or exact Book-currency
amounts. Quotes keep a phrase together. Accents and case are folded consistently.
A typo fallback applies to recorded account and document names only, when direct
matching found no word or account. Descriptions and memos keep exact and prefix
matching; text resembling a merchant name does not establish a counterparty.

Returned transaction amounts are unsigned Book-currency debit totals, including
zero and totals larger than one line can hold. Zero is never an amount selector. The response states
`amount_role: "book_debit_total"`, Book ID, currency, scale, schema version and
recorded-state marker. These amounts describe journal entries, not cash flow or
native account movements. No counterparty is inferred from text; its group is
explicitly `not_recorded` until such facts exist.

Queries allow 200 characters, eight terms and 64 characters per indexed word.
Overlong words are rejected, never dropped from an AND query. Empty or punctuation
queries return empty finder groups and leave Activity unfiltered. Typo matching
does not enumerate entry-history words. Account and source-name populations
are bounded; an oversized population fails rather than silently truncating.

The search tables are derived indexes. Posting, reversal and restore maintain
them automatically, and upgrades backfill existing entries. Backups retain the
authoritative records and rebuild these indexes on restore. No PostgreSQL
extension is required.
