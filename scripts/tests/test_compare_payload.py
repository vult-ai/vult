from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import compare_payload as compare

ENV = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "C", "LC_ALL": "C", "ZERO_AR_DATE": "1"}
PROGRAM = "#include <stdio.h>\nint main(void) { puts(\"payload\"); return 0; }\n"


def entitlements(path, count):
    keys = "".join(f"<key>com.example.padding-{i}</key><true/>" for i in range(count))
    path.write_text('<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" '
                    f'"http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>{keys}</dict></plist>')


@unittest.skipUnless(sys.platform == "darwin", "Mach-O signatures are handled with macOS codesign")
class ComparePayloadTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        source = self.root / "program.c"
        source.write_text(PROGRAM)
        self.left, self.right = self.root / "left", self.root / "right"
        for side in (self.left, self.right):
            (side / "bin").mkdir(parents=True)
            subprocess.run(["/usr/bin/clang", "-O2", "-o", side / "bin/program", source], check=True, env=ENV, cwd=self.root)
            (side / "share").mkdir()
            (side / "share/notice.txt").write_text("same text\n")
        # A much larger signature on one side, as a release signature would be.
        plist = self.root / "large.plist"
        entitlements(plist, 400)
        subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", "--options", "runtime", "--entitlements", plist, self.right / "bin/program"], check=True, env=ENV, capture_output=True)

    def test_identical_code_matches_under_a_different_signature(self):
        report = compare.compare(self.left, self.right)
        self.assertTrue(report["identical"], report)
        self.assertEqual(report["mach_o"], 1)

    def test_signature_removal_leaves_only_the_linkedit_size_to_normalize(self):
        data = bytearray((self.right / "bin/program").read_bytes())
        self.assertTrue(compare.has_signature(data))
        digest = compare.canonical_macho(self.right / "bin/program", self.root)
        self.assertEqual(digest, compare.canonical_macho(self.left / "bin/program", self.root))

    def test_changed_code_is_a_difference(self):
        path = self.right / "bin/program"
        data = bytearray(path.read_bytes())
        data[data.index(b"payload")] ^= 1
        path.write_bytes(data)
        self.assertEqual(compare.compare(self.left, self.right)["content"], ["bin/program"])

    def test_changed_ordinary_file_is_a_difference(self):
        (self.right / "share/notice.txt").write_text("other text\n")
        self.assertEqual(compare.compare(self.left, self.right)["content"], ["share/notice.txt"])

    def test_missing_extra_and_mode_differences_are_reported(self):
        (self.left / "share/notice.txt").chmod(0o600)
        (self.right / "share/extra.txt").write_text("extra\n")
        report = compare.compare(self.left, self.right)
        self.assertFalse(report["identical"])
        self.assertEqual(report["only_right"], ["share/extra.txt"])
        self.assertEqual(report["mode"], ["share/notice.txt"])

    def test_links_are_refused(self):
        (self.right / "share/link").symlink_to("notice.txt")
        with self.assertRaisesRegex(ValueError, "symbolic link"):
            compare.compare(self.left, self.right)

    def test_malformed_mach_o_is_refused(self):
        with self.assertRaisesRegex(ValueError, "Mach-O|load command"):
            compare.normalize_linkedit(bytearray((0xFEEDFACF).to_bytes(4, "little") + bytes(28) + bytes(8)))


if __name__ == "__main__":
    unittest.main()
