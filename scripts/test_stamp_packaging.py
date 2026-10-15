import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("stamp-packaging.py")
ROOT = SCRIPT.parents[1]
REVISION = "0123456789abcdef0123456789abcdef01234567"
DATE = "Thu, 01 Jan 2026 00:00:00 +0000"
FORMULA_VERSION = re.compile(r'(?m)^\s*version "([^"]+)"$')
FORMULA_TAG = re.compile(r'(?m)^\s*tag: "([^"]+)"$')
CONTROL_MAINTAINER = re.compile(r"(?m)^Maintainer:[ \t]*(.+?)[ \t]*$")
RULES_PACKAGES = re.compile(r"(?m)^(DRIVER_PACKAGES|EXTENSION_PACKAGES)[ \t]*=[ \t]*(.+?)[ \t]*$")


def read_tree(root: Path) -> dict[Path, str]:
    return {
        path: path.read_text()
        for path in sorted((root / "packaging").rglob("*"))
        if path.is_file()
    }


class StampPackagingTest(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "repo"
        shutil.copytree(ROOT / "packaging", self.root / "packaging")

    def run_stamp(self, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args, "--root", str(self.root)],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_checkout_recipes_agree_on_one_placeholder_version(self) -> None:
        versions, tags = set(), set()
        for formula in sorted((ROOT / "packaging/homebrew").glob("*.rb")):
            source = formula.read_text()
            versions.add(FORMULA_VERSION.search(source).group(1))
            tags.add(FORMULA_TAG.search(source).group(1))
        self.assertEqual(versions, {"0.1.0"})
        self.assertEqual(tags, {"v0.1.0"})
        changelog = (ROOT / "packaging/debian/changelog").read_text()
        self.assertTrue(changelog.startswith("kvlite (0.1.0-1) "), changelog[:40])

    def test_debian_maintainer_is_not_a_placeholder(self) -> None:
        control = (ROOT / "packaging/debian/control").read_text()
        maintainer = CONTROL_MAINTAINER.search(control).group(1)
        self.assertNotIn("example.", maintainer)
        self.assertRegex(maintainer, r"<[^@\s]+@[^@\s]+>")

    def test_every_built_bundle_has_a_package_formula_and_table_row(self) -> None:
        rules = (ROOT / "packaging/debian/rules").read_text()
        control = (ROOT / "packaging/debian/control").read_text()
        table = (ROOT / "packaging/README.md").read_text()
        names = {}
        for field, value in RULES_PACKAGES.findall(rules):
            names[field] = value.split()
        self.assertTrue(names["DRIVER_PACKAGES"])
        self.assertTrue(names["EXTENSION_PACKAGES"])
        for name in names["DRIVER_PACKAGES"] + names["EXTENSION_PACKAGES"]:
            self.assertIn(f"Package: kvlite-{name}\n", control)
            self.assertTrue((ROOT / f"packaging/homebrew/kvlite-{name}.rb").is_file())
            self.assertIn(f"| `kvlite-{name}` |", table)
        self.assertIn("Package: kvlite\n", control)
        self.assertTrue((ROOT / "packaging/homebrew/kvlite.rb").is_file())
        self.assertIn("| `kvlite` |", table)

    def test_rocksdb_stays_out_of_the_default_build(self) -> None:
        rules = (ROOT / "packaging/debian/rules").read_text()
        control = (ROOT / "packaging/debian/control").read_text()
        self.assertNotIn("rocksdb", RULES_PACKAGES.search(rules).group(2).split())
        self.assertTrue((ROOT / "packaging/homebrew/kvlite-rocksdb.rb").is_file())
        self.assertIn("# Package: kvlite-rocksdb", control)

    def test_stamps_every_formula_and_prepends_the_changelog_entry(self) -> None:
        result = self.run_stamp("v1.2.3", "--revision", REVISION, "--date", DATE)
        self.assertEqual(result.returncode, 0, result.stderr)
        for formula in sorted((self.root / "packaging/homebrew").glob("*.rb")):
            source = formula.read_text()
            self.assertIn('tag: "v1.2.3",', source)
            self.assertIn(f'revision: "{REVISION}"', source)
            self.assertIn('version "1.2.3"', source)
            self.assertEqual(source.count("revision:"), 1)
        changelog = (self.root / "packaging/debian/changelog").read_text()
        self.assertTrue(
            changelog.startswith("kvlite (1.2.3-1) unstable; urgency=medium\n"),
            changelog[:60],
        )
        self.assertIn(
            " -- KVLite packagers <webong@users.noreply.github.com>  " + DATE,
            changelog,
        )
        self.assertIn("kvlite (0.1.0-1)", changelog)

    def test_bare_version_and_repeated_runs_are_idempotent(self) -> None:
        args = ("v1.2.3", "--revision", REVISION, "--date", DATE)
        self.assertEqual(self.run_stamp(*args).returncode, 0)
        stamped = read_tree(self.root)
        second = self.run_stamp(*args)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn("already current", second.stdout)
        self.assertEqual(read_tree(self.root), stamped)
        bare = self.run_stamp("1.2.3", "--revision", REVISION, "--date", DATE)
        self.assertEqual(bare.returncode, 0, bare.stderr)
        self.assertEqual(read_tree(self.root), stamped)

    def test_check_reports_staleness_without_writing(self) -> None:
        before = read_tree(self.root)
        stale = self.run_stamp("v1.2.3", "--revision", REVISION, "--check")
        self.assertEqual(stale.returncode, 1)
        self.assertIn("kvlite-http.rb: tag 'v0.1.0' != 'v1.2.3'", stale.stderr)
        self.assertIn("top entry is not 1.2.3-1", stale.stderr)
        self.assertEqual(read_tree(self.root), before)

        self.assertEqual(
            self.run_stamp("v1.2.3", "--revision", REVISION, "--date", DATE).returncode, 0
        )
        current = self.run_stamp("v1.2.3", "--revision", REVISION, "--check")
        self.assertEqual(current.returncode, 0, current.stderr)
        self.assertIn("is current for 1.2.3", current.stdout)

    def test_check_ignores_the_changelog_stamp_date(self) -> None:
        self.assertEqual(
            self.run_stamp("v1.2.3", "--revision", REVISION, "--date", DATE).returncode, 0
        )
        changelog = self.root / "packaging/debian/changelog"
        changelog.write_text(
            changelog.read_text().replace(DATE, "Sat, 07 Mar 2026 12:34:56 +0000")
        )
        check = self.run_stamp("v1.2.3", "--revision", REVISION, "--check")
        self.assertEqual(check.returncode, 0, check.stderr)

    def test_rejects_non_release_versions_and_bad_revisions(self) -> None:
        before = read_tree(self.root)
        for args in (
            ("v1.2.3-canary.4", "--revision", REVISION),
            ("1.2", "--revision", REVISION),
            ("v1.2.3", "--revision", "not-a-sha"),
            ("v1.2.3", "--revision", ""),
        ):
            result = self.run_stamp(*args)
            self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertEqual(read_tree(self.root), before)

    def test_rejects_a_placeholder_maintainer(self) -> None:
        control = self.root / "packaging/debian/control"
        control.write_text(
            control.read_text().replace(
                "webong@users.noreply.github.com", "packages@example.com"
            )
        )
        result = self.run_stamp("v1.2.3", "--revision", REVISION)
        self.assertEqual(result.returncode, 2)
        self.assertIn("reserved example domain", result.stderr)


if __name__ == "__main__":
    unittest.main()
