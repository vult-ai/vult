from pathlib import Path
import importlib.util
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("go_notices", ROOT / "scripts/go_notices.py")
notices = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notices)


class GoNoticesTest(unittest.TestCase):
    def test_every_linked_module_needs_a_notice_for_its_exact_version(self):
        text = "# Notices\n\n## example.com/kept v1.2.0\n\n```text\nMIT\n```\n\n## example.com/older v0.1.0\n"
        modules = {("example.com/kept", "v1.2.0"), ("example.com/older", "v0.2.0"), ("example.com/new", "v1.0.0")}
        self.assertEqual(notices.missing_notices(text, modules), ["example.com/new v1.0.0", "example.com/older v0.2.0"])


if __name__ == "__main__":
    unittest.main()
