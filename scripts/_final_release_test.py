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
"""Offline regressions for rewriting .final_builds indices onto GitHub Release assets."""
from pathlib import Path
import tempfile
import unittest

from _final_release import asset_name, index_files, release_name, rewrite_index_text, rewrite_indices

PKG_INDEX = """builds:
  7f659a1354a0817bf9289c96d7238dc2d59aaa13260553bdf07d18f0d75037de:
    version: 7f659a1354a0817bf9289c96d7238dc2d59aaa13260553bdf07d18f0d75037de
    blobstore_id: fe75327f-689b-4a17-76f6-6eb22cb1df2d
    sha1: sha256:d5a82b16f998486c8faffe0d997be7f5bf3840a0f95e03595825c8866f7354c2
  aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111:
    version: aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111
    blobstore_id: v0.1.0/package-bosh-gdc-cpi-aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111.tgz
    sha1: sha256:0000000000000000000000000000000000000000000000000000000000000000
format-version: "2"
"""

LICENSE_INDEX = """builds:
  d0df1a42b66dd34aea649dc14f8e0969a8becc3b178433ef1e306221b718a517:
    version: d0df1a42b66dd34aea649dc14f8e0969a8becc3b178433ef1e306221b718a517
    blobstore_id: 93a0d12f-2d99-416e-43ab-58be98c50679
    sha1: sha256:2046c55a480a51c292aeb669ace5e60370201c371170f4bdb22512007cc69ad6
format-version: "2"
"""


class AssetNameTest(unittest.TestCase):
    def test_asset_names_are_stable_and_unique_per_fingerprint(self):
        self.assertEqual(asset_name("package", "bosh-gdc-cpi", "abc"), "package-bosh-gdc-cpi-abc.tgz")
        self.assertEqual(asset_name("job", "gdc_cpi", "abc"), "job-gdc_cpi-abc.tgz")
        self.assertEqual(asset_name("license", "license", "abc"), "license-abc.tgz")


class RewriteIndexTextTest(unittest.TestCase):
    def test_only_local_uuid_blob_ids_are_rewritten(self):
        text, rewritten = rewrite_index_text(PKG_INDEX, "package", "bosh-gdc-cpi", "0.2.0")
        fp = "7f659a1354a0817bf9289c96d7238dc2d59aaa13260553bdf07d18f0d75037de"
        self.assertEqual(rewritten, [(fp, "fe75327f-689b-4a17-76f6-6eb22cb1df2d", f"package-bosh-gdc-cpi-{fp}.tgz")])
        self.assertIn(f"    blobstore_id: v0.2.0/package-bosh-gdc-cpi-{fp}.tgz\n", text)
        # The entry already published under v0.1.0 is left untouched.
        self.assertIn("    blobstore_id: v0.1.0/package-bosh-gdc-cpi-aaaa1111", text)
        # Everything else (sha1, format-version, ordering) is byte-for-byte preserved.
        self.assertEqual(text.count("\n"), PKG_INDEX.count("\n"))
        self.assertTrue(text.endswith('format-version: "2"\n'))

    def test_already_rewritten_index_is_a_noop(self):
        once, _ = rewrite_index_text(PKG_INDEX, "package", "bosh-gdc-cpi", "0.2.0")
        twice, rewritten = rewrite_index_text(once, "package", "bosh-gdc-cpi", "0.3.0")
        self.assertEqual(rewritten, [])
        self.assertEqual(once, twice)


class RewriteIndicesTest(unittest.TestCase):
    def test_indices_are_rewritten_and_blobs_staged_as_assets(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "config").mkdir()
            (root / "config" / "final.yml").write_text("name: bosh-gdc-cpi\nblobstore:\n  provider: s3\n")
            pkg_dir = root / ".final_builds" / "packages" / "bosh-gdc-cpi"
            lic_dir = root / ".final_builds" / "license"
            pkg_dir.mkdir(parents=True)
            lic_dir.mkdir(parents=True)
            (pkg_dir / "index.yml").write_text(PKG_INDEX)
            (lic_dir / "index.yml").write_text(LICENSE_INDEX)
            blobstore = root / "blobstore"
            blobstore.mkdir()
            (blobstore / "fe75327f-689b-4a17-76f6-6eb22cb1df2d").write_bytes(b"package-blob")
            (blobstore / "93a0d12f-2d99-416e-43ab-58be98c50679").write_bytes(b"license-blob")
            assets = root / "assets"

            self.assertEqual(release_name(root), "bosh-gdc-cpi")
            self.assertEqual([(k, n) for k, n, _ in index_files(root)], [("package", "bosh-gdc-cpi"), ("license", "license")])

            staged = rewrite_indices(root, "0.1.0", blobstore, assets)

            self.assertEqual([s["asset"] for s in staged], [
                "package-bosh-gdc-cpi-7f659a1354a0817bf9289c96d7238dc2d59aaa13260553bdf07d18f0d75037de.tgz",
                "license-d0df1a42b66dd34aea649dc14f8e0969a8becc3b178433ef1e306221b718a517.tgz",
            ])
            for item in staged:
                self.assertTrue(Path(item["path"]).is_file(), item)
            self.assertEqual((assets / staged[0]["asset"]).read_bytes(), b"package-blob")
            self.assertEqual((assets / staged[1]["asset"]).read_bytes(), b"license-blob")
            self.assertIn("blobstore_id: v0.1.0/package-bosh-gdc-cpi-7f659a13", (pkg_dir / "index.yml").read_text())
            self.assertIn("blobstore_id: v0.1.0/license-d0df1a42", (lic_dir / "index.yml").read_text())

    def test_missing_local_blob_is_an_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            lic_dir = root / ".final_builds" / "license"
            lic_dir.mkdir(parents=True)
            (lic_dir / "index.yml").write_text(LICENSE_INDEX)
            with self.assertRaises(FileNotFoundError):
                rewrite_indices(root, "0.1.0", root / "empty", root / "assets")


if __name__ == "__main__":
    unittest.main()
