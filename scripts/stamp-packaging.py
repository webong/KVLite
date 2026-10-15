#!/usr/bin/env python3
"""Stamp the packaging recipes with a concrete release version.

Every Homebrew formula under ``packaging/homebrew`` pins its ``url`` to a git
tag and the Debian changelog names a version, so both ship with a placeholder
until the first packaged release exists. Run this from the tagged commit to
point every recipe at that release and commit the result; nothing outside
``packaging/`` is touched.

    scripts/stamp-packaging.py v0.2.0
    scripts/stamp-packaging.py v0.2.0 --check   # CI: fail if stale, write nothing
"""

import argparse
import re
import subprocess
import sys
from datetime import datetime, timezone
from email.utils import format_datetime
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BASE_VERSION = r"((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))"
VERSION_PATTERN = re.compile(rf"^v?{BASE_VERSION}$")
REVISION_PATTERN = re.compile(r"^[0-9a-f]{7,40}$")
MAINTAINER_PATTERN = re.compile(r"^.+ <[^@\s<>]+@[^@\s<>]+>$")
RESERVED_DOMAINS = ("example.com", "example.org", "example.net")
TAG_LINE = re.compile(r'^[ \t]*tag: "[^"]*",?[ \t]*$')
REVISION_LINE = re.compile(r'^[ \t]*revision: "[^"]*",?[ \t]*$')
VERSION_LINE = re.compile(r'(?m)^(?P<indent>[ \t]*)version "[^"]*"[ \t]*$')
CHANGELOG_ENTRY = re.compile(r"^kvlite \(([^)]+)\)")
STAMPED_TAG = re.compile(r'(?m)^[ \t]*tag: "([^"]+)",?[ \t]*$')
STAMPED_REVISION = re.compile(r'(?m)^[ \t]*revision: "([^"]+)",?[ \t]*$')
STAMPED_VERSION = re.compile(r'(?m)^[ \t]*version "([^"]+)"[ \t]*$')
CONTROL_MAINTAINER = re.compile(r"(?m)^Maintainer:[ \t]*(.+?)[ \t]*$")


def parse_maintainer(control: Path) -> str:
    match = CONTROL_MAINTAINER.search(control.read_text())
    if not match:
        raise ValueError(f"no Maintainer field in {control}")
    maintainer = match.group(1)
    if not MAINTAINER_PATTERN.fullmatch(maintainer):
        raise ValueError(f"invalid Maintainer in {control}: {maintainer!r}")
    domain = maintainer.rsplit("@", 1)[1].split(">", 1)[0].lower()
    if domain in RESERVED_DOMAINS:
        raise ValueError(f"reserved example domain in {control}: {maintainer!r}")
    return maintainer


def stamp_formula(path: Path, tag: str, version: str, revision: str) -> str:
    lines = path.read_text().splitlines(keepends=True)
    tag_indexes = [index for index, line in enumerate(lines) if TAG_LINE.match(line)]
    if len(tag_indexes) != 1:
        raise ValueError(f"expected one tag line in {path}")
    tag_index = tag_indexes[0]
    indent = re.match(r"[ \t]*", lines[tag_index]).group(0)
    lines[tag_index] = f'{indent}tag: "{tag}",\n'
    if tag_index + 1 < len(lines) and REVISION_LINE.match(lines[tag_index + 1]):
        lines[tag_index + 1] = f'{indent}revision: "{revision}"\n'
    else:
        lines.insert(tag_index + 1, f'{indent}revision: "{revision}"\n')
    updated, version_count = VERSION_LINE.subn(
        rf'\g<indent>version "{version}"', "".join(lines)
    )
    if version_count != 1:
        raise ValueError(f"expected one version line in {path}")
    return updated


def changelog_header(version: str, distribution: str, urgency: str) -> str:
    return f"kvlite ({version}-1) {distribution}; urgency={urgency}"


def stamp_changelog(
    path: Path,
    version: str,
    revision: str,
    maintainer: str,
    distribution: str,
    urgency: str,
    date: str,
) -> str:
    source = path.read_text()
    if not CHANGELOG_ENTRY.search(source):
        raise ValueError(f"unparsable changelog header in {path}")
    header = changelog_header(version, distribution, urgency)
    if source.startswith(header + "\n"):
        return source
    entry = (
        f"{header}\n\n"
        f"  * Release {version}, stamped from {revision} by\n"
        f"    scripts/stamp-packaging.py.\n\n"
        f" -- {maintainer}  {date}\n\n"
    )
    return entry + source


