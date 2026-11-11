from pathlib import Path
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import package_linux as linux

NOTICES = {"LICENSE": b"license text\n", "NOTICE": b"notice\n", "THIRD_PARTY_NOTICES.md": b"# notices\n"}


class LinuxImageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def build(self, where, program=b"\x7fELF synthetic program"):
        return linux.image(self.root / where, "vult-server", program, NOTICES)

    def test_same_inputs_give_the_same_digest_anywhere(self):
        first = self.build("a")
        os.utime(self.root, (1, 1))
        second = self.build("somewhere/else/entirely")
        self.assertEqual(first, second)
        for name in ("index.json", "oci-layout"):
            self.assertEqual((self.root / "a" / name).read_bytes(), (self.root / "somewhere/else/entirely" / name).read_bytes())

    def test_any_change_gives_another_digest(self):
        self.assertNotEqual(self.build("a"), self.build("b", b"\x7fELF synthetic programme"))

    def test_layer_has_fixed_owners_times_and_modes(self):
        self.build("a")
        layout = self.root / "a"
        manifest_digest = json.loads((layout / "index.json").read_text())["manifests"][0]["digest"]
        manifest = json.loads((layout / "blobs/sha256" / manifest_digest.split(":")[1]).read_text())
        self.assertNotIn("created", (layout / "blobs/sha256" / manifest["config"]["digest"].split(":")[1]).read_text())
        blob = layout / "blobs/sha256" / manifest["layers"][0]["digest"].split(":")[1]
        with tarfile.open(blob) as tar:
            members = tar.getmembers()
        self.assertEqual([m.name for m in members], sorted(m.name for m in members))
        modes = {m.name: m.mode for m in members}
        self.assertEqual(modes["vult-server"], 0o555)
        self.assertEqual(modes["usr/share/licenses/vult/LICENSE"], 0o444)
        for m in members:
            self.assertEqual((m.mtime, m.uid, m.gid, m.uname, m.gname), (0, 0, 0, "", ""), m.name)
        self.assertEqual(manifest["layers"][0]["mediaType"], "application/vnd.oci.image.layer.v1.tar")

    @unittest.skipUnless(shutil.which("skopeo"), "skopeo checks the layout as a registry client would")
    def test_layout_is_a_valid_image(self):
        digest = self.build("a")
        out = subprocess.run(["skopeo", "inspect", "--raw", f"oci:{self.root / 'a'}"], capture_output=True, check=True)
        self.assertEqual(linux.sha256(out.stdout), digest)
        config = json.loads(subprocess.run(["skopeo", "inspect", "--config", f"oci:{self.root / 'a'}"], capture_output=True, check=True).stdout)
        self.assertEqual(config["config"]["User"], "65532:65532")
        self.assertEqual(config["config"]["Entrypoint"], ["/vult-server"])


if __name__ == "__main__":
    unittest.main()
