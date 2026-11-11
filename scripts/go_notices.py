#!/usr/bin/env python3
"""Checks that THIRD_PARTY_NOTICES.md names every Go module linked into vult.

Each module compiled into the macOS or Linux executable needs a heading
"## <module path> <version>" in THIRD_PARTY_NOTICES.md, followed by its license.
The packaging scripts run this before building, so a release cannot ship a
dependency whose license notice is missing or for another version.

    python3 scripts/go_notices.py [--go /path/to/go]
"""
import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
# The platforms the native packages target.
TARGETS = (("darwin", "arm64"), ("linux", "amd64"))
FORMAT = "{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}"


# What the packages ship: vult as built with and without the update
# installer, and the update installer itself.
BUILDS = (("webui", "./cmd/vult"), ("webui,vult_updates", "./cmd/vult"), ("", "./cmd/vult-update-helper"))


def linked_modules(go="go", source=ROOT, env=None):
    """Every non-main module compiled into what the packages ship, as {(path, version)}."""
    modules = set()
    for goos, goarch in TARGETS:
        target_env = dict(env or os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0", GOWORK="off")
        for tags, package in BUILDS:
            output = subprocess.check_output(
                [go, "list", "-e", "-mod=readonly", "-deps", "-tags=" + tags, "-f", FORMAT, package],
                cwd=source, env=target_env, timeout=300,
            ).decode()
            # -e: listing still works before the browser interface is built for embedding.
            modules.update(tuple(line.split(" ", 1)) for line in output.splitlines() if line.strip())
    return modules


def missing_notices(notices, modules):
    """The modules without a "## <path> <version>" heading, sorted."""
    headings = set(re.findall(r"^## (\S+ \S+)\s*$", notices, re.M))
    return sorted(f"{path} {version}" for path, version in modules if f"{path} {version}" not in headings)


def check(go="go", source=ROOT, env=None):
    notices = (Path(source) / "THIRD_PARTY_NOTICES.md").read_text(encoding="utf-8")
    absent = missing_notices(notices, linked_modules(go, source, env))
    if absent:
        raise SystemExit("THIRD_PARTY_NOTICES.md has no license notice for these linked Go modules:\n  " + "\n  ".join(absent))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--go", default="go", help="Go executable")
    check(parser.parse_args().go)
    print("Every linked Go module has a license notice.")
