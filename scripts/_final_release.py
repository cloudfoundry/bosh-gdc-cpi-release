#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""Produce committed BOSH final-release metadata whose blobs live on GitHub Releases.

bosh.io (https://github.com/cloudfoundry/bosh-io-releases) ignores GitHub Releases
and instead rebuilds every final release from the metadata committed on the default
branch: releases/<name>/<name>-<version>.yml plus the .final_builds/ indices, whose
blobstore_id entries must be downloadable anonymously from the blobstore declared in
config/final.yml.

This repository uses GitHub Release assets as that read-only blobstore. The bosh CLI
s3 provider in config/final.yml is pointed at

    https://github.com/<owner>/<repo>/releases/download/<blobstore_id>

with credentials_source: none, so a blobstore_id of the form v<version>/<asset> resolves
to a public release asset. Because that endpoint is read-only, final-release metadata
is generated in two steps:

  1. `finalize`: run `bosh finalize-release` against the already-built release tarball
     using a throwaway `provider: local` blobstore, then rewrite every freshly added
     blobstore_id (a local UUID) to v<version>/<kind>-<name>-<fingerprint>.tgz and stage
     the matching blob under --assets-dir so the caller can upload it as a release asset.
  2. `verify`: in a pristine temporary copy of the release metadata (no src/, no blob
     cache), run `bosh create-release releases/<name>/<name>-<version>.yml --tarball ...`
     exactly like bosh.io does, proving the blobs can be fetched from GitHub.

Only the standard library is used; scripts/_final_release_test.py covers the index
rewriting against fixture data.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

FINAL_BUILDS_DIR = ".final_builds"
RELEASES_DIR = "releases"
RELEASE_DIR_FILES = ("config", "jobs", "packages", "LICENSE", "NOTICE", RELEASES_DIR, FINAL_BUILDS_DIR)

_NAME_RE = re.compile(r"^(?:name|final_name):\s*(\S+)\s*$", re.MULTILINE)
_BUILD_KEY_RE = re.compile(r"^  ([0-9a-f]+):\s*$")
_BLOB_ID_RE = re.compile(r"^(    blobstore_id:\s*)(\S+)\s*$")


def release_name(repo_root: Path) -> str:
    text = (repo_root / "config" / "final.yml").read_text(encoding="utf-8")
    match = _NAME_RE.search(text)
    if not match:
        raise ValueError("config/final.yml must define 'name' (or 'final_name')")
    return match.group(1).strip("'\"")


def index_files(repo_root: Path) -> list[tuple[str, str, Path]]:
    """Return (kind, name, index_path) for every .final_builds index.yml in the release dir."""
    found: list[tuple[str, str, Path]] = []
    base = repo_root / FINAL_BUILDS_DIR
    for kind in ("jobs", "packages"):
        kind_dir = base / kind
        if not kind_dir.is_dir():
            continue
        for name_dir in sorted(p for p in kind_dir.iterdir() if p.is_dir()):
            index = name_dir / "index.yml"
            if index.is_file():
                found.append((kind.rstrip("s"), name_dir.name, index))
    license_index = base / "license" / "index.yml"
    if license_index.is_file():
        found.append(("license", "license", license_index))
    return found


def asset_name(kind: str, name: str, fingerprint: str) -> str:
    if kind == "license":
        return f"license-{fingerprint}.tgz"
    return f"{kind}-{name}-{fingerprint}.tgz"


def rewrite_index_text(text: str, kind: str, name: str, version: str) -> tuple[str, list[tuple[str, str, str]]]:
    """Rewrite local UUID blobstore_ids in one index.yml; returns (new_text, [(fingerprint, uuid, asset)]).

    The bosh CLI writes indices as:
        builds:
          <fingerprint>:
            version: <fingerprint>
            blobstore_id: <id>
            sha1: sha256:...
        format-version: "2"
    Entries whose blobstore_id already contains a '/' were finalized by an earlier release
    and keep pointing at that release's asset, which stays downloadable forever.
    """
    rewritten: list[tuple[str, str, str]] = []
    out: list[str] = []
    fingerprint = ""
    for line in text.splitlines(keepends=True):
        key = _BUILD_KEY_RE.match(line)
        if key:
            fingerprint = key.group(1)
        blob = _BLOB_ID_RE.match(line)
        if blob and fingerprint and "/" not in blob.group(2):
            asset = asset_name(kind, name, fingerprint)
            rewritten.append((fingerprint, blob.group(2), asset))
            line = f"{blob.group(1)}v{version}/{asset}\n"
        out.append(line)
    return "".join(out), rewritten


def rewrite_indices(repo_root: Path, version: str, local_blobstore: Path, assets_dir: Path) -> list[dict[str, str]]:
    """Point every freshly added blobstore_id at a GitHub Release asset and stage the blob."""
    assets_dir.mkdir(parents=True, exist_ok=True)
    staged: list[dict[str, str]] = []
    for kind, name, index_path in index_files(repo_root):
        new_text, rewritten = rewrite_index_text(index_path.read_text(encoding="utf-8"), kind, name, version)
        if not rewritten:
            continue
        for fingerprint, blob_id, asset in rewritten:
            blob_path = local_blobstore / blob_id
            if not blob_path.is_file():
                raise FileNotFoundError(f"blob '{blob_id}' for {kind} '{name}' not found in {local_blobstore}")
            shutil.copyfile(blob_path, assets_dir / asset)
            staged.append({
                "kind": kind,
                "name": name,
                "fingerprint": fingerprint,
                "asset": asset,
                "path": str(assets_dir / asset),
            })
        index_path.write_text(new_text, encoding="utf-8")
    return staged


def finalize(repo_root: Path, tarball: Path, version: str, assets_dir: Path, bosh: str = "bosh") -> dict:
    """Run `bosh finalize-release` with a throwaway local blobstore, then rewrite the indices."""
    final_yml = repo_root / "config" / "final.yml"
    original = final_yml.read_text(encoding="utf-8")
    name = release_name(repo_root)
    with tempfile.TemporaryDirectory(prefix="final-release-blobstore-") as tmp:
        local_blobstore = Path(tmp) / "blobstore"
        local_blobstore.mkdir()
        try:
            final_yml.write_text(
                f"name: {name}\nblobstore:\n  provider: local\n  options:\n    blobstore_path: {local_blobstore}\n",
                encoding="utf-8",
            )
            subprocess.run(
                [bosh, "finalize-release", f"--dir={repo_root}", f"--version={version}", "--force", str(tarball)],
                check=True,
            )
        finally:
            final_yml.write_text(original, encoding="utf-8")
        staged = rewrite_indices(repo_root, version, local_blobstore, assets_dir)
    manifest = repo_root / RELEASES_DIR / name / f"{name}-{version}.yml"
    if not manifest.is_file():
        raise FileNotFoundError(f"expected final release manifest {manifest} after finalize-release")
    return {
        "name": name,
        "version": version,
        "manifest": str(manifest),
        "release_index": str(repo_root / RELEASES_DIR / name / "index.yml"),
        "assets": staged,
    }


def verify(repo_root: Path, version: str, output_tarball: Path, bosh: str = "bosh") -> str:
    """Rebuild the final release from committed metadata only, the way bosh.io does."""
    name = release_name(repo_root)
    manifest_rel = Path(RELEASES_DIR) / name / f"{name}-{version}.yml"
    if not (repo_root / manifest_rel).is_file():
        raise FileNotFoundError(f"missing final release manifest {repo_root / manifest_rel}")
    with tempfile.TemporaryDirectory(prefix="final-release-verify-") as tmp:
        clean = Path(tmp) / "release"
        clean.mkdir()
        for item in RELEASE_DIR_FILES:
            src = repo_root / item
            if src.is_dir():
                shutil.copytree(src, clean / item, ignore=shutil.ignore_patterns("*.tgz", "*.sha256", "*.md"))
            elif src.is_file():
                shutil.copyfile(src, clean / item)
        env = dict(os.environ)
        env["HOME"] = str(Path(tmp) / "home")  # empty blob cache: every blob must come from GitHub
        env["BOSH_NON_INTERACTIVE"] = "true"
        subprocess.run(
            [bosh, "create-release", str(manifest_rel), f"--dir={clean}", f"--tarball={output_tarball}"],
            check=True,
            env=env,
        )
    return str(output_tarball)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", type=Path, default=Path("."), help="release directory (repository root)")
    parser.add_argument("--bosh", default="bosh", help="bosh CLI binary")
    sub = parser.add_subparsers(dest="command", required=True)

    fin = sub.add_parser("finalize", help="finalize a release tarball and stage its blobs as release assets")
    fin.add_argument("--tarball", type=Path, required=True)
    fin.add_argument("--version", required=True)
    fin.add_argument("--assets-dir", type=Path, required=True)
    fin.add_argument("--json", type=Path, help="write the finalize result as JSON here")

    ver = sub.add_parser("verify", help="rebuild the final release from committed metadata only")
    ver.add_argument("--version", required=True)
    ver.add_argument("--tarball", type=Path, required=True, help="where to write the rebuilt tarball")

    args = parser.parse_args()
    try:
        if args.command == "finalize":
            result = finalize(args.repo.resolve(), args.tarball.resolve(), args.version, args.assets_dir.resolve(), args.bosh)
            text = json.dumps(result, indent=2)
            if args.json:
                args.json.write_text(text + "\n", encoding="utf-8")
            print(text)
        elif args.command == "verify":
            path = verify(args.repo.resolve(), args.version, args.tarball.resolve(), args.bosh)
            print(f"Rebuilt final release {args.version} from committed metadata: {path}")
    except (ValueError, FileNotFoundError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"ERROR: {error}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
