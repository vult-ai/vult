#!/usr/bin/env python3
"""Build the allowlisted source and a native macOS installer. Publishes nothing."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import stat
import subprocess
import tarfile
import tempfile
import urllib.request
from go_notices import check as check_go_notices
from make_installer import render

VERSION = "0.5.2"
GO_VERSION = "go1.27.3"
PG_VERSION = "18.6"
PG_SHA256 = "555610c24d53e4316da5b7d3fc25c279d96856d5e0e23ee308c328c5fa881d9f"
PREFIX = Path("/Library/PrivilegedHelperTools/ai.vult.ledger")
ENV = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "C", "LC_ALL": "C", "SOURCE_DATE_EPOCH": "0", "ZERO_AR_DATE": "1", "COPYFILE_DISABLE": "1"}


def digest(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


# Institution and brand artwork is served from the shared asset origin only. Its
# catalogs and content-addressed originals never enter a source tree or package;
# synthetic test artwork under web/unit/fixtures/ is the one exception.
ARTWORK_CATALOGS = {"catalog.v1.json", "brand-logos.v1.json", "brands.v1.json"}
ARTWORK_ORIGINAL = re.compile(r"^[0-9a-f]{64}\.(?:svg|png|webp)$")


def shared_artwork(name):
    parts = Path(name).parts
    if parts[:3] == ("web", "unit", "fixtures"):
        return False
    return "shared-assets" in parts or parts[-1] in ARTWORK_CATALOGS or bool(ARTWORK_ORIGINAL.match(parts[-1]))


def interface_files(root):
    if root.is_symlink() or not root.is_dir():
        raise ValueError("interface must be a regular build directory")
    files, total = {}, 0
    for path in sorted(root.rglob("*")):
        mode = path.lstat().st_mode
        if stat.S_ISDIR(mode):
            continue
        if not stat.S_ISREG(mode) or any(part.startswith(".") for part in path.relative_to(root).parts):
            raise ValueError("unexpected interface build member")
        if shared_artwork(path.relative_to(root).as_posix()):
            raise ValueError("shared artwork is served from its origin, not packaged")
        total += path.stat().st_size
        if total > 64 << 20 or len(files) >= 4096:
            raise ValueError("interface build exceeds supported size")
        files[path.relative_to(root).as_posix()] = digest(path)
    if "index.html" not in files or not 0 < (root / "index.html").stat().st_size <= 1 << 20:
        raise ValueError("interface index is missing or oversized")
    manifest = json.dumps(files, sort_keys=True, separators=(",", ":")).encode()
    return files, hashlib.sha256(manifest).hexdigest()


def stage_interface(root, destination, expected):
    files, actual = interface_files(root)
    if actual != expected:
        raise ValueError("interface build checksum mismatch")
    destination.mkdir(parents=True, exist_ok=False)
    for name, checksum in files.items():
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(root / name, target, follow_symlinks=False)
        if target.is_symlink() or digest(target) != checksum:
            raise ValueError("interface build changed while staging")
        target.chmod(0o644)
    return actual


def run(args, cwd=None, env=None, timeout=600):
    command = [str(x) for x in args]
    process = subprocess.Popen(command, cwd=cwd, env=env or ENV,
                               stdin=subprocess.DEVNULL, start_new_session=True)
    try:
        code = process.wait(timeout=timeout)
        if code:
            raise subprocess.CalledProcessError(code, command)
    except BaseException:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
        raise


def selected_files(source):
    files = []
    for name in (source / "packaging/source-paths.txt").read_text().splitlines():
        if not name or name.startswith("#"):
            continue
        rel = Path(name)
        if rel.is_absolute() or ".." in rel.parts or name in files:
            raise ValueError("invalid or duplicate source path")
        if any(part in (".DS_Store", ".git", "__pycache__") or part.startswith("._") for part in rel.parts):
            raise ValueError("machine metadata cannot be selected as source")
        if shared_artwork(name):
            raise ValueError("shared artwork is served from its origin, not published as source")
        path = source / rel
        if path.is_symlink() or not path.is_file() or any((source / Path(*rel.parts[:i])).is_symlink() for i in range(1, len(rel.parts))):
            raise ValueError("source paths must be regular files")
        files.append(name)
    return sorted(files)


def materialize(source, target, files):
    for name in files:
        dest = target / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source / name, dest)
        dest.chmod(0o755 if name.startswith("scripts/") or name.startswith("packaging/macos/scripts/") else 0o644)


def build_postgres(work, source_archive=None):
    archive = work / "postgresql.tar.bz2"
    url = f"https://ftp.postgresql.org/pub/source/v{PG_VERSION}/postgresql-{PG_VERSION}.tar.bz2"
    source = source_archive.open("rb") if source_archive else urllib.request.urlopen(url, timeout=45)
    with source as r, archive.open("xb") as f:
        total = 0
        while chunk := r.read(1 << 20):
            total += len(chunk)
            if total > 64 << 20:
                raise ValueError("source archive exceeds supported size")
            f.write(chunk)
    if digest(archive) != PG_SHA256:
        raise ValueError("PostgreSQL source checksum mismatch")
    with tarfile.open(archive) as tar:
        members = tar.getmembers()
        for entry in members:
            rel = Path(entry.name)
            if rel.is_absolute() or ".." in rel.parts or not (entry.isfile() or entry.isdir()):
                raise ValueError("unexpected source archive member")
        tar.extractall(work, members=members, filter="data")
    pg = work / f"postgresql-{PG_VERSION}"
    env = dict(ENV, CC="/usr/bin/clang", CFLAGS="-O2 -g0", MACOSX_DEPLOYMENT_TARGET="26.0", TMPDIR=str(work))
    run(["./configure", f"--prefix={PREFIX}/postgres", "--without-icu", "--without-readline", "--without-zlib", "--without-lz4", "--without-zstd", "--disable-nls"], pg, env)
    run(["/usr/bin/make", "-j2"], pg, env)
    stage = work / "postgres-stage"
    run(["/usr/bin/make", "install", f"DESTDIR={stage}"], pg, env)
    return stage / str(PREFIX).lstrip("/") / "postgres"


def runtime_payload(pg, dest):
    output = subprocess.check_output([str(pg / "bin/postgres"), "--version"], env=ENV, timeout=10).decode().strip()
    if output != f"postgres (PostgreSQL) {PG_VERSION}":
        raise ValueError("unexpected PostgreSQL runtime version")
    for part in ("bin", "lib", "share"):
        (dest / part).mkdir(parents=True)
    for name in ("postgres", "initdb", "pg_ctl"):
        path = pg / "bin" / name
        if path.is_symlink() or not path.is_file():
            raise ValueError("runtime binary must be a regular file")
        shutil.copyfile(path, dest / "bin" / name)
        (dest / "bin" / name).chmod(0o755)
    for path in sorted((pg / "lib").glob("*.dylib")):
        if path.is_symlink():
            continue
        shutil.copyfile(path, dest / "lib" / path.name)
        (dest / "lib" / path.name).chmod(0o755)
    for path in sorted((pg / "share").rglob("*")):
        if path.is_symlink():
            raise ValueError("unexpected runtime data link")
        output = dest / "share" / path.relative_to(pg / "share")
        if path.is_dir():
            output.mkdir(exist_ok=True)
        else:
            shutil.copyfile(path, output)
            output.chmod(0o644)


def build_browser_setup(source, product, signing_identity):
    app = product / "Vult Browser Setup.app"
    contents = app / "Contents"
    (contents / "MacOS").mkdir(parents=True)
    shutil.copyfile(source / "packaging/macos/browser-setup.plist", contents / "Info.plist")
    executable = contents / "MacOS/browser-setup"
    run(["/usr/bin/clang", "-fobjc-arc", "-Os", "-arch", "arm64", "-mmacosx-version-min=26.0",
         "-Wall", "-Wextra", "-Werror", "-framework", "Cocoa", "-framework", "Security",
         "packaging/macos/browser_setup.m", "-o", executable], cwd=source)
    executable.chmod(0o755)
    run(["/usr/bin/strip", "-S", executable])
    run(["/usr/bin/codesign", "--force", "--sign", signing_identity or "-", "--options", "runtime", app])


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--output", type=Path, help="new artifact directory")
    p.add_argument("--web-dist", type=Path, help="built interface from the same source")
    p.add_argument("--web-sha256", help="expected interface file-manifest checksum")
    p.add_argument("--print-web-sha256", type=Path, help="print an interface file-manifest checksum and exit")
    p.add_argument("--go", default="go", help="Go 1.27.3 executable")
    p.add_argument("--postgres-archive", type=Path, help="use an existing checksum-verified source archive; native code is always rebuilt cleanly")
    p.add_argument("--sign-application", help="Developer ID Application signing identity")
    p.add_argument("--sign-installer", help="Developer ID Installer signing identity")
    args = p.parse_args()
    if args.print_web_sha256:
        print(interface_files(args.print_web_sha256)[1])
        return
    if not args.output or not args.web_dist or not args.web_sha256:
        p.error("--output, --web-dist and --web-sha256 are required")
    _, web_checksum = interface_files(args.web_dist)
    if web_checksum != args.web_sha256:
        p.error("interface build checksum mismatch")
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        p.error("build on Apple Silicon macOS")
    go = shutil.which(args.go)
    if not go:
        p.error("Go 1.27.3 is required")
    # Preserve only the ordinary Go module/build caches, never task-specific ones.
    env = dict(ENV, HOME=str(Path.home()), CGO_ENABLED="0", GOTOOLCHAIN="local", GOPROXY="https://proxy.golang.org,direct", GOSUMDB="sum.golang.org")
    version = subprocess.check_output([go, "version"], env=env, timeout=10).decode().split()[2]
    if version != GO_VERSION:
        p.error("the exact pinned Go version is required")
    source = Path(__file__).resolve().parents[1]
    files = selected_files(source)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="vult-package-") as temporary:
        work = Path(temporary)
        clean = work / "source"
        materialize(source, clean, files)
        stage_interface(args.web_dist, clean / "web/dist", args.web_sha256)
        # Every Go module compiled in must ship with its license notice.
        check_go_notices(go, clean, env)
        pg = build_postgres(work, args.postgres_archive)
        payload = work / "payload"
        product = payload / str(PREFIX).lstrip("/")
        product.mkdir(parents=True)
        runtime_payload(pg, product / "postgres")
        run([go, "build", "-tags=webui", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", product / "vult", "./cmd/vult"], clean, env)
        (product / "vult").chmod(0o755)
        for path in sorted(product.rglob("*")):
            if path.is_file() and (path.suffix == ".dylib" or path.parent.name == "bin" or path.name == "vult"):
                run(["/usr/bin/strip", "-S", path])
                command = ["/usr/bin/codesign", "--force", "--sign", args.sign_application or "-"]
                if args.sign_application or path.name == "vult":
                    command.extend(["--options", "runtime"])
                run(command + [path])
        for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md"):
            shutil.copyfile(clean / name, product / name)
        build_browser_setup(clean, product, args.sign_application)
        launch = payload / "Library/LaunchDaemons"
        launch.mkdir(parents=True)
        shutil.copyfile(clean / "packaging/macos/ai.vult.ledger.plist", launch / "ai.vult.ledger.plist")
        shutil.copyfile(clean / "packaging/macos/ai.vult.browser.plist", launch / "ai.vult.browser.plist")
        # Only unchanged, explicitly inventoried program files may be removed.
        inventory = {"format": "vult-program-files-v1", "version": VERSION,
                     "files": {path.relative_to(product).as_posix(): digest(path)
                               for path in sorted(product.rglob("*")) if path.is_file()},
                     "launch_daemon_sha256": digest(launch / "ai.vult.ledger.plist"),
                     "browser_daemon_sha256": digest(launch / "ai.vult.browser.plist")}
        (product / "program-files.json").write_text(json.dumps(inventory, indent=2, sort_keys=True) + "\n")
        for directory in payload.rglob("*"):
            if directory.is_dir():
                directory.chmod(0o755)
        package = output / f"vult-{VERSION}-macos-arm64.pkg"
        run(["/usr/bin/xattr", "-cr", payload])
        cmd = ["/usr/bin/pkgbuild", "--root", payload, "--scripts", clean / "packaging/macos/scripts", "--identifier", "ai.vult.ledger", "--version", VERSION, "--install-location", "/", "--ownership", "recommended", "--min-os-version", "26.0", "--compression", "latest", "--filter", r"(^|/)([.]DS_Store|[.][_]([^/]*))$"]
        if args.sign_installer:
            cmd.extend(["--sign", args.sign_installer])
        run(cmd + [package])
        # Generate the standalone installer only after the package is final.
        # Generated release files are not part of the source archive.
        installer = render(VERSION, "macos-arm64", "26.0", digest(package)).encode()
        (output / "install.sh").write_bytes(installer)
        (output / "install.sh").chmod(0o755)
        archive = output / f"vult-{VERSION}-source.tar.gz"
        # Normalize archive metadata and omit machine paths and VCS metadata.
        import gzip
        with archive.open("xb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as zipped:
            with tarfile.open(fileobj=zipped, mode="w") as tar:
                for name in files:
                    data = (clean / name).read_bytes()
                    info = tarfile.TarInfo(f"vult-{VERSION}/{name}")
                    info.size = len(data)
                    info.mode = (clean / name).stat().st_mode & 0o777
                    info.uid = info.gid = info.mtime = 0
                    tar.addfile(info, io.BytesIO(data))
        manifest = {"version": VERSION, "platform": "macos-arm64", "minimum_macos": "26.0", "go": GO_VERSION, "postgresql": PG_VERSION, "postgresql_source_sha256": PG_SHA256, "schema_sha256": digest(clean / "ledger/migrations/0001_ledger.sql"), "source_files": {name: digest(clean / name) for name in files}, "application_signing": "developer-id" if args.sign_application else "ad-hoc", "installer_signing": "developer-id" if args.sign_installer else "unsigned", "notarized": False}
        manifest["migrations"] = {path.name: digest(path) for path in sorted((clean / "ledger/migrations").glob("*.sql"))}
        manifest["installer_sha256"] = hashlib.sha256(installer).hexdigest()
        manifest["interface_sha256"] = web_checksum
        (output / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        artifacts = sorted(output.iterdir())
        (output / "SHA256SUMS").write_text("".join(f"{digest(path)}  {path.name}\n" for path in artifacts))
    print(f"Built {package.name}; no installation or publication was performed.")


if __name__ == "__main__":
    main()
