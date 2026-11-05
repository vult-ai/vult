# Security

Report vulnerabilities privately through **Security → Report a vulnerability**
on this repository. Do not put exploit details, credentials or financial data in
public issues or pull requests. Include the Vult and operating-system versions, required
attacker access, reproduction steps and a small example using synthetic data.

## Trust boundaries

### Local macOS service

The local installation separates ordinary user and agent processes from a dedicated service
account. Only that service can read its PostgreSQL files and database credentials.
Code and its parent directories are protected by administrator ownership. The
service accepts requests through a local Unix socket and never opens a TCP port.

Owner operations require native macOS administrator authorization. Authorizations
are short-lived, single-use and bound to an action, payload and caller identity.
Agent sessions are memory-only, expire after eight hours, and have one fixed Book
and access level. The database checks the connection's live revocation state on
each call. Restarting the service invalidates all sessions.

### Linux server

The server runs as an unprivileged identity behind HTTPS. Its database and
plaintext backend listener must remain private. Only explicitly trusted proxies
can supply the exact Host and forwarding headers; foreign browser origins and
cross-site writes are rejected. Reverse proxies must not record credentials,
sensitive URL parameters or financial payloads.

Operator credentials stay offline. The online ledger, sign-in and owner APIs use
distinct restricted PostgreSQL logins with no table ownership, role membership
or RLS bypass. Human sign-in does not grant tenant access by itself: Open checks
explicit ownership, scoped Books, live sessions and fresh MFA for new owner
effects. OIDC requires an operator-enforced MFA policy. External identity
providers are trusted to verify identity and fresh MFA, never to choose tenant
membership or financial permissions.

Remote agents use OAuth with exact callbacks/resource, S256 PKCE, short-lived
access credentials and single-use rotating refresh credentials. Each grant
selects one Book and Read or Write access. Credentials and live ownership are
rechecked inside ledger transactions; pooled connections retain no scope.
Replayed refresh credentials revoke their active grant generation. Browser
cookies cannot authorize an agent call, and agents cannot administer owners.

Extension admission can deny authorized requests, not grant access. New durable
quota effects share the financial transaction after recorded-outcome lookup.
Server diagnostics use fixed messages and exclude financial data and raw
dependency errors. Server authentication/grants require a separate protected
database backup; a ledger-only export is not a complete server backup.

See [server setup and recovery](docs/server.md) for role, TLS and backup rules.

### Shared ledger

The service is trusted to set database scope from the authenticated session.
Agents never receive a database login or a way to send SQL. Row-level policies
and scoped foreign keys also protect against accidental cross-Book queries.

The database and Go boundary enforce exact amounts, balanced atomic postings,
immutable entries, full reversals, closed periods and consistent request receipts.
The recording timestamp and actor are assigned by the service. Source documents
and instructions supplied to an agent remain untrusted; Vult cannot determine
whether a balanced entry describes a real event.

## Limits

- Root, administrators who approve operations, the service account and the
  operating system are trusted. Vult does not protect against those principals.
- A compromised approved client can use the Book and actions granted to it.
  It may disclose anything that connection can read. Revoke access promptly.
- Processes running as the same user share that user's broader security
  environment. Session authorization is not a general-purpose agent sandbox.
- The local PostgreSQL files are not encrypted by Vult. Use FileVault to protect
  a powered-off or stolen Mac. Plaintext exports need equivalent care.
- Backups are authenticated age-encrypted files. Keep the separate recovery key
  offline. Losing both the installation and the key makes recovery impossible.
- Supported installer paths reject symlinks, unsafe ownership, writable code
  paths and unsupported permissive access-control lists.

Security fixes are delivered as new releases. Existing published artifacts and
migrations are not silently replaced.
