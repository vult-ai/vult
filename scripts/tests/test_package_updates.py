from pathlib import Path
import json
import os
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import package_macos as package


class UpdatePackagingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def trust(self, root, repository):
        directory = self.root / "trust"
        directory.mkdir(exist_ok=True)
        (directory / "root.json").write_text(json.dumps(root))
        (directory / "repository.json").write_text(json.dumps(repository))
        return directory

    def test_update_trust_needs_a_root_and_https(self):
        good_root = {"signed": {"_type": "root"}, "signatures": []}
        good_repo = {"metadata_url": "https://updates.invalid/metadata", "targets_url": "https://updates.invalid/targets"}
        package.update_trust(self.trust(good_root, good_repo))
        for root, repo in [
            ({"signed": {"_type": "targets"}}, good_repo),
            (good_root, dict(good_repo, metadata_url="http://updates.invalid/metadata")),
            (good_root, dict(good_repo, extra="https://updates.invalid")),
            (good_root, {"metadata_url": "https://updates.invalid/metadata"}),
        ]:
            with self.assertRaises(ValueError):
                package.update_trust(self.trust(root, repo))

    def program(self):
        product = self.root / "program"
        (product / "postgres/bin").mkdir(parents=True)
        (product / "vult").write_bytes(b"program")
        (product / "vult").chmod(0o4755)
        (product / "postgres/bin/postgres").write_bytes(b"database")
        (product / "postgres/bin/postgres").chmod(0o777)
        return product

    def test_program_archive_is_plain_and_repeatable(self):
        product = self.program()
        first, second = self.root / "a.tar", self.root / "b.tar"
        package.program_archive(product, first)
        os.utime(product / "vult", (1, 1))
        package.program_archive(product, second)
        self.assertEqual(first.read_bytes(), second.read_bytes())
        with tarfile.open(first) as tar:
            members = {m.name: m for m in tar.getmembers()}
        self.assertEqual(sorted(members), ["postgres", "postgres/bin", "postgres/bin/postgres", "vult"])
        for m in members.values():
            self.assertTrue(m.isfile() or m.isdir())
            self.assertEqual((m.uid, m.gid, m.mtime), (0, 0, 0))
            self.assertEqual(m.mode & 0o7022, 0, m.name)

    def test_program_archive_refuses_links(self):
        product = self.program()
        (product / "link").symlink_to("/etc")
        with self.assertRaises(ValueError):
            package.program_archive(product, self.root / "c.tar")


if __name__ == "__main__":
    unittest.main()
