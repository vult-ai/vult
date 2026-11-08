# Vult

A double-entry ledger for agents. Your agent keeps your Book; you see it, check
it and trust it. Agents supply debits and credits; Vult records a balanced,
immutable journal, and you review it in your browser.

Vult 0.4.0 targets local use on **macOS 26 on Apple Silicon** and
**Ubuntu 24.04 LTS on x86-64 with GNOME and Chrome**, plus a separate
**Linux server** behind HTTPS. The native installers include PostgreSQL; agents
use its protected local socket and the browser uses local HTTPS, with no Docker
dependency. Server mode adds human sign-in,
scoped owner administration and OAuth for remote agents.

Original documents are encrypted at rest and kept with their Book. Journal
entries can cite document locations and have explicit supporting evidence.
Citation corrections and evidence changes append history without changing
posted accounting. See [documents](docs/documents.md).

## Start a Book

On a supported Mac, install from your own terminal:

```sh
curl -fsSL https://vult.sh | sh
```

The installer downloads a fixed release, checks its SHA-256, and asks for native
administrator approval to install the protected service. It includes PostgreSQL;
you do not need Docker, Go or a compiler. See [installation](docs/install.md).
For Ubuntu, use the separate [native Ubuntu installer](packaging/ubuntu/README.md)
with its matching release archive. The commands below are the same there, run as
`/opt/vult/vult`, with approval through your desktop's system prompt.

The macOS package is **unsigned and not notarized by Apple**. Its executable
signatures are ad-hoc. The terminal installer checks the pinned package checksum
and does not disable macOS security protections.

To read the script first:

```sh
curl -fsSL https://vult.sh -o install.sh
less install.sh
sh install.sh
```

Then initialize the ledger and create a Book:

```sh
vult init
vult book create --name "Personal" --currency USD --scale 2
vult book list
```

Owner commands request macOS administrator authorization. `init` displays a
random backup recovery key in the terminal. Store it offline and keep it away
from agents. The local database relies on operating-system permissions and disk
encryption; enable FileVault.

Use the returned Book UUID to create a connection:

```sh
vult connection create --name "Personal agent" --book BOOK_ID --access write
```

The result contains a generic MCP `command` and `args` configuration. Add that
entry to your client's MCP settings. The command is always `vult mcp`, regardless
of whether the client is Claude Code, Claude Desktop, Codex or another MCP client.
See [MCP setup](docs/mcp.md).

Each connection selects **one Book** and either `read` or `write` access. Starting
an MCP session requests native owner approval. A connection UUID alone is not a
credential. Approval lasts for that running bridge for at most eight hours;
restarting Vult requires approval again.

```sh
vult connection list
vult connection revoke CONNECTION_ID
```

The installer places the protected executable at
`/Library/PrivilegedHelperTools/ai.vult.ledger/vult`. It adds a `vult` shortcut
only when `/usr/local/bin` is protected and no command already occupies that
name. Otherwise, use the full executable path above.

## Review your Book in the browser

