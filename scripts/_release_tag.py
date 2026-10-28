#!/usr/bin/env python3
"""Decide whether a release tag may be published, where it comes from, and whether it is Latest.

A patch tag vX.Y.Z whose Z is above zero comes from the release/X.Y branch
whenever that branch exists. It must be a final tag, and its commit must be on
that branch, whether or not the commit is also on main. That is how we ship a
fix for an older minor line after main has moved on, and it stops main's newer
work from going out under the older line's version. Every other tag must
point at a commit on main (unless --allow-branch is explicitly set for a
candidate/branch workflow run). That covers every tag whose Z is zero, and
every patch tag or prerelease for a line that has no release branch. A tag
that breaks these rules is refused, and the message says which rule it broke
and how to fix the tag.

A release is marked Latest only when its tag is the highest final version tag
in the repository, so a patch for an older line never takes the badge from a
newer release. Prereleases are never Latest, so they don't count as higher.

The release workflow runs this from the tagged checkout, and
scripts/_release_tag_test.py covers each rule against throwaway repositories.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
from pathlib import Path
import re
import subprocess

TAG_PATTERN = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.]+))?")


class TagRefused(ValueError):
    """The tag breaks a release rule, and the message says how to fix it."""


@dataclass(frozen=True)
class Version:
    major: int
    minor: int
    patch: int
    prerelease: str

    @property
    def text(self) -> str:
        suffix = f"-{self.prerelease}" if self.prerelease else ""
        return f"{self.major}.{self.minor}.{self.patch}{suffix}"

    @property
    def line(self) -> str:
        return f"{self.major}.{self.minor}"

    @property
    def branch(self) -> str:
        return f"release/{self.line}"

    @property
    def triple(self) -> tuple[int, int, int]:
        return (self.major, self.minor, self.patch)


def parse_tag(tag: str) -> Version:
    match = TAG_PATTERN.fullmatch(tag)
    if not match:
        raise TagRefused(f"tag '{tag}' is not vX.Y.Z or vX.Y.Z-<prerelease>")
    major, minor, patch, prerelease = match.groups()
    return Version(int(major), int(minor), int(patch), prerelease or "")


def bump_version(version_text: str, mode: str) -> str:
    """Compute the next SemVer string (X.Y.Z) from version_text according to mode."""
    normalized = version_text.strip()
    if not normalized.startswith("v"):
        normalized = f"v{normalized}"
    ver = parse_tag(normalized)
    if mode == "bump-major":
        return f"{ver.major + 1}.0.0"
    if mode == "bump-minor":
        return f"{ver.major}.{ver.minor + 1}.0"
    if mode == "bump-patch":
        return f"{ver.major}.{ver.minor}.{ver.patch + 1}"
    if mode == "noop":
        return f"{ver.major}.{ver.minor}.{ver.patch}"
    raise TagRefused(f"unsupported bump mode '{mode}'")


def latest_final_tag(tags: list[str]) -> str:
    """Return the highest final SemVer tag (vX.Y.Z) in tags, or empty string if none exists."""
    final_versions: list[Version] = []
    for tag in tags:
        try:
            ver = parse_tag(tag)
        except TagRefused:
            continue
        if not ver.prerelease:
            final_versions.append(ver)
    if not final_versions:
        return ""
    highest = max(final_versions, key=lambda v: v.triple)
    return f"v{highest.text}"


def next_version_from_tags(tags: list[str], mode: str = "bump-patch", default_initial: str = "0.1.0") -> str:
    """Derive the next target SemVer (X.Y.Z) from existing final vX.Y.Z git tags."""
    if mode not in ("bump-patch", "bump-minor", "bump-major", "noop"):
        raise TagRefused(f"unsupported bump mode '{mode}'")
    highest_tag = latest_final_tag(tags)
    if not highest_tag:
        if mode == "bump-major":
            return "1.0.0"
        return default_initial
    return bump_version(highest_tag, mode)


def is_latest(version: Version, tags: list[str]) -> bool:
    """Report whether no final version tag in the repository is higher than this one."""
    if version.prerelease:
        return False
    for tag in tags:
        try:
            other = parse_tag(tag)
        except TagRefused:
            continue
        if not other.prerelease and other.triple > version.triple:
            return False
    return True


class GitRepo:
    """The git questions the release rule asks, answered from a checkout and its remote."""

    def __init__(self, path: Path, remote: str = "origin") -> None:
        self.path = path
        self.remote = remote

    def _git(self, *args: str, allowed: tuple[int, ...] = (0,)) -> subprocess.CompletedProcess[str]:
        result = subprocess.run(["git", *args], cwd=self.path, capture_output=True, text=True, check=False)
        if result.returncode not in allowed:
            detail = result.stderr.strip() or result.stdout.strip()
            raise RuntimeError(f"git {' '.join(args)} exited {result.returncode}: {detail}")
        return result

    def fetch(self, branch: str) -> None:
        self._git("fetch", "--no-tags", self.remote, f"+refs/heads/{branch}:refs/remotes/{self.remote}/{branch}")

    def has_branch(self, branch: str) -> bool:
        result = self._git("ls-remote", "--exit-code", "--heads", self.remote, f"refs/heads/{branch}", allowed=(0, 2))
        return result.returncode == 0

    def contains(self, branch: str, sha: str) -> bool:
        result = self._git("merge-base", "--is-ancestor", sha, f"refs/remotes/{self.remote}/{branch}", allowed=(0, 1))
        return result.returncode == 0

    def release_branches_containing(self, sha: str) -> list[str]:
        prefix = f"refs/remotes/{self.remote}/"
        result = self._git("for-each-ref", "--contains", sha, "--format=%(refname)", f"{prefix}release/")
        return sorted(line.removeprefix(prefix) for line in result.stdout.splitlines() if line)

    def tags(self) -> list[str]:
        return self._git("tag", "--list").stdout.split()

    def nearest_final_tag_before(self, sha: str) -> str:
        result = self._git(
            "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "--exclude", "*-*", f"{sha}^",
            allowed=(0, 128),
        )
        return result.stdout.strip() if result.returncode == 0 else ""

    def commits_since_tag(self, tag: str, sha: str = "HEAD") -> int:
        """Count new commits between tag and sha (comparing ancestry or commit timestamps if diverged)."""
        if not tag:
            return int(self._git("rev-list", "--count", sha).stdout.strip())
        tag_sha = self._git("rev-parse", f"{tag}^{{commit}}").stdout.strip()
        target_sha = self._git("rev-parse", f"{sha}^{{commit}}").stdout.strip()
        if tag_sha == target_sha:
            return 0
        is_tag_ancestor = self._git("merge-base", "--is-ancestor", tag_sha, target_sha, allowed=(0, 1)).returncode == 0
        is_target_ancestor = self._git("merge-base", "--is-ancestor", target_sha, tag_sha, allowed=(0, 1)).returncode == 0
        if is_tag_ancestor or is_target_ancestor:
            return int(self._git("rev-list", "--count", f"{tag_sha}..{target_sha}").stdout.strip())
        tag_ts = int(self._git("log", "-1", "--format=%ct", tag_sha).stdout.strip())
        merge_base = self._git("merge-base", tag_sha, target_sha).stdout.strip()
        return int(self._git("rev-list", "--count", f"--after={tag_ts}", f"{merge_base}..{target_sha}").stdout.strip())


def check_new_commits(git: GitRepo, sha: str = "HEAD") -> dict[str, str]:
    """Check whether any new commits exist between the latest final SemVer tag and sha."""
    last_tag = latest_final_tag(git.tags())
    count = git.commits_since_tag(last_tag, sha)
    has_new = not last_tag or count > 0
    return {
        "latest_tag": last_tag,
        "commits_since_tag": str(count),
        "has_new_commits": "true" if has_new else "false",
    }


def _where_else(git: GitRepo, sha: str) -> str:
    branches = git.release_branches_containing(sha)
    if not branches:
        return ""
    tags = " or ".join(f"v{branch.removeprefix('release/')}.N" for branch in branches)
    return f"The commit is on {' and '.join(branches)}, so a tag for it would be {tags}.\n"


def release_source(tag: str, version: Version, sha: str, git: GitRepo, *, allow_branch: bool = False) -> str:
    """Return the branch this tag releases from, or raise TagRefused naming the broken rule."""
    branch = version.branch
    if version.patch > 0 and git.has_branch(branch):
        if version.prerelease and not allow_branch:
            raise TagRefused(
                f"tag '{tag}' is a prerelease, and {branch} exists, so a patch for {version.line}\n"
                "comes from that branch as a final tag.\n"
                f"Delete the tag, put the fix on {branch}, and tag it "
                f"v{version.major}.{version.minor}.{version.patch}."
            )
        git.fetch(branch)
        if git.contains(branch, sha):
            return branch
        if allow_branch:
            return "branch"
        git.fetch("main")
        on_main = ""
        if git.contains("main", sha):
            on_main = f"The commit is on main, and main's newer work must not ship as {version.text}.\n"
        raise TagRefused(
            f"tag '{tag}' points at {sha}, which is not on {branch}.\n"
            f"{branch} exists, so a patch for {version.line} comes from that branch.\n"
            f"{on_main}{_where_else(git, sha)}"
            f"Delete the tag, cherry-pick the fix onto {branch} with -x, and tag that commit."
        )
    git.fetch("main")
    if git.contains("main", sha):
        return "main"
    if allow_branch:
        return "branch"
    if version.prerelease or version.patch == 0:
        raise TagRefused(
            f"tag '{tag}' points at {sha}, which is not on main.\n"
            "Only a final patch tag (vX.Y.Z with Z above zero) may come from a release branch,\n"
            "so this tag must point at main. Delete the tag, land the commit on main, and tag again."
        )
    raise TagRefused(
        f"tag '{tag}' points at {sha}, which is not on main,\n"
        f"and there is no {branch} branch to release it from.\n"
        f"{_where_else(git, sha)}"
        f"Delete the tag, then either land the commit on main or cut {branch}\n"
        f"from v{version.line}.0 with the fix on it, and tag again."
    )


def release_plan(tag: str, sha: str, git: GitRepo, *, allow_branch: bool = False) -> dict[str, str]:
    version = parse_tag(tag)
    source = release_source(tag, version, sha, git, allow_branch=allow_branch)
    return {
        "version": version.text,
        "prerelease": "true" if version.prerelease else "false",
        "source": source,
        "latest": "true" if is_latest(version, git.tags()) else "false",
        "notes_start_tag": "" if source in ("main", "branch") else git.nearest_final_tag_before(sha),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--tag", help="the release tag name, such as v0.1.0")
    parser.add_argument("--sha", help="the commit the tag points at")
    parser.add_argument("--remote", default="origin")
    parser.add_argument("--repo", type=Path, default=Path("."))
    parser.add_argument("--allow-branch", action="store_true", help="allow candidate/branch releases outside main")
    parser.add_argument("--bump", choices=["bump-patch", "bump-minor", "bump-major", "noop"],
                        help="print the next SemVer (X.Y.Z) bumped from --tag and exit")
    parser.add_argument("--resolve-next", choices=["bump-patch", "bump-minor", "bump-major", "noop"],
                        help="derive the next SemVer (X.Y.Z) from existing vX.Y.Z git tags in --repo and exit")
    parser.add_argument("--check-new-commits", action="store_true",
                        help="check whether new commits exist since the latest final vX.Y.Z tag")
    parser.add_argument("--github-output", type=Path, help="append the plan here as key=value lines")
    args = parser.parse_args()
    try:
        git_repo = GitRepo(args.repo, args.remote)
        if args.check_new_commits:
            commit_info = check_new_commits(git_repo, args.sha or "HEAD")
            print(
                f"latest_tag={commit_info['latest_tag'] or 'none'} "
                f"commits_since_tag={commit_info['commits_since_tag']} "
                f"has_new_commits={commit_info['has_new_commits']}"
            )
            if args.github_output:
                with args.github_output.open("a", encoding="utf-8") as stream:
                    for key, value in commit_info.items():
                        stream.write(f"{key}={value}\n")
            return 0
        if args.resolve_next:
            print(next_version_from_tags(git_repo.tags(), args.resolve_next))
            return 0
        if not args.tag:
            parser.error("--tag is required unless --resolve-next is specified")
        if args.bump:
            print(bump_version(args.tag, args.bump))
            return 0
        if not args.sha:
            parser.error("--sha is required unless --bump or --resolve-next is specified")
        plan = release_plan(args.tag, args.sha, git_repo, allow_branch=args.allow_branch)
    except (TagRefused, RuntimeError) as error:
        parser.exit(1, f"ERROR: {error}\n")
    print(f"Releasing {args.tag} from {plan['source']}, Latest {plan['latest']}")
    if args.github_output:
        with args.github_output.open("a", encoding="utf-8") as stream:
            for key, value in plan.items():
                stream.write(f"{key}={value}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
