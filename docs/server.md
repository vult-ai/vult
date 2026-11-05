# Running a server

`vult-server` is a separate Linux executable. It does not change the macOS
`vult` command, native service, socket approval or bundled PostgreSQL.

`GET`/`HEAD /healthz` checks liveness and `/readyz` checks the database schema
receipt. Configured sign-in is served under `/auth/`, OAuth under `/oauth/`, and
the eleven ledger tools through `/mcp`. Probe readiness alone does not verify
identity-provider or deployment policy configuration.

## Build

Use Go 1.27.3 and select the server's architecture (`amd64` or `arm64`):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o build/vult-server ./cmd/vult-server
docker build --platform linux/amd64 -f packaging/linux/Dockerfile -t vult-server .
```

The image contains only the static executable and runs as UID/GID 65532. It has
no shell, database, migration runner or embedded credentials. The build context
selects only the executable. No image is downloaded by the scratch recipe.

## Run behind HTTPS

Use a dedicated unprivileged service identity and an existing PostgreSQL 18
database initialized through trusted operator tooling. Supply only its restricted
runtime login: no superuser, role membership, database/schema creation or public
table ownership. Startup verifies the actual database identity and schema receipt;
it never initializes or migrates the database. Installation/operator credentials
stay offline. Authentication and scoped owner operations use two additional,
distinct restricted roles; neither can provision tenants or migrate the database.

Store the configuration and database URL in separate regular files, readable
only by UID 65532 (mode 0400 or 0600), under a root-controlled directory. Start
from [server.json](../packaging/linux/server.json). Give the database URL an
explicit host, database and runtime user. Remote databases require
`sslmode=verify-full`; plaintext is allowed only over a loopback address or local
Unix socket. Mount the host's trusted CA bundle for TLS verification.

On a Linux host, with its HTTPS proxy and PostgreSQL independently managed:

```sh
docker run --name vult-server --network host --read-only \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --memory 256m --cpus 1 --pids-limit 128 \
  --mount type=bind,src=/etc/vult/server.json,dst=/run/vult/server.json,readonly \
  --mount type=bind,src=/etc/vult/runtime.dsn,dst=/run/vult/runtime.dsn,readonly \
  --mount type=bind,src=/etc/ssl/certs/ca-certificates.crt,dst=/etc/ssl/certs/ca-certificates.crt,readonly \
  vult-server --config /run/vult/server.json --dsn-file /run/vult/runtime.dsn
