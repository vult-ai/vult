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
The same eleven tools are used; Write never includes owner administration.

Do not put access credentials in a URL or copy browser cookies into an agent.
Access credentials expire after 15 minutes; refresh credentials rotate on use.
Reconnect can retain an existing connection actor and its request outcomes.
See [remote agent authorization](server.md#remote-agents) for scope, revocation
and recovery rules. The local macOS bridge and its native approval are unchanged.

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

Tools do not take tenant IDs, Book IDs or actor IDs. Those come from the
connection. Read-only connections omit all four write tools; the service also
enforces that boundary if a client invents a call.

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
