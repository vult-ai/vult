import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("make_installer", ROOT / "scripts/make_installer.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


def root_script(script):
    match = re.search(r"set installLines to \{¬\n(.*?)\}\n", script, re.S)
    if not match:
        raise AssertionError("missing readable administrator script")
    lines = []
    for line in match.group(1).splitlines():
        line = line.strip()
        if line.endswith(", ¬"):
            line = line[:-3]
        lines.append(json.loads(line))
    return "\n".join(lines) + "\n"


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.payload = self.work / "payload.pkg"
        self.payload.write_bytes(b"synthetic package\n")
        self.digest = hashlib.sha256(self.payload.read_bytes()).hexdigest()
        self.script = installer.render("0.1.0", "macos-arm64", "26.0", self.digest)
        self.log = self.work / "installed.json"
        self.owner_log = self.work / "owner-approved"
        self.download_log = self.work / "downloads.json"
        self.stages = self.work / "stages"
        self.stages.mkdir()
        self.env = {"PATH": "/usr/bin:/bin", "HOME": str(self.work), "TMPDIR": str(self.work),
                    "FIXTURE_PAYLOAD": str(self.payload), "FIXTURE_INSTALL_LOG": str(self.log),
                    "FIXTURE_OWNER_LOG": str(self.owner_log), "FIXTURE_EXPECTED_SHA": self.digest,
                    "FIXTURE_DOWNLOAD_LOG": str(self.download_log)}

    def mock(self, name, text):
        path = self.work / name
        path.write_text(text)
        path.chmod(0o755)
        # Explicit interpreters also work on a noexec temporary filesystem.
        interpreter = shlex.quote(sys.executable) if text.startswith("#!/usr/bin/python3") else "/bin/sh"
        return interpreter + " " + shlex.quote(str(path))

    def run_shell(self, text, args=(), extra_env=None):
        path = self.work / "run.sh"
        path.write_text(text)
        env = dict(self.env, **(extra_env or {}))
        return subprocess.run(["/bin/sh", str(path), *args], env=env, capture_output=True, text=True, timeout=10)

    def root_fixture(self):
        curl = self.mock("curl", """#!/usr/bin/python3
import json, os, pathlib, sys
args=sys.argv[1:]
assert args[0]=='-q'
assert not any(a.startswith('--retry') for a in args)
for option,value in [('--proto','=https'),('--proto-redir','=https'),('--max-redirs','5'),('--max-time','180'),('--max-filesize','134217728')]:
    assert args[args.index(option)+1]==value
assert args[-1]=='https://github.com/vult-ai/vult/releases/download/v0.1.0/vult-0.1.0-macos-arm64.pkg'
log=pathlib.Path(os.environ['FIXTURE_DOWNLOAD_LOG'])
calls=json.loads(log.read_text()) if log.exists() else []
calls.append(args)
log.write_text(json.dumps(calls))
if os.environ.get('FIXTURE_DOWNLOAD_FAIL'):sys.exit(22)
pathlib.Path(args[args.index('--output')+1]).write_bytes(pathlib.Path(os.environ['FIXTURE_PAYLOAD']).read_bytes())
""")
        if shutil.which("shasum"):
            shasum = shlex.quote(shutil.which("shasum"))
        elif shutil.which("sha256sum"):
            shasum = self.mock("shasum", """#!/bin/sh
[ "$1" = -a ] && [ "$2" = 256 ] || exit 99
shift 2
exec /usr/bin/sha256sum "$@"
""")
        else:
            self.fail("a system checksum verifier is required")
        native = self.mock("installer", """#!/usr/bin/python3
import hashlib,json,os,pathlib,sys
assert sys.argv[1]=='-pkg' and sys.argv[3:]==['-target','/']
p=pathlib.Path(sys.argv[2])
assert hashlib.sha256(p.read_bytes()).hexdigest()==os.environ['FIXTURE_EXPECTED_SHA']
pathlib.Path(os.environ['FIXTURE_INSTALL_LOG']).write_text(json.dumps(sys.argv[1:]))
if os.environ.get('FIXTURE_INSTALL_FAIL'):sys.exit(1)
""")
        uid = self.mock("root-id", "#!/bin/sh\nprintf '0\\n'\n")
        self.root_uid_command = uid
        # Substitute only unavailable macOS commands. The production control
        # flow, checksum gate and cleanup remain the actual generated shell.
        return root_script(self.script).replace("/usr/bin/curl", curl).replace("/usr/bin/shasum", shasum).replace("/usr/sbin/installer", native).replace("/usr/bin/id", uid).replace("/private/var/tmp/vult-install.XXXXXXXX", str(self.stages / "vult-install.XXXXXXXX"))

    def outer_fixture(self):
        uname = self.mock("uname", "#!/bin/sh\nif [ \"$1\" = -s ]; then printf '%s\\n' \"${FIXTURE_OS:-Darwin}\"; else printf '%s\\n' \"${FIXTURE_ARCH:-arm64}\"; fi\n")
        version = self.mock("sw_vers", "#!/bin/sh\nprintf '%s\\n' \"${FIXTURE_VERSION:-26.1}\"\n")
        owner = self.mock("osascript", "#!/bin/sh\n/bin/cat >/dev/null\nprintf approved >\"$FIXTURE_OWNER_LOG\"\nexit \"${FIXTURE_OWNER_FAIL:-0}\"\n")
        return self.script.replace("/usr/bin/uname", uname).replace("/usr/bin/sw_vers", version).replace("/usr/bin/osascript", owner)

    def test_success_verifies_before_install_and_removes_staging(self):
        r = self.run_shell(self.root_fixture())
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(self.log.exists())
        self.assertEqual(list(self.stages.iterdir()), [])

    def test_corrupt_download_never_reaches_installer(self):
        self.payload.write_bytes(b"different package")
        r = self.run_shell(self.root_fixture())
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("checksum mismatch", r.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual(len(json.loads(self.download_log.read_text())), 1)
        self.assertEqual(list(self.stages.iterdir()), [])

    def test_download_failure_never_installs_or_retries(self):
        r = self.run_shell(self.root_fixture(), extra_env={"FIXTURE_DOWNLOAD_FAIL": "1"})
        self.assertNotEqual(r.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertEqual(len(json.loads(self.download_log.read_text())), 1)
        self.assertEqual(list(self.stages.iterdir()), [])

    def test_installer_failure_is_honest_and_cleans_staging(self):
        r = self.run_shell(self.root_fixture(), extra_env={"FIXTURE_INSTALL_FAIL": "1"})
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("did not complete", r.stderr)
        self.assertNotIn("Nothing was installed", r.stderr)
        self.assertEqual(list(self.stages.iterdir()), [])

    def test_administrator_payload_requires_root(self):
        script = self.root_fixture()
        r = self.run_shell(script.replace(self.root_uid_command, "/usr/bin/id"))
        self.assertNotEqual(r.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertFalse(self.download_log.exists())

    def test_supported_platform_requests_native_approval(self):
        r = self.run_shell(self.outer_fixture())
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(self.owner_log.exists())

    def test_unsupported_platforms_fail_before_approval(self):
        for env in ({"FIXTURE_OS": "Linux"}, {"FIXTURE_ARCH": "x86_64"}, {"FIXTURE_VERSION": "25.6"}, {"FIXTURE_VERSION": "invalid"}):
            with self.subTest(env=env):
                r = self.run_shell(self.outer_fixture(), extra_env=env)
                self.assertNotEqual(r.returncode, 0)
                self.assertFalse(self.owner_log.exists())

    def test_cancelled_approval_has_no_fallback(self):
        r = self.run_shell(self.outer_fixture(), extra_env={"FIXTURE_OWNER_FAIL": "1"})
        self.assertNotEqual(r.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_arguments_and_incomplete_input_do_not_request_approval(self):
        text = self.outer_fixture()
        r = self.run_shell(text, args=("--override",))
        self.assertNotEqual(r.returncode, 0)
        self.assertFalse(self.owner_log.exists())
        entry = text.rindex('vult_install "$@"')
        for prefix in (text[:entry], text[:text.index("VULT_ADMIN\n}")], text[:text.index("do shell script")]):
            with self.subTest(length=len(prefix)):
                self.run_shell(prefix)
                self.assertFalse(self.owner_log.exists())

    def test_platform_metadata_and_release_pins(self):
        universal = installer.render("0.1.0", "macos-universal", "26.0", self.digest)
        self.assertIn("arm64|x86_64)", universal)
        self.assertIn("vult-0.1.0-macos-universal.pkg", universal)
        for version, platform, minimum, digest in [("0.1.0;id", "macos-arm64", "26.0", self.digest), ("../x", "macos-arm64", "26.0", self.digest), ("0.1.0", "../x", "26.0", self.digest), ("0.1.0", "macos-arm64", "26;id", self.digest), ("0.1.0", "macos-arm64", "26.0", "x" * 64)]:
            with self.subTest(version=version, platform=platform):
                with self.assertRaises(ValueError): installer.render(version, platform, minimum, digest)

    def artifacts(self):
        path = self.work / "artifacts"
        path.mkdir()
        (path / "vult-0.1.0-macos-arm64.pkg").write_bytes(self.payload.read_bytes())
        data = json.dumps({"version": "0.1.0", "platform": "macos-arm64", "minimum_macos": "26.0"}).encode()
        (path / "manifest.json").write_bytes(data)
        (path / "SHA256SUMS").write_text(f"{hashlib.sha256(data).hexdigest()}  manifest.json\n{self.digest}  vult-0.1.0-macos-arm64.pkg\n")
        return path

    def test_preparation_pins_existing_package_without_changing_it(self):
        artifacts = self.artifacts()
        sums = (artifacts / "SHA256SUMS").read_bytes()
        output = self.work / "site"
        release = installer.prepare(artifacts, output)
        self.assertEqual(release["package_sha256"], self.digest)
        self.assertIn(self.digest, (output / "install.sh").read_text())
        self.assertEqual((artifacts / "SHA256SUMS").read_bytes(), sums)
        self.assertEqual(hashlib.sha256((output / "install.sh").read_bytes()).hexdigest(), release["installer_sha256"])
        with self.assertRaises(FileExistsError): installer.prepare(artifacts, output)

    def test_changed_manifest_or_package_is_rejected_before_output(self):
        artifacts = self.artifacts()
        output = self.work / "site"
        package = artifacts / "vult-0.1.0-macos-arm64.pkg"
        package.write_bytes(b"modified package")
        with self.assertRaises(ValueError): installer.prepare(artifacts, output)
        self.assertFalse(output.exists())
        package.write_bytes(self.payload.read_bytes())
        with (artifacts / "manifest.json").open("ab") as f: f.write(b" ")
        with self.assertRaises(ValueError): installer.prepare(artifacts, output)
        self.assertFalse(output.exists())

    def test_generated_installer_must_match_release_manifest(self):
        artifacts = self.artifacts()
        manifest = artifacts / "manifest.json"
        metadata = json.loads(manifest.read_text())
        metadata["installer_sha256"] = hashlib.sha256(self.script.encode()).hexdigest()

        def update_manifest():
            data = json.dumps(metadata).encode()
            manifest.write_bytes(data)
            (artifacts / "SHA256SUMS").write_text(f"{hashlib.sha256(data).hexdigest()}  manifest.json\n{self.digest}  vult-0.1.0-macos-arm64.pkg\n")

        update_manifest()
        installer.prepare(artifacts, self.work / "matching")
        metadata["installer_sha256"] = "0" * 64
        update_manifest()
        output = self.work / "mismatched"
        with self.assertRaises(ValueError): installer.prepare(artifacts, output)
        self.assertFalse(output.exists())

    def test_package_links_are_not_accepted(self):
        artifacts = self.artifacts()
        package = artifacts / "vult-0.1.0-macos-arm64.pkg"
        package.unlink()
        package.symlink_to(self.payload)
        with self.assertRaises(OSError): installer.prepare(artifacts, self.work / "site")


if __name__ == "__main__":
    unittest.main()
