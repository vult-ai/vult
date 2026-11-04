# Contributing

Read README.md and CONTRIBUTING.md before changing code. Use synthetic data.
Preserve exact integer money, immutable postings, tenant and Book isolation,
and stable request outcomes. Never add credentials or financial records to tests.

Run gofmt, go vet and the tests applicable to your change. Database tests require
an isolated PostgreSQL 18 database and fail when it is missing. Never point them
at an existing installation. Keep changes small and describe actual validation.
