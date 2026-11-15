"""Every file the macOS package installs in the program folder is one the
installed program recognizes, so it can stop, upgrade and uninstall itself."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


def recognized():
    source = (ROOT / "internal/daemon/uninstall.go").read_text()
    body = source[source.index("func programPath("):]
    case = re.search(r"switch name \{\s*case (.*?):\s*default:", body, re.S).group(1)
    return set(re.findall(r'"([^"]+)"', case))


def packaged():
    source = (ROOT / "scripts/package_macos.py").read_text()
    names = set(re.findall(r'product / "([^"/]+)"', source)) - {"program-files.json"}
    return names


class ProgramPathTests(unittest.TestCase):
    def test_every_packaged_program_file_is_recognized(self):
        known = recognized()
        self.assertIn("vult-enclave", packaged(), "the scan finds the enclave helper")
        for name in packaged():
            if name == "postgres":
                continue  # recognized by its postgres/ prefix
            if name.endswith(".app"):
                self.assertTrue(any(k.startswith(name + "/") for k in known), name)
                continue
            self.assertIn(name, known, f"{name} is installed but not recognized by programPath")
        for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md"):
            self.assertIn(name, known)


if __name__ == "__main__":
    unittest.main()
