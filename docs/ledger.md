# Ledger model

The ledger definition is [0001_ledger.sql](../ledger/migrations/0001_ledger.sql).
There are nine ledger tables and one installation receipt table:

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

`ledger.Admin` is installation-wide authority for a trusted operator or native
local owner. Applications serving tenant owners use `Admin.ForTenant` to obtain
a `ledger.TenantAdmin`, whose operations always select one tenant. Its scope must
come from application-verified ownership, never an unverified request parameter
or identity-provider role claim. Binding a tenant is not authentication or a
membership check. This API requires trusted administration database credentials;
it does not replace runtime row-level security or authorize a remote session.
It has no tenant provisioning, installation backup or restore methods.

[0002_server_access.sql](../ledger/migrations/0002_server_access.sql) adds verified
identity mappings, one owner per tenant and bounded, revocable sessions. Remote
owner operations use `access.Service.WithOwner` with separate restricted
credentials. Row policies require a live session, origin and stored ownership;
selecting a tenant UUID cannot grant access. Session and ownership rows remain
locked through completion, including concurrent revocation. Owner administration
needs authentication from the preceding five minutes. Sources uploads use the
live owner session and CSRF proof. Installation provisioning and identity
recovery remain offline operator actions.

Migrations apply in filename order. Existing receipt checksums and sequence must
match before any forward change. Vult 0.4 upgrades the released v0.1, v0.2 and
v0.3 ledgers through one v0.4 migration, [0004_recorded_book.sql](../ledger/migrations/0004_recorded_book.sql),
which includes owner document retention. The three published migrations keep
their original checksums. An unrecognized migration receipt requires recovery.
The original ledger checksum remains the v1
data-backup identifier; authentication and connection grants are separate from
the financial snapshot and must not be revived by data-only restore.

The runtime must use READ COMMITTED for writes. Writes update and lock the Book
until commit; deferred checks then see preceding committed changes. The database
rejects writes under snapshot isolation, including REPEATABLE READ. Reads hold a
shared Book lock so multi-query responses describe one coherent state.

A journal has 2–256 positive debit/credit lines, balanced exactly. Posting is one
transaction, including its durable request receipt. Entries and lines cannot be
updated or deleted, and later transactions cannot append lines to an old entry.
Recorded results are checked against their journal values. Balances cannot exceed
signed 64-bit range at any retained posting sequence.

[0003_documents.sql](../ledger/migrations/0003_documents.sql) also checks
each inserted line against its immutable receipt. Running deferred constraints
early cannot make an entry accept additional lines later in the transaction.
Historical range validation retains every posting prefix and checks live write
access before its scoped scan. It runs with the migration owner's read access;
other journal checks still use the caller's restricted role and row policies.
The trusted restore path runs the full range check once per completed Book,
after every entry's other constraints and before committing any restored rows.

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
