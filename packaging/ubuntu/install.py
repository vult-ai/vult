"""Install a checksum-pinned local Ubuntu payload. Never download packages."""
import errno
import fcntl
import grp
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import platform
import pwd
import re
import stat
import subprocess
import sys
import tarfile
import time

VERSION = "0.5.0"
FORMAT = "vult-program-files-linux-v1"
PROGRAM = "/opt/vult"
RETAINED = "/var/lib/vult"
MANIFEST = "program-files.json"
UPDATE = RETAINED + "/program-update.json"
RUNTIME_UPDATE = RETAINED + "/program-update-runtime.json"
USERS = ("_vultledger", "_vultbrowser")
UNITS = {name: f"/etc/systemd/system/ai.vult.{name}.service"
         for name in ("ledger", "browser")}
SERVICE_STATES = frozenset(("active", "activating", "deactivating", "reloading", "inactive", "failed"))
POLICY = "/usr/share/polkit-1/actions/ai.vult.owner.policy"
EXTERNAL = frozenset((*UNITS.values(), POLICY))
MAX_ARCHIVE = 128 << 20
MAX_PAYLOAD = 256 << 20
MAX_FILES = 4096
ENV = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "C", "LC_ALL": "C"}
SYSTEMD_PATHS = ("/etc/systemd/system", "/run/systemd/system",
                 "/usr/local/lib/systemd/system", "/usr/lib/systemd/system",
                 "/etc/systemd/system.control", "/run/systemd/system.control",
                 "/run/systemd/transient", "/run/systemd/generator.early",
                 "/run/systemd/generator", "/run/systemd/generator.late")


class InstallError(Exception):
    pass


def sha(data):
    return hashlib.sha256(data).hexdigest()


