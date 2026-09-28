import json
import subprocess
import sys
import tempfile
import tomllib
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("stage-language-bindings.py")


class StageLanguageBindingsTest(unittest.TestCase):
    def run_stage(self, version: str, output: Path, name: str = "usekvlite") -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), version, str(output), "--pypi-name", name],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_stages_six_source_packages_with_tag_version(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "bindings"
            result = self.run_stage("v1.2.3", output)
            self.assertEqual(result.returncode, 0, result.stderr)
            python = tomllib.loads((output / "python" / "pyproject.toml").read_text())
            rust = tomllib.loads((output / "rust" / "Cargo.toml").read_text())
            node = json.loads((output / "node" / "package.json").read_text())
            php = json.loads((output / "php" / "composer.json").read_text())
            self.assertEqual(python["project"]["name"], "usekvlite")
            self.assertEqual(python["project"]["version"], "1.2.3")
            self.assertEqual(rust["package"]["version"], "1.2.3")
            self.assertEqual(node["version"], "1.2.3")
            self.assertEqual(node["name"], "kvlite")
            self.assertEqual(php["name"], "kvlite/kvlite")
            for language in ("go", "php", "python", "node", "ruby", "rust"):
                self.assertTrue((output / language / "LICENSE").is_file())
            self.assertIn('VERSION = "1.2.3"', (output / "ruby" / "lib" / "kvlite" / "version.rb").read_text())
            self.assertTrue((output / "ruby" / "kvlite.gemspec").is_file())
            self.assertIn('spec.name = "kvlite"', (output / "ruby" / "kvlite.gemspec").read_text())
            self.assertTrue((output / "ruby" / "test" / "mock_kvlite.c").is_file())
            self.assertIn("module github.com/webong/kvlite", (output / "go" / "go.mod").read_text())
            self.assertTrue((output / "go" / "testdata" / "mock_kvlite.c").is_file())
            self.assertTrue((output / "php" / "composer.json").is_file())
            self.assertTrue((output / "python" / "src" / "kvlite").is_dir())
            self.assertTrue((output / "node" / "native" / "kvlite_native.c").is_file())
            self.assertTrue((output / "rust" / "src" / "lib.rs").is_file())

    def test_rejects_invalid_tag_and_unrelated_pypi_name(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "bindings"
            for version, name in (("v1.2.3-beta", "usekvlite"), ("v01.2.3", "usekvlite"), ("v1.2.3", "kvlite")):
                result = self.run_stage(version, output, name)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())

    def test_refuses_to_replace_an_existing_staging_directory(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "bindings"
            output.mkdir()
            sentinel = output / "keep"
            sentinel.write_text("user data")
            result = self.run_stage("v1.2.3", output)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(sentinel.read_text(), "user data")


if __name__ == "__main__":
    unittest.main()
