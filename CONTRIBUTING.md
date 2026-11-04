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
- Don't add telemetry, network services, external money movement or a database fallback.

## Build and test

Use Go 1.27.3 on Apple Silicon macOS. Build with:

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
