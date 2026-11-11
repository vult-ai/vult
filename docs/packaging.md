# Native packaging

The [Ubuntu package](../packaging/ubuntu/README.md#build-offline) has a separate
offline builder and installer. It bundles native PostgreSQL and keeps retained
data separate from program files, as the macOS package does.

## macOS

Build on Apple Silicon macOS 26 with Xcode command-line tools, Python 3.12 or
later, and Go 1.27.3:

```sh
cd web
npm ci --ignore-scripts
npm run build
cd ..
python3 scripts/package_macos.py --print-web-sha256 web/dist
python3 scripts/package_macos.py --output dist --web-dist web/dist \
  --web-sha256 INTERFACE_MANIFEST_SHA256
```

Use the interface manifest checksum from the same reviewed source build. The
builder refuses a missing or changed interface, stages those exact files and
embeds them with the `webui` build tag. The release manifest retains its checksum.
The source archive contains the interface source, not generated build assets.

The builder copies only `packaging/source-paths.txt` into a temporary source tree,
downloads PostgreSQL 18.6 from the PostgreSQL project, verifies its pinned SHA-256,
builds the native runtime and Vult, and creates a flat installer, source archive,
manifest and checksums. It does not install, start services, publish or run CI.
It preserves the ordinary shared Go caches. The build uses two compiler jobs.

After the package is complete, the builder generates `install.sh` with its exact
package checksum and records the script's checksum in the manifest. The script
is a release artifact, excluded from the source tree and source archive. The
tagged template and generator reproduce it from the final package metadata.
Do not rebuild the package after publishing its installer pin without
regenerating the release metadata.

The package contains the ledger executable, PostgreSQL runtime, license notices
and a launch daemon definition. A checksum inventory lists only program files
that may be removed by uninstall. It contains no database, credentials or recovery
key. On a fresh installation, the installer creates an unused dedicated service
identity, initializes its private configuration and starts that service. A
reinstall retains the existing identity and configuration. Existing unrelated
commands and accounts are not replaced.

The service uses these fixed paths:

| Purpose | Path |
| --- | --- |
| Executable and native PostgreSQL | `/Library/PrivilegedHelperTools/ai.vult.ledger/` |
| Protected data | `/Library/Application Support/ai.vult.ledger/data/` |
| Local socket | `/Library/Application Support/ai.vult.ledger/socket/service.sock` |
| Launch daemon | `/Library/LaunchDaemons/ai.vult.ledger.plist` |

Code is owned by root; data and configuration belong to `_vultledger`. The socket
directory survives reboot and is writable only by the service identity. Socket
permissions permit reaching the service, not accessing financial data without
authorization. PostgreSQL has no TCP listener and its own private socket.

### Updates from the app

`--updates DIR` adds the update installer behind the app's Update button. `DIR`
holds the update repository's first `root.json` and a `repository.json` naming
its HTTPS `metadata_url` and `targets_url`; the package trusts only that root.
The build also writes `vult-VERSION-macos-arm64.tar`, the program directory as
an update installs it. That archive is reproducible: two builds of the same
commit with the pinned toolchain produce identical bytes. To list it in the
update repository, write its release contract from the build's manifest:

```sh
python3 scripts/release_contract.py --manifest dist/manifest.json \
  --commit FULL_COMMIT --out dist/contract.json
```

After the package bytes and checksums are final, prepare the root installer:

```sh
python3 scripts/make_installer.py --artifacts dist --output dist/vult-sh
```

Serve its `install.sh` as the HTTPS response for `/` at `vult.sh`. The short
installation command is `curl -fsSL https://vult.sh | sh`; details are in
[installation](install.md). This preparation does not change hosting.

Without signing identities, the package is unsigned and its executable signatures
are ad-hoc. That does not identify an Apple-registered developer or provide an
Apple notarization ticket. For signed browser downloads, pass Developer ID identities:

```sh
python3 scripts/package_macos.py --output dist --web-dist web/dist \
  --web-sha256 INTERFACE_MANIFEST_SHA256 \
  --sign-application "Developer ID Application: SIGNING_IDENTITY" \
  --sign-installer "Developer ID Installer: SIGNING_IDENTITY"
```

For that download route, notarize and staple the signed installer through Apple's
supported tooling. Regenerate the package checksums and root installer afterwards
so its pin describes the final bytes.

The terminal installer never disables macOS security checks or clears quarantine.
Test its actual download and installation on a clean Mac before distribution.
Also qualify reboot, ordinary-user file denial, native approval, MCP operation,
revocation, retry recovery and encrypted backup/restore. Post synthetic entries,
uninstall, reinstall and compare every recorded journal row and retry receipt;
the retained account ID and credential bytes must also match. Test interrupted
installation and missing-data recovery failures. Source-level tests model file
removal but do not prove launchd, installed ownership, native prompts or a
warning-free experience.
