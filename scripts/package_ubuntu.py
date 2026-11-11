#!/usr/bin/env python3
"""Build an offline native Ubuntu 24.04 x86-64 payload. Publishes nothing."""
import argparse
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import platform
import posixpath
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile

from go_notices import check as check_go_notices
from package_macos import (ENV as COMMON_ENV, GO_VERSION, PG_SHA256, PG_VERSION,
                           VERSION, digest, interface_files, materialize, run,
                           selected_files, stage_interface)

PREFIX = Path("/opt/vult")
TARGET = "ubuntu-24.04-amd64"
ENV = {key: value for key, value in COMMON_ENV.items() if key != "COPYFILE_DISABLE"}
SYSTEM_LIBRARIES = {"libc.so.6", "libm.so.6", "libpthread.so.0", "libdl.so.2",
                    "librt.so.1", "libresolv.so.2", "ld-linux-x86-64.so.2"}
PUBLIC_FILES = ("packaging/ubuntu/ai.vult.ledger.service",
                "packaging/ubuntu/ai.vult.browser.service",
                "packaging/ubuntu/updater/ai.vult.update-helper.service",
                "packaging/ubuntu/updater/ai.vult.update-helper.path",
                "packaging/ubuntu/install.py", "packaging/ubuntu/README.md", "scripts/package_ubuntu.py",
                "scripts/tests/test_ubuntu_installer.py")


