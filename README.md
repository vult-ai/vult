# Vult

A local double-entry ledger for agents. Agents supply debits and credits;
Vult records a balanced, immutable journal.

Vult 0.1.0 supports **macOS 26 on Apple Silicon**. Its native installer includes
PostgreSQL. It has no web application, network listener or Docker dependency.

## Start a Book

Install from your own terminal:

```sh
curl -fsSL https://vult.sh | sh
```

The installer downloads a fixed release, checks its SHA-256, and asks for native
administrator approval to install the protected service. It includes PostgreSQL;
you do not need Docker, Go or a compiler. See [installation](docs/install.md).

The v0.1.0 package is **unsigned and not notarized by Apple**. Its executable
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

## What agents can do

Create and revise accounts, post general journal entries, reverse entries,
read journals, read balances and a trial balance, and check earlier request
outcomes. There are eleven MCP tools; read connections expose seven.

Amounts are **integer strings in the Book's minor unit**: `"1250"` means 12.50
when the scale is 2. Vult never calculates exchange rates. Each Book has one
fixed currency and scale.

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
vult backup --output books.age
vult restore --input books.age
vult uninstall
vult --version
vult help
```

Closed periods cannot be reopened in this version. Export is **plaintext**;
backup is encrypted with the recovery key. Output files are created with private
permissions and never overwrite existing files. Restore requires an empty
installation, preserves recorded financial rows, and does not restore connection
grants. Create new connections after a restore. Backups support up to 64 MiB of
logical data; journal exports support up to 100,000 entries.

`vult uninstall` requests administrator approval and removes only the program,
service and startup entry. It keeps your Books, credentials and recovery key at
`/Library/Application Support/ai.vult.ledger/`, retains the `_vultledger` account,
and leaves backups wherever you saved them. Reinstalling reuses that account and
stored data. There is no command to delete financial data. See
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

This version is the general ledger only: no statement or receipt storage,
reconciliation, bank connections, payment execution, transaction templates or web
interface. Agents interpret source documents outside Vult and submit journal
entries. Later releases can add capabilities with forward migrations; published
releases and migration files remain fixed.

## Build and contribute

See [CONTRIBUTING.md](CONTRIBUTING.md) for build and test requirements and
[native packaging](docs/packaging.md) for installer creation.

Apache-2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[third-party notices](THIRD_PARTY_NOTICES.md).
