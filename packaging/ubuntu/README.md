# Native Ubuntu installation

This package targets Ubuntu 24.04 LTS on x86-64, using systemd, GNOME and Google
Chrome. Install Chrome (`google-chrome-stable`), `libnss3-tools` and Zenity first,
and use a GNOME session with its polkit authentication agent. The installer does
not download or install these prerequisites.

Keep the generated installer with its matching release archive. From your own
terminal, run:

```sh
sh install.sh ./vult-0.5.0-ubuntu-24.04-amd64.tar.gz
```

The installer requests `sudo` approval, verifies the archive checksum pinned in
the script and validates the entire program inventory before installing files.
Python source is supplied on stdin to the fixed system interpreter; no
user-writable Python script is opened after approval. Trust in the installer
itself comes from the release source through which you obtained it.

Programs and bundled PostgreSQL live under `/opt/vult`. Retained state lives
under `/var/lib/vult`. Separate locked `_vultledger` and `_vultbrowser` system
identities run the ledger and browser services. PostgreSQL is private to this
installation and does not listen on TCP. Browser sign-in uses loopback HTTPS;
certificate approval and passkey enrollment take place in your own session.

Run `/opt/vult/vult init`, then `/opt/vult/vult open` from your desktop terminal.
Keep the recovery key from initialization offline. Enable LUKS disk encryption;
file permissions do not encrypt the PostgreSQL data files.

Browser setup asks before adding this installation's exact, non-CA certificate
to your Chrome NSS certificate database. It does not install a system-wide CA.
Chrome opens a token-free local HTTPS page. Enter the one-use setup code shown
in the separate native dialog, then save your passkey and recovery codes. The
code is not passed in the browser command line. Cancelling keeps your Books
intact; run `vult open` again to continue.

Installation refuses account collisions, mismatched installation records,
modified program files, unknown program directories and service overrides.
Missing initialized storage or configuration requires recovery; installing again
does not generate a replacement ledger or new keys. Service control requires
systemd's supported unit settings and a cgroup v2 hierarchy.

An interrupted software copy can be resumed with the same installer and exact
release archive. A protected update record blocks native startup while program
files are incomplete. Repair accepts only the recorded old or selected release
bytes and completes the selected release before configuration or schema actions.
If startup fails afterward, Vult stops the verified release's services and keeps
all data and the update record. It never restores old code over a potentially
upgraded database. Missing initialized configuration, incomplete account setup
or unrecognized files still require inspection and recovery.

`vult uninstall` removes only unchanged inventoried software and its startup
entries. Books, database files, credentials, document keys, certificates,
installation records, backups and both system accounts remain for reinstall.
There is no purge operation. Modified and unknown software files are retained.

## Build offline

Use Ubuntu 24.04 x86-64 with Go 1.27.3, a C compiler, GNU make, Binutils, Bison and
Flex. Populate the ordinary Go module cache beforehand. Prepare the interface
from the same source and retain its file-manifest checksum. Supply the existing
PostgreSQL 18.6 source archive; its pinned SHA-256 is checked before extraction.

```sh
python3 scripts/package_ubuntu.py \
  --web-dist web/dist --web-sha256 INTERFACE_MANIFEST_SHA256 \
  --postgres-archive postgresql-18.6.tar.bz2 --output build/ubuntu-release
```

The builder performs no module or PostgreSQL downloads. It rebuilds PostgreSQL
without ICU, readline, zlib, LZ4, Zstandard or NLS, bundles its runtime under
`/opt/vult/postgres`, and checks ELF dependencies and protected library search
paths. It builds the command with `webui` and records source, interface, migration,
program-inventory and artifact hashes. The normalized source archive uses the
same source selection as the macOS package. Building does not install or publish
anything.
