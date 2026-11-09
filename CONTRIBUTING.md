# Contributing to Vult

Thanks for helping. Vult holds people's financial records, so correctness and security come before features.

## Before you start

- For anything larger than a small fix, open an issue first so we can agree on the approach.
- Security vulnerabilities go through [SECURITY.md](SECURITY.md), never a public issue or pull request.

## Ledger rules

Every change must preserve these:

- **Money is exact.** Amounts are integers in minor units. Never use floating point, and never round unless a documented rule says how.
- **Postings are immutable.** A posted entry is never edited or deleted. Corrections are new entries, such as reversals.
- **Every entry balances.** Debits equal credits, enforced by the database, not only by Go code.
- **Books are isolated.** No query, tool or test may read or write across Books or tenants.
- **Retries are stable.** The same request returns the same recorded outcome; a conflicting request with a reused key is rejected.
- **Released migrations never change.** Changes to the schema are new forward migrations.

## Data and tests

- Use synthetic data only. Never commit real statements, receipts, accounts, credentials or financial records, including in fixtures and screenshots.
- Database tests need an isolated PostgreSQL 18 database, and they fail, not skip, when it's missing. Never point them at an existing Vult installation.
- Add tests for any accounting or security behaviour you change.
- Never weaken a test, check or guard to make a change pass. Fix the change.

## Pull requests

- One purpose per pull request. Keep it small.
- Run `gofmt`, `go vet` and the tests that apply to your change, and say exactly which checks you ran.
- Name code, tests and commits for what they do.
- Don't add telemetry, external money movement or a database fallback. Server
  changes must preserve the [network boundary](docs/server.md).

## Build and test

Use Go 1.27.3. Native local targets are Apple Silicon macOS and Ubuntu 24.04
x86-64; the separate server supports Linux. Build the local command with:

```sh
go build -trimpath -buildvcs=false -o build/vult ./cmd/vult
go vet ./...
```

Tests need one disposable PostgreSQL 18 server. Give it a fresh, isolated
cluster and a bootstrap role that can create and drop logical databases and
restricted login roles. Tests create a separate database per case and remove
their own databases and roles on success and failure. They never use an
installed Vult service.

Write that synthetic server's DSN into a private file, then run:

```sh
VULT_TEST_ISOLATED=1 VULT_TEST_DSN_FILE=/path/to/synthetic-dsn go test -race -count=1 ./...
```

Missing database configuration is a failure, not a skipped test. Never use an
existing personal, development or production database as the test server.

While you work, check only what your change touches:

```sh
VULT_TEST_ISOLATED=1 VULT_TEST_DSN_FILE=/path/to/synthetic-dsn scripts/check-changes.sh
```

It reads the files changed since `HEAD` (or `--base REF`, or `--files ...`). A
style change runs the design checks; a web source change adds the typecheck,
the unit tests that load that file and the tests that load the whole app; a Go
change runs vet and the changed packages' tests without the race detector. A
typical edit takes seconds. Before committing, run every check once:

```sh
VULT_TEST_ISOLATED=1 VULT_TEST_DSN_FILE=/path/to/synthetic-dsn scripts/check-changes.sh --full
```

Browser components follow the [typography standard](docs/design/typography.md).

The browser sign-in script has dependency-free tests using Node's test runner:

```sh
node --test access/passkeys.test.mjs
```

To review the interface in a browser, build it and start the preview server.
It serves the real server and interface on `127.0.0.1:8080` with synthetic
Books, using the same disposable PostgreSQL configuration as the tests:

```sh
(cd web && npm run build)
VULT_TEST_ISOLATED=1 VULT_TEST_DSN_FILE=/path/to/synthetic-dsn \
  VULT_PREVIEW_WEB="$PWD/web/dist" \
  go test -tags browserpreview -count=1 -timeout 0 -run TestBrowserPreview ./server/
```

Open `http://127.0.0.1:8080/__preview/sign-in` to sign in as the synthetic
owner. After `npm run build` in `web`, reload the page to see the new build;
the preview needs a restart only for Go changes.

Each start gets a fresh database by default. To keep your changes across
restarts, add `VULT_PREVIEW_STATE=/path/to/a/private/folder` and point
`VULT_TEST_DSN_FILE` at a PostgreSQL 18 server you keep for the preview. The
preview then reuses one database, its owner and its sources, and seeds the
synthetic Books only the first time. Delete that database and folder to start
over. With the preview running, the browser journey test creates a Book,
closes a period, reads a source and the Dashboard, and signs out:

```sh
cd web && VULT_E2E_CHANNEL=chrome npx playwright test
```

`VULT_E2E_CHANNEL=chrome` uses an installed Chrome with a temporary profile.

The installer generator and synthetic shell-control tests use Python 3.11 or later:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -v
```

They replace platform and privileged commands with test doubles. They do not
exercise real administrator prompts or install a service.

The source package is selected by `packaging/source-paths.txt`. Add new public
files there when they belong in the distribution. Build outputs, credentials and
version-control metadata are not source inputs.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
