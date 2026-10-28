#!/usr/bin/env python3
"""Offline release tag rule regressions, run against throwaway git repositories."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from _release_tag import (
    GitRepo,
    TagRefused,
    bump_version,
    check_new_commits,
    is_latest,
    latest_final_tag,
    next_version_from_tags,
    parse_tag,
    release_plan,
)

SCRIPT = Path(__file__).resolve().parent / "_release_tag.py"


def setUpModule():
    os.environ.update({
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_AUTHOR_NAME": "release-tag-test",
        "GIT_AUTHOR_EMAIL": "release-tag-test@example.invalid",
        "GIT_COMMITTER_NAME": "release-tag-test",
        "GIT_COMMITTER_EMAIL": "release-tag-test@example.invalid",
    })


def git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


class ParseTagTest(unittest.TestCase):
    def test_final_and_prerelease_tags_parse(self):
        self.assertEqual(parse_tag("v0.8.1").text, "0.8.1")
        self.assertEqual(parse_tag("v0.8.1").branch, "release/0.8")
        self.assertEqual(parse_tag("v1.10.0-rc.2").prerelease, "rc.2")

    def test_malformed_tags_are_refused(self):
        for tag in ["v0.8", "v0.8.1.2", "0.8.1", "release-0.8.1", "v0.08.1", "v0.8.1-", "v0.8.1+build"]:
            with self.subTest(tag=tag):
                with self.assertRaises(TagRefused):
                    parse_tag(tag)

    def test_bump_version_modes(self):
        self.assertEqual(bump_version("v0.1.0", "bump-patch"), "0.1.1")
        self.assertEqual(bump_version("0.1.0", "bump-minor"), "0.2.0")
        self.assertEqual(bump_version("v0.1.0", "bump-major"), "1.0.0")
        self.assertEqual(bump_version("v0.1.0", "noop"), "0.1.0")

    def test_next_version_from_tags(self):
        self.assertEqual(next_version_from_tags([], "bump-patch"), "0.1.0")
        self.assertEqual(next_version_from_tags(["v0.1.0-dev.abc1234"], "bump-patch"), "0.1.0")
        self.assertEqual(next_version_from_tags([], "bump-major"), "1.0.0")
        self.assertEqual(next_version_from_tags(["v0.1.0", "v0.2.0-rc.1"], "bump-patch"), "0.1.1")
        self.assertEqual(next_version_from_tags(["v0.1.0", "v0.1.2"], "bump-minor"), "0.2.0")
        self.assertEqual(next_version_from_tags(["v0.1.0", "v0.2.3"], "bump-major"), "1.0.0")


class IsLatestTest(unittest.TestCase):
    def test_only_the_highest_final_tag_is_latest(self):
        tags = ["v0.7.3", "v0.8.0", "v0.8.1", "v0.9.0", "not-a-version"]
        self.assertTrue(is_latest(parse_tag("v0.9.0"), tags))
        self.assertFalse(is_latest(parse_tag("v0.8.1"), tags))
        self.assertTrue(is_latest(parse_tag("v0.8.1"), ["v0.8.0", "v0.8.1"]))

    def test_versions_compare_as_numbers(self):
        self.assertTrue(is_latest(parse_tag("v0.10.0"), ["v0.9.0", "v0.10.0"]))
        self.assertFalse(is_latest(parse_tag("v0.9.9"), ["v0.9.9", "v0.10.0"]))

    def test_prereleases_are_never_latest_and_never_outrank_a_final(self):
        self.assertFalse(is_latest(parse_tag("v0.9.0-rc.1"), ["v0.8.1", "v0.9.0-rc.1"]))
        self.assertTrue(is_latest(parse_tag("v0.8.2"), ["v0.8.1", "v0.8.2", "v0.9.0-rc.1"]))


class Fixture:
    """A bare origin and a working clone of it, built one commit at a time."""

    def __init__(self, root):
        self.origin = root / "origin.git"
        self.work = root / "work"
        git(root, "init", "-q", "--bare", "--initial-branch=main", str(self.origin))
        git(root, "clone", "-q", str(self.origin), str(self.work))
        git(self.work, "symbolic-ref", "HEAD", "refs/heads/main")

    def commit(self, message):
        git(self.work, "commit", "-q", "--allow-empty", "-m", message)
        return git(self.work, "rev-parse", "HEAD")


class TagCase(unittest.TestCase):
    def plan(self, tag, sha):
        git(self.work, "tag", "-f", tag, sha)
        try:
            return release_plan(tag, sha, GitRepo(self.work))
        finally:
            git(self.work, "tag", "-d", tag)

    def refusal(self, tag, sha):
        with self.assertRaises(TagRefused) as caught:
            self.plan(tag, sha)
        return str(caught.exception)


class ReleaseSourceTest(TagCase):
    """main carries v0.7.0, v0.8.0, and one later commit; each release branch carries one fix."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        repo = Fixture(Path(cls.tmp.name))
        cls.v070 = repo.commit("cut 0.7.0")
        git(repo.work, "tag", "v0.7.0")
        cls.v080 = repo.commit("cut 0.8.0")
        git(repo.work, "tag", "v0.8.0")
        cls.main_tip = repo.commit("work after 0.8.0")
        git(repo.work, "push", "-q", "origin", "main")
        git(repo.work, "switch", "-q", "-c", "release/0.7", cls.v070)
        cls.on_07 = repo.commit("fix for 0.7")
        git(repo.work, "push", "-q", "origin", "release/0.7")
        git(repo.work, "switch", "-q", "-c", "release/0.8", cls.v080)
        cls.on_08 = repo.commit("fix for 0.8")
        git(repo.work, "push", "-q", "origin", "release/0.8")
        git(repo.work, "switch", "-q", "-c", "unpushed", cls.v080)
        cls.stray = repo.commit("pushed nowhere")
        git(repo.work, "switch", "-q", "--detach", cls.v080)
        cls.work = repo.work

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_tags_for_a_line_without_a_branch_release_from_main(self):
        for tag in ["v0.9.0", "v0.9.0-rc.1", "v0.9.1"]:
            with self.subTest(tag=tag):
                plan = self.plan(tag, self.main_tip)
                self.assertEqual(plan["source"], "main")
                self.assertEqual(plan["notes_start_tag"], "")

    def test_patch_for_a_branched_line_releases_from_its_branch(self):
        self.assertEqual(self.plan("v0.8.1", self.on_08), {
            "version": "0.8.1", "prerelease": "false", "source": "release/0.8",
            "latest": "true", "notes_start_tag": "v0.8.0",
        })
        self.assertEqual(self.plan("v0.7.1", self.on_07), {
            "version": "0.7.1", "prerelease": "false", "source": "release/0.7",
            "latest": "false", "notes_start_tag": "v0.7.0",
        })

    def test_patch_on_main_is_refused_once_the_release_branch_exists(self):
        message = self.refusal("v0.8.1", self.main_tip)
        self.assertIn("release/0.8 exists", message)
        self.assertIn("main's newer work must not ship as 0.8.1", message)

    def test_command_line_writes_the_plan_and_refuses_loudly(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "github-output"
            git(self.work, "tag", "v0.8.1", self.on_08)
            try:
                ok = subprocess.run(
                    [sys.executable, str(SCRIPT), "--repo", str(self.work), "--tag", "v0.8.1",
                     "--sha", self.on_08, "--github-output", str(output)],
                    capture_output=True, text=True, check=False,
                )
            finally:
                git(self.work, "tag", "-d", "v0.8.1")
            self.assertEqual(ok.returncode, 0, ok.stderr)
            self.assertIn("Releasing v0.8.1 from release/0.8, Latest true", ok.stdout)
            self.assertEqual(output.read_text().splitlines(), [
                "version=0.8.1", "prerelease=false", "source=release/0.8", "latest=true", "notes_start_tag=v0.8.0",
            ])
            refused = subprocess.run(
                [sys.executable, str(SCRIPT), "--repo", str(self.work), "--tag", "v0.8.0", "--sha", self.on_08],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(refused.returncode, 1)
            self.assertTrue(refused.stderr.startswith("ERROR: tag 'v0.8.0' points at"), refused.stderr)

    def test_check_new_commits_since_latest_tag(self):
        self.assertEqual(latest_final_tag(["v0.7.0", "v0.8.0", "v0.9.0-dev.abc1234"]), "v0.8.0")
        repo = GitRepo(self.work)
        self.assertEqual(check_new_commits(repo, self.v080), {
            "latest_tag": "v0.8.0",
            "commits_since_tag": "0",
            "has_new_commits": "false",
        })
        self.assertEqual(check_new_commits(repo, self.v070), {
            "latest_tag": "v0.8.0",
            "commits_since_tag": "0",
            "has_new_commits": "false",
        })
        self.assertEqual(check_new_commits(repo, self.main_tip), {
            "latest_tag": "v0.8.0",
            "commits_since_tag": "1",
            "has_new_commits": "true",
        })


if __name__ == "__main__":
    unittest.main()
