# Book settings

A Book keeps its original identifier, accounting currency, precision and creation
timestamp. Display and calendar settings are recorded as immutable revisions.
Each revision retains the authenticated actor, recording time, previous name,
accepted request and exact outcome. Renaming a Book changes its current display
name in owner listings, agent reads and connection consent without changing any
posted entry.

The owner or founder name is a display label. It does not establish a legal
identity or grant access. A personal or business profile is also an accepted
label, not registry verification.

## Unknown values

Older Books have no accepted profile, start date or accounting calendar. These
values remain absent until the owner supplies them. Vult does not infer a start
date from the first posting, choose a fiscal boundary or use the server's timezone
as an accounting timezone.

A start date describes the Book. It does not exclude earlier postings or establish
historical coverage. Opening, brought-forward and period contributions require
their own accounting facts.

A fiscal boundary consists of a month and day valid every year. February 29 is
not an annual boundary. The accounting timezone is a named timezone with its
transitions; server builds include timezone data. The host-dependent name `Local`
is unsupported. A client receives `calendar_configured: false` while either the
boundary or usable timezone is missing.

## Saving and retrying

Settings writes require a live owner session, a recent authentication proof and,
for browser requests, Origin and CSRF checks. Agent View and Manage connections
can read their Book's current settings. A write connection can change its Book's
logo through the scoped image operation.

A save includes the current metadata revision, starting at zero, and an
`idempotency_key`. A changed revision rejects a stale save. Reusing the same key
and request returns the recorded outcome; a different request with that key
conflicts. The original retry does not require a second recent proof or another
admission charge, but the owner session must still be live. An interrupted reply
can be resolved through its Book and original request key.

Name and owner display name are explicit on every save. Omitted optional fields
retain their accepted values. An empty string clears an optional text or date;
a zero month/day pair clears the fiscal boundary. JSON nulls are rejected.

## Book logo

The switcher shows the current logo from the owned Book catalog. The logo
reference contains `id`, `sha256`, `content_type` and `size`. When a Book has
no image, the interface shows its initials.

- `PUT /api/v1/book-identity/logo?book_id=…` accepts the exact bytes of a
  JPEG or PNG and an `X-Vult-Image-SHA256` header. Each side can be up to 256
  pixels and the encoded image up to 147,456 bytes. The engine verifies the
  hash, format and decoded image.
- `DELETE` at the same path removes the current logo. Writes return
  `schema_version`, `logo` and the Book's `metadata_revision`. Repeating
  the current image or an already applied removal keeps that revision.
- `GET /api/v1/book-identity/logo/{id}?book_id=…&revision={sha256}` serves
  the current exact bytes. Successful responses are private, immutable and
  vary by session cookie. Replacement gives the image a new ID. Requests
  for a superseded or removed ID return 404.
- `GET /api/v1/book-identity/logo?book_id=…` returns the current logo and up to
  20 changes, newest first, with actor ID, actor kind, recorded time and
  revision. Pass `next_before_revision` as `before_revision` for the next
  page.

Browser writes need a live owner session, Origin and CSRF. Remote write
connections can use the same PUT and DELETE with their Book-scoped bearer
credential and an optional `Idempotency-Key`. Agent calls also work through
`set_book_logo` in MCP, including local connections. Only the signed-in
owner can change their personal profile image.

## Backup and recovery

Current snapshots and portable archives include settings revisions, retained
outcomes and the current Book logo bytes. Logo history retains references and
provenance for earlier changes. Restore verifies the original header, revision chain, accepted
fields and complete rows before committing. It does not restore sign-in sessions
or connection authority.

The original version-1 formats and preceding version-2 currency formats remain
readable with their original table inventories. Portable archives must include
the exact corresponding migration checksums. An older backup establishes no
settings revisions; recovery does not manufacture them.
