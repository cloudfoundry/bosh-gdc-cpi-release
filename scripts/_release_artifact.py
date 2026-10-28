#!/usr/bin/env python3
"""Inspect a CPI release without extracting its archive or trusting its filename."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import tarfile
from pathlib import Path

VALID_RELEASE_NAMES = ("bosh-gdc-cpi", "bosh-gdch-cpi", "bosh_gdch_cpi")


def release_identity(path: Path, expected_sha256: str = "") -> dict[str, str]:
    path = path.expanduser().resolve(strict=True)
    if not path.is_file():
        raise ValueError("CPI release must be a regular file")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    actual = digest.hexdigest()
    if expected_sha256 and (not re.fullmatch(r"[0-9a-fA-F]{64}", expected_sha256)
                            or actual != expected_sha256.lower()):
        raise ValueError("CPI release checksum mismatch")
    with tarfile.open(path, "r:gz") as archive:
        manifests = [entry for entry in archive if entry.name in ("release.MF", "./release.MF")]
        if len(manifests) != 1 or not manifests[0].isfile() or manifests[0].size > 1024 * 1024:
            raise ValueError("CPI release requires one bounded regular release.MF")
        stream = archive.extractfile(manifests[0])
        if stream is None:
            raise ValueError("CPI release manifest cannot be read")
        with stream:
            manifest = stream.read().decode("utf-8")
    fields = {}
    for key in ("name", "version"):
        values = re.findall(r"^" + key + r":[ \t]*([^\r\n]+)$", manifest, re.MULTILINE)
        if len(values) != 1:
            raise ValueError("CPI release manifest has missing or duplicate identity")
        value = values[0].strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+\-]*", value):
            raise ValueError("CPI release identity must be a simple scalar")
        fields[key] = value
    if fields["name"] not in VALID_RELEASE_NAMES:
        raise ValueError("archive is not a GDC CPI release")
    return {"path": str(path), "name": fields["name"], "version": fields["version"], "sha256": actual}


def report_identity(path: Path | None) -> dict[str, str]:
    """Keep failure reports writable when artifact resolution never completed."""
    if path is not None:
        try:
            return release_identity(path)
        except (OSError, ValueError, tarfile.TarError, UnicodeError):
            pass
    return {"name": "unresolved", "version": "unresolved", "sha256": "unresolved"}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("--sha256", default="")
    parser.add_argument("--expected-name", default="")
    parser.add_argument("--expected-version", default="")
    parser.add_argument("--github-env", type=Path)
    args = parser.parse_args()
    try:
        identity = release_identity(args.archive, args.sha256)
        if args.expected_name and identity["name"] != args.expected_name:
            raise ValueError(
                f"CPI release name mismatch: expected '{args.expected_name}', found '{identity['name']}'"
            )
        if args.expected_version and identity["version"] != args.expected_version:
            raise ValueError(
                f"CPI release version mismatch: expected '{args.expected_version}', found '{identity['version']}'"
            )
        if args.github_env:
            if any("\n" in value or "\r" in value for value in identity.values()):
                raise ValueError("release identity cannot contain line breaks")
            with args.github_env.open("a", encoding="utf-8") as stream:
                for key, value in identity.items():
                    stream.write(f"GDC_CPI_RELEASE_{key.upper()}={value}\n")
        print(json.dumps(identity, sort_keys=True))
    except (OSError, ValueError, tarfile.TarError, UnicodeError) as error:
        parser.exit(1, f"release artifact: {error}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
