"""Guard the registry publication contract recorded in the release workflow.

Publishing to PyPI, npm, RubyGems, and crates.io is opt-in per destination and
must go through OIDC trusted publishing: no long-lived registry secret may
appear in the workflow, every destination needs its own protected environment
and ``id-token: write``, and publication may only follow a published GitHub
Release. The npm job additionally must not let ``setup-node`` write an
``_authToken`` line, which suppresses the OIDC exchange.

The checks are textual on purpose: the repository ships no YAML parser, and
the same style guards the packaging recipes in ``test_stamp_packaging.py``.
"""

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/release-artifacts.yml"
RESEARCH = ROOT / "packaging/PUBLISHING-RESEARCH.md"

DESTINATIONS = {
    "publish-python": ("KVLITE_PUBLISH_PYPI", "pypi"),
    "publish-node": ("KVLITE_PUBLISH_NPM", "npm"),
    "publish-ruby": ("KVLITE_PUBLISH_RUBY", "rubygems"),
    "publish-rust": ("KVLITE_PUBLISH_CRATES", "crates-io"),
}

JOB_HEADER = re.compile(r"^  (?P<name>[A-Za-z0-9_-]+):[ \t]*$")
SECRET_REFERENCE = re.compile(r"secrets\.[A-Za-z0-9_]+")
LONG_LIVED_TOKEN = re.compile(
    r"^(?:[ \t]*)(?:NODE_AUTH_TOKEN|NPM_TOKEN|TWINE_PASSWORD|"
    r"RUBYGEMS_API_KEY|GEM_HOST_API_KEY|CARGO_REGISTRY_TOKEN):[ \t]*\$\{\{ *secrets",
    re.MULTILINE,
)


def split_jobs(source: str) -> dict[str, str]:
    """Split the workflow into top-level job blocks (two-space indented keys)."""
    jobs: dict[str, list[str]] = {}
    current: str | None = None
    for line in source.splitlines(keepends=True):
        header = JOB_HEADER.match(line)
        if header:
            current = header.group("name")
            jobs[current] = []
            continue
        if current is not None:
            jobs[current].append(line)
    return {name: "".join(lines) for name, lines in jobs.items()}


class RegistryPublicationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text()
        cls.jobs = split_jobs(cls.workflow)
        cls.research = RESEARCH.read_text()

    def test_every_destination_has_a_gated_oidc_job(self) -> None:
        for job, (variable, environment) in DESTINATIONS.items():
            with self.subTest(job=job):
                self.assertIn(job, self.jobs)
                body = self.jobs[job]
                self.assertIn(f"if: vars.{variable} == 'true'", body)
                self.assertIn(f"environment: {environment}", body)
                self.assertIn("id-token: write", body)

    def test_publication_follows_a_published_release(self) -> None:
        for job in DESTINATIONS:
            with self.subTest(job=job):
                self.assertRegex(self.jobs[job], r"needs: \[[^\]]*github-release")

    def test_no_registry_secret_appears_in_the_workflow(self) -> None:
        self.assertIsNone(SECRET_REFERENCE.search(self.workflow))
        self.assertIsNone(LONG_LIVED_TOKEN.search(self.workflow))

    def test_npm_job_avoids_the_auth_token_placeholder(self) -> None:
        # setup-node's registry-url writes _authToken=${NODE_AUTH_TOKEN}; an
        # empty expansion makes npm skip the OIDC exchange (ENEEDAUTH/404).
        body = self.jobs["publish-node"]
        self.assertNotRegex(body, r"(?m)^\s+registry-url:")
        self.assertNotRegex(body, r"(?m)^\s+NODE_AUTH_TOKEN:")

    def test_documented_environments_match_the_workflow(self) -> None:
        for _job, (_variable, environment) in DESTINATIONS.items():
            with self.subTest(environment=environment):
                self.assertIn(f"environment: {environment}", self.workflow)
                self.assertIn(f"`{environment}`", self.research)

    def test_research_doc_names_every_opt_in_variable(self) -> None:
        for variable, _environment in DESTINATIONS.values():
            with self.subTest(variable=variable):
                self.assertIn(variable, self.research)


if __name__ == "__main__":
    unittest.main()