def plan(
    root: Path,
    version: str,
    revision: str,
    maintainer: str,
    distribution: str,
    urgency: str,
    date: str,
) -> dict[Path, str]:
    tag = f"v{version}"
    formulas = sorted((root / "packaging" / "homebrew").glob("*.rb"))
    if not formulas:
        raise ValueError(f"no Homebrew formulas under {root / 'packaging' / 'homebrew'}")
    updates = {path: stamp_formula(path, tag, version, revision) for path in formulas}
    changelog = root / "packaging" / "debian" / "changelog"
    if not changelog.is_file():
        raise ValueError(f"no Debian changelog at {changelog}")
    updates[changelog] = stamp_changelog(
        changelog, version, revision, maintainer, distribution, urgency, date
    )
    return updates


def verify(root: Path, version: str, revision: str) -> list[str]:
    """Report recipes that do not point at this release.

    Compares the facts a release pins (tag, revision, version, changelog
    header) rather than whole files, so a stamped tree stays current however
    long ago its changelog entry was dated.
    """
    tag = f"v{version}"
    problems = []
    formulas = sorted((root / "packaging" / "homebrew").glob("*.rb"))
    if not formulas:
        return [f"no Homebrew formulas under {root / 'packaging' / 'homebrew'}"]
    for path in formulas:
        source = path.read_text()
        for pattern, expected, field in (
            (STAMPED_TAG, tag, "tag"),
            (STAMPED_REVISION, revision, "revision"),
            (STAMPED_VERSION, version, "version"),
        ):
            match = pattern.search(source)
            if not match:
                problems.append(f"{path}: no {field} line")
            elif match.group(1) != expected:
                problems.append(f"{path}: {field} {match.group(1)!r} != {expected!r}")
    changelog = root / "packaging" / "debian" / "changelog"
    if not changelog.is_file():
        problems.append(f"no Debian changelog at {changelog}")
    elif not changelog.read_text().startswith(f"kvlite ({version}-1) "):
        problems.append(f"{changelog}: top entry is not {version}-1")
    return problems


def head_revision(root: Path) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "HEAD"],
        capture_output=True,
        text=True,
        check=False,
    )
    return result.stdout.strip()


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("version", help="release tag, e.g. v0.2.0 or 0.2.0")
    parser.add_argument(
        "--revision", help="git revision the formulas pin (default: current HEAD)"
    )
    parser.add_argument(
        "--root",
        type=Path,
        default=ROOT,
        help="repository root holding packaging/ (default: this checkout)",
    )
    parser.add_argument("--distribution", default="unstable", help="Debian distribution")
    parser.add_argument("--urgency", default="medium", help="Debian urgency")
    parser.add_argument(
        "--maintainer",
        help="Debian changelog maintainer (default: Maintainer from debian/control)",
    )
    parser.add_argument("--date", help="Debian changelog date (default: now, UTC)")
    parser.add_argument(
        "--check",
        action="store_true",
        help="report recipes that do not point at this release and exit non-zero; "
        "change nothing",
    )
    args = parser.parse_args(argv)

    match = VERSION_PATTERN.fullmatch(args.version)
    if not match:
        parser.error("version must be a stable tag such as v0.2.0")
    version = match.group(1)
    revision = args.revision or head_revision(args.root)
    if not REVISION_PATTERN.fullmatch(revision):
        parser.error("revision must be a 7-40 character lowercase hex git revision")
    try:
        maintainer = args.maintainer or parse_maintainer(
            args.root / "packaging" / "debian" / "control"
        )
    except ValueError as error:
        print(f"stamp-packaging: {error}", file=sys.stderr)
        return 2
    if not MAINTAINER_PATTERN.fullmatch(maintainer):
        parser.error(f"maintainer must look like 'Name <address@host>', got {maintainer!r}")
    date = args.date or format_datetime(datetime.now(timezone.utc))

    if args.check:
        problems = verify(args.root, version, revision)
        for problem in problems:
            print(f"stamp-packaging: {problem}", file=sys.stderr)
        if problems:
            return 1
        print(f"stamp-packaging: packaging is current for {version}")
        return 0

    try:
        updates = plan(
            args.root,
            version,
            revision,
            maintainer,
            args.distribution,
            args.urgency,
            date,
        )
    except ValueError as error:
        print(f"stamp-packaging: {error}", file=sys.stderr)
        return 2

    stale = [path for path, expected in updates.items() if path.read_text() != expected]
    if not stale:
        print(f"stamp-packaging: already current for {version}")
        return 0
    for path in stale:
        path.write_text(updates[path])
        print(f"stamp-packaging: updated {path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
