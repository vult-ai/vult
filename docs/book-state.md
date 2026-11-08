# Book state reads

`GET /api/v1/state` is a session-only read for the bundled interface. Every
request selects an owned `book_id` and an explicit `view`.

`transactions` returns the filtered Activity page described in
[Recorded Activity](activity.md). `accounts` and `dashboard` require explicit
`dashboard_from` and `dashboard_to` dates. Transaction filters do not silently
narrow those views. `settings`, `documents` and `statements` return metadata
bootstrap state without reading financial history. They do not fabricate empty
balances or imply that the corresponding feature's records have been loaded.

The response carries version 1, its `read_view`, Book metadata, the bounded
account catalog and a revision. The Dashboard's `dashboard_unit` parameter
requests a [Dashboard snapshot](dashboard.md) with its period, figures and
comparisons. Accounts and Dashboard reads without that parameter carry
`as_of`, `period`, `account_balances`, `position` and `summary`.

Account balance identity fields contain permanent IDs. `gl_code` is optional and
editable. `kind` is the authoritative stored class; `role` is separately retained.
`balance_minor` is the debit-positive recorded Book carrying value, while
`period_change_minor` covers the requested accounting-date window. Native
currency, scale, amount and `native_status` are separate measurements. Incomplete
or unrecorded native history has no asserted native balance.

`position` contains recorded assets, liabilities, posted equity, accumulated
results and net assets in the Book currency. The mathematical equation does not
confirm historical completeness. `source_coverage` remains `unknown`. Book start
and unknown fiscal/calendar facts never establish a zero opening position.

Cash is only the carrying subtotal of explicit cash-role asset accounts. Without
that scope, cash is absent and `cash_status` is `unavailable`. A known subtotal is
labelled `partial`. Missing values are never returned as invented zeroes.

The hierarchy basis is `flat_recorded_accounts`. Names containing colons or other
separators do not establish parent relationships. Metadata-only states leave
financial fields unrequested; callers must use the response's view rather than
treating absent financial data as empty history.
