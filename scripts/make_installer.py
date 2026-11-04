#!/usr/bin/env python3
"""Prepare the version-pinned HTTPS root installer from existing release artifacts."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat

ARCHITECTURES = {
    "macos-arm64": "arm64",
    "macos-x86_64": "x86_64",
    "macos-universal": "arm64|x86_64",
}
TEMPLATE = Path(__file__).resolve().parents[1] / "packaging/install.sh.in"

ROOT_SCRIPT = r"""set -eu
umask 077
[ "$(/usr/bin/id -u)" -eq 0 ] || exit 1
stage=$(/usr/bin/mktemp -d /private/var/tmp/vult-install.XXXXXXXX)
trap '/bin/rm -rf -- "$stage"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
if ! /usr/bin/curl -q --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --max-redirs 5 --connect-timeout 15 --max-time 180 --max-filesize 134217728 --output "$stage/vult.pkg" '@PACKAGE_URL@'; then
    echo 'Vult: Could not download the release. Nothing was installed.' >&2; exit 1
fi
if ! /usr/bin/printf '%s  %s\n' '@PACKAGE_SHA256@' "$stage/vult.pkg" | /usr/bin/shasum -a 256 -c -; then
    echo 'Vult: Release checksum mismatch. Nothing was installed.' >&2; exit 1
fi
if ! /usr/sbin/installer -pkg "$stage/vult.pkg" -target /; then
    echo 'Vult: Installation did not complete. Check the system install log before trying again.' >&2; exit 1
fi
echo 'Run /Library/PrivilegedHelperTools/ai.vult.ledger/vult init from your own terminal.'
"""


def file_digest(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as f:
        before = os.fstat(f.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or not 0 < before.st_size <= 128 << 20:
            raise ValueError("expected a regular release artifact of at most 128 MiB")
        value = hashlib.file_digest(f, "sha256").hexdigest()
        after = os.fstat(f.fileno())
        if (before.st_size, before.st_mtime_ns, before.st_ino) != (after.st_size, after.st_mtime_ns, after.st_ino):
            raise ValueError("release artifact changed during verification")
        return value


def render(version, platform, minimum_macos, package_sha256):
    if not isinstance(version, str) or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?", version):
        raise ValueError("invalid release version")
    if not isinstance(platform, str) or platform not in ARCHITECTURES:
        raise ValueError("unsupported package platform")
    if not isinstance(minimum_macos, str) or not re.fullmatch(r"[1-9][0-9]?\.0", minimum_macos):
        raise ValueError("a whole-major macOS minimum is required")
    if not isinstance(package_sha256, str) or not re.fullmatch(r"[a-f0-9]{64}", package_sha256):
        raise ValueError("invalid package checksum")
    filename = f"vult-{version}-{platform}.pkg"
    url = f"https://github.com/vult-ai/vult/releases/download/v{version}/{filename}"
    # Fetch and verify in the administrator-owned directory, so an ordinary
    # process cannot replace the verified file before installation.
    root_lines = ROOT_SCRIPT.replace("@PACKAGE_URL@", url).replace("@PACKAGE_SHA256@", package_sha256).splitlines()
    apple_lines = "set installLines to {¬\n" + ", ¬\n".join("    " + json.dumps(line) for line in root_lines) + "}\n"
    result = TEMPLATE.read_text().replace("@VERSION@", version).replace("@ARCHITECTURES@", ARCHITECTURES[platform]).replace("@MINIMUM_MAJOR@", minimum_macos.split(".")[0]).replace("@ROOT_LINES@", apple_lines.rstrip())
    if re.search(r"@[A-Z_]+@", result):
        raise ValueError("unresolved installer template field")
    return result


def prepare(artifacts, output):
    manifest_path = artifacts / "manifest.json"
    manifest_bytes = manifest_path.read_bytes()
    manifest = json.loads(manifest_bytes)
    checksums = {}
    for line in (artifacts / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split("  ", 1)
        if name in checksums or not re.fullmatch(r"[a-f0-9]{64}", digest):
            raise ValueError("invalid release checksums")
        checksums[name] = digest
    if hashlib.sha256(manifest_bytes).hexdigest() != checksums.get("manifest.json"):
        raise ValueError("release manifest checksum mismatch")
    version, platform, minimum = (manifest[key] for key in ("version", "platform", "minimum_macos"))
    # Validate all metadata before deriving a local filename.
    render(version, platform, minimum, "0" * 64)
    filename = f"vult-{version}-{platform}.pkg"
    digest = file_digest(artifacts / filename)
    if digest != checksums.get(filename):
        raise ValueError("package checksum mismatch")
    script = render(version, platform, minimum, digest).encode()
    release = {"version": version, "platform": platform, "minimum_macos": minimum,
               "package": filename, "package_sha256": digest,
               "installer_sha256": hashlib.sha256(script).hexdigest()}
    output.mkdir(mode=0o755, parents=False, exist_ok=False)
    (output / "install.sh").write_bytes(script)
    (output / "install.sh").chmod(0o755)
    data = (json.dumps(release, indent=2, sort_keys=True) + "\n").encode()
    (output / "release.json").write_bytes(data)
    (output / "SHA256SUMS").write_text(f"{release['installer_sha256']}  install.sh\n{hashlib.sha256(data).hexdigest()}  release.json\n")
    return release


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--artifacts", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True, help="new directory for the root script and metadata")
    args = p.parse_args()
    release = prepare(args.artifacts, args.output)
    print(f"Prepared Vult {release['version']} root installer; nothing hosted or installed.")


if __name__ == "__main__":
    main()
