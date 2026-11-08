# Owner settings and downloads

These routes connect the bundled interface to the engine. Their contract can
change with the bundled interface.

All routes require a live owner session and reject Authorization headers.
POST requests require the installation Origin and the session's X-CSRF-Token.
Connection revocation and Book changes use the same recent sign-in proof as
other owner administration. Book requests require an explicit owned UUID. An
unowned Book or connection returns 404.

JSON responses include `schema_version: 1`. Downloads and avatars carry
`X-Vult-Schema-Version: 1` alongside their original payload. Responses use
`Cache-Control: no-store`. JSON writes reject unknown, duplicate and differently
cased keys and reject null values except for explicit avatar removal.

## Personal settings and photo

`GET /api/v1/me/settings` returns `user_id` from the same verified identity as
`/auth/session`, a decimal-string `revision`, and the profile, preferences and
appearance. Settings are independent of Books and remain available across owner
sessions on the installation. A new owner starts at revision `"0"`.

The profile contains `version: 1`, `display_name` and `photo: null`. When a photo
exists, `photo_asset` adds `id`, `sha256`, `content_type` and `size`. Appearance
is `system`, `light` or `dark` and defaults to `system`. The preferences object
contains `version: 1`, `language: "en"`, `regional_format`, `date_format`,
`time_format`, `digit_grouping` and `negative_style`. New settings use `en-US`,
`standard`, `24h`, grouping enabled and `minus`. Regional format uses the
bundled interface's 32 supported values. Date format also accepts
`month-day-year`, `day-month-year` and `iso`. Time format also accepts `12h`,
and negative style also accepts `parentheses`.

`POST /api/v1/me/settings` accepts `expected_revision` and partial `profile`,
`preferences` and `appearance` values. Display names are bounded to 80 UTF-16
units without control characters. A stale revision returns 409. An accepted
change increments the revision once, and the response contains the complete
settings. Concurrent saves serialize at the owner record.

A photo change accepts `profile.photo` as a prepared 256 × 256 JPEG data URL
up to 196,608 characters and 147,456 decoded bytes, or null to remove it.
`GET /api/v1/me/avatar/{id}?revision={sha256}` returns the current JPEG with
`nosniff` and `private, no-store`. The asset ID and digest must match the signed-in
owner's current photo. Replacement and removal retire the previous locator.

## Dashboard period

`GET /api/v1/books` includes an opaque `view_scope` for the signed-in owner
and a `views` object keyed by Book UUID. Each value has `period` and
`range: {from, to}`. The object contains saved selections for visible Books.
An owner with no saved selections receives an empty object.

`POST /api/v1/book-view-preference` accepts `book_id`, `scope` from the catalog
and `selection: {period, range: {from, to}}`. Use a Dashboard preset or
`custom` as the period. Both dates use `YYYY-MM-DD`, with `from` on or before
`to`. A mismatched scope returns 403. A save returns `status: "saved"` and
replaces the owner's choice for that Book across sessions on the installation.
This preference lives in personal settings outside the Book's data and
revision history.

## Agent connections

`GET /api/v1/access/tokens` returns up to 1,000 active connections in `tokens`.
Each contains its ID, name, `connection: true`, creation time and its single
Book grant. `expires_at` is null for the durable grant. A read grant exposes
role `view` and right `read`. A write grant exposes role `manage` and rights
`read` and `write`.

`GET /api/v1/access/tokens/{id}/books` returns the connection ID, name, string
revision and a one-element `books` list containing `book_id`, `name` and `role`.
Revision `"1"` represents an active grant and `"2"` a revoked grant. The Book
state marker describes the Book name read with the grant.

`POST /api/v1/access/tokens/{id}/revoke` accepts an empty body or `{}` and returns
`status: "revoked"`. It uses the same revocation operation as the CLI. Repeating
a revocation succeeds with the same recorded status.

## Book identity and address

`GET /api/v1/book-identity?book_id={id}` returns `subject`, `history`, `has_more`
and `next_offset`, with the recorded Book state marker. The subject has its
Book UUID, nullable `kind`, numeric `revision` and `identity`. Revisions retain
prior and new identities and kinds, the owner actor, reason, optional effective
date and recorded time. History is newest first, ten records per page. The
optional `offset` accepts 0 through 10,000.

`POST /api/v1/book-identity` accepts `book_id`, `revision`, `identity`, `reason`,
`idempotency_key`, optional `kind` and optional `effective_date`. Omit kind or
send an empty string to leave it unclassified. Omit the effective date or send
an empty string when it is unknown. A write validates catalog selections,
retains the catalog labels with the revision and returns a receipt with
`schema_version`, `book_id`, `revision`, `state_revision` and
`state_captured_at`. Use GET for the updated identity and history. Reusing the
same key and payload returns the result originally recorded for that key.
A stale revision or conflicting key returns 409.

`GET /api/v1/book-address?book_id={id}` returns `book_id`, `code`, `slug`,
numeric `revision`, `aliases` and the recorded Book state marker. The fixed code
and previous slugs remain aliases. `GET /api/v1/books` includes an `addresses`
array so the interface can resolve links from its owned Book catalog. If Books
with matching slugs come under one owner after recovery, the interface uses the
fixed code for an ambiguous current slug. Retained aliases stay in the archive.

`POST /api/v1/book-address` accepts `book_id`, `revision` and `slug`. If the query
also supplies `book_id`, the selectors must agree. Slugs are normalized to
lowercase and use 2–63 ASCII letters, digits or single separating hyphens.
Reserved routes and four-character code shapes are invalid. An empty slug
selects the fixed code. A Book can retain up to 256 custom aliases. A competing
address or a stale revision for a different slug returns 409. Retrying the
current slug returns the current address.

## Book downloads

Each download is a GET with `?book_id={id}` and returns an attachment with a
fixed filename. `X-Vult-State-Revision` and `X-Vult-State-Captured-At` describe
the exported snapshot. Each listener generates one encrypted download at a
time. Up to four downloads can have open spools, including the one being
generated. The server checks current Book ownership again before sending a
completed download. Transfers time out after 15 seconds without write progress.
See the [server instructions](server.md#run-behind-https) for the staging directory.

| Route under `/api/v1/exports/` | Contents |
| --- | --- |
| `book.tar` | Portable plaintext archive from the CLI archive writer, including originals, relationship history, Book identity revisions and addresses. |
| `journal.json` | CLI journal JSON, including account and entry detail. |
| `transactions.csv` | Entries with economic and accounting dates, the exact debit total in minor units and a line count. |
| `journal-lines.csv` | Posting lines with IDs, debit or credit side, exact minor units and native amounts where recorded. |
| `accounts.csv` | Account IDs, codes, names, classes, revisions and native units. |

Archive limits match the CLI at 64 MiB of metadata and 8 GiB total. Journal JSON
is bounded to 64 MiB. CSV is bounded to 32 MiB per file. JSON and CSV accept up
to 100,000 records of each exported kind. Spreadsheet text that could be read as
a formula is marked as literal. Book downloads contain the selected Book's data.
