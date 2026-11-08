# Passkey sign-in

The built-in passkey sign-in uses this installation's exact HTTPS origin and
relying-party host. Credentials prove identity; Open separately resolves tenant
and Book permissions. An identity cannot choose a tenant in a sign-in request.
Managed identity integrations continue through their separate provider interface.

Prepare a first owner through the offline administrator tool. Put the username,
HTTPS origin and optional existing `tenant_id` in a private input file:

```json
{"username":"owner","origin":"https://ledger.example.com"}
```

```sh
vult-server-admin passkey-setup --dsn-file operator.dsn --input owner.json --output enrollment.json
```

The command reserves a new private output file before changing anything. It
contains a five-minute setup link, tenant ID and any newly generated recovery
codes. Keep it private; do not paste it into service logs. The setup token is in
the URL fragment, removed by the sign-in page before any request. The browser
submits it only to this installation in an origin- and CSRF-checked request.
The same setup code can be entered on `/auth/setup` without putting it in a
browser-launch command. It has the same authority, expiry and one-use checks as
the setup link. The page never puts an entered code into its URL or storage.

Configure authentication with `mode: "passkey"` and the same `username`, restricted
auth/owner database URL files and canonical HTTPS origin. Both database roles are
required; neither is an operator credential. A built-in passphrase/authenticator
installation can enable `passkeys: true` in its existing `builtin` configuration
after preparing a passkey for that same owner and tenant. OIDC and external
identity configuration are unchanged and do not enable the built-in passkey path.

Open the setup link in your own browser, create the passkey and complete the
device's user-verification prompt. Later visits to `/auth/login` offer passkey
sign-in. In a combined built-in configuration, the passphrase and authenticator
form remains available. Sign-in cookies are Secure, HttpOnly and host-only;
credential replies and setup authority never become agent credentials.

Each ceremony is bound to the initiating browser, exact origin, purpose and
identity epoch, expires after five minutes, and can be used only once. Failed
assertions consume their ceremony; retrying starts a fresh one. A successful
sign-in creates a new session; it does not refresh a stolen older session.
Existing CSRF and five-minute write-freshness checks still govern owner writes.

For offline lost-key recovery, use the same private input with:

```sh
vult-server-admin passkey-recover --dsn-file operator.dsn --input owner.json --output recovery-enrollment.json
```

Recovery preserves the identity, tenant and Books, revokes earlier passkeys and
identity sessions, invalidates pending ceremonies and replaces recovery codes.
Use its new setup link to register again. Setup never reopens automatically when
credentials are missing. A lost setup result can be replaced through this explicit
offline recovery command. No financial restore imports sign-in authority.

A self-hosted owner can also choose **Use a recovery code** on the sign-in page.
One accepted code revokes earlier sign-ins and passkeys and prepares a replacement
credential in that same browser. The code does not create a session: complete the
replacement passkey's user-verification prompt to sign in. Each code works once;
unused codes remain valid. Failed code attempts share a durable bounded window.
If a reply is lost after a code is used, another saved code or offline recovery
can replace the unfinished setup. Local browser recovery requires native
administrator approval instead; the online code endpoint is disabled there.

## Local browser transport

The local browser transport uses HTTPS at a randomly named, installation-specific
`vult-<identifier>.localhost` origin with a fixed port. It binds only a literal
loopback address, verifies the actual listener, and rejects proxy headers, foreign
origins, wrong TLS server names and plaintext requests. Its certificate is a
self-signed server leaf for that one hostname, with no certificate-signing
authority. Certificate trust and native service setup are separate installation
steps; a configuration file does not install trust in the operating system.

Local browser configuration requires `local_browser: true`, private
`local_certificate_file` and `local_key_file` paths, and built-in passkey sign-in.
The HTTPS origin's explicit port must match `listen`. It cannot enable an
external identity provider or trusted proxies. The local listener exposes the
bundled interface and session API, not HTTP MCP, OAuth or owner-administration
routes. Agents continue to use the local Unix socket.

The macOS package runs the browser as `_vultbrowser`, separately from
`_vultledger`. It gives that process only runtime, authentication and tenant-owner
database credentials. Database files, operator credentials, document keys and
backup recovery material remain private to the ledger account. PostgreSQL stays
off TCP; its shared socket directory is writable only by the ledger account.

Installation asks for administrator approval and prepares the public certificate
without trusting it. Run `vult open` from your own terminal: its visible setup
application requests macOS approval for SSL trust scoped to the one local
hostname. The certificate has no CA authority. After normal HTTPS verification,
native enrollment approval displays recovery codes to keep offline and opens
the five-minute setup link in your browser.
Later runs open the sign-in page. The link stays out of command-line arguments,
files and logs. If a passkey is lost, run `vult browser reset`; native approval
revokes the old browser credentials while retaining the same Books and origin.

Upgrades and reinstall retain the local identity, certificate, credentials and
data. Uninstall stops both processes and removes their startup entries, while
retaining those files and both system accounts for reinstall. Certificate trust
for the installation-specific hostname is retained too; no listener runs after
uninstall. A certificate that expires or fails verification blocks the browser;
it does not enable plaintext access or choose another origin.