def installer_module(source):
    spec = importlib.util.spec_from_file_location("vult_ubuntu_install", source / "packaging/ubuntu/install.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def ubuntu_host(release=Path("/etc/os-release")):
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise ValueError("build on Ubuntu 24.04 x86-64")
    fields = dict(line.split("=", 1) for line in release.read_text().splitlines() if "=" in line and not line.startswith("#"))
    if fields.get("ID", "").strip('"') != "ubuntu" or fields.get("VERSION_ID", "").strip('"') != "24.04":
        raise ValueError("build on Ubuntu 24.04 x86-64")


def build_postgres(work, source_archive):
    archive_path = work / "postgresql.tar.bz2"
    fd = os.open(source_archive, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source, archive_path.open("xb") as copied:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 64 << 20:
            raise ValueError("expected a bounded regular PostgreSQL source archive")
        total = 0
        while chunk := source.read(1 << 20):
            total += len(chunk)
            if total > 64 << 20:
                raise ValueError("PostgreSQL source exceeds supported size")
            copied.write(chunk)
    if digest(archive_path) != PG_SHA256:
        raise ValueError("PostgreSQL source checksum mismatch")
    with tarfile.open(archive_path, "r:bz2") as archive:
        members = archive.getmembers()
        if len(members) > 30000 or sum(item.size for item in members) > 512 << 20:
            raise ValueError("PostgreSQL source exceeds supported size")
        for item in members:
            path = Path(item.name)
            if path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != f"postgresql-{PG_VERSION}" or not (item.isfile() or item.isdir()):
                raise ValueError("unexpected PostgreSQL source member")
        archive.extractall(work, members=members, filter="data")
    source = work / f"postgresql-{PG_VERSION}"
    env = dict(ENV, CC="/usr/bin/cc", CFLAGS=f"-O2 -g0 -ffile-prefix-map={work}=.", TMPDIR=str(work))
    run(["./configure", f"--prefix={PREFIX}/postgres", "--without-icu", "--without-readline",
         "--without-zlib", "--without-lz4", "--without-zstd", "--disable-nls"], source, env)
    run(["/usr/bin/make", "-j2"], source, env)
    stage = work / "postgres-stage"
    run(["/usr/bin/make", "install", f"DESTDIR={stage}"], source, env)
    return stage / str(PREFIX).lstrip("/") / "postgres"


def copy_regular(source, destination, mode):
    if not stat.S_ISREG(source.lstat().st_mode):
        raise ValueError("payload input must be a regular file")
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination, follow_symlinks=False)
    destination.chmod(mode)


def runtime_payload(pg, destination):
    output = subprocess.check_output([str(pg / "bin/postgres"), "--version"], env=ENV, timeout=10).decode().strip()
    if output != f"postgres (PostgreSQL) {PG_VERSION}":
        raise ValueError("unexpected PostgreSQL runtime version")
    for part in ("bin", "lib", "share"):
        (destination / part).mkdir(parents=True)
    for name in ("postgres", "initdb", "pg_ctl"):
        copy_regular(pg / "bin" / name, destination / "bin" / name, 0o755)
    libraries = 0
    for source in sorted((pg / "lib").rglob("*")):
        if not re.search(r"\.so(?:\.[0-9.]+)?$", source.name):
            continue
        actual = source.resolve(strict=True)
        if not actual.is_relative_to((pg / "lib").resolve()) or not actual.is_file():
            raise ValueError("runtime library link escapes the build")
        copy_regular(actual, destination / "lib" / source.relative_to(pg / "lib"), 0o755)
        libraries += 1
    if not libraries:
        raise ValueError("PostgreSQL runtime libraries are missing")
    for source in sorted((pg / "share").rglob("*")):
        if source.is_symlink():
            raise ValueError("unexpected runtime data link")
        target = destination / "share" / source.relative_to(pg / "share")
        if source.is_dir():
            target.mkdir(parents=True, exist_ok=True)
        else:
            copy_regular(source, target, 0o644)


def elf_dependencies(product):
    result = {}
    bundled = {path.name for path in (product / "postgres/lib").rglob("*") if path.is_file()}
    for path in sorted(product.rglob("*")):
        if not path.is_file():
            continue
        with path.open("rb") as source:
            if source.read(4) != b"\x7fELF":
                continue
        header = subprocess.check_output(["/usr/bin/readelf", "-h", path], env=ENV, timeout=10).decode()
        if "Advanced Micro Devices X86-64" not in header:
            raise ValueError("runtime contains an unexpected ELF architecture")
        dynamic = subprocess.check_output(["/usr/bin/readelf", "-d", path], env=ENV, timeout=10).decode()
        needed = sorted(set(re.findall(r"\(NEEDED\).*\[([^\]]+)\]", dynamic)))
        if set(needed) - SYSTEM_LIBRARIES - bundled:
            raise ValueError("runtime requires an unbundled non-system library")
        search = re.findall(r"\((?:RPATH|RUNPATH)\).*\[([^\]]*)\]", dynamic)
        allowed_search = {"/opt/vult/postgres/lib", "$ORIGIN", "$ORIGIN/../lib"}
        if any(not value or any(part not in allowed_search for part in value.split(":")) for value in search):
            raise ValueError("runtime contains an unsafe library search path")
        if set(needed) & bundled and not search:
            raise ValueError("bundled runtime dependency has no protected library search path")
        origin = str(PREFIX / path.relative_to(product).parent)
        directories = [posixpath.normpath(part.replace("$ORIGIN", origin)) for value in search for part in value.split(":")]
        for library in set(needed) - SYSTEM_LIBRARIES:
            if not any(directory.startswith("/opt/vult/postgres/lib") and
                       (product / Path(directory).relative_to(PREFIX) / library).is_file() for directory in directories):
                raise ValueError("bundled runtime dependency is outside the library search path")
        result[path.relative_to(product).as_posix()] = needed
    if "vult" not in result or not {"postgres/bin/postgres", "postgres/bin/initdb", "postgres/bin/pg_ctl"}.issubset(result):
        raise ValueError("runtime executable is not an x86-64 ELF file")
    return result


def write_archive(path, entries):
    with path.open("xb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w") as archive:
            for name, (data, mode) in sorted(entries.items()):
                info = tarfile.TarInfo(name)
                info.size, info.mode = len(data), mode
                info.uid = info.gid = info.mtime = 0
                archive.addfile(info, io.BytesIO(data))


def render_installer(control, pinned):
    if not re.fullmatch(r"[0-9a-f]{64}", pinned):
        raise ValueError("invalid release archive checksum")
    delimiter = "VULT_UBUNTU_INSTALLER_PYTHON"
    if delimiter in control.splitlines():
        raise ValueError("unexpected installer source delimiter")
    # Python source is delivered on stdin, never loaded from a user-writable
    # pathname after sudo. Sudo obtains a password from the terminal, not stdin.
    return ("#!/bin/sh\n# Vult " + VERSION + " for Ubuntu 24.04 x86-64\n\n"
            "vult_install() {\n    set -eu\n"
            "    if [ \"$#\" -ne 1 ]; then\n"
            "        echo 'Vult: Supply one local release archive.' >&2\n        return 1\n    fi\n"
            "    if [ \"$(/usr/bin/id -u)\" -eq 0 ]; then\n"
            "        set -- /usr/bin/python3 -I - \"$1\" '" + pinned + "'\n"
            "    else\n        set -- /usr/bin/sudo -- /usr/bin/python3 -I - \"$1\" '" + pinned + "'\n    fi\n"
            "    \"$@\" <<'" + delimiter + "'\n" + control.rstrip() + "\n" + delimiter + "\n}\n\n"
            "# An incomplete download must not start installation.\nvult_install \"$@\"\n")


def updater_files(go, clean, env, work, control, trust):
    """The update installer, its systemd units and the trust store it ships with."""
    helper = work / "vult-update-helper"
    run([go, "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w",
         "-o", helper, "./cmd/vult-update-helper"], clean, env)
    files = {control.UPDATER_PROGRAM: helper.read_bytes()}
    for unit in control.UPDATER_UNITS:
        files[unit] = (clean / "packaging/ubuntu/updater" / Path(unit).name).read_bytes()
    for path in control.UPDATER_TRUST:
        files[path] = (trust / Path(path).name).read_bytes()
    return files


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="new artifact directory")
    parser.add_argument("--web-dist", type=Path, required=True, help="built interface from the same source")
    parser.add_argument("--web-sha256", required=True, help="expected interface file-manifest checksum")
    parser.add_argument("--postgres-archive", type=Path, required=True, help="existing pinned PostgreSQL source archive; no download")
    parser.add_argument("--go", default="go", help="Go 1.27.3 executable with a populated module cache")
    parser.add_argument("--updates", type=Path, help="include the update installer behind the app's Update button, trusting the update repository described in this directory (root.json and repository.json)")
    args = parser.parse_args()
    if args.updates:
        from package_macos import update_trust
        try:
            update_trust(args.updates)
        except ValueError as error:
            parser.error(str(error))
    ubuntu_host()
    if interface_files(args.web_dist)[1] != args.web_sha256:
        parser.error("interface build checksum mismatch")
    go = shutil.which(args.go)
    if not go or subprocess.check_output([go, "version"], env=ENV, timeout=10).decode().split()[2] != GO_VERSION:
        parser.error("the exact pinned Go version is required")
    source = Path(__file__).resolve().parents[1]
    files = selected_files(source)
    if not set(PUBLIC_FILES).issubset(files):
        parser.error("Ubuntu packaging files must be in the source selection")
    # Offline builds still verify cached module contents against committed go.sum.
    # They do not query a module proxy or the checksum service.
    env = dict(ENV, HOME=str(Path.home()), CGO_ENABLED="0", GOOS="linux", GOARCH="amd64",
               GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off", GOVCS="*:off")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="vult-package-") as temporary:
        work = Path(temporary)
        clean = work / "source"
        materialize(source, clean, files)
        stage_interface(args.web_dist, clean / "web/dist", args.web_sha256)
        control = installer_module(clean)
        run([go, "mod", "verify"], clean, env)
        # Every Go module compiled in must ship with its license notice.
        check_go_notices(go, clean, env)
        pg = build_postgres(work, args.postgres_archive)
        product = work / "product"
        product.mkdir()
        runtime_payload(pg, product / "postgres")
        tags = "webui,vult_updates" if args.updates else "webui"
        run([go, "build", "-mod=readonly", "-tags=" + tags, "-trimpath", "-buildvcs=false",
             "-ldflags=-s -w", "-o", product / "vult", "./cmd/vult"], clean, env)
        (product / "vult").chmod(0o755)
        for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md"):
            copy_regular(clean / name, product / name, 0o644)
        dependencies = elf_dependencies(product)
        external = {control.UNITS[name]: (clean / f"packaging/ubuntu/ai.vult.{name}.service").read_bytes()
                    for name in ("ledger", "browser")}
        external[control.POLICY] = (clean / "internal/local/ai.vult.owner.policy").read_bytes()
        if args.updates:
            external.update(updater_files(go, clean, env, work, control, args.updates))
        inventory = {"format": control.FORMAT, "version": VERSION,
                     "files": {path.relative_to(product).as_posix(): digest(path)
                               for path in sorted(product.rglob("*")) if path.is_file()},
                     "external_files": {path: control.sha(data) for path, data in external.items()}}
        inventory_bytes = (json.dumps(inventory, indent=2, sort_keys=True) + "\n").encode()
        control.inventory(inventory_bytes, current=True)
        entries = {"opt/vult/" + path.relative_to(product).as_posix(): (path.read_bytes(), stat.S_IMODE(path.stat().st_mode))
                   for path in sorted(product.rglob("*")) if path.is_file()}
        entries.update({path.lstrip("/"): (data, 0o755 if path == getattr(control, "UPDATER_PROGRAM", None) else 0o644)
                        for path, data in external.items()})
        entries["opt/vult/program-files.json"] = (inventory_bytes, 0o644)
        bundle = output / f"vult-{VERSION}-{TARGET}.tar.gz"
        write_archive(bundle, entries)
        bundle_checksum = digest(bundle)
        control.archive_payload(bundle, bundle_checksum)
        installer = render_installer((clean / "packaging/ubuntu/install.py").read_text(), bundle_checksum).encode()
        (output / "install.sh").write_bytes(installer)
        (output / "install.sh").chmod(0o755)
        source_entries = {f"vult-{VERSION}/" + name: ((clean / name).read_bytes(), stat.S_IMODE((clean / name).stat().st_mode))
                          for name in files}
        write_archive(output / f"vult-{VERSION}-source.tar.gz", source_entries)
        manifest = {"version": VERSION, "platform": TARGET, "minimum_ubuntu": "24.04",
                    "go": GO_VERSION, "postgresql": PG_VERSION, "postgresql_source_sha256": PG_SHA256,
                    "schema_sha256": digest(clean / "ledger/migrations/0001_ledger.sql"),
                    "source_files": {name: digest(clean / name) for name in files},
                    "migrations": {path.name: digest(path) for path in sorted((clean / "ledger/migrations").glob("*.sql"))},
                    "interface_sha256": args.web_sha256, "payload_sha256": bundle_checksum,
                    "program_manifest_sha256": control.sha(inventory_bytes), "installer_sha256": control.sha(installer),
                    "elf_dependencies": dependencies}
        (output / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        artifacts = sorted(output.iterdir())
        (output / "SHA256SUMS").write_text("".join(f"{digest(path)}  {path.name}\n" for path in artifacts))
    print(f"Built {bundle.name}; no installation or publication was performed.")


if __name__ == "__main__":
    main()
