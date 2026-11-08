# Running a server

`vult-server` is a separate Linux executable. It does not change the macOS
`vult` command, native service, socket approval or bundled PostgreSQL.

`GET`/`HEAD /healthz` checks liveness and `/readyz` checks the database schema
receipt. Configured sign-in is served under `/auth/`, OAuth under `/oauth/`, and
the ledger tools through `/mcp`, with document tools when storage is configured.
With `mcp_origin` set, MCP and agent OAuth move to that origin instead (see
[Remote agents](#remote-agents)).
Probe readiness alone does not verify
identity-provider or deployment policy configuration.

## Build

Use Go 1.27.3, Node 26.7.0 and npm 11.19.0. Build the interface from the same
committed source:

```sh
(
set -eu
cd web
npm ci --ignore-scripts
npm run build
cd ..
python3 scripts/package_macos.py --print-web-sha256 web/dist
)
```

Keep the printed interface manifest checksum with the source revision. Replace
`INTERFACE_MANIFEST_SHA256` below with that checksum. Stage a new source tree
with the same source selection and verified interface as the macOS builder,
then select the server's architecture (`amd64` or `arm64`):

```sh
(
set -eu
python3 - INTERFACE_MANIFEST_SHA256 <<'PY'
from pathlib import Path
import sys

source = Path.cwd()
sys.path.insert(0, str(source / "scripts"))
from package_macos import materialize, selected_files, stage_interface

staged = source / "build/server-source"
staged.mkdir(parents=True, exist_ok=False)
materialize(source, staged, selected_files(source))
stage_interface(source / "web/dist", staged / "web/dist", sys.argv[1])
PY
(cd build/server-source && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=webui -trimpath -buildvcs=false -o ../vult-server ./cmd/vult-server)
docker build --platform linux/amd64 -f packaging/linux/Dockerfile -t vult-server .
)
```

The image contains the static executable and its license notices, and runs as
UID/GID 65532. The notices are in `/usr/share/licenses/vult/`. It has
no shell, database, migration runner or embedded credentials. The build context
selects only the executable and those three notice files. No image is downloaded
by the scratch recipe.

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

Give the bundled browser a dedicated writable staging directory owned by
UID/GID 65532 with mode 0700. Set TMPDIR to its mounted path. The browser
limits its encrypted download spools to 8 GiB plus 128 MiB in total per
listener. Allow that much free space to accommodate the largest export and
concurrent transfers.

On a Linux host, with its HTTPS proxy and PostgreSQL independently managed:

```sh
sudo install -d -m 0700 -o 65532 -g 65532 /var/lib/vult-export-tmp
docker run --name vult-server --network host --read-only \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --memory 256m --cpus 1 --pids-limit 128 \
  --env TMPDIR=/run/vult/exports \
  --mount type=bind,src=/var/lib/vult-export-tmp,dst=/run/vult/exports \
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

### Institution and brand artwork

The interface shows bank and brand logos from a public artwork origin,
`https://assets.vult.ai` by default. The browser reads its catalogs and images
directly, without credentials, cookies or a referrer, and sends no Book data.
`shared_asset_origin` sets another canonical HTTPS origin, such as
`"https://artwork.example.com"`, or `"off"` to show initials only. The page's
content security policy allows that one origin for images and catalogs. When it
can't be reached, avatars show initials.

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

The offline `revoke-identity` command takes private `issuer` and `subject` input
with the same `--dsn-file`, `--input` and new `--output` arguments. It invalidates
that identity's existing browser sessions and delegated agent credentials across
its tenants, without changing Books or ownership. An unknown identity fails;
other identities remain connected. This is session/credential revocation, not
an account-disable operation: block compromised sign-in at the identity provider
or use built-in recovery before allowing fresh authentication. It is never an
HTTP or agent operation. Preserve the private result and inspect the current
state before retrying an uncertain operator command.

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

To give agents their own address, set `mcp_origin` to a second canonical HTTPS
origin on a different host, such as `https://mcp.ledger.example.com`, and route
that host through the same trusted proxy. Agents then use that origin itself as
the MCP URL and resource, and it is the OAuth issuer with registration and token
exchange. Owners still sign in and consent at `origin`, which stops serving
`/mcp`, `/oauth/register`, `/oauth/token` and OAuth discovery. The MCP origin
serves only those routes. Existing grants stay valid, but connected clients must
reconnect with the new URL. `mcp_origin` cannot be combined with
`local_browser`.

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
`TransactionAdmission` method applies durable quota changes only to new writes,
after retained-outcome lookup, in the same transaction. `add_document` admits a
new byte reservation once; deduplicated intake does not reserve more bytes.
`complete_document_upload` rechecks fresh admission without reserving the bytes
again. The embedding policy distinguishes these operations and accounts for
current storage use. Identical retries do not charge again, and rolled-back
writes retain no charge. Admission cannot
grant a tenant or Book permission.

`OwnerAdmission` can restrict a new scoped owner effect in its transaction.
It runs after ownership/fresh-MFA verification and retained-outcome lookup, so
owner retries also avoid duplicate charges. It cannot create owner authority.
For approval, it sees `decide_operation`. The approved posting runs
`Admission.Check` and, when configured, `TransactionAdmission` with the
preparer's recorded scope in the live owner transaction. If only
`OwnerAdmission` is configured for transactional policy, it also checks the
underlying posting. The deployment must allow its posting policy to run on the
restricted owner connection. A refusal rolls back both decision and posting.

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

## Document storage

To enable originals, add a `documents` object to the private server configuration:

```json
{
  "directory": "/var/lib/vult/originals",
  "key_file": "/run/vult/document-keys.json"
}
```

The directory must already exist, owned by the dedicated service user with mode
0700. The key file is private mode 0400 or 0600. Provision it once with the
offline `vult-server-admin document-key` command: its private input is
`{"tenant_id":"TENANT_ID"}` and `--output` selects a new key file. Existing output
is never replaced. Retain old key versions and an offline recovery copy.
Opening the server never provisions or replaces a missing key.

Linux disk storage requires a filesystem that can verify the absence of POSIX
access and default ACLs. Extended ACLs and unverifiable access controls are
refused, even when the ordinary mode bits appear private.

Alternatively, configure `key_directory` instead of `key_file`. The directory
must already exist, owned by the service account with mode 0700. To provision a
tenant explicitly, give `document-key` private input containing `tenant_id` and
`key_directory`; its output reports readiness without containing the key.
This form supports destination key provisioning during portable restore. The
two key configuration fields are mutually exclusive. The online server only
looks keys up; it never generates them.

Schedule `vult-server-admin expire-documents` under the storage-owning account,
with offline operator credentials. Its private input supplies `directory` and an
optional `book_id`. Expiry needs no document keys. Each invocation reclaims at
most 100 expired, never-used originals per Book, from at most 20 eligible Books
when no Book is selected, oldest outstanding expiry first, with a 30-second
database-operation budget. It requires
an installation-superuser login with no role switching; a runtime login fails
instead of silently reporting an empty sweep. The private result reports
`books`, `expired` and whether `more` work remains. A failure can follow earlier
committed Book batches; repeat maintenance after correcting the failure rather
than treating it as a single all-or-nothing sweep.
It cannot remove any original previously cited or attached as evidence. Keep
operator credentials out of the online server's mounts and process environment.

The storage interface publishes whole encrypted objects atomically and accepts
only opaque identifiers. An embedding application can supply `Options.Documents`
with alternative storage and key providers. Keys stay outside the ledger
database. Both providers must preserve tenant boundaries, original hashes and
old decryption keys. Neither provider grants access to a Book.

Document transfer paths contain short-lived credentials. Proxy access logs must
omit those paths, and application diagnostics must exclude original names and
contents. Downloads are always attachments, never browser-rendered originals.

### Automatic server expiry

For the Linux container deployment, enable the supplied systemd timer as part
of setup. It runs a separate one-shot maintenance container after boot and
hourly, without adding operator authority to the web server. Build its image
using the same architecture as the server:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o build/vult-server-admin ./cmd/vult-server-admin
docker build --platform linux/amd64 -f packaging/linux/Dockerfile.maintenance -t vult-maintenance .
```

Provision `/etc/vult/operator/` as a root-owned mode-0700 directory. Inside it,
place `operator.dsn` and `expiry.json`, each mode 0400 and owned by the image's
UID/GID 65532. The root-only parent prevents host service users from reading
these files; Docker exposes them only in the separate worker. Never mount this
directory, its credentials or the Docker socket into the web container. Its
runtime/authentication/owner roles stay restricted. The expiry input is:

```json
{"directory":"/var/lib/vult/originals"}
```

The originals directory must retain its service UID/GID and private permissions.
The worker mounts no document keys. Adjust the unit's paths for your deployment,
then install and enable both files:

```sh
sudo install -m 644 packaging/linux/vult-document-expiry.service /etc/systemd/system/
sudo install -m 644 packaging/linux/vult-document-expiry.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now vult-document-expiry.timer
systemctl list-timers vult-document-expiry.timer
```

Each container gets fresh private tmpfs output and is removed afterward. The
unit removes its named worker on timeout/shutdown and before retrying; it never
removes unrelated containers or volumes. Batch backlog is reported as a fixed
operational message; detailed counts stay in the invocation's private output.
For a non-container deployment, arrange equivalent credential/process isolation
when scheduling the same offline command. The network server must not gain an
operator pool or access to maintenance credentials.

## Portable server recovery

The offline `backup`, `restore` and `export` commands include originals, journal
records, citation/evidence history and saved agent outcomes. They exclude login
credentials and delegated grants; retain the separate server authentication
backup described above. Run recovery with installation credentials under the
storage-owning account, while application ingress is closed.

All three commands use `--dsn-file FILE --input FILE --output FILE`. Their
private JSON input contains `directory` and either `key_file` or `key_directory`.
For `backup`, add `recovery_key_file`, containing a 32–128-character recovery key.
An optional `book_id` limits backup to one Book. The output is an encrypted
portable archive. Keep its recovery key separately from the archive.

For `export`, require `book_id`; the output is a plaintext portable archive.
Treat it as sensitive financial data. For `restore`, require `archive_file` and
`recovery_key_file`, with an empty destination database and storage. The command
accepts new portable archives and earlier encrypted finance-only backups. With
`key_directory`, restore explicitly provisions destination tenant keys; with
`key_file`, provision all required tenant keys beforehand. Old key files are
never overwritten. Successful restore writes a private status result; reconnect
agents explicitly afterward.

Output destinations must be new. Files are published only after the stream
completes. Wrong recovery keys, truncated input and hash mismatches fail without
publishing a success result. If a commit or result publication is uncertain,
inspect the destination before retrying. Preserve any staged objects identified
by a cleanup error until recovery resolves them. Upgrades and uninstall retain
the original storage and key directories.

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
