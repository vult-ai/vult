"""Every PostgreSQL program Vult's code runs ships in its packages."""
from pathlib import Path
import re
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import package_macos

# A program under postgres/bin, named either as a path ("bin/pg_ctl") or as a
# path element ("bin", "pg_ctl"), or passed by name to runPostgres.
NAMED = re.compile(r'"bin/(\w+)"|"bin",\s*"(\w+)"|runPostgres\([^,]+,\s*"(\w+)"')


def programs_run():
    files = subprocess.check_output(["git", "ls-files", "*.go"], cwd=ROOT, text=True).split()
    found = {}
    for name in files:
        if name.endswith("_test.go"):
            continue
        for match in NAMED.finditer((ROOT / name).read_text()):
            found.setdefault(next(group for group in match.groups() if group), name)
    return found


class RuntimeBinaryTests(unittest.TestCase):
    def test_every_program_the_code_runs_is_packaged(self):
        found = programs_run()
        self.assertIn("pg_controldata", found, "the scan finds the clean-shutdown check")
        missing = {program: where for program, where in found.items() if program not in package_macos.RUNTIME_BINARIES}
        self.assertEqual(missing, {}, "programs the code runs but the packages don't ship")

    def test_the_ubuntu_package_ships_the_same_programs(self):
        text = (ROOT / "scripts/package_ubuntu.py").read_text()
        self.assertIn("for name in RUNTIME_BINARIES:", text)


if __name__ == "__main__":
    unittest.main()
