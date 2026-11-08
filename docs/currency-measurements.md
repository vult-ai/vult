# Currency measurements

A journal line's `amount_minor` remains its recorded value in the Book's currency
and precision. It is a nonnegative integer string; debit or credit supplies its
sign. Existing amounts keep their original meaning.

A line can also contain two independent kinds of measurement:

- `native` moves an account's balance in its accepted denomination. Currency,
  scale, quantity and the immutable account unit revision travel together.
- `contexts` describe amounts such as an invoice denomination. They do not move
  any native balance. Each context has a role, amount and optional source text.

Native and contextual denominations use the versioned currency catalog. A Book
can retain its historical precision even when it differs from the current
catalog's minor unit. That Book precision never defines an invoice denomination
or the base unit of an exchange quote.

## Account history and completeness

Account currency and role changes are immutable account revisions. Display
renames preserve those definitions. Changing a denomination never converts or
relabels earlier movements, and an exact reversal uses the original definition.

When creating a new ledger account, `native_origin: "empty_ledger"` explicitly
establishes a zero starting position. It is a statement about the new ledger
account, not proof that a bank account contains no money. Omit it when the
starting position is not established.

An account with missing native history reports `status: "incomplete"`, with no
asserted native total. Assigning a currency does not fill that gap. A calculation
that consumes a native carrying position is unavailable while the position is
incomplete. Known zero movement and absent movement remain different facts.

## Valuation basis

Every newly posted line retains a valuation basis and policy:

| Basis | Retained inputs | Policy |
| --- | --- | --- |
| `manual` | Supplied Book amount | `supplied-book-value-v1` |
| `actual_source` | Actual source-provided Book amount | `actual-source-book-value-v1` |
| `applied_quote` | Denominated input, exact quote, dates and provenance | `quoted-rate-half-even-v1` |
| `historical_carrying` | Accepted cash position and amount consumed | `moving-average-cash-v1` |
| `authoritative_allocation` | Entry allocation group and authoritative Book total | `largest-remainder-v1` |

Omitting a valuation on a new API request records the supplied Book value as a
manual input. Historical lines that never recorded a basis remain unknown; reads
and reversals do not invent one. Source-provided zero with an unknown conversion
method is distinct from a quote calculation that rounded to zero.

A quote states that one unit of `base` equals `rate` units of `quote`. The rate is
an exact positive decimal string, with up to 64 integer and 64 fractional digits;
it is not a scaled money amount. Its observation and requested dates and supplied
provenance are retained. Supplied provenance is not verification of a publisher.
The ledger does not infer a quote by dividing two accepted amounts.

Conversions round once per independent event, to the Book minor unit, using
half-even rounding. For example, JPY 1 at a hypothetical USD 0.001 per JPY records
USD 0.00 while preserving the JPY 1 movement. Twenty independent events valued
at USD 0.004 each still record USD 0.00 in total; a separate valuation of the
remaining JPY 20 would be USD 0.08. Changing transport batch size changes neither
accepted result.

## Operation constraints and residuals

An entry can have zero Book debits and credits when it has a real native monetary
movement. Context alone cannot create an empty monetary operation. A zero-valued
expense counterpart does not acquire an invented native denomination.

`measurement_policy.native_transfers` declares fee-free transfers by line
position. Each group requires known movements in the same native unit and
balanced native and Book totals. A JPY 2 movement cannot be paired with JPY 1
merely because both Book values round to zero.

An authoritative allocation retains its total and positive integer weights. The
largest-remainder rule assigns floor shares, then remaining minor units by
descending fractional remainder, breaking ties by ascending line position. One
cent shared equally among positions 1, 2 and 3 therefore becomes `[1, 0, 0]`.
The members have one debit or credit side and sum exactly to the supplied total.

The cash allocator applies only to an account classified as `asset` with an
accepted `cash` role. It preserves the unit and role revisions, the latest
accepted account sequence, the native and Book position before consumption, and
both amounts consumed. Partial consumption uses half-even rounding; final
consumption releases the entire remaining Book amount. One account can appear
only once in an entry that consumes its carrying value. This avoids separately
rounding multiple uses of the same position.

Allocation order follows acceptance sequence, not economic date. A backdated
acquisition accepted later cannot rewrite an earlier allocation. Identified
receivables and payables do not use this pooled-cash rule.

A valuation-only adjustment records a nonzero Book effect and a known zero native
movement. Such an entry is separate from a calculation of a remaining position;
recorded postings are never silently replaced by a later valuation. Reporting
under a particular accounting framework requires its supported measurement and
presentation policies; these primitives do not assert compliance.

## Integrity and recovery

Requests retain their canonical input and accepted outcome. A retry recovers that
outcome before consulting current account settings. Reversals mirror the original
native movements, contextual amounts, valuation inputs and allocation policy.
Book and complete native balance prefixes remain within signed 64-bit bounds;
intermediate arithmetic uses wider exact values.

New snapshots and portable archives use version 5, which retains Book addresses
and legal identity history. A version 5 portable archive requires a recognized
migration chain containing the v0.4 migration, which also supports owner-retained
originals. Released version 1 portable archives use the three original migration
receipts. Ledger snapshots use their own format checks and accept versions 1
through 5. Older snapshots receive a fixed address when restored.
Only newly absent optional columns are normalized; complete retained rows,
receipts, document hashes and financial constraints are still verified.
Unrecognized migration sets and inconsistent measurements are rejected atomically.
