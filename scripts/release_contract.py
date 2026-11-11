#!/usr/bin/env python3
"""Write the release contract for a built Vult package.

The contract is the signed promise an installation checks before it installs
an update: which version and platform it is for, which database schemas it
can upgrade from, which PostgreSQL it runs, and which public commit it was
built from. Everything comes from the package's manifest.json and the commit,
so the release signer and an independent rebuild produce the same contract.

    scripts/release_contract.py --manifest out/manifest.json \
        --commit 0123...cdef --out out/contract.json
"""

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

# The oldest update installer that can install this release. Raise it when a
# release needs something an older installer does not do.
MINIMUM_UPDATER = "0.7.0"
REPOSITORY = "vult-ai/vult"
MIGRATION = re.compile(r"^([0-9]{4})_[a-z0-9_]+\.sql$")


def contract(manifest: dict, commit: str, method: str) -> dict:
    platform = manifest["platform"]
    minimum_os = {"macos-arm64": "minimum_macos", "ubuntu-amd64": "minimum_ubuntu"}.get(platform)
    if minimum_os is None:
        raise SystemExit(f"no contract for platform {platform}")
    migrations = manifest["migrations"]
    schemas = sorted({MIGRATION.match(name).group(1) for name in migrations if MIGRATION.match(name)})
    if len(schemas) != len(migrations):
        raise SystemExit("every migration must be named NNNN_name.sql")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise SystemExit("--commit must be a full commit id")
    # Pinned build inputs: toolchain, bundled PostgreSQL, interface and source.
    inputs = {key: manifest[key] for key in ("go", "clang", "macos_sdk", "postgresql_source_sha256",
                                             "interface_sha256", "source_files") if key in manifest}
    canonical = json.dumps(inputs, sort_keys=True, separators=(",", ":")).encode()
    version = manifest["version"]
    return {
        "version": version,
        "platform": platform,
        "minimum_os": manifest[minimum_os],
        # Any earlier schema can upgrade: the maintenance step applies every
        # later migration in one transaction.
        "schema_from": schemas,
        "schema_to": schemas[-1],
        "migrations": migrations,
        "minimum_updater": MINIMUM_UPDATER,
        "postgresql": {"major": int(manifest["postgresql"].split(".")[0]), "version": manifest["postgresql"],
                       "source_sha256": manifest["postgresql_source_sha256"]},
        "source": {"repository": REPOSITORY, "commit": commit, "tag": "v" + version},
        "build": {"method": method, "inputs_sha256": hashlib.sha256(canonical).hexdigest()},
    }


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--manifest", type=Path, required=True, help="manifest.json from the package build")
    p.add_argument("--commit", required=True, help="the public commit the package was built from")
    p.add_argument("--method", default="identical", choices=["identical", "signature-removed"],
                   help="how the rebuild matched: every byte, or every byte once code signatures are removed")
    p.add_argument("--out", type=Path, help="write here instead of standard output")
    args = p.parse_args()
    text = json.dumps(contract(json.loads(args.manifest.read_text()), args.commit, args.method), indent=2, sort_keys=True) + "\n"
    if args.out:
        args.out.write_text(text)
    else:
        sys.stdout.write(text)


if __name__ == "__main__":
    main()
