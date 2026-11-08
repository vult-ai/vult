# MCP connections

The local macOS service uses MCP stdio. A client starts the protected `vult` executable
and exchanges JSON-RPC messages on stdin/stdout. Diagnostics use stderr. There is
no network listener in local mode or client-specific Vult command.

Create a connection from your own terminal:

```sh
vult connection create --name "Personal assistant" --book BOOK_ID --access write
```

Use its returned connection UUID in the client configuration:

```json
{
  "mcpServers": {
    "vult": {
      "command": "/Library/PrivilegedHelperTools/ai.vult.ledger/vult",
      "args": ["mcp", "--connection", "CONNECTION_ID"]
    }
  }
}
```

The example is the generic JSON shape used by many MCP clients. For Claude Code,
Claude Desktop and Codex, put the same executable and argument array into that
product's MCP configuration format. Do not add credentials, recovery keys,
database URLs or environment overrides. A connection name is just a label.

For Codex, the equivalent `config.toml` entry is:

```toml
[mcp_servers.vult]
command = "/Library/PrivilegedHelperTools/ai.vult.ledger/vult"
args = ["mcp", "--connection", "CONNECTION_ID"]
startup_timeout_sec = 90
```

Allow time for the native approval prompt when configuring a client startup
timeout. For Claude Code, `MCP_TIMEOUT=90000 claude` allows 90 seconds.

Starting a bridge requests native administrator approval. Approving a session
permits that bridge to use the connection for up to eight hours. The credential
stays in memory, is bound to the calling OS user, and is lost when the service
restarts. Reconnect to obtain another approval when a session expires.

## Remote server connections

A separately configured Linux server exposes Streamable HTTP MCP at
`https://ledger.example.com/mcp`, or at the root of its `mcp_origin` when one is
configured, such as `https://mcp.ledger.example.com`. Use that URL in your
client's remote MCP configuration. The client must support OAuth discovery, public-client
registration, S256 PKCE and the exact MCP resource. Sign in through the server's
HTTPS login, select an owned Book and choose Read or Write before consenting.
Ledger and proposal tools use the selected Book and permission. Configuring
document storage adds original-file and evidence tools; Write never includes
owner administration.