```

The example binds only to loopback. [Caddyfile](../packaging/linux/Caddyfile)
shows an HTTPS proxy that overwrites Host and the three accepted forwarding
headers, disables its HTTP administration API and excludes request access/error
logs. Trust only explicit private/loopback proxy CIDRs. Requests with duplicate
headers, unexpected forwarding headers, foreign Host/Origin or cross-site browser
writes are rejected. The backend must not be exposed as a public plaintext port.
Wildcard binding additionally requires `private_ingress: true` and private,
firewalled container ingress; it never relaxes proxy or HTTPS validation.

SIGTERM closes the listener with a bounded graceful shutdown. Request headers,
bodies, time and concurrency are bounded; a process-wide rate limit is a basic
resource bound, separate from tenant quotas. There are no request access logs.
Errors, dependency failures and recovered panics produce fixed diagnostics, never
request bodies, credentials, financial descriptions or raw SQL/provider errors.
Configure the HTTPS proxy and deployment logging to the same rule: no financial
payloads, cookies, authorization headers or sensitive URL parameters.

## Extension contracts

`IdentityProvider` returns issuer, subject and the time of verified MFA. Open separately verifies
membership, tenant/Book scope and the requested operation. `Admission` can deny an
already-authorized operation; it cannot grant access. Durable financial quotas
also belong in the write transaction. These contracts do not install routes or
turn an identity into owner authority. Authentication and OAuth implementations
must retain cookie/session CSRF protection and live authorization checks.

An external provider can also implement `IdentityPages`. Open supplies the login
CSRF nonce to its renderer and routes only `/identity/` to its ceremony handler.
These routes retain the same ingress, request limits and timeouts. Writes require
the core login cookie, exact Origin and matching `X-CSRF-Token`. The provider
returns a verified identity to core sign-in; it never issues an Open session or
chooses tenant membership itself. Keep any provider credentials and data outside
the shared ledger package.

## Sign-in and offline setup

Build the separate operator command with:

```sh
go build -o build/vult-server-admin ./cmd/vult-server-admin
```

It is not included in the network image. Run it from a trusted operator session
with a private operator DSN file. Inputs and outputs are regular files with mode
0600. It never prints enrollment secrets and never overwrites a previous output.
An interrupted operation may have committed: inspect its recorded state before
retrying a command that creates records.

Create three distinct restricted PostgreSQL LOGIN roles using your database's
credential provisioning mechanism. Grant no role memberships, superuser, database
creation, replication or RLS bypass. Keep database/schema ownership offline.
Prepare private JSON input with `runtime_role`, `auth_role` and `owner_role`:

```sh
vult-server-admin migrate --dsn-file operator.dsn --input roles.json --output migrated.json
```

For one built-in owner, generate a private 32-byte credential key:

```sh
umask 077
openssl rand -out credential.key 32
```

Keep this key separate from the database and in protected operator backups.
Initialization input contains `username`, a `passphrase` of at least 16
characters and `key_file`. An optional existing `tenant_id` binds upgraded Books;
omitting it creates a tenant. Initialization cannot replace existing ownership.

```sh
vult-server-admin init --dsn-file operator.dsn --input owner.json --output enrollment.json
```

Import `AuthenticatorURI` from the result into your authenticator and store
`RecoveryCodes` offline. First sign-in requires an authenticator code; recovery
codes work only after enrollment and are single use. Attempts and accepted code
steps persist across restarts and replicas. Eight attempts per five minutes share
one account budget, including unknown usernames. Passphrases use bounded Argon2id;
the authenticator secret is encrypted with the separate credential key.

Add an `authentication` object to the private server configuration:

```json
{
  "mode": "builtin",
  "auth_dsn_file": "/run/vult/auth.dsn",
  "owner_dsn_file": "/run/vult/owner.dsn",
  "username": "example",
  "credential_key_file": "/run/vult/credential.key"
}
```

Mount these additional private files read-only and visit `/auth/login` through
HTTPS. Cookies are host-only, Secure, HttpOnly and SameSite=Lax. Sessions expire
after two hours; writes require MFA within five minutes. Foreign-origin forms and
missing CSRF proofs fail. Browser identity or tenant claims cannot grant access.

For optional OIDC, use `mode: "oidc"`, the same DSN fields, plus `issuer`,
`client_id`, `client_secret_file` and `mfa_policy: true`. Register the exact HTTPS
redirect URI `<origin>/auth/callback`. First require MFA in the provider's policy;
the flag records this operator requirement. The provider must support S256 PKCE
and signed ID tokens with a fresh `auth_time`, exact issuer/audience and nonce.
Bind a known subject offline with `bind-owner`, using private `issuer`, `subject`
and optional `tenant_id` input. Unknown identities cannot enroll themselves;
provider role and tenant claims are ignored. `external` mode requires a custom
server supplying its verified `IdentityProvider` through `OpenAccess`.

The offline `book` command takes private `tenant_id`, `name`, `currency` and
`scale` input. Built-in `recover` takes new `username`, `passphrase` and `key_file`
input. It preserves Books and identity, replaces both factors and invalidates
prior identity sessions and delegated remote credentials. Recovery is never an
agent or HTTP operation. Preserve the old key until recovery succeeds and protect
the new enrollment output.

Use database-native encrypted backups for server authentication/authorization
state as well as financial records. The ledger backup/export intentionally
excludes login credentials and delegated grants. Restore server backups offline,
revoke old sessions/grants and verify isolation and journals before opening
ingress. A financial export alone is not a full server backup.

## Remote agents

Configure your MCP client with `https://ledger.example.com/mcp`, replacing the
origin with your deployment. The service advertises OAuth discovery and dynamic
public-client registration. It accepts exact registered HTTPS or IP-loopback
callbacks, authorization code flow with S256 PKCE, and the exact `/mcp` resource.
It does not accept bearer tokens in URLs or browser cookies as agent credentials.

