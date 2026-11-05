# Documents, sources and evidence

Vult stores original bytes without interpreting, rendering or extracting text
from them. Agents read documents and decide what accounting those documents
support. Documents belong to one tenant and one Book; identical content in two
Books remains two separate documents.

An original has a permanent ID, SHA-256 and byte count. Uploads declare those
values before streaming. A completed document becomes available only after its
whole length and hash have been verified and encrypted storage has committed.
Pending upload records support retries; they are not usable documents.
Retained outcomes describe the original attempt; inspect the document to learn
its current availability. An unused original expires after 30 days. Reuploading
the same content keeps its document identity and obtains a new storage lease.

The document limit is 32 MiB. Supported media types are PDF, PNG, JPEG, WebP and
CSV. Downloads always use a fixed attachment name, `application/octet-stream`,
`nosniff`, and no caching. Content is untrusted, regardless of its declared type.

## Storage and keys

Originals are encrypted with age streaming encryption. An authenticated manifest
inside each encrypted object binds its tenant, Book, document, storage identity,
key version, size and SHA-256. Storage receives only opaque object identifiers.
Keys are supplied by the service, outside the ledger database. A key provider
must enforce tenant ownership and retain old keys while originals use them.

The native service provisions tenant key files in its private data directory.
It checks existing document key references before provisioning: missing historic
keys stop startup for recovery. Reinstall and upgrades reuse those files.
Server operators provision a private versioned key file explicitly; opening
storage never silently generates a replacement key. Keep an offline recovery
copy and restrict it to the dedicated service account.

The disk backend uses a private, service-owned directory. It stages ciphertext,
verifies the input, synchronizes the file and publishes it without replacing any
existing object. Failed writes never publish partial ciphertext. An object
storage implementation must provide the same atomic publication semantics.

Successful streaming reads require the final authenticated frame and exact
original hash. Callers discard partial downloads on any error. Diagnostic errors
never include original filenames, document contents, storage paths or keys.

## File transfer

A local MCP companion reads only explicitly approved import files and writes
only to an approved export directory. It moves bytes over a Unix socket for a
local installation. HTTPS clients transfer bytes with the server's short-lived
upload/download capabilities. Tool JSON carries metadata rather
than a base64 copy of the document. A server never opens a client-supplied local
path. A remote-only client must implement the upload/download capability flow;
returning an upload URL alone does not transfer a file.

## Sources and evidence

Posting may cite several documents and structured page, row or line locations,
or none. Original citations remain recorded. A citation correction appends an
actor, time and reason; correcting a citation does not reverse valid accounting.

Evidence is a separate, explicitly typed relationship. Attachments and
detachments append history. A statement source is never automatically treated
as a receipt. Repeated use of one document location by several active entries is
reported for inspection, without prohibiting legitimate splits.

Evidence roles are `receipt`, `invoice`, `contract`, `statement` and `other`.
The missing-evidence query can select one role; a statement never satisfies a
receipt query. With no role selected it reports entries without any active
evidence link. This is a query about recorded links, not a rule requiring every
entry to have evidence. Account and date filters use the entry's accounting date.
Both source revisions and evidence history have bounded, independent cursors.

Only originals that have never been used may expire. An earlier source or
evidence link protects an original even after detachment. Expiry must serialize
with posting, attachment and backup. This rule governs ordinary operations; it
does not grant a tenant-erasure capability to owners or agents.

The native service reclaims unused expired originals at startup and hourly, in
oldest-first batches of at most 20 Books and 100 originals per Book. Each sweep
has a 30-second deadline; a busy Book can hold it for at most five seconds.
A failed Book does not stop the other selected Books, and diagnostics report
only that cleanup needs retrying. Server operators can enable the supplied
[isolated expiry timer](server.md#automatic-server-expiry) for the offline
`expire-documents` command; the online server receives no installation credentials.
Pending uploads expire after 15 minutes; completed unused originals after 30
days. A prior source or evidence link permanently excludes ordinary expiry.

Backups and portable exports must include documents and relationship history.
Restore must check every original hash and work with a new storage key.
Uninstalling or upgrading keeps documents and keys along with the Books.

The portable archive contains a bounded metadata manifest, opaque original-byte
members and a mandatory completion record. It includes source/evidence history
and retained request outcomes, but excludes connection grants. Metadata is
limited to 64 MiB and the complete archive to 8 GiB. Backups encrypt this format;
plain portable exports contain sensitive financial information.

Restore requires an empty installation. It verifies every original, assigns new
ciphertext storage identities and encrypts with the destination's keys without
changing document IDs, hashes, journal rows or relationship history. Unused
content already expired at backup time remains unavailable; its identity and
history still survive. Partial or failed output must be discarded. A failed
restore rolls back its database work and removes its newly staged objects; an
uncertain commit retains them until the operator resolves the outcome.

On macOS, `vult backup --output books.age` streams the encrypted archive through
the protected service. `vult restore --input books.age` accepts both this format
and earlier encrypted finance-only backups. Recovery asks for the backup's
recovery key in the owner's terminal; it never accepts that key from an agent
tool. `vult export --documents --book BOOK_ID --output book.tar` writes a
plaintext portable archive for one Book. Native approval identifies that effect.

Outputs remain in private staging until the complete stream and final service
result succeed. Existing destinations are never overwritten. Restore binds
native approval to the encrypted input's exact size and SHA-256, verifies its
authenticated end, and never imports multiple tenants into a local installation.