def checksum(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def program_mode(name):
    return 0o755 if name == "vult" or name.startswith(("postgres/bin/", "postgres/lib/")) else 0o644


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise InstallError("Duplicate inventory field.")
        result[key] = value
    return result


def inventory(data, current=False):
    try:
        value = json.loads(data, object_pairs_hook=unique_object)
    except (ValueError, UnicodeError) as error:
        raise InstallError("Invalid program inventory.") from error
    if not isinstance(value, dict) or set(value) != {"format", "version", "files", "external_files"}:
        raise InstallError("Unsupported program inventory.")
    if value["format"] != FORMAT or not isinstance(value["version"], str) or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", value["version"]):
        raise InstallError("Unsupported program inventory.")
    if current and value["version"] != VERSION:
        raise InstallError("Payload version does not match the installer.")
    files, external = value["files"], value["external_files"]
    if not isinstance(files, dict) or not 1 <= len(files) <= MAX_FILES or not isinstance(external, dict) or set(external) != EXTERNAL:
        raise InstallError("Incomplete program inventory.")
    required = {"vult", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md",
                "postgres/bin/postgres", "postgres/bin/initdb", "postgres/bin/pg_ctl"}
    if not required.issubset(files) or not any(name.startswith("postgres/lib/") for name in files) or not any(name.startswith("postgres/share/") for name in files):
        raise InstallError("Incomplete native runtime inventory.")
    for name, digest in files.items():
        parts = PurePosixPath(name).parts if isinstance(name, str) else ()
        if not parts or name != PurePosixPath(name).as_posix() or name.startswith("/") or any(part in (".", "..") or part.startswith(".") for part in parts) or "\\" in name:
            raise InstallError("Invalid managed program path.")
        if name not in required and not name.startswith("postgres/lib/") and not name.startswith("postgres/share/"):
            raise InstallError("Unknown managed program path.")
        if not checksum(digest):
            raise InstallError("Invalid program checksum.")
    if any(not checksum(digest) for digest in external.values()):
        raise InstallError("Invalid external program checksum.")
    return value


def archive_payload(path, expected):
    if not checksum(expected):
        raise InstallError("A pinned archive checksum is required.")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        before = os.fstat(source.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or not 0 < before.st_size <= MAX_ARCHIVE:
            raise InstallError("Expected one bounded regular release archive.")
        data = source.read(MAX_ARCHIVE + 1)
        after = os.fstat(source.fileno())
        if (before.st_ino, before.st_size, before.st_mtime_ns) != (after.st_ino, after.st_size, after.st_mtime_ns) or sha(data) != expected:
            raise InstallError("Release archive checksum mismatch.")
    # Do not decompress or interpret a member until the complete archive is trusted.
    entries, total = {}, 0
    try:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
            for member in archive:
                name = member.name
                parts = PurePosixPath(name).parts
                if not member.isfile() or name in entries or not parts or name != PurePosixPath(name).as_posix() or name.startswith("/") or any(part in (".", "..") or part.startswith(".") for part in parts) or "\\" in name:
                    raise InstallError("Unexpected release archive member.")
                total += member.size
                if member.size < 0 or total > MAX_PAYLOAD or len(entries) >= MAX_FILES + len(EXTERNAL) + 1 or member.mode not in (0o644, 0o755):
                    raise InstallError("Release payload exceeds its bounds.")
                content = archive.extractfile(member)
                if content is None:
                    raise InstallError("Unreadable release archive member.")
                body = content.read(member.size + 1)
                if len(body) != member.size:
                    raise InstallError("Incomplete release archive member.")
                entries[name] = (body, member.mode)
    except (tarfile.TarError, EOFError, OSError) as error:
        raise InstallError("Invalid release archive.") from error
    manifest_name = PROGRAM.lstrip("/") + "/" + MANIFEST
    if manifest_name not in entries or len(entries[manifest_name][0]) > 1 << 20:
        raise InstallError("Missing release program inventory.")
    value = inventory(entries[manifest_name][0], current=True)
    selected = {PROGRAM.lstrip("/") + "/" + name: digest for name, digest in value["files"].items()}
    selected.update({name.lstrip("/"): digest for name, digest in value["external_files"].items()})
    if set(entries) != set(selected) | {manifest_name}:
        raise InstallError("Release contents do not match the program inventory.")
    for name, digest in selected.items():
        if sha(entries[name][0]) != digest:
            raise InstallError("Release member checksum mismatch.")
        wanted_mode = program_mode(name[len("opt/vult/"):]) if name.startswith("opt/vult/") else 0o644
        if entries[name][1] != wanted_mode:
            raise InstallError("Unexpected release member permissions.")
    if entries[manifest_name][1] != 0o644:
        raise InstallError("Invalid inventory permissions.")
    return value, entries


def command(args, timeout=30):
    try:
        return subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, text=True, env=ENV, cwd="/",
                              timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise InstallError("Installation control failed; stored data was preserved.") from error


class Installer:
    def __init__(self, root=Path("/"), run=command):
        self.root = root
        self.run = run

    def path(self, name):
        if not name.startswith("/") or name != str(PurePosixPath(name)) or ".." in PurePosixPath(name).parts:
            raise InstallError("Invalid installation path.")
        return self.root / name.lstrip("/")

    def exists(self, name):
        return os.path.lexists(self.path(name))

    def protected(self, name, uid=0, gid=None, mode=None, directory=False):
        fd = os.open(self.path(name), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            if info.st_uid != uid or gid is not None and info.st_gid != gid or stat.S_IMODE(info.st_mode) & 0o022 or mode is not None and stat.S_IMODE(info.st_mode) != mode:
                raise InstallError("Installation ownership or permissions are unsafe.")
            if directory != stat.S_ISDIR(info.st_mode) or not directory and (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1):
                raise InstallError("Unexpected protected installation file.")
            for attribute in ("system.posix_acl_access", "system.posix_acl_default"):
                try:
                    os.getxattr(fd, attribute)
                except OSError as error:
                    if error.errno != errno.ENODATA:
                        raise InstallError("Installation access controls could not be verified.") from error
                else:
                    raise InstallError("Unexpected installation access control list.")
            return info
        finally:
            os.close(fd)

    def read(self, name, uid=0, mode=0o644, limit=1 << 20):
        self.protected(name, uid=uid, mode=mode)
        fd = os.open(self.path(name), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as source:
            data = source.read(limit + 1)
        if len(data) > limit:
            raise InstallError("Installation record is oversized.")
        return data

    def checked(self, args, timeout=30):
        result = self.run(args, timeout=timeout)
        if result.returncode:
            raise InstallError("Installation control failed; stored data was preserved.")
        return result.stdout

    def prerequisites(self):
        if platform.system() != "Linux" or platform.machine() != "x86_64":
            raise InstallError("This package requires Ubuntu 24.04 on x86-64.")
        release = self.path("/etc/os-release").read_text()
        fields = dict(line.split("=", 1) for line in release.splitlines() if "=" in line and not line.startswith("#"))
        if fields.get("ID", "").strip('"') != "ubuntu" or fields.get("VERSION_ID", "").strip('"') != "24.04":
            raise InstallError("This package requires Ubuntu 24.04 on x86-64.")
        if not self.path("/run/systemd/system").is_dir():
            raise InstallError("A running systemd system is required.")
        if not self.path("/sys/fs/cgroup/cgroup.controllers").is_file():
            raise InstallError("A cgroup v2 system is required.")
        for name in ("/usr/bin/systemctl", "/usr/sbin/useradd", "/usr/sbin/groupadd", "/usr/sbin/usermod", "/usr/bin/certutil", "/usr/bin/zenity", "/usr/bin/google-chrome-stable"):
            if not os.access(self.path(name), os.X_OK):
                raise InstallError("Install the system tools, Chrome, libnss3-tools and Zenity first. A GNOME polkit authentication agent is also required.")
        if self.checked(["/usr/bin/systemctl", "show", "polkit.service", "--property=LoadState", "--value"]).strip() != "loaded":
            raise InstallError("Install polkit and use a GNOME session with its authentication agent.")

    def lock(self):
        self.protected("/run", directory=True)
        fd = os.open(self.path("/run/vult-install.lock"), os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
        try:
            info = os.fstat(fd)
            if info.st_uid != 0 or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600:
                raise InstallError("The installation lock is not protected.")
            self.protected("/run/vult-install.lock", mode=0o600)
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except BaseException:
            os.close(fd)
            raise

    def identity(self, name, marker, format_name):
        try:
            user = pwd.getpwnam(name)
        except KeyError:
            user = None
        try:
            group = grp.getgrnam(name)
        except KeyError:
            group = None
        if user is None and group is None and not self.exists(marker):
            return None
        if user is None or group is None or not self.exists(marker):
            raise InstallError("A system identity has no matching installation record.")
        if user.pw_uid == 0 or group.gr_gid == 0 or user.pw_gid != group.gr_gid or user.pw_dir != "/nonexistent" or user.pw_shell != "/usr/sbin/nologin":
            raise InstallError("The dedicated system identity changed.")
        if any(item.pw_uid == user.pw_uid and item.pw_name != name for item in pwd.getpwall()) or any(item.gr_gid == group.gr_gid and item.gr_name != name for item in grp.getgrall()):
            raise InstallError("A dedicated system identity is shared.")
        expected = f"{format_name} {user.pw_uid} {group.gr_gid}\n".encode()
        if self.read(marker, limit=128) != expected:
            raise InstallError("The installation identity changed.")
        locked = False
        for line in self.path("/etc/shadow").read_text().splitlines():
            fields = line.split(":")
            if fields[0] == name:
                locked = len(fields) > 1 and fields[1].startswith(("!", "*"))
        if not locked:
            raise InstallError("A dedicated system account is not locked.")
        return user.pw_uid, group.gr_gid

    def identities(self):
        ledger = self.identity(USERS[0], RETAINED + "/installation", "vult-service-v1")
        browser = self.identity(USERS[1], RETAINED + "/browser-installation", "vult-browser-v1")
        if (ledger is None) != (browser is None) or ledger and (ledger[0] == browser[0] or ledger[1] == browser[1]):
            raise InstallError("Incomplete or overlapping system identities need inspection.")
        if ledger:
            for name, allowed in ((USERS[0], {ledger[1], browser[1]}), (USERS[1], {browser[1]})):
                memberships = {item.gr_gid for item in grp.getgrall() if name in item.gr_mem}
                memberships.add(ledger[1] if name == USERS[0] else browser[1])
                if memberships != allowed:
                    raise InstallError("Dedicated account group membership changed.")
            if set(grp.getgrnam(USERS[0]).gr_mem) - {USERS[0]} or set(grp.getgrnam(USERS[1]).gr_mem) - set(USERS):
                raise InstallError("A dedicated group has unexpected members.")
        return ledger, browser

    def units(self, installed, recovery=False, stopped=False):
        names = [Path(path).name for path in UNITS.values()]
        for base in SYSTEMD_PATHS:
            for parent in (*reversed(PurePosixPath(base).parents), PurePosixPath(base)):
                if self.exists(str(parent)):
                    self.protected(str(parent), directory=True)
            for name in ("service.d", *(unit + ".d" for unit in names)):
                path = base + "/" + name
                if self.exists(path):
                    self.protected(path, directory=True)
                    if any(self.path(path).iterdir()):
                        raise InstallError("Service overrides need inspection before installation.")
            if base != "/etc/systemd/system":
                if any(self.exists(base + "/" + name) for name in names):
                    raise InstallError("A competing service definition needs inspection.")
        for path in UNITS.values():
            result = self.run(["/usr/bin/systemctl", "show", Path(path).name,
                               "--property=LoadState", "--property=FragmentPath", "--property=DropInPaths",
                               "--property=ActiveState", "--property=MainPID"], timeout=15)
            fields = {}
            for line in result.stdout.splitlines():
                if "=" not in line:
                    raise InstallError("Service state could not be verified.")
                key, value = line.split("=", 1)
                if key in fields:
                    raise InstallError("Service state could not be verified.")
                fields[key] = value
            if set(fields) != {"LoadState", "FragmentPath", "DropInPaths", "ActiveState", "MainPID"} or fields["DropInPaths"] or not fields["MainPID"].isdigit():
                raise InstallError("Service state could not be verified.")
            if recovery:
                if fields["LoadState"] == "loaded":
                    if result.returncode or fields["FragmentPath"] != path or fields["ActiveState"] not in SERVICE_STATES:
                        raise InstallError("The recorded service definition changed.")
                elif fields != {"LoadState": "not-found", "FragmentPath": "", "DropInPaths": "", "ActiveState": "inactive", "MainPID": "0"}:
                    raise InstallError("Unrecognized service state blocks program repair.")
            elif installed:
                if result.returncode or fields["LoadState"] != "loaded" or fields["FragmentPath"] != path or fields["ActiveState"] not in SERVICE_STATES:
                    raise InstallError("The installed service definition changed.")
            elif fields != {"LoadState": "not-found", "FragmentPath": "", "DropInPaths": "", "ActiveState": "inactive", "MainPID": "0"}:
                raise InstallError("An unrecognized service is still registered.")
            if stopped and (fields["MainPID"] != "0" or fields["ActiveState"] not in ("inactive", "failed")):
                raise InstallError("A recorded service has not stopped.")

    def cgroups_stopped(self):
        base = "/sys/fs/cgroup"
        if not self.path(base + "/cgroup.controllers").is_file():
            raise InstallError("A cgroup v2 system is required.")
        for path in UNITS.values():
            directory = base + "/system.slice/" + Path(path).name
            for parent in (base, base + "/system.slice", directory):
                if not self.exists(parent):
                    break
                info = self.path(parent).lstat()
                if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o022:
                    raise InstallError("Service cgroup ownership could not be verified.")
            else:
                fd = os.open(self.path(directory + "/cgroup.events"), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                try:
                    info = os.fstat(fd)
                    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) & 0o022:
                        raise InstallError("Service cgroup state could not be verified.")
                    data = os.read(fd, 4097)
                finally:
                    os.close(fd)
                try:
                    lines = data.decode("ascii").splitlines()
                except UnicodeError as error:
                    raise InstallError("Service cgroup state could not be verified.") from error
                fields = {}
                for line in lines:
                    pair = line.split()
                    if len(pair) != 2 or pair[0] in fields:
                        raise InstallError("Service cgroup state could not be verified.")
                    fields[pair[0]] = pair[1]
                if len(data) > 4096 or fields.get("populated") != "0":
                    raise InstallError("A recorded service cgroup is still populated.")

    def journal(self, archive_sha256, candidate_sha256):
        for parent in ("/", "/var", "/var/lib", RETAINED):
            if self.exists(parent):
                self.protected(parent, directory=True)
        present = [name for name in (UPDATE, RUNTIME_UPDATE) if self.exists(name)]
        if len(present) > 1:
            raise InstallError("Conflicting program update records need inspection.")
        if not present:
            return None, None
        name = present[0]
        try:
            value = json.loads(self.read(name, mode=0o600, limit=2 << 20), object_pairs_hook=unique_object)
        except (ValueError, UnicodeError) as error:
            raise InstallError("Invalid program update record.") from error
        keys = {"format", "phase", "archive_sha256", "candidate_inventory_sha256", "prior_inventory", "new_installation"}
        if not isinstance(value, dict) or set(value) != keys or value["format"] != "vult-program-update-v1" or value["phase"] not in ("pre-runtime", "runtime") or type(value["new_installation"]) is not bool:
            raise InstallError("Unsupported program update record.")
        if value["archive_sha256"] != archive_sha256 or value["candidate_inventory_sha256"] != candidate_sha256 or not checksum(archive_sha256) or not checksum(candidate_sha256):
            raise InstallError("Resume this program update with its exact original release archive.")
        if name == RUNTIME_UPDATE and value["phase"] != "runtime":
            raise InstallError("Invalid runtime update record.")
        prior = value["prior_inventory"]
        if prior is not None:
            if not isinstance(prior, str) or len(prior.encode()) > 1 << 20 or value["new_installation"]:
                raise InstallError("Invalid prior program inventory.")
            inventory(prior.encode())
        return name, value

    def save_journal(self, value):
        data = (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()
        if len(data) > 2 << 20:
            raise InstallError("Program update record exceeds its bound.")
        self.write(UPDATE, data, 0o600)

    def temporary(self, name):
        return str(PurePosixPath(name).parent / (".vult-write-" + sha(name.encode())))

    def repair_program(self, update, candidate, candidate_bytes):
        prior = inventory(update["prior_inventory"].encode()) if update["prior_inventory"] is not None else None
        old = {PROGRAM + "/" + name: digest for name, digest in prior["files"].items()} if prior else {}
        if prior:
            old.update(prior["external_files"])
            old[PROGRAM + "/" + MANIFEST] = sha(update["prior_inventory"].encode())
        new = {PROGRAM + "/" + name: digest for name, digest in candidate["files"].items()}
        new.update(candidate["external_files"])
        new[PROGRAM + "/" + MANIFEST] = sha(candidate_bytes)
        for path in set(old) | set(new):
            allowed = {new.get(path)}
            if update["phase"] == "pre-runtime":
                allowed.add(old.get(path))
            else:
                allowed.add(None)  # Restore identical candidate software after removal.
            actual = None
            if self.exists(path):
                mode = program_mode(path[len(PROGRAM) + 1:]) if path.startswith(PROGRAM + "/") else 0o644
                actual = sha(self.read(path, mode=mode, limit=128 << 20))
            if actual not in allowed:
                raise InstallError("Unrecognized program bytes block update repair.")
        directories = {PROGRAM}
        for path in set(old) | set(new):
            if path.startswith(PROGRAM + "/"):
                directories.update(str(parent) for parent in PurePosixPath(path).parents if str(parent) == PROGRAM or str(parent).startswith(PROGRAM + "/"))
        temporaries = {self.temporary(path): (program_mode(path[len(PROGRAM) + 1:]) if path.startswith(PROGRAM + "/") else 0o644) for path in new}
        for path, mode in temporaries.items():
            if self.exists(path):
                info = self.protected(path)
                if stat.S_IMODE(info.st_mode) not in (0o600, mode):
                    raise InstallError("Unexpected interrupted program write.")
        if self.exists(PROGRAM):
            for path in self.path(PROGRAM).rglob("*"):
                absolute = "/" + path.relative_to(self.root).as_posix()
                if path.is_dir() and not path.is_symlink():
                    if absolute not in directories:
                        raise InstallError("Unknown program directories block update repair.")
                    self.protected(absolute, mode=0o755, directory=True)
                elif absolute in temporaries:
                    pass  # Already checked as a fixed, root-owned transaction temp.
                elif absolute not in old and absolute not in new:
                    raise InstallError("Unknown program files block update repair.")
        return prior

    def program(self):
        manifest = PROGRAM + "/" + MANIFEST
        if not self.exists(manifest):
            if self.exists(PROGRAM) and any(self.path(PROGRAM).iterdir()) or any(self.exists(path) for path in EXTERNAL):
                raise InstallError("Unrecognized installed software needs inspection.")
            return None
        value = inventory(self.read(manifest))
        expected = {PROGRAM + "/" + name: digest for name, digest in value["files"].items()}
        expected.update(value["external_files"])
        for path, digest in expected.items():
            mode = program_mode(path[len(PROGRAM) + 1:]) if path.startswith(PROGRAM + "/") else 0o644
            if sha(self.read(path, mode=mode, limit=128 << 20)) != digest:
                raise InstallError("Installed program files changed; nothing was replaced.")
        directories = {PROGRAM}
        for name in value["files"]:
            for parent in PurePosixPath(name).parents:
                directories.add(PROGRAM + ("/" + parent.as_posix() if parent.as_posix() != "." else ""))
        for path in self.path(PROGRAM).rglob("*"):
            absolute = "/" + path.relative_to(self.root).as_posix()
            if path.is_dir() and not path.is_symlink():
                if absolute not in directories:
                    raise InstallError("Unrecognized program directories need inspection.")
                self.protected(absolute, mode=0o755, directory=True)
            elif absolute not in expected and absolute != manifest:
                raise InstallError("Unrecognized program files need inspection.")
        return value

    def preflight(self, update=None, candidate=None, candidate_bytes=None):
        for name in ("/", "/opt", "/var", "/var/lib", "/etc", "/etc/systemd", "/etc/systemd/system", "/usr", "/usr/share", "/usr/share/polkit-1", "/usr/share/polkit-1/actions"):
            self.protected(name, directory=True)
        for name in (PROGRAM, RETAINED):
            if self.exists(name):
                self.protected(name, mode=0o755, directory=True)
        ledger, browser = self.identities()
        new_preparation = update is not None and update["phase"] == "pre-runtime" and update["new_installation"]
        if ledger is None:
            allowed = {Path(UPDATE).name} if new_preparation else set()
            if self.exists(RETAINED) and any(path.name not in allowed for path in self.path(RETAINED).iterdir()):
                raise InstallError("Unrecognized retained storage needs recovery.")
        else:
            for name, uid, gid, mode in (("data", *ledger, 0o700), ("socket", *ledger, 0o755),
                                          ("browser", 0, browser[1], 0o750), ("postgres", ledger[0], browser[1], 0o750)):
                path = RETAINED + "/" + name
                if self.exists(path) or not new_preparation:
                    self.protected(path, uid, gid, mode, directory=True)
            configuration = RETAINED + "/data/configuration.json"
            if self.exists(configuration) or not new_preparation:
                self.protected(configuration, ledger[0], mode=0o400)
            elif any(self.exists(path) and any(self.path(path).iterdir()) for path in (RETAINED + "/data", RETAINED + "/browser")):
                raise InstallError("Missing retained configuration needs recovery.")
        old = self.repair_program(update, candidate, candidate_bytes) if update is not None else self.program()
        if old and ledger is None:
            raise InstallError("Installed software has no retained system identities.")
        self.units(old is not None, recovery=update is not None)
        if update is None and old is None and self.exists(RETAINED + "/data/database/postmaster.pid"):
            raise InstallError("A retained database needs inspection before reinstall.")
        return old, ledger, browser

    def directory(self, name, uid=0, gid=0, mode=0o755):
        if self.exists(name):
            self.protected(name, uid, gid, mode, directory=True)
            return
        self.path(name).mkdir(mode=mode)
        os.chown(self.path(name), uid, gid)
        os.chmod(self.path(name), mode)

    def write(self, name, data, mode=0o644):
        parent = PurePosixPath(name).parent.as_posix()
        self.protected(parent, directory=True)
        temporary_name = self.temporary(name)
        temporary = self.path(temporary_name)
        if self.exists(temporary_name):
            info = self.protected(temporary_name)
            if stat.S_IMODE(info.st_mode) not in (0o600, mode):
                raise InstallError("Unexpected interrupted installation write.")
            temporary.unlink()
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(fd, "wb") as output:
                output.write(data)
                output.flush()
                os.fchown(output.fileno(), 0, 0)
                os.fchmod(output.fileno(), mode)
                os.fsync(output.fileno())
            os.replace(temporary, self.path(name))
            self.sync_directory(parent)
        finally:
            temporary.unlink(missing_ok=True)

    def sync_directory(self, name):
        fd = os.open(self.path(name), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def disable_units(self):
        for path in UNITS.values():
            if self.exists(path):
                self.checked(["/usr/bin/systemctl", "disable", Path(path).name])

    def stop_candidate(self, candidate, candidate_bytes):
        if self.program() != candidate or sha(self.read(PROGRAM + "/" + MANIFEST)) != sha(candidate_bytes):
            raise InstallError("Candidate identity changed; service cleanup needs inspection.")
        self.units(True)
        self.disable_units()
        self.checked([PROGRAM + "/vult", "_stop"], timeout=135)
        self.units(True, stopped=True)
        self.cgroups_stopped()

    def create_identities(self):
        for name in USERS:
            self.checked(["/usr/sbin/groupadd", "--system", name])
            self.checked(["/usr/sbin/useradd", "--system", "--gid", name, "--home-dir", "/nonexistent",
                          "--no-create-home", "--shell", "/usr/sbin/nologin", "--password", "!", name])
        self.checked(["/usr/sbin/usermod", "--append", "--groups", USERS[1], USERS[0]])
        ledger = pwd.getpwnam(USERS[0])
        browser = pwd.getpwnam(USERS[1])
        for name, entry, format_name in (("installation", ledger, "vult-service-v1"),
                                         ("browser-installation", browser, "vult-browser-v1")):
            self.write(RETAINED + "/" + name, f"{format_name} {entry.pw_uid} {entry.pw_gid}\n".encode())
        return self.identities()

    def shortcut(self):
        try:
            for name in ("/usr", "/usr/local", "/usr/local/bin"):
                self.protected(name, directory=True)
        except (OSError, InstallError):
            return
        if not self.exists("/usr/local/bin/vult"):
            self.path("/usr/local/bin/vult").symlink_to(PROGRAM + "/vult")

    def install(self, value, entries, archive_sha256):
        manifest_name = PROGRAM.lstrip("/") + "/" + MANIFEST
        candidate_bytes = entries[manifest_name][0]
        if not checksum(archive_sha256) or inventory(candidate_bytes, current=True) != value:
            raise InstallError("A verified release and matching inventory are required.")
        journal_name, update = self.journal(archive_sha256, sha(candidate_bytes))
        old, ledger, browser = self.preflight(update, value, candidate_bytes)
        if update is None:
            update = {"format": "vult-program-update-v1", "phase": "pre-runtime",
                      "archive_sha256": archive_sha256, "candidate_inventory_sha256": sha(candidate_bytes),
                      "prior_inventory": self.read(PROGRAM + "/" + MANIFEST).decode() if old else None,
                      "new_installation": ledger is None}
            self.directory(RETAINED)
            self.save_journal(update)
        elif journal_name == RUNTIME_UPDATE:
            os.replace(self.path(RUNTIME_UPDATE), self.path(UPDATE))
            self.sync_directory(RETAINED)
        # Never execute a helper from mixed program bytes. Coherent prior or
        # candidate installations can stop through the canonical trusted entry.
        coherent = None
        try:
            coherent = self.program()
        except (InstallError, OSError):
            pass
        if coherent is not None and coherent != old and coherent != value:
            raise InstallError("Unrecognized installed identity blocks program repair.")
        if coherent is None:
            self.units(False, recovery=True, stopped=True)
            self.cgroups_stopped()
        self.disable_units()
        if coherent is not None:
            self.checked([PROGRAM + "/vult", "_stop"], timeout=135)
        self.units(False, recovery=True, stopped=True)
        self.cgroups_stopped()
        if ledger is None:
            ledger, browser = self.create_identities()
        for name, uid, gid, mode in (("data", *ledger, 0o700), ("socket", *ledger, 0o755),
                                      ("browser", 0, browser[1], 0o750), ("postgres", ledger[0], browser[1], 0o750)):
            self.directory(RETAINED + "/" + name, uid, gid, mode)
        self.directory(PROGRAM)
        for name, (body, mode) in sorted(entries.items()):
            if name == manifest_name:
                continue
            absolute = "/" + name
            if absolute.startswith(PROGRAM + "/"):
                for parent in reversed(PurePosixPath(absolute).parents):
                    if str(parent).startswith(PROGRAM + "/"):
                        self.directory(str(parent))
            self.write(absolute, body, mode)
        if old:
            obsolete_directories = set()
            for name, digest in old["files"].items():
                if name not in value["files"]:
                    path = PROGRAM + "/" + name
                    if self.exists(path):
                        if sha(self.read(path, mode=program_mode(name), limit=128 << 20)) != digest:
                            raise InstallError("An obsolete program file changed; it was retained.")
                        self.path(path).unlink()
                        self.sync_directory(str(PurePosixPath(path).parent))
                    obsolete_directories.update(str(parent) for parent in PurePosixPath(path).parents if str(parent).startswith(PROGRAM + "/"))
            for directory in sorted(obsolete_directories, key=len, reverse=True):
                try:
                    self.path(directory).rmdir()
                except OSError as error:
                    if error.errno not in (errno.ENOTEMPTY, errno.ENOENT):
                        raise
        self.write(PROGRAM + "/" + MANIFEST, entries[manifest_name][0])
        if self.program() != value or sha(self.read(PROGRAM + "/" + MANIFEST)) != sha(candidate_bytes):
            raise InstallError("Candidate program verification failed before startup.")
        self.checked(["/usr/bin/systemctl", "daemon-reload"])
        self.units(True)
        update["phase"] = "runtime"
        self.save_journal(update)
        if self.exists(RUNTIME_UPDATE):
            raise InstallError("Conflicting runtime update record needs inspection.")
        os.replace(self.path(UPDATE), self.path(RUNTIME_UPDATE))
        self.sync_directory(RETAINED)
        try:
            self.checked([PROGRAM + "/vult", "_install"], timeout=60)
            self.shortcut()
            self.checked(["/usr/bin/systemctl", "enable", Path(UNITS["ledger"]).name])
            self.checked(["/usr/bin/systemctl", "start", Path(UNITS["ledger"]).name])
            deadline = time.monotonic() + 90
            while self.run([PROGRAM + "/vult", "status"], timeout=5).returncode:
                if time.monotonic() >= deadline:
                    raise InstallError("The ledger did not become ready. Stored data was not reinitialized.")
                time.sleep(1)
            self.checked([PROGRAM + "/vult", "_browser_install"], timeout=60)
            self.checked(["/usr/bin/systemctl", "enable", Path(UNITS["browser"]).name])
            self.checked(["/usr/bin/systemctl", "start", Path(UNITS["browser"]).name])
            self.checked(["/usr/bin/systemctl", "is-active", "--quiet", Path(UNITS["browser"]).name])
            self.checked([PROGRAM + "/vult", "_browser_check"], timeout=50)
        except BaseException as failure:
            try:
                self.stop_candidate(value, candidate_bytes)
            except (InstallError, OSError):
                raise InstallError("Candidate startup failed and service cleanup needs inspection. All stored data was retained; no old code was restored.") from failure
            raise InstallError("Candidate startup failed. Its services were stopped and all data retained; retry the same release or perform recovery. No old code was restored.") from failure
        name, sealed = self.journal(archive_sha256, sha(candidate_bytes))
        if name != RUNTIME_UPDATE or sealed != update:
            raise InstallError("Runtime update record changed; it was retained.")
        self.path(RUNTIME_UPDATE).unlink()
        self.sync_directory(RETAINED)


def main():
    if len(sys.argv) != 3 or os.geteuid() != 0:
        raise InstallError("Run the generated installer with one local release archive; administrator approval is required.")
    os.umask(0o077)
    value, entries = archive_payload(sys.argv[1], sys.argv[2])
    installer = Installer()
    installer.prerequisites()
    lock = installer.lock()
    try:
        installer.install(value, entries, sys.argv[2])
    finally:
        os.close(lock)  # Keep the lock inode stable until the next reboot.
    print("Vult installed. Run /opt/vult/vult open from your own GNOME terminal. Books, keys and both system accounts are retained on uninstall.")


if __name__ == "__main__":
    try:
        main()
    except (InstallError, OSError) as error:
        message = str(error) if isinstance(error, InstallError) else "Installation did not complete; stored data was preserved."
        print("Vult: " + message, file=sys.stderr)
        sys.exit(1)
