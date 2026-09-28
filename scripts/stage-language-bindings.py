#!/usr/bin/env python3
"""Stage versioned, source-only language packages without editing the checkout."""

import argparse
import json
import re
import shutil
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
VERSION_PATTERN = re.compile(r"^v?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$")
PYPI_NAME_PATTERN = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$")
PACKAGE_FILES = {
    "go": (
        "go.mod", "README.md", "db.go", "db_test.go", "native_unix.go",
        "native_unix_test.go", "native_unsupported.go", "testdata",
    ),
    "php": ("composer.json", "README.md", "src"),
    "python": ("pyproject.toml", "README.md", "src"),
    "node": ("package.json", "README.md", "binding.gyp", "native", "src"),
    "ruby": ("webong-kvlite.gemspec", "README.md", "lib", "test"),
    "rust": ("Cargo.toml", "README.md", "src"),
}


def replace_toml_field(path: Path, field: str, value: str) -> None:
    source = path.read_text()
    updated, count = re.subn(
        rf'(?m)^{re.escape(field)} = "[^"]+"$',
        f'{field} = "{value}"',
        source,
        count=1,
    )
    if count != 1:
        raise ValueError(f"expected one {field!r} in {path}")
    path.write_text(updated)


def stage(version: str, pypi_name: str, output: Path) -> None:
    match = VERSION_PATTERN.fullmatch(version)
    if not match:
        raise ValueError("version must be a stable tag such as v0.1.0")
    if not PYPI_NAME_PATTERN.fullmatch(pypi_name):
        raise ValueError("invalid PyPI distribution name")
    if re.sub(r"[-_.]+", "-", pypi_name).lower() == "kvlite":
        raise ValueError("PyPI distribution 'kvlite' belongs to another project")
    release_version = match.group(1)
    output.mkdir(parents=True, exist_ok=False)
    for language, paths in PACKAGE_FILES.items():
        source = ROOT / "lib" / "bindings" / language
        destination = output / language
        destination.mkdir()
        for relative in paths:
            from_path = source / relative
            to_path = destination / relative
            if from_path.is_dir():
                shutil.copytree(from_path, to_path)
            else:
                shutil.copy2(from_path, to_path)
        shutil.copy2(ROOT / "LICENSE", destination / "LICENSE")

    shutil.copy2(ROOT / "lib" / "bindings" / "test-fixtures" / "mock_kvlite.c", output / "ruby" / "test" / "mock_kvlite.c")

    replace_toml_field(output / "python" / "pyproject.toml", "name", pypi_name)
    replace_toml_field(output / "python" / "pyproject.toml", "version", release_version)
    replace_toml_field(output / "rust" / "Cargo.toml", "version", release_version)
    ruby_version = output / "ruby" / "lib" / "kvlite" / "version.rb"
    updated, count = re.subn(r'(?m)^  VERSION = "[^"]+"$', f'  VERSION = "{release_version}"', ruby_version.read_text(), count=1)
    if count != 1:
        raise ValueError(f"expected one Ruby gem version in {ruby_version}")
    ruby_version.write_text(updated)
    node_manifest = output / "node" / "package.json"
    node = json.loads(node_manifest.read_text())
    node["version"] = release_version
    node_manifest.write_text(json.dumps(node, indent=2) + "\n")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", help="stable release tag, for example v0.1.0")
    parser.add_argument("output", type=Path, help="new staging directory")
    parser.add_argument("--pypi-name", required=True, help="approved PyPI distribution name")
    args = parser.parse_args()
    stage(args.version, args.pypi_name, args.output)


if __name__ == "__main__":
    main()