Registration is limited to 128 new clients per hour and 4,096 retained clients.
Unused registrations may expire after 24 hours. New registrations reclaim a
bounded batch of old clients that have neither grants nor retained authorization
requests. Pending requests and the code-replay retention window are preserved;
clients with grants remain registered, including after revocation. A client
whose unused registration expired must register again before authorization.

Sign in, select one owned Book and Read or Write access, then confirm. Write
includes account revisions and journal posting, never owner administration.
Access credentials last 15 minutes; refresh credentials rotate on use and expire
after 90 days. Replaying a consumed credential revokes that grant's active
generation. A selected reconnect retains the same actor and request outcomes;
permission/credential changes activate only after successful code exchange.

The server rechecks and leases identity, ownership, delegated grant and token
inside each ledger transaction. Pooled sessions retain no authorization context.
Credential-free discovery, registration and token requests support CORS without
cookies; login, consent and owner operations keep same-origin CSRF protection.
The reverse proxy must retain its exact Host/HTTPS boundary.

Remote journal pages hold at most 20 entries, replies at most 4 MiB before MCP
encoding, and account/trial-balance lists at most 2,000 accounts. An oversized
result fails explicitly rather than silently omitting records. Four concurrent
MCP requests, finite body/read/write deadlines and fixed diagnostics bound the
network surface. These limits do not change local socket behavior.

A private admission implementation may deny an authorized request. Its optional
`TransactionAdmission` method applies durable quota changes only to new financial
requests, after retained-outcome lookup, in the same transaction. Identical retries
do not charge again, and rolled-back writes retain no charge. Admission cannot
grant a tenant or Book permission.

`OwnerAdmission` can restrict a new scoped owner effect in its transaction.
It runs after ownership/fresh-MFA verification and retained-outcome lookup, so
owner retries also avoid duplicate charges. It cannot create owner authority.

## Scoped owner API

Human sessions can administer only their explicitly owned tenants. These JSON
routes require the session cookie; POST additionally requires the exact HTTPS
Origin and the `X-CSRF-Token` returned by `GET /auth/session`. Agent bearer tokens
are rejected. Installation, migration, identity binding and recovery stay offline.

| Method | Path under `/owner/tenants/{tenant}` | Input/result |
| --- | --- | --- |
| GET | `/books` | Owned Books, at most 1,000; an oversized list fails. |
| POST | `/books` | `request_key`, `name`, `currency`, `scale`; returns the new Book. |
| GET | `/connections` | Owned connection metadata, at most 1,000. |
| POST | `/connections/{connection}/revoke` | `request_key`; revokes the connection. |
| POST | `/books/{book}/close-period` | `request_key`, `from`, `through`; closes an open date range. |
| GET | `/requests/{request_key}` | The recorded owner result, or `status: unresolved`. |

Owner keys contain 1–128 ASCII letters, digits or `._~-`. Keep a key and its
exact input until the result is known. Identical retries return the original
result, including after a reply is lost; changed input with that key conflicts.
Keys are scoped to the owner identity and tenant, separately from agent journal
receipts. A retained result requires a live session; a new effect also requires
MFA within five minutes. Concurrent retries apply once. Missing outcomes never
mean rejection. Revocation and period closes obey the same live authorization
and database rules as other ledger operations.

## Embedding the server

`server.New(config, server.Options{})` constructs health-only ingress. Its zero
options do not enable login, owner administration or ledger routes. A `Ready`
callback is available only in this health-only mode.

For an authenticated service, open the restricted runtime database and access
service first, then pass both in one options value:

```go
httpServer, err := server.New(config, server.Options{
    Database: runtime,
    Access: authentication,
    Admission: admission,
})
```

`Database` and `Access` are required together. Their origin must agree with the
public configuration. Authenticated readiness always verifies the installed
ledger schema; a custom readiness callback cannot replace it. `Admission` is
optional and can deny already-authorized work, never grant access. The caller
retains responsibility for closing database and access resources.
