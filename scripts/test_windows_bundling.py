"""Windows bundling contracts that the unix jobs cannot exercise.

Two Windows-only behaviors are pinned here by simulating the native tools:

- `dumpbin` writes CRLF into a pipe, so every dependency name arrives with a
  trailing CR. An unfolded CR turns a system DLL into an unresolvable
  dependency and a real DLL into a name that matches no file on disk.
- GNU coreutils under MSYS2 records binary-mode entries in SHA256SUMS as
  `hash *path`, so the release manifest cannot read the recorded path
  verbatim.

Both run anywhere the release scripts run, with no Windows toolchain.
"""

import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BUNDLER = ROOT / "scripts/bundle-native-deps.sh"
BUILD_RELEASE = ROOT / "scripts/build-release.sh"
# A dependency listing the way dumpbin writes it: a banner naming the file
# being inspected (in the Windows spelling MSYS2 hands it), CRLF line endings,
# system DLLs, API sets, and one library that must reach the bundle.
DUMPBIN = (
    "#!/usr/bin/env bash\n"
    "printf 'Dump of file C:\\\\inspected\\\\%s\\r\\n\\r\\n' \"$(basename \"$2\")\"\n"
    "printf 'File Type: EXECUTABLE IMAGE\\r\\n\\r\\n"
    "  Image has the following dependencies:\\r\\n\\r\\n"
    "    KERNEL32.dll\\r\\n"
    "    libkvliteweirdep.dll\\r\\n"
    "    api-ms-win-crt-runtime-l1-1-0.dll\\r\\n'\n"
)


def run_bash(script: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    merged = dict(os.environ)
    if env:
        merged.update(env)
    return subprocess.run(
        ["bash", "-c", script], capture_output=True, text=True, check=False, env=merged
    )


class WindowsBundlingTest(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def fake_dumpbin(self) -> Path:
        tools = self.root / "tools"
        tools.mkdir()
        dumpbin = tools / "dumpbin"
        dumpbin.write_text(DUMPBIN)
        dumpbin.chmod(0o755)
        return tools

    def test_dependencies_survive_crlf_from_the_native_tool(self) -> None:
        staging = self.root / "staging"
        (staging / "bin").mkdir(parents=True)
        (staging / "bin" / "prog.exe").write_bytes(b"program")
        deps = self.root / "deps"
        deps.mkdir()
        (deps / "libkvliteweirdep.dll").write_bytes(b"library")
        tools = self.fake_dumpbin()

        result = run_bash(
            f'bash "{BUNDLER}" "{staging}" windows-amd64',
            env={
                "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}",
                "KVLITE_BUNDLE_LIB_PATH": str(deps),
            },
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(
            (staging / "bin" / "libkvliteweirdep.dll").is_file(),
            "the declared dependency was not copied beside the executable",
        )
        self.assertFalse(
            (staging / "bin" / "KERNEL32.dll").exists(),
            "a system DLL was treated as a bundle dependency",
        )
        self.assertNotIn("KERNEL32.dll\r", result.stderr, "a trailing CR reached a dependency name")

    def test_native_windows_tools_are_not_called_with_slash_options(self) -> None:
        # An MSYS2 shell reads a "/option" argument as an absolute path and
        # rewrites it before the tool sees it. The failure surfaces as an
        # empty dependency list, which reads like a binary without any.
        slash_option = re.compile(r"\b(dumpbin|cl|link|rc|mt)\s+/")
        offenders = []
        for script in sorted((ROOT / "scripts").glob("*.sh")):
            for number, line in enumerate(script.read_text().splitlines(), start=1):
                if slash_option.search(line):
                    offenders.append(f"{script.name}:{number}")
        self.assertEqual(offenders, [])

    def test_release_manifest_reads_binary_mode_checksums(self) -> None:
        # hash_for is lifted from the release script itself, so this cannot
        # drift from the implementation.
        staging = self.root / "manifest"
        staging.mkdir()
        extraction = (
            f"eval \"$(sed -n '/^hash_for()/,/^}}/p' '{BUILD_RELEASE}')\"; "
            f"staging_dir='{staging}'; hash_for 'bin/kvlite.exe'"
        )
        cases = {
            "unix text mode": ("aaa  bin/kvlite.exe\n", "aaa\n"),
            "msys2 binary mode": ("bbb *bin/kvlite.exe\r\n", "bbb\n"),
            "msys2 without CR": ("ccc *bin/kvlite.exe\n", "ccc\n"),
        }
        for name, (contents, expected) in cases.items():
            (staging / "SHA256SUMS").write_text(contents)
            result = run_bash(extraction)
            self.assertEqual(result.returncode, 0, f"{name}: {result.stderr}")
            self.assertEqual(result.stdout, expected, name)


if __name__ == "__main__":
    sys.exit(unittest.main())
