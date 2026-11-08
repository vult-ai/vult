# Account reads

The bundled interface reads an owned Book through three session-only routes:

- `account-activity?book_id=…&as_of=…` returns recorded entry counts per account.
- `account-hierarchy-activity` accepts the same scope and an optional JSON `codes`
  array of distinct permanent account IDs. Groups represent individual accounts,
  stored account classes and the explicitly selected population. Account names
  never establish parents.
- `account-detail` requires `book_id`, a JSON `codes` selection, `from` and `to`.
  `view=ledger` selects one account; `view=group` selects accounts of one stored
  class. Page size is 1–100, default 100. A nonzero `offset` requires the prior
  response's `revision`. A changed population or account definition returns 409.

For selections that exceed the 8 KiB query limit, send the same
`account-hierarchy-activity` or `account-detail` request as POST. Keep the Book,
dates and pagination in the query and put only `{"codes":[...]}` in the JSON
body, up to 400 KiB. The POST requires the owner session, Origin and CSRF proof
and performs a read. The GET and POST response shapes and pagination agree.

The Accounts workspace starts with `state?view=accounts`, which includes the
selected period's positions and `account_activity` from the same recorded
snapshot. The interface reuses that activity for filters that preserve the
group figures. A narrower group that changes those figures needs an explicit
selection. Search results use the ledger's complete matching account IDs.

All accounting windows use accounting dates. Economic dates and recorded times
remain separate on ledger rows. Lines are ordered by accounting date, immutable
entry sequence and line position. Pagination never resets a running balance.
Group counts deduplicate entries containing several selected lines, including
zero-Book entries. The 52 weekly buckets count accepted records; they do not
establish statement coverage. Source coverage stays `unknown`.

Financial values are integer minor-unit strings in the stated Book currency and
scale. Asset and expense detail balances are debit-positive; liability, equity
and income detail balances are credit-positive. Income and expense totals cover
the requested period; other classes show the recorded position through `to`.
An opening shown in the ledger is only the sum of earlier accepted postings,
labelled `recorded_prior_postings`, not an assertion of complete history.

Native balances are independently scoped measurements. Only complete native
history supplies an amount, currency and scale; incomplete or unrecorded history
has an explicit status and no asserted amount. These values never replace the
Book carrying amounts. Complete journal entries remain available through the
entry-detail route for native movements, context and valuation provenance.

Reads use one owner-authorized database snapshot and a captured recorded-time
cutoff. They reject more than 10,000 selected accounts or 100,000 selected lines
instead of returning partial totals. The interface receives no data from another
Book or tenant, and bearer credentials cannot access these owner routes.
