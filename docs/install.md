# Install Vult

Run this from your own terminal on a supported Mac:

```sh
curl -fsSL https://vult.sh | sh
```

The script checks your processor and macOS version before asking for native
administrator approval. It downloads one fixed GitHub release into an
administrator-owned temporary directory, verifies its pinned SHA-256, then runs
the macOS installer. It removes its download on success or failure. No Docker,
Homebrew, Go or compilation is needed.

Run the script as your normal user, never with `sudo sh`. The native approval
prompt authorizes the protected download and macOS package installer. The script
performs no telemetry and fetches no other packages or tools.

The package contains the native ledger service and PostgreSQL. After installation:

```sh
vult init
vult book create --name "Personal" --currency USD --scale 2
```

Keep the recovery key shown by `init` offline. The installer does not initialize
your Books, request financial records or send any of them over the network.

If an existing command or unsafe shell directory prevents creation of the short
`vult` command, use `/Library/PrivilegedHelperTools/ai.vult.ledger/vult`.

## Inspect or pin an installer

To download and read the script before running it:

```sh
curl -fsSL https://vult.sh -o install.sh
less install.sh
sh install.sh
```

The root script pins an explicit version and package checksum; it never downloads
`main` or an unversioned binary. Keep the exact script with its GitHub release so
a particular version remains available after the root changes. All installation
operations are inside a function invoked on the final line. An interrupted
download that does not reach that line cannot start installation.

The package checksum checks that the downloaded bytes match the release selected
by the script. Trust in the script itself comes from the HTTPS distribution path;
this is not an Apple Developer ID signature or notarization claim.

## Remove or reinstall

From your own terminal:

```sh
vult uninstall
```

Before requesting administrator approval, Vult prints where your data remains.
Removal stops the ledger and its private PostgreSQL, then removes only unchanged
program files listed in the installed package inventory, the startup entry and
Vult's own shell shortcut. Modified and unrecognized files are retained and
reported. If stopping the service or database fails, program removal stops too.

Uninstall never deletes:

- Books and database files in `/Library/Application Support/ai.vult.ledger/data/`;
- the credentials and recovery key stored with that database;
- the installation record or the `_vultledger` system account and group;
- backups and exports, wherever you saved them.

There is no purge or delete-data command. Financial data remains protected by the
same service identity after removal. Back up your Books before removing or
upgrading software, and keep the recovery key separately.

Reinstall with the normal install command. Vult reuses the retained account,
credentials and database; it never resets an initialized ledger. Missing
configuration, a missing initialized database, or a missing ledger schema stops
startup and requires recovery. Installing over these conditions does not create
an empty replacement or generate replacement keys.

Version 0.2.0 upgrades a recognized v0.1.0 ledger through checksum-verified forward
migrations. It preserves Books, journal rows, request outcomes, connections and
local keys. Missing, altered, gapped or unknown migration receipts stop startup
and require recovery; they never trigger a new empty ledger. Released migrations
are unchanged. PostgreSQL remains on major version 18; a major-version database
upgrade is not performed by this installer.

## Distribution

The source is in `packaging/install.sh.in` and `scripts/make_installer.py`.
After building the final package and `SHA256SUMS`, run:

```sh
python3 scripts/make_installer.py --artifacts dist --output dist/vult-sh
```

This creates a new directory containing `install.sh`, `release.json` and its own
`SHA256SUMS`. Existing package artifacts are not modified. Recreate this bundle
when the package bytes change, including after signing or stapling.

Serve `install.sh` for HTTPS `GET /` at `vult.sh`, with
`Content-Type: text/plain; charset=utf-8`, `X-Content-Type-Options: nosniff`, and
`Cache-Control: no-cache`. Serve the file verbatim, with the same response for
browsers and terminal clients. The website remains at `vult.ai`.

Attach the generated `install.sh` to the release instead of committing it to the
source tree. Its SHA-256 is recorded in the release manifest. Reproduce the
script with the tagged template and generator, then compare the generated file,
the release attachment and the actual HTTPS response byte for byte.
Create the repository and GitHub release with the package and checksums first,
then enable the HTTPS root script, and announce only after a real installation
check. The distribution domain needs two-factor authentication and registrar
lock. Serving the script does not require application login on that domain.

The release manifest determines the package and supported processor checks.
A universal package must actually be built and tested for both processors before
its manifest is used; script generation does not qualify another architecture.

The script does not remove quarantine, disable Gatekeeper or use the installer's
untrusted-certificate override. macOS security policies remain active. Qualify
the actual terminal path on a clean Mac, including administrator cancellation,
service isolation and reboot, before claiming the installation experience works.
