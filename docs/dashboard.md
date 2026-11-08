# Dashboard reads

The Dashboard uses `GET /api/v1/state?view=dashboard` with explicit
`book_id`, `dashboard_from`, `dashboard_to` and `dashboard_unit`. The unit is
`day`, `week`, `month`, `quarter` or `year`. The owner-only response carries
`schema_version: 1`, a `state_revision`, and one `dashboard_snapshot` bound to
the selected Book and period. The snapshot contains the carrying position,
cash subtotal and account rows, signed recognition chart, prior-year activity
totals, and sampled trends. The browser applies the complete response together
with its dates. A foreign Book returns 404. Responses use `Cache-Control: no-store`.

The separate `GET /api/v1/dashboard`, `/dashboard/projection` with
`view=recognition`, and `/dashboard/cash-summary` reads take explicit `book_id`,
`from` and `to` dates. All Dashboard figures use exact Book-currency integer
strings with explicit currency and scale.

Recognition totals and chart buckets use accounting dates and the same accepted
posting rules as financial reports. Account identities are permanent IDs;
editable GL codes are separate. A projection describes whole civil dates, never
invented bank observations or intraday occurrence times. Unset timezone and
coverage facts remain unknown. A comparison before the supported date range is
explicitly unavailable.

Cash scope uses asset accounts explicitly assigned the `cash` role. Their total
is a recorded Book carrying subtotal with `cash_status: "partial"`; it does not
assert complete cash-equivalent scope or an observed bank balance. Without that
scope, `cash_status` is `unavailable` and monetary fields are absent, not zero.
`cash-summary` also returns `position_error` or `movement_error` when the related
measurement is unavailable.

Cash-account rows preserve `native_status` separately from carrying value. Native
currency, scale and amount remain absent where not recorded. Incomplete native
history has no asserted closing native amount. A known zero Book value can still
carry a nonzero native movement. Account revisions and retained posting facts
are bound into the snapshot revision.

Recorded money-in/out nets the cash sides within each entry. An own-cash transfer
that nets to zero contributes neither money in nor money out; a card payment
changes cash without recording a second expense. Explicit zero-native valuation
adjustments affect carrying value rather than money movement. A mixed movement
across multiple cash accounts with additional counterparts remains unavailable
until its transfer and valuation treatment is established. Its finding retains
entry IDs and exact amounts. Unspecified cash against equity stays included and
visible for opening/contribution review; names never exclude it.

The `cash_movement` financial report explains the same recorded changes with
posting-level contributions. It remains separate from formal cash-flow
classification, source statements and reconciliation.
