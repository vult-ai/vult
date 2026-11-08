from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import package_macos as package


class NativeInterfaceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "interface"
        self.source.mkdir()
        (self.source / "index.html").write_text("<!doctype html><div id=root></div>")
        (self.source / "assets").mkdir()
        (self.source / "assets/app-12345678.js").write_text("export const value=1;")

    def test_shared_artwork_is_refused_in_source_and_interface(self):
        for name in ("shared-assets/README.md", "web/catalog.v1.json", "docs/brand-logos.v1.json", "x/" + "a" * 64 + ".png"):
            self.assertTrue(package.shared_artwork(name), name)
        for name in ("web/unit/fixtures/shared-assets/catalog.v1.json", "web/src/lib/shared-institutions.ts", "web/public/brand/vult-logo.svg"):
            self.assertFalse(package.shared_artwork(name), name)
        (self.source / "brand-logos.v1.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "shared artwork"):
            package.interface_files(self.source)

    def test_published_source_list_carries_no_shared_artwork(self):
        self.assertEqual([n for n in (ROOT / "packaging/source-paths.txt").read_text().splitlines() if n and not n.startswith("#") and package.shared_artwork(n)], [])

    def test_stage_embeds_exact_pinned_files(self):
        expected, checksum = package.interface_files(self.source)
        target = self.root / "staged"
        self.assertEqual(package.stage_interface(self.source, target, checksum), checksum)
        self.assertEqual(package.interface_files(target), (expected, checksum))

    def test_changed_build_is_refused_before_staging(self):
        _, checksum = package.interface_files(self.source)
        (self.source / "assets/app-12345678.js").write_text("export const value=2;")
        with self.assertRaises(ValueError):
            package.stage_interface(self.source, self.root / "staged", checksum)
        self.assertFalse((self.root / "staged").exists())

    def test_missing_empty_and_linked_interface_are_refused(self):
        (self.source / "index.html").unlink()
        with self.assertRaises(ValueError):
            package.interface_files(self.source)
        (self.source / "index.html").write_bytes(b"")
        with self.assertRaises(ValueError):
            package.interface_files(self.source)
        (self.source / "index.html").write_text("<!doctype html>")
        (self.source / "assets/linked").symlink_to(self.source / "index.html")
        with self.assertRaises(ValueError):
            package.interface_files(self.source)
