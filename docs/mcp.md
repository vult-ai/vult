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
`https://ledger.example.com/mcp`. Use that URL in your client's remote MCP
configuration. The client must support OAuth discovery, public-client
registration, S256 PKCE and the exact MCP resource. Sign in through the server's
HTTPS login, select an owned Book and choose Read or Write before consenting.
The original eleven tools remain available. Configuring document storage adds
nine tools; Write never includes owner administration.

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
| `revise_account` | write | Change name/code using an expected revision |
| `post_journal` | write | Post general debits and credits atomically |
| `reverse_journal` | write | Post a complete linked reversal |
| `add_document` | write | Prepare an original; local companion also streams its bytes |
| `get_document` | read | Inspect an original and obtain its scoped byte transfer |
| `list_documents` | read | Page through available verified originals |
| `correct_source_citation` | write | Append a citation correction without changing accounting |
| `attach_evidence` | write | Append an explicitly typed supporting relationship |
| `detach_evidence` | write | Append a reasoned detachment while retaining history |
| `get_entry_documents` | read | Read current sources/evidence or paged history |
| `get_source_usage` | read | Inspect repeated use of one document location |
| `list_entries_missing_evidence` | read | Report gaps by account, accounting date and role |

Tools do not take tenant IDs, Book IDs or actor IDs. Those come from the
connection. Document-enabled write connections expose 20 tools and read
connections expose 12. Without documents the counts remain eleven and seven.
Read-only connections omit write tools; the service also enforces that boundary
if a client invents a call. `post_journal` accepts optional `sources` only when
documents are enabled; existing no-source inputs retain their behavior.

The repository includes a [bookkeeping skill](../skills/vult-bookkeeping/SKILL.md)
covering exact money, request recovery, source/evidence relationships and
statement reuse checks. Loading a skill does not grant accounting permission.

## Exact money and retries

For a Book with currency USD and scale 2, a debit of `"1250"` is 12.50 USD.
Amounts must be positive decimal integer strings, with separate `debit` or
`credit` sides. Decimal numbers, exponent notation, zero and amounts beyond
signed 64-bit range are rejected. Account balances must remain in that range;
entry sums are checked with wider exact arithmetic.

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
