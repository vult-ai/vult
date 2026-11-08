from copy import deepcopy
import errno
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import package_ubuntu as package

install = package.installer_module(ROOT)


def payload():
    files = {"vult": b"synthetic command", "LICENSE": b"license", "NOTICE": b"notice",
             "THIRD_PARTY_NOTICES.md": b"notices", "postgres/bin/postgres": b"postgres",
             "postgres/bin/initdb": b"initdb", "postgres/bin/pg_ctl": b"pg_ctl",
             "postgres/lib/plpgsql.so": b"module", "postgres/share/postgres.bki": b"catalog"}
    external = {install.UNITS[name]: (ROOT / f"packaging/ubuntu/ai.vult.{name}.service").read_bytes()
                for name in ("ledger", "browser")}
    external[install.POLICY] = b"synthetic policy"
    value = {"format": install.FORMAT, "version": install.VERSION,
             "files": {name: install.sha(data) for name, data in files.items()},
             "external_files": {name: install.sha(data) for name, data in external.items()}}
    entries = {"opt/vult/" + name: (data, 0o755 if name == "vult" or name.startswith(("postgres/bin/", "postgres/lib/")) else 0o644)
               for name, data in files.items()}
    entries.update({name.lstrip("/"): (data, 0o644) for name, data in external.items()})
    entries["opt/vult/program-files.json"] = (json.dumps(value).encode(), 0o644)
    return value, entries


class FixtureInstaller(install.Installer):
    """Redirect paths and OS controls to synthetic files; never invoke a process."""
    def __init__(self, root):
        super().__init__(root, self.control)
        self.owners = {}
        self.calls = []
        self.fail = set()
        self.unit_changes = {}
        self.acl_failure = None
        self.write_failures = set()
        self.timeouts = []
        self.cgroup_busy = False
        self.actions = {}
        self.syncs = []

    def protected(self, name, uid=0, gid=None, mode=None, directory=False):
        owner, group = self.owners.get(name, (0, 0))
        if owner != uid or gid is not None and group != gid:
            raise install.InstallError("Unexpected synthetic owner.")
        effect = self.acl_failure if self.acl_failure is not None else OSError(errno.ENODATA, "no ACL")
        with mock.patch.object(install.os, "getxattr", side_effect=effect, create=True):
            return super().protected(name, os.getuid(), os.getgid(), mode, directory)

    def directory(self, name, uid=0, gid=0, mode=0o755):
        if self.exists(name):
            self.protected(name, uid, gid, mode, directory=True)
        else:
            self.path(name).mkdir(mode=mode)
            self.path(name).chmod(mode)
            self.owners[name] = (uid, gid)

    def write(self, name, data, mode=0o644):
        if name in self.write_failures:
            raise OSError(errno.ENOSPC, "synthetic full filesystem")
        self.protected(str(Path(name).parent), directory=True)
        temporary = self.path(self.temporary(name))
        if temporary.exists():
            temporary.unlink()
        self.path(name).write_bytes(data)
        self.path(name).chmod(mode)
        self.owners[name] = (0, 0)

    def control(self, args, timeout=30):
        self.calls.append(tuple(args))
        self.timeouts.append((tuple(args), timeout))
        if tuple(args) in self.fail:
            return subprocess.CompletedProcess(args, 1, "", "")
        if tuple(args) in self.actions:
            self.actions[tuple(args)]()
        if args[:2] == ["/usr/bin/systemctl", "show"]:
            path = "/etc/systemd/system/" + args[2]
            loaded = self.exists(path)
            fields = {"LoadState": "loaded" if loaded else "not-found",
                      "FragmentPath": path if loaded else "", "DropInPaths": "",
                      "ActiveState": "inactive", "MainPID": "0"}
            fields.update(self.unit_changes)
            return subprocess.CompletedProcess(args, 0, "".join(f"{key}={value}\n" for key, value in fields.items()), "")
        return subprocess.CompletedProcess(args, 0, "", "")

    def cgroups_stopped(self):
        if self.cgroup_busy:
            raise install.InstallError("Synthetic service cgroup is populated.")

    def sync_directory(self, name):
        self.syncs.append(name)


class UbuntuInstallerTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.fixture = FixtureInstaller(self.root)
        self.value, self.entries = payload()
        self.pin = "1" * 64
        self.users = {name: SimpleNamespace(pw_name=name, pw_uid=uid, pw_gid=uid,
                                           pw_dir="/nonexistent", pw_shell="/usr/sbin/nologin")
                      for name, uid in zip(install.USERS, (997, 998))}
        self.groups = {name: SimpleNamespace(gr_name=name, gr_gid=uid,
                                             gr_mem=[install.USERS[0]] if uid == 998 else [])
                       for name, uid in zip(install.USERS, (997, 998))}
        self.addCleanup(mock.patch.stopall)
        def lookup(items, key):
            if key not in items:
                raise KeyError(key)
            return items[key]
        mock.patch.object(install.pwd, "getpwnam", side_effect=lambda name: lookup(self.users, name)).start()
        mock.patch.object(install.pwd, "getpwall", side_effect=lambda: list(self.users.values())).start()
        mock.patch.object(install.grp, "getgrnam", side_effect=lambda name: lookup(self.groups, name)).start()
        mock.patch.object(install.grp, "getgrall", side_effect=lambda: list(self.groups.values())).start()
        for name in ("/opt", "/var/lib", "/etc/systemd/system", "/usr/share/polkit-1/actions", "/usr/local/bin"):
            self.fixture.path(name).mkdir(parents=True, exist_ok=True)
        self.fixture.path("/etc/shadow").write_text("_vultledger:!:0:0:99999:7:::\n_vultbrowser:!:0:0:99999:7:::\n")

    def put(self, name, data=b"synthetic", mode=0o644, uid=0, gid=0):
        path = self.fixture.path(name)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        path.chmod(mode)
        self.fixture.owners[name] = (uid, gid)

    def retained(self):
        self.fixture.directory(install.RETAINED)
        for name, uid, gid, mode in (("data", 997, 997, 0o700), ("socket", 997, 997, 0o755),
                                      ("browser", 0, 998, 0o750), ("postgres", 997, 998, 0o750)):
            self.fixture.directory(install.RETAINED + "/" + name, uid, gid, mode)
        for name, text in (("installation", "vult-service-v1 997 997\n"),
                            ("browser-installation", "vult-browser-v1 998 998\n")):
            self.put(install.RETAINED + "/" + name, text.encode())
        self.put(install.RETAINED + "/data/configuration.json", b"synthetic credentials", 0o400, 997, 997)
        self.put(install.RETAINED + "/data/journal", b"retained journal", 0o600, 997, 997)
        self.put(install.RETAINED + "/data/document-keys/key", b"retained key", 0o400, 997, 997)
        self.put(install.RETAINED + "/browser-identity.json", b"retained public identity")
        self.put(install.RETAINED + "/backup.age", b"retained encrypted backup", 0o600)

    def installed(self):
        self.retained()
        for name, (data, mode) in self.entries.items():
            self.put("/" + name, data, mode)
        self.fixture.path(install.PROGRAM).chmod(0o755)
        for path in self.fixture.path(install.PROGRAM).rglob("*"):
            if path.is_dir():
                path.chmod(0o755)

    def snapshot(self, path):
        return {name.relative_to(path).as_posix(): (name.read_bytes(), stat.S_IMODE(name.stat().st_mode))
                for name in path.rglob("*") if name.is_file() and not name.is_symlink()}

    def retained_snapshot(self):
        return {name: value for name, value in self.snapshot(self.fixture.path(install.RETAINED)).items()
                if name not in (Path(install.UPDATE).name, Path(install.RUNTIME_UPDATE).name)}

    def candidate(self):
        value, entries = deepcopy(self.value), deepcopy(self.entries)
        for name, data in (("vult", b"candidate command"), ("NOTICE", b"candidate notice")):
            old, mode = entries["opt/vult/" + name]
            entries["opt/vult/" + name] = (data, mode)
            value["files"][name] = install.sha(data)
        name = install.UNITS["browser"]
        body, mode = entries[name.lstrip("/")]
        body += b"\n# Local browser service\n"
        entries[name.lstrip("/")] = (body, mode)
        value["external_files"][name] = install.sha(body)
        entries["opt/vult/program-files.json"] = (json.dumps(value).encode(), 0o644)
        return value, entries

    def archive(self, entries=None):
        path = self.root / "release.tar.gz"
        package.write_archive(path, self.entries if entries is None else entries)
        return path, package.digest(path)

    def test_pinned_payload_has_exact_program_and_external_inventory(self):
        path, pinned = self.archive()
        value, entries = install.archive_payload(path, pinned)
        self.assertEqual(value, self.value)
        self.assertEqual(entries, self.entries)

    def test_wrong_pin_is_rejected_before_archive_parsing(self):
        path, _ = self.archive()
        with mock.patch.object(install.tarfile, "open") as parse:
            with self.assertRaises(install.InstallError):
                install.archive_payload(path, "0" * 64)
            parse.assert_not_called()

    def test_archive_links_are_rejected(self):
        path, pinned = self.archive()
        link = self.root / "linked.tar.gz"
        link.symlink_to(path)
        with self.assertRaises(OSError):
            install.archive_payload(link, pinned)
        link.unlink()
        os.link(path, link)
        with self.assertRaises(install.InstallError):
            install.archive_payload(path, pinned)

    def test_member_checksum_unknown_file_and_wrong_mode_are_rejected(self):
        for kind in ("checksum", "unknown", "mode", "missing", "traversal"):
            with self.subTest(kind=kind):
                entries = deepcopy(self.entries)
                if kind == "checksum":
                    entries["opt/vult/vult"] = (b"changed", 0o755)
                elif kind == "unknown":
                    entries["opt/vult/unknown"] = (b"unknown", 0o644)
                elif kind == "mode":
                    entries["opt/vult/vult"] = (entries["opt/vult/vult"][0], 0o644)
                elif kind == "missing":
                    del entries[install.POLICY.lstrip("/")]
                else:
                    entries["opt/vult/../../etc/other"] = (b"unexpected", 0o644)
                path = self.root / (kind + ".tar.gz")
                package.write_archive(path, entries)
                with self.assertRaises(install.InstallError):
                    install.archive_payload(path, package.digest(path))

    def test_duplicate_and_nonregular_members_are_rejected(self):
        for kind in ("duplicate", "symlink", "hardlink", "directory"):
            with self.subTest(kind=kind):
                path = self.root / (kind + ".tar.gz")
                with tarfile.open(path, "w:gz") as archive:
                    entry = tarfile.TarInfo("opt/vult/vult")
                    entry.mode = 0o755
                    if kind == "duplicate":
                        entry.size = 1
                        archive.addfile(entry, io.BytesIO(b"x"))
                        archive.addfile(entry, io.BytesIO(b"x"))
                    else:
                        entry.type = {"symlink": tarfile.SYMTYPE, "hardlink": tarfile.LNKTYPE, "directory": tarfile.DIRTYPE}[kind]
                        entry.linkname = "/etc/other"
                        archive.addfile(entry)
                with self.assertRaises(install.InstallError):
                    install.archive_payload(path, package.digest(path))

    def test_inventory_never_accepts_arbitrary_external_paths(self):
        for kind in ("extra", "missing", "program-path", "duplicate-field", "unknown-field", "version"):
            with self.subTest(kind=kind):
                value = deepcopy(self.value)
                if kind == "extra":
                    value["external_files"]["/etc/other"] = "0" * 64
                elif kind == "missing":
                    value["external_files"].pop(install.POLICY)
                elif kind == "program-path":
                    value["files"]["postgres/lib/../../../var/lib/vult/data/journal"] = "0" * 64
                elif kind == "unknown-field":
                    value["retained"] = "/var/lib/vult"
                elif kind == "version":
                    value["version"] = "1.0.0"
                data = json.dumps(value).encode()
                if kind == "duplicate-field":
                    data = data[:-1] + b',"version":"0.4.0"}'
                with self.assertRaises(install.InstallError):
                    install.inventory(data, current=True)

    def test_unknown_accounts_or_partial_markers_are_rejected_before_control(self):
        with self.assertRaises(install.InstallError):
            self.fixture.preflight()
        self.assertEqual(self.fixture.calls, [])
        self.retained()
        self.fixture.path(install.RETAINED + "/browser-installation").unlink()
        with self.assertRaises(install.InstallError):
            self.fixture.preflight()
        self.assertEqual(self.fixture.calls, [])

    def test_changed_identity_unlocked_account_and_extra_group_are_rejected(self):
        self.retained()
        for kind in ("marker", "locked", "group", "overlap", "shell"):
            with self.subTest(kind=kind):
                marker = self.fixture.path(install.RETAINED + "/installation")
                shadow = self.fixture.path("/etc/shadow")
                old_marker, old_shadow = marker.read_bytes(), shadow.read_bytes()
                old_uid, old_shell = self.users[install.USERS[0]].pw_uid, self.users[install.USERS[0]].pw_shell
                if kind == "marker":
                    marker.write_text("vult-service-v1 999 997\n")
                elif kind == "locked":
                    shadow.write_text("_vultledger:unlocked:0:0:99999:7:::\n_vultbrowser:!:0:0:99999:7:::\n")
                elif kind == "group":
                    self.groups["extra"] = SimpleNamespace(gr_name="extra", gr_gid=999, gr_mem=[install.USERS[0]])
                elif kind == "overlap":
                    self.users[install.USERS[0]].pw_uid = 998
                else:
                    self.users[install.USERS[0]].pw_shell = "/bin/sh"
                with self.assertRaises(install.InstallError):
                    self.fixture.preflight()
                marker.write_bytes(old_marker)
                shadow.write_bytes(old_shadow)
                self.groups.pop("extra", None)
                self.users[install.USERS[0]].pw_uid, self.users[install.USERS[0]].pw_shell = old_uid, old_shell
        self.assertEqual(self.fixture.calls, [])

    def test_missing_retained_configuration_never_becomes_fresh_storage(self):
        self.retained()
        self.fixture.path(install.RETAINED + "/data/configuration.json").unlink()
        with self.assertRaises(OSError):
            self.fixture.preflight()
        self.assertEqual(self.fixture.calls, [])

    def test_unknown_program_and_changed_unit_or_policy_fail_before_stop(self):
        self.installed()
        for name in ("/opt/vult/unknown", install.UNITS["ledger"], install.POLICY):
            with self.subTest(name=name):
                path = self.fixture.path(name)
                existed = path.exists()
                old = path.read_bytes() if existed else None
                self.put(name, b"changed")
                with self.assertRaises(install.InstallError):
                    self.fixture.install(self.value, self.entries, self.pin)
                self.assertFalse(any(args[-1] == "_stop" for args in self.fixture.calls))
                if existed:
                    path.write_bytes(old)
                else:
                    path.unlink()

    def test_dropins_and_alternate_fragment_fail_before_stop(self):
        self.installed()
        for kind in ("dropin", "global", "fragment", "reported-dropin", "competing"):
            with self.subTest(kind=kind):
                change = None
                if kind in ("dropin", "global", "competing"):
                    name = {"dropin": "/etc/systemd/system/ai.vult.ledger.service.d/extra.conf",
                            "global": "/run/systemd/system/service.d/extra.conf",
                            "competing": "/run/systemd/system/ai.vult.ledger.service"}[kind]
                    self.put(name)
                    change = self.fixture.path(name)
                else:
                    self.fixture.unit_changes = {"FragmentPath": "/run/systemd/system/other.service"} if kind == "fragment" else {"DropInPaths": "/etc/systemd/system/service.d/extra.conf"}
                with self.assertRaises(install.InstallError):
                    self.fixture.install(self.value, self.entries, self.pin)
                self.assertFalse(any(args[-1] == "_stop" for args in self.fixture.calls))
                if change:
                    change.unlink()
                self.fixture.unit_changes = {}

    def test_stop_failure_does_not_replace_software_or_retained_data(self):
        self.installed()
        before_program = self.snapshot(self.fixture.path(install.PROGRAM))
        before_data = self.retained_snapshot()
        self.fixture.fail.add(("/opt/vult/vult", "_stop"))
        with self.assertRaises(install.InstallError):
            self.fixture.install(self.value, self.entries, self.pin)
        self.assertEqual(self.snapshot(self.fixture.path(install.PROGRAM)), before_program)
        self.assertEqual(self.retained_snapshot(), before_data)
        self.assertTrue(self.fixture.exists(install.UPDATE))
        self.assertFalse(self.fixture.exists(install.RUNTIME_UPDATE))
        self.assertFalse(any(args[1] in ("daemon-reload", "start") for args in self.fixture.calls if len(args) > 1))

    def test_acl_unsupported_and_errors_refuse_before_any_installation_mutation(self):
        self.installed()
        before_files = self.snapshot(self.root)
        before_entries = {path.relative_to(self.root).as_posix(): stat.S_IMODE(path.lstat().st_mode)
                          for path in self.root.rglob("*")}
        before_owners = deepcopy(self.fixture.owners)
        for name, number in (("unsupported", errno.ENOTSUP), ("operation-unsupported", errno.EOPNOTSUPP),
                             ("denied", errno.EACCES), ("io-error", errno.EIO)):
            for attribute in ("access", "default"):
                with self.subTest(error=name, attribute=attribute):
                    failure = OSError(number, "synthetic ACL failure")
                    self.fixture.acl_failure = failure if attribute == "access" else [OSError(errno.ENODATA, "no access ACL"), failure]
                    with self.assertRaises(install.InstallError):
                        self.fixture.install(self.value, self.entries, self.pin)
                    self.assertEqual(self.fixture.calls, [])
                    self.assertEqual(self.snapshot(self.root), before_files)
                    self.assertEqual({path.relative_to(self.root).as_posix(): stat.S_IMODE(path.lstat().st_mode)
                                      for path in self.root.rglob("*")}, before_entries)
                    self.assertEqual(self.fixture.owners, before_owners)

    def test_upgrade_preserves_storage_and_orders_ledger_before_browser(self):
        self.installed()
        before = self.snapshot(self.fixture.path(install.RETAINED))
        self.fixture.install(self.value, self.entries, self.pin)
        self.assertEqual(self.snapshot(self.fixture.path(install.RETAINED)), before)
        calls = self.fixture.calls
        self.assertLess(calls.index(("/opt/vult/vult", "_stop")), calls.index(("/usr/bin/systemctl", "daemon-reload")))
        self.assertLess(calls.index(("/usr/bin/systemctl", "start", "ai.vult.ledger.service")), calls.index(("/opt/vult/vult", "_browser_install")))
        self.assertLess(calls.index(("/opt/vult/vult", "_browser_install")), calls.index(("/usr/bin/systemctl", "start", "ai.vult.browser.service")))

    def test_reinstall_reuses_retained_identities_without_account_creation(self):
        self.retained()
        before = self.snapshot(self.fixture.path(install.RETAINED))
        self.fixture.install(self.value, self.entries, self.pin)
        self.assertEqual(self.snapshot(self.fixture.path(install.RETAINED)), before)
        self.assertFalse(any(args[0].startswith("/usr/sbin/") for args in self.fixture.calls))

    def test_interrupted_program_copy_retries_same_archive_forward(self):
        self.installed()
        value, entries = self.candidate()
        before = self.retained_snapshot()
        self.fixture.write_failures.add("/opt/vult/vult")
        with self.assertRaises(OSError):
            self.fixture.install(value, entries, self.pin)
        self.assertTrue(self.fixture.exists(install.UPDATE))
        self.assertFalse(self.fixture.exists(install.RUNTIME_UPDATE))
        self.assertEqual(self.fixture.path("/opt/vult/vult").read_bytes(), self.entries["opt/vult/vult"][0])
        self.assertEqual(self.fixture.path("/opt/vult/NOTICE").read_bytes(), entries["opt/vult/NOTICE"][0])
        with self.assertRaises(install.InstallError):
            self.fixture.program()  # The ordinary preflight really rejects the mixed copy.
        self.assertFalse(any(args[-1] in ("_install", "_browser_install", "_browser_check") for args in self.fixture.calls))
        self.assertEqual(self.retained_snapshot(), before)
        self.fixture.write_failures.clear()
        def before_native_configuration():
            self.assertFalse(self.fixture.exists(install.UPDATE))
            self.assertTrue(self.fixture.exists(install.RUNTIME_UPDATE))
            self.assertEqual(self.fixture.program(), value)
        self.fixture.actions[("/opt/vult/vult", "_install")] = before_native_configuration
        self.fixture.install(value, entries, self.pin)
        self.assertEqual(self.fixture.program(), value)
        self.assertEqual(self.retained_snapshot(), before)
        self.assertFalse(self.fixture.exists(install.UPDATE))
        self.assertFalse(self.fixture.exists(install.RUNTIME_UPDATE))
        self.assertIn((("/opt/vult/vult", "_stop"), 135), self.fixture.timeouts)
        self.assertIn((("/opt/vult/vult", "_browser_check"), 50), self.fixture.timeouts)

    def test_repair_refuses_changed_release_bytes_and_journal(self):
        self.installed()
        value, entries = self.candidate()
        self.fixture.write_failures.add("/opt/vult/vult")
        with self.assertRaises(OSError):
            self.fixture.install(value, entries, self.pin)
        self.fixture.write_failures.clear()
        update_path = self.fixture.path(install.UPDATE)
        original_record = update_path.read_bytes()
        original_program = self.fixture.path("/opt/vult/vult").read_bytes()
        for kind in ("archive", "unknown-bytes", "both-journals", "unknown-field", "mode", "hardlink"):
            with self.subTest(kind=kind):
                pin = "2" * 64 if kind == "archive" else self.pin
                if kind == "unknown-bytes":
                    self.fixture.path("/opt/vult/vult").write_bytes(b"unrecognized command")
                elif kind == "both-journals":
                    self.put(install.RUNTIME_UPDATE, original_record, 0o600)
                elif kind == "unknown-field":
                    record = json.loads(original_record)
                    record["other_path"] = "/etc/other"
                    update_path.write_text(json.dumps(record))
                elif kind == "mode":
                    update_path.chmod(0o644)
                elif kind == "hardlink":
                    os.link(update_path, self.root / "journal-copy")
                self.fixture.calls.clear()
                before = self.snapshot(self.root)
                with self.assertRaises(install.InstallError):
                    self.fixture.install(value, entries, pin)
                self.assertEqual(self.fixture.calls, [])
                self.assertEqual(self.snapshot(self.root), before)
                if kind == "both-journals":
                    self.fixture.path(install.RUNTIME_UPDATE).unlink()
                if kind == "hardlink":
                    (self.root / "journal-copy").unlink()
                update_path.write_bytes(original_record)
                update_path.chmod(0o600)
                self.fixture.path("/opt/vult/vult").write_bytes(original_program)

    def test_runtime_failures_stop_only_verified_candidate_and_retry_forward(self):
        original_fixture = self.fixture
        shadow = original_fixture.path("/etc/shadow").read_bytes()
        failures = (("/opt/vult/vult", "_install"),
                    ("/usr/bin/systemctl", "start", "ai.vult.ledger.service"),
                    ("/opt/vult/vult", "_browser_install"),
                    ("/usr/bin/systemctl", "start", "ai.vult.browser.service"),
                    ("/usr/bin/systemctl", "is-active", "--quiet", "ai.vult.browser.service"),
                    ("/opt/vult/vult", "_browser_check"))
        for index, failure in enumerate(failures):
            with self.subTest(failure=failure):
                root = self.root / ("runtime-" + str(index))
                root.mkdir()
                self.fixture = FixtureInstaller(root)
                for name in ("/opt", "/var/lib", "/etc/systemd/system", "/usr/share/polkit-1/actions", "/usr/local/bin"):
                    self.fixture.path(name).mkdir(parents=True, exist_ok=True)
                self.fixture.path("/etc/shadow").write_bytes(shadow)
                self.installed()
                value, entries = self.candidate()
                before = self.retained_snapshot()
                self.fixture.fail.add(failure)
                with self.assertRaisesRegex(install.InstallError, "services were stopped"):
                    self.fixture.install(value, entries, self.pin)
                self.assertEqual(self.fixture.program(), value)
                self.assertEqual(self.retained_snapshot(), before)
                self.assertFalse(self.fixture.exists(install.UPDATE))
                self.assertTrue(self.fixture.exists(install.RUNTIME_UPDATE))
                call = self.fixture.calls.index(failure)
                self.assertIn(("/opt/vult/vult", "_stop"), self.fixture.calls[call + 1:])
                self.fixture.fail.clear()
                self.fixture.install(value, entries, self.pin)
                self.assertEqual(self.fixture.program(), value)
                self.assertEqual(self.retained_snapshot(), before)
                self.assertFalse(self.fixture.exists(install.RUNTIME_UPDATE))
        self.fixture = original_fixture

    def test_failure_after_forward_state_keeps_new_code_and_state(self):
        self.installed()
        value, entries = self.candidate()
        before = self.retained_snapshot()
        def forward_state():
            self.put(install.RETAINED + "/data/schema-receipt", b"synthetic forward receipt", 0o400, 997, 997)
        self.fixture.actions[("/usr/bin/systemctl", "start", "ai.vult.ledger.service")] = forward_state
        self.fixture.fail.add(("/opt/vult/vult", "_browser_install"))
        with self.assertRaises(install.InstallError):
            self.fixture.install(value, entries, self.pin)
        after = self.retained_snapshot()
        self.assertEqual({name: data for name, data in after.items() if name != "data/schema-receipt"}, before)
        self.assertEqual(after["data/schema-receipt"][0], b"synthetic forward receipt")
        self.assertEqual(self.fixture.program(), value)
        self.fixture.fail.clear()
        self.fixture.actions.clear()
        self.fixture.install(value, entries, self.pin)
        self.assertEqual(self.retained_snapshot(), after)
        self.assertEqual(self.fixture.program(), value)

    def test_activating_candidate_failure_disables_stops_and_retains_data(self):
        self.installed()
        value, entries = self.candidate()
        before = self.retained_snapshot()
        observed_stops = []
        def restarting_browser():
            self.fixture.unit_changes = {"ActiveState": "activating", "MainPID": "321"}
        def stop_units():
            observed_stops.append(dict(self.fixture.unit_changes))
            self.fixture.unit_changes = {"ActiveState": "inactive", "MainPID": "0"}
        self.fixture.actions[("/usr/bin/systemctl", "start", "ai.vult.browser.service")] = restarting_browser
        self.fixture.actions[("/opt/vult/vult", "_stop")] = stop_units
        failed_check = ("/usr/bin/systemctl", "is-active", "--quiet", "ai.vult.browser.service")
        self.fixture.fail.add(failed_check)
        with self.assertRaisesRegex(install.InstallError, "services were stopped"):
            self.fixture.install(value, entries, self.pin)
        self.assertEqual(observed_stops[-1], {"ActiveState": "activating", "MainPID": "321"})
        failure_index = self.fixture.calls.index(failed_check)
        stop_index = self.fixture.calls.index(("/opt/vult/vult", "_stop"), failure_index + 1)
        for service in ("ai.vult.ledger.service", "ai.vult.browser.service"):
            self.assertLess(self.fixture.calls.index(("/usr/bin/systemctl", "disable", service), failure_index + 1), stop_index)
        self.assertEqual(self.fixture.program(), value)
        self.assertEqual(self.retained_snapshot(), before)
        self.assertFalse(self.fixture.exists(install.UPDATE))
        self.assertTrue(self.fixture.exists(install.RUNTIME_UPDATE))
        self.fixture.units(True, stopped=True)
        self.fixture.unit_changes = {"ActiveState": "activating", "MainPID": "321"}
        with self.assertRaises(install.InstallError):
            self.fixture.units(True, stopped=True)
        self.fixture.unit_changes = {"ActiveState": "inactive", "MainPID": "0"}
        self.fixture.fail.clear()
        self.fixture.actions.clear()
        self.fixture.install(value, entries, self.pin)
        self.assertEqual(self.retained_snapshot(), before)
        self.assertEqual(self.fixture.program(), value)

    def test_recorded_first_install_can_finish_program_copy_without_resetting_storage(self):
        self.fixture.directory(install.RETAINED)
        for name, uid, gid, mode in (("data", 997, 997, 0o700), ("socket", 997, 997, 0o755),
                                      ("browser", 0, 998, 0o750), ("postgres", 997, 998, 0o750)):
            self.fixture.directory(install.RETAINED + "/" + name, uid, gid, mode)
        self.put(install.RETAINED + "/installation", b"vult-service-v1 997 997\n")
        self.put(install.RETAINED + "/browser-installation", b"vult-browser-v1 998 998\n")
        record = {"format": "vult-program-update-v1", "phase": "pre-runtime", "archive_sha256": self.pin,
                  "candidate_inventory_sha256": install.sha(self.entries["opt/vult/program-files.json"][0]),
                  "prior_inventory": None, "new_installation": True}
        self.put(install.UPDATE, json.dumps(record).encode(), 0o600)
        self.put("/opt/vult/NOTICE", self.entries["opt/vult/NOTICE"][0])
        self.fixture.path(install.PROGRAM).chmod(0o755)
        def initial_configuration():
            self.assertTrue(self.fixture.exists(install.RUNTIME_UPDATE))
            self.put(install.RETAINED + "/data/configuration.json", b"initial configuration", 0o400, 997, 997)
        self.fixture.actions[("/opt/vult/vult", "_install")] = initial_configuration
        self.fixture.install(self.value, self.entries, self.pin)
        self.assertEqual(self.fixture.path(install.RETAINED + "/data/configuration.json").read_bytes(), b"initial configuration")
        self.assertFalse(self.fixture.exists(install.RUNTIME_UPDATE))

    def test_populated_cgroup_blocks_replacement_after_stop(self):
        self.installed()
        value, entries = self.candidate()
        before = self.snapshot(self.fixture.path(install.PROGRAM))
        self.fixture.cgroup_busy = True
        with self.assertRaises(install.InstallError):
            self.fixture.install(value, entries, self.pin)
        self.assertEqual(self.snapshot(self.fixture.path(install.PROGRAM)), before)
        self.assertTrue(self.fixture.exists(install.UPDATE))
        self.assertFalse(any(args[-1] in ("_install", "_browser_install") for args in self.fixture.calls))

    def test_runtime_record_reinstall_restores_candidate_but_refuses_old_code(self):
        self.installed()
        value, entries = self.candidate()
        self.fixture.fail.add(("/opt/vult/vult", "_browser_check"))
        with self.assertRaises(install.InstallError):
            self.fixture.install(value, entries, self.pin)
        self.fixture.fail.clear()
        before = self.retained_snapshot()
        self.fixture.path("/opt/vult/vult").write_bytes(self.entries["opt/vult/vult"][0])
        self.fixture.calls.clear()
        with self.assertRaises(install.InstallError):
            self.fixture.install(value, entries, self.pin)
        self.assertEqual(self.fixture.calls, [])
        for name in entries:
            self.fixture.path("/" + name).unlink()
        self.fixture.install(value, entries, self.pin)
        self.assertEqual(self.fixture.program(), value)
        self.assertEqual(self.retained_snapshot(), before)
        self.assertFalse(self.fixture.exists(install.RUNTIME_UPDATE))

    def test_protected_files_reject_links_modes_and_extended_acls(self):
        fixture = install.Installer(self.root)
        path = self.root / "protected"
        path.write_bytes(b"public")
        path.chmod(0o644)
        with mock.patch.object(install.os, "getxattr", side_effect=OSError(errno.ENODATA, "no ACL"), create=True):
            fixture.protected("/protected", uid=os.getuid())
            path.chmod(0o666)
            with self.assertRaises(install.InstallError):
                fixture.protected("/protected", uid=os.getuid())
            path.chmod(0o644)
            os.link(path, self.root / "copy")
            with self.assertRaises(install.InstallError):
                fixture.protected("/protected", uid=os.getuid())
            (self.root / "copy").unlink()
            path.unlink()
            path.symlink_to(self.root / "etc/shadow")
            with self.assertRaises(OSError):
                fixture.protected("/protected", uid=os.getuid())
        path.unlink()
        path.write_bytes(b"public")
        with mock.patch.object(install.os, "getxattr", return_value=b"extended ACL", create=True):
            with self.assertRaises(install.InstallError):
                fixture.protected("/protected", uid=os.getuid())

    def test_generated_bootstrap_has_one_final_call_and_stdin_python(self):
        rendered = package.render_installer((ROOT / "packaging/ubuntu/install.py").read_text(), "1" * 64)
        self.assertTrue(rendered.rstrip().endswith('vult_install "$@"'))
        self.assertEqual(rendered.count('\nvult_install "$@"'), 1)
        self.assertIn('/usr/bin/sudo -- /usr/bin/python3 -I - "$1"', rendered)
        self.assertNotIn("sudo -S", rendered)
        self.assertNotIn("curl ", rendered)
        with self.assertRaises(ValueError):
            package.render_installer("pass", "untrusted")

    def test_bootstrap_rejects_arguments_and_incomplete_input_before_approval(self):
        rendered = package.render_installer((ROOT / "packaging/ubuntu/install.py").read_text(), "1" * 64)
        script = self.root / "install.sh"
        script.write_text(rendered)
        syntax = subprocess.run(["/bin/sh", "-n", script], capture_output=True, timeout=5)
        self.assertEqual(syntax.returncode, 0, syntax.stderr.decode())
        marker = self.root / "approval-requested"
        fake_id = self.root / "id"
        fake_id.write_text('#!/bin/sh\nprintf requested > "' + str(marker) + '"\nprintf 1\n')
        fake_id.chmod(0o755)
        script.write_text(rendered.replace("/usr/bin/id", str(fake_id)))
        for args in ([], ["one", "two"]):
            result = subprocess.run(["/bin/sh", script, *args], capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())
        script.write_text(rendered.replace("/usr/bin/id", str(fake_id)).rsplit('\nvult_install "$@"', 1)[0])
        result = subprocess.run(["/bin/sh", script, "unused-archive"], capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0)
        self.assertFalse(marker.exists())

    def test_postgres_bad_source_never_reaches_extraction_or_build(self):
        source = self.root / "postgres.tar.bz2"
        source.write_bytes(b"corrupt source")
        work = self.root / "work"
        work.mkdir()
        with mock.patch.object(package.tarfile, "open") as extraction, mock.patch.object(package, "run") as build:
            with self.assertRaises(ValueError):
                package.build_postgres(work, source)
            extraction.assert_not_called()
            build.assert_not_called()

    def test_runtime_library_link_cannot_escape_verified_build(self):
        source = self.root / "postgres"
        (source / "bin").mkdir(parents=True)
        (source / "lib").mkdir()
        (source / "share").mkdir()
        for name in ("postgres", "initdb", "pg_ctl"):
            (source / "bin" / name).write_bytes(b"synthetic executable")
        outside = self.root / "outside.so"
        outside.write_bytes(b"must not ship")
        (source / "lib/escape.so").symlink_to(outside)
        with mock.patch.object(package.subprocess, "check_output", return_value=f"postgres (PostgreSQL) {package.PG_VERSION}".encode()):
            with self.assertRaises(ValueError):
                package.runtime_payload(source, self.root / "runtime")

    def test_elf_dependency_and_search_path_are_checked_without_execution(self):
        product = self.root / "product"
        names = ("vult", "postgres/bin/postgres", "postgres/bin/initdb", "postgres/bin/pg_ctl", "postgres/lib/libpq.so.5")
        for name in names:
            path = product / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"\x7fELFsynthetic")
        for kind in ("system", "bundled", "unknown", "unsafe-search", "missing-search"):
            with self.subTest(kind=kind):
                def readelf(args, **unused):
                    self.assertEqual(args[0], "/usr/bin/readelf")
                    if args[1] == "-h":
                        return b"Machine: Advanced Micro Devices X86-64"
                    if Path(args[2]).name != "initdb":
                        return b""
                    return {"system": b"(NEEDED) [libc.so.6]",
                            "bundled": b"(NEEDED) [libpq.so.5]\n(RUNPATH) [/opt/vult/postgres/lib]",
                            "unknown": b"(NEEDED) [libunexpected.so]",
                            "unsafe-search": b"(RUNPATH) [/tmp]",
                            "missing-search": b"(NEEDED) [libpq.so.5]"}[kind]
                with mock.patch.object(package.subprocess, "check_output", side_effect=readelf):
                    if kind in ("system", "bundled"):
                        package.elf_dependencies(product)
                    else:
                        with self.assertRaises(ValueError):
                            package.elf_dependencies(product)

    def test_units_keep_accounts_private_database_and_bounded_shutdown(self):
        ledger = (ROOT / "packaging/ubuntu/ai.vult.ledger.service").read_text()
        browser = (ROOT / "packaging/ubuntu/ai.vult.browser.service").read_text()
        self.assertIn("User=_vultledger\n", ledger)
        self.assertIn("SupplementaryGroups=_vultbrowser\n", ledger)
        self.assertIn("KillMode=mixed\n", ledger)
        self.assertIn("TimeoutStopSec=75\n", ledger)
        self.assertIn("User=_vultbrowser\n", browser)
        self.assertNotIn("SupplementaryGroups=", browser)
        self.assertNotIn("/var/lib/vult/data", browser)
        self.assertNotIn("PrivateNetwork=", browser)
        self.assertIn("KillMode=control-group\n", browser)
        self.assertIn("TimeoutStopSec=20\n", browser)
        self.assertIn("IPAddressDeny=any\n", browser)
        self.assertIn("IPAddressAllow=localhost\n", browser)
        for unit in (ledger, browser):
            self.assertIn("Slice=system.slice\n", unit)
            self.assertIn("ProtectControlGroups=yes\n", unit)
            self.assertIn("CapabilityBoundingSet=\n", unit)
            self.assertIn("AmbientCapabilities=\n", unit)


if __name__ == "__main__":
    unittest.main()
