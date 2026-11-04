# Ledger model

The authoritative definition is [0001_ledger.sql](../ledger/migrations/0001_ledger.sql).
There are nine domain tables and one installation receipt table:

| Table | Authoritative fact |
| --- | --- |
| `tenants` | Ownership boundary; one tenant in a local installation |
| `books` | Fixed Book identity, currency and minor-unit scale |
| `connections` | A named, scoped, revocable actor grant |
| `accounts` | Permanent account identity and accounting class |
| `account_revisions` | Append-only names and display codes |
| `requests` | Canonical request bytes and exact recorded result |
| `entries` | Journal identity, actor, dates and reversal relationship |
| `lines` | Ordered debits/credits against permanent account identities |
| `closed_periods` | Inclusive accounting-date ranges closed to posting |
| `schema_receipts` | Installation migration filename and checksum |

Tenant-owned relationships include the tenant and Book in their foreign keys.
An account, line, receipt or reversal cannot point across a Book. The local
service has separate administration and restricted runtime database credentials.
Only the runtime credential is used for agent accounting calls.

The runtime must use READ COMMITTED for writes. Writes update and lock the Book
until commit; deferred checks then see preceding committed changes. The database
rejects writes under snapshot isolation, including REPEATABLE READ. Reads hold a
shared Book lock so multi-query responses describe one coherent state.

A journal has 2–256 positive debit/credit lines, balanced exactly. Posting is one
transaction, including its durable request receipt. Entries and lines cannot be
updated or deleted, and later transactions cannot append lines to an old entry.
Recorded results are checked against their journal values. Balances cannot exceed
signed 64-bit range at any retained posting sequence.

Economic date describes the event; accounting date selects its accounting period;
recorded time is assigned by the service. A reversal preserves the original
economic date and every line's account, amount, memo and ordering, switches sides,
and links each new line to its original. Its accounting date must be open. One
entry can have at most one direct reversal.

An account's `asset`, `liability`, `equity`, `income` or `expense` class is fixed.
Append-only revisions change only its name and display code. Journal lines retain
permanent account IDs. Request keys are unique per actor and Book and retain
exact result bytes, including timestamps and generated identifiers.

Restore is an explicit administrative operation into an empty installation.
It preserves every backed-up row, verifies equality before commit, advances the
local sequence counter, and leaves connection grants revoked. It does not
recalculate journal entries through business templates.

Released migrations and releases are immutable. Later versions may extend or
change the model through new forward migrations with tested upgrades.