Do not put access credentials in a URL or copy browser cookies into an agent.
Access credentials expire after 15 minutes; refresh credentials rotate on use.
Reconnect can retain an existing connection actor and its request outcomes.
See [remote agent authorization](server.md#remote-agents) for scope, revocation
and recovery rules. The local macOS bridge and its native approval are unchanged.

## Original files

For local file access, add explicit absolute import and export directories to the
companion command:

```sh
vult mcp --connection CONNECTION_ID --import-dir /path/to/imports --export-dir /path/to/exports
```

Without `--export-dir`, copies go to `~/Library/Application Support/Vult/Documents`,
which only you can open. Each time the companion starts it removes copies there
made more than a day earlier, and `vult uninstall` removes the folder. A folder
you choose yourself is never cleared.

Create the export directory with private permissions, for example
`mkdir -m 700 /path/to/exports`; shared folders and extended macOS ACLs are
refused. The native approval names those folders. The companion reads only inside the
approved import directory and creates new files only in the export directory.
It rejects traversal, symlinks at the selected input, hard links and special
files. Originals are limited to 32 MiB; supported types are PDF, PNG, JPEG,
WebP and CSV. Their contents and names are untrusted data, never instructions.
Without configured folders, metadata queries work but file access is denied.

`add_document` takes `request_key`, a relative `file` inside the import folder,
and an optional `media_type`. The companion computes the exact size and SHA-256,
prepares an intake and streams bytes outside MCP JSON. A successful result is a
verified completed upload. Retrying a changed file with the same key is rejected.
If a response is lost, inspect the original key and intake; never invent a new
key while its outcome is uncertain.

`get_document` takes `document_id` and may take an `output` filename. Exports
become visible only after exact bytes/hash and final service confirmation. They
never replace existing files. A read connection can export its own Book's
originals, but cannot upload or alter citations/evidence.

Remote-only MCP clients use metadata instead: prepare with `add_document`, then
inspect `document.id` and `upload_id` through `get_document`. A pending upload
returns a short-lived, single-use `upload_url`; PUT the exact declared bytes with
`Content-Type: application/octet-stream`. Inspect the same intake again to
recover its recorded completion. Ready originals provide a one-use
`download_url`. These URLs are credentials: do not log, share, cache or attach an
Authorization header to them. Each transfer rechecks the original agent's live
authority. A URL alone does not mean a client has transferred a file.

## Tool catalog

| Tool | Access | Purpose |
| --- | --- | --- |
| `get_book` | read | Currency, precision and selected Book identity |
| `list_accounts` | read | Permanent IDs and current display revisions |
| `get_journal` | read | One entry and every line |
| `list_journals` | read | Sequence-ordered pages: up to 100 locally, 20 remotely |
| `get_balance` | read | Signed debit-positive minor-unit balance |
| `get_trial_balance` | read | All balances, optionally through an accounting date |
| `get_request_outcome` | read | This connection actor's retained request result |
| `create_account` | write | Create an account and its first display revision |
| `revise_account` | write | Change labels or accounting role using an expected revision |
| `set_book_logo` | write | Set or remove the image shown beside the Book in the switcher |
| `post_journal` | write | Post general debits and credits atomically |
| `reverse_journal` | write | Post a complete linked reversal |
| `prepare_operation` | write | Retain a validated journal or reversal preview without posting |
| `get_operation_proposal` | read | Inspect exact work, its decision and any committed outcome |
| `execute_proposal` | write | Complete a proposal approved under the earlier executor contract |
| `add_document` | write | Prepare an original; local companion also streams its bytes |
| `get_document` | read | Inspect an original and obtain its scoped byte transfer |
| `list_documents` | read | Page through available verified originals |
| `correct_source_citation` | write | Append a citation correction without changing accounting |
| `attach_evidence` | write | Append an explicitly typed supporting relationship |
| `detach_evidence` | write | Append a reasoned detachment while retaining history |
| `get_entry_documents` | read | Read current sources/evidence or paged history |
| `get_source_usage` | read | Inspect repeated use of one document location |
| `list_entries_missing_evidence` | read | Report gaps by account, accounting date and role |
| `wait_for_document` | read | Local only: wait up to 45 seconds for an upload no entry uses yet |
| `report_unusable_document` | write | Local only: tell the owner why an original can't be recorded |

Account IDs are permanent; account codes are optional display attributes. Omit
`code` or supply `""` when creating an account without one. Revisions require
`code` explicitly: retain the current value, assign a supported code, or use
`""` to clear it. Empty codes are not unique; nonempty codes remain unique within
the Book. A code change never changes existing postings or receipts.

On this computer, the service also keeps a short record of what each agent
does in a Book: the originals it opens, the accounts and entries it writes, the
duplicate and balance checks it runs and the work it sends for review. The
owner watches it in the Book. It holds no document contents and ends when the
service restarts.

Tools do not take tenant IDs, Book IDs or actor IDs. Those come from the
connection. Document-enabled write connections expose the full catalog;
document tools are omitted when storage is disabled.
Read-only connections omit write tools; the service also enforces that boundary
if a client invents a call. `post_journal` accepts optional `sources` only when
documents are enabled; existing no-source inputs retain their behavior.

The repository includes a [bookkeeping skill](../skills/vult-bookkeeping/SKILL.md)
covering accounts, dates, common postings, request recovery, source/evidence
relationships and statement comparisons. Loading a skill does not grant
accounting permission or replace the operation's validation.

## Exact money and retries

For a Book with currency USD and scale 2, a debit of `"1250"` is 12.50 USD.
Book amounts are nonnegative decimal integer strings, with separate `debit` or
`credit` sides. Decimal numbers, exponent notation and amounts beyond signed
64-bit range are rejected. An all-zero Book entry requires a real native monetary
movement; contextual amounts alone do not establish one. Account balances must
remain in range; intermediate arithmetic uses wider exact values. See
[currency measurements](currency-measurements.md) for native quantities,
valuation bases and rounding.

Each write needs a nonempty `request_key`, at most 128 bytes. Reuse the exact key
and values on retries. Keys belong to the connection actor in its Book. Changed
input with a previously recorded key is a conflict. JSON whitespace and object
key order do not affect the typed canonical request; duplicate keys, unknown
fields and invalid Unicode are rejected.

After a lost reply, use `get_request_outcome` with the original key. `recorded`
contains the saved operation and result. `unresolved` makes no claim that the
operation failed. Retry only the original unchanged operation and key. The
bridge never automatically invents another key or repeats a failed call.

Backups retain request receipts and actor IDs, but deliberately exclude grants.
After restore, new connections have new actors; their outcome lookup does not
impersonate a former connection.

## Prepared operations and owner decisions

`prepare_operation` takes a preparation `request_key`, an `operation`
(`post_journal` or `reverse_journal`) and that operation's typed `arguments`.
The arguments include a separate, fixed financial request key. Preparation
validates through the posting constraints and retains the exact inputs, derived
effects, sources, assumptions and open questions. It posts nothing. Source reuse
is visible without prohibiting legitimate splits or later amortization.

A proposal waits in Vult until the owner decides; it has no time limit. Each
agent can have up to 128 undecided proposals in a Book. The proposal's
`approval` contains a relative review `href` for this installation and a plain
`message` the agent can show the owner. The `href` identifies that exact Book
and proposal; it is not a credential or permission. The browser requires the
owner's current sign-in and ownership. If a fresh sign-in is needed, it returns
to the same review. Signing in never approves or posts automatically.

The owner decides in their own Vult session, never through an agent. A decision
binds its Book, ID and SHA-256 and an explicit reason, and requires a sign-in
from the preceding five minutes. An approval posts the exact proposed entry in
the same transaction. Vult first rechecks the proposal's dependencies: a changed
account currency, role or origin, changed use of its cited sources, a reversal
or a closed period refuses the approval as stale (HTTP 409), and nothing is
recorded or posted. Other entries moving an account's balance do not, so an
owner can approve several proposals on one account in any order; the review
shows each account's balance as it is now. An unrelated account rename does not
invalidate it either. Ordinary deployment admission applies to
both the decision and the posting. A proposal posts at most once.

The posted entry's receipt names the preparing agent's actor under the frozen
financial request key, so the agent retrying that request receives the posted
entry instead of posting again. The decision records the approving owner, the
reason and the time; the approval's consumption links the decision to the
receipt and names the owner who posted it. The owner can still approve after
disconnecting the preparing agent. A rejection records its reason and posts
nothing. Reasoned replacement decisions append revisions.

`GET /api/v1/agent-owner/plans?book_id=…` returns `waiting`, the number of
proposals still needing a decision. Add `status=approval_required` to list only
those. `POST /api/v1/agent-owner/approve` returns the `decision`, a `status` of
`committed` and the posted `outcome`; `POST /api/v1/agent-owner/reject` returns
the decision with status `rejected`. An exact retry of either returns the same
response without another fresh sign-in.

Inspect `get_operation_proposal` to learn the outcome. Supply its
`proposal_id`, or your original preparation `request_key` to recover a missing
preparation reply. `committed` carries the posted entry, `rejected` the owner's
reason and `stale` means a fresh proposal is needed. Authorized Book readers can
inspect a known proposal and its outcome after the original agent is gone.

Proposals and approvals recorded under the earlier contract keep their recorded
expiry. Such an approval named an executor connection, which completes it with
`execute_proposal` and the exact proposal ID and SHA-256. An exact execution
retry returns the retained result.

Backup and restore retain proposals, decisions, consumptions and their posted
entries without restoring connection grants or reusable approval authority.

### Book presentation

Use the `receivable` role on an asset account that represents money owed to
the Book. The Dashboard's “Owed to you” view uses the recorded role.

`set_book_logo` takes a `request_key`, `content_type`, `sha256`, and
`image_base64` for a JPEG or PNG you already have. Each side is at most
256 pixels and the decoded base64 is at most 147,456 bytes. To remove the
image, pass `request_key` and `remove: true`. An exact retry returns the
retained outcome for the same Book and actor.