Run `vult open` from your own terminal. The first time, it sets up a passkey for
this installation; see [open in your browser](docs/install.md#open-in-your-browser).
The browser shows what your agents recorded:

- **Dashboard:** income and expenses by period, where the money went, what you
  own and owe, and cash by account. Refunds and credits stay visible as
  reductions.
- **Transactions and accounts:** every entry with its lines, dates and original
  documents, and every account's balance and history.
- **Statements:** balance sheet, income statement, trial balance, general ledger
  and journal for any period, with the facts each rests on (basis, coverage and
  when the figures were recorded), exportable as CSV.
- **Sources and search:** the original documents kept with the Book, and search
  across entries, accounts and documents.
- **Settings:** your name, photo, appearance and number and date formats, the
  Book's logo, details and address, connected agents, and downloads of the whole Book.

The Dashboard speaks plainly; Statements give professionals the basis, coverage
and checks behind each figure. See [dashboard](docs/dashboard.md),
[reports](docs/reports.md) and [search](docs/search.md).

## What agents can do

Create and revise accounts, post general journal entries, reverse entries,
read journals, read balances and a trial balance, search the Book, and check
earlier request outcomes. Agents can queue exact journal entries and reversals
for owner review. An approval posts the reviewed entry, and the agent can read
the outcome. Read connections expose inspection tools; write connections also
expose recording and proposal tools. The
[bookkeeping skill](skills/vult-bookkeeping/SKILL.md) covers accounts, dates,
common postings, statement comparisons, source reuse and safe retry recovery.

Amounts are **integer strings in the Book's minor unit**: `"1250"` means 12.50
when the scale is 2. Each Book has one fixed currency and scale. Native movements,
contextual amounts and explicit valuation inputs are described in
[currency measurements](docs/currency-measurements.md).

Every posting has an economic date, an accounting date and a service-recorded
timestamp. A reversal mirrors every original line and uses an open accounting
date. Account identities stay fixed when names or display codes change.

Writes require a request key. Retrying the same operation and input under that
key returns the saved result. Reusing a key with different input fails. If a
reply is lost, check the original key; an unresolved outcome does not mean the
write was rejected. Never invent a replacement key for an uncertain request.

## Owner commands

```sh
vult status
vult period close --book BOOK_ID --from 2026-01-01 --through 2026-01-31
vult verify --book BOOK_ID
vult export --book BOOK_ID --output journal.json
vult export --documents --book BOOK_ID --output book.tar
vult backup --output books.age
vult restore --input books.age
vult uninstall
vult --version
vult help
```

Closing a period is permanent. Export is **plaintext**;
backup is encrypted with the recovery key. Output files are created with private
permissions and never overwrite existing files. Restore requires an empty
installation, preserves recorded financial rows, and does not restore connection
grants. Create new connections after a restore. Backups include originals and
their relationship history, with up to 64 MiB of metadata and an 8 GiB archive.
Document-inclusive export is a plaintext portable archive. Existing encrypted
finance-only backups remain recoverable. Journal JSON exports support up to
100,000 entries. The bundled interface also provides
[owner settings and Book downloads](docs/owner-settings.md).

`vult uninstall` requests administrator approval and removes only the program,
service and startup entry. It keeps your Books, credentials and recovery key at
`/Library/Application Support/ai.vult.ledger/`, retains the `_vultledger` account,
and leaves backups wherever you saved them. It also removes the copies of
statements that agents read, kept in `~/Library/Application Support/Vult/Documents`
for a day after each was made. Reinstalling reuses that account and
stored data. See
[removal and reinstall](docs/install.md#remove-or-reinstall).

Agents cannot create Books, grant access, close periods or invoke owner backup
and export commands. A connection can read its own Book through the normal
journal tools, so grant read access only to clients you trust with those records.

## Security and scope

The service and database run as a separate system account. Protected executable,
configuration and data paths prevent ordinary agent accounts from opening the
database directly. Local socket requests need a scoped session or a single-use
owner authorization. Administrator/root access remains fully trusted. This is
not a sandbox for other applications or for a compromised operating system.

See [SECURITY.md](SECURITY.md) and the [ledger model](docs/ledger.md).

Agents interpret original documents and submit journal entries; Vult retains the
recorded financial effects and their document relationships. Published releases
and migration files remain fixed; later releases add capabilities with forward
migrations.

## Build and contribute

See [CONTRIBUTING.md](CONTRIBUTING.md) for build and test requirements and
[native packaging](docs/packaging.md) for installer creation.

For self-hosting, see [running a Linux server](docs/server.md). Built-in sign-in
uses a passphrase and authenticator; OIDC is optional. Server identity and billing
integrations use extension interfaces; Vult itself checks tenant and Book access.
The macOS command continues to use native approval over its local socket.

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[third-party notices](THIRD_PARTY_NOTICES.md). Some interface components are
adapted from [TUFFUI](https://tuffui.com) (MIT).
