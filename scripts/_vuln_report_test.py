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
"""Offline regressions for the govulncheck report renderer and fixable-vulnerability gate."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from _vuln_report import Finding, parse_stream, render

SCRIPT = Path(__file__).resolve().parent / "_vuln_report.py"


def stream(*msgs: dict) -> str:
    return "\n".join(json.dumps(m) for m in msgs) + "\n"


OSV_STDLIB = {"osv": {"id": "GO-2026-6617", "aliases": ["CVE-2026-97032"],
                      "summary": "HTTP/2 server crash due to HPACK encoder race in net/http"}}
OSV_AWS = {"osv": {"id": "GO-2022-0646", "aliases": ["CVE-2020-8911", "GHSA-f5pg-7wfw-84q9"],
                   "summary": "CBC padding oracle issue in AWS S3 Crypto SDK for golang"}}
OSV_PGP = {"osv": {"id": "GO-2026-5932", "aliases": [],
                   "summary": "The golang.org/x/crypto/openpgp package is unmaintained"}}


def finding(osv, module, version, fixed, function="F"):
    return {"finding": {"osv": osv, "fixed_version": fixed,
                        "trace": [{"module": module, "version": version, "package": module, "function": function}]}}


class ParseStreamTest(unittest.TestCase):
    def test_findings_are_collapsed_per_module_and_osv(self):
        text = stream(
            {"config": {"scanner_name": "govulncheck"}},
            OSV_STDLIB, OSV_AWS, OSV_PGP,
            finding("GO-2026-6617", "stdlib", "go1.25.0", "v1.26.9", "net/http.Serve"),
            finding("GO-2026-6617", "stdlib", "go1.25.0", "v1.26.9", "net/http.Get"),
            finding("GO-2022-0646", "github.com/aws/aws-sdk-go", "v1.45.7", "", "s3crypto.NewDecryptionClient"),
            finding("GO-2026-5932", "golang.org/x/crypto", "v0.58.0", None),
            "not json at all",
        )
        found = parse_stream(text.replace('"not json at all"', "not json at all"))
        self.assertEqual([(f.module, f.osv) for f in found], [
            ("stdlib", "GO-2026-6617"),
            ("github.com/aws/aws-sdk-go", "GO-2022-0646"),
            ("golang.org/x/crypto", "GO-2026-5932"),
        ])
        std = found[0]
        self.assertTrue(std.fixable)
        self.assertEqual((std.current, std.fixed, std.aliases), ("go1.25.0", "v1.26.9", ["CVE-2026-97032"]))
        self.assertIn("HPACK", std.summary)
        self.assertFalse(found[1].fixable)
        self.assertFalse(found[2].fixable)

    def test_empty_stream_has_no_findings(self):
        self.assertEqual(parse_stream(stream({"config": {}}, {"progress": {"message": "x"}})), [])

    def test_pretty_printed_stream_is_parsed(self):
        # govulncheck -json emits indented multi-line objects, not NDJSON
        text = json.dumps(OSV_STDLIB, indent=2) + "\n" + json.dumps(
            finding("GO-2026-6617", "stdlib", "go1.25.0", "v1.26.9"), indent=2) + "\n"
        found = parse_stream(text)
        self.assertEqual([(f.module, f.osv, f.fixed) for f in found], [("stdlib", "GO-2026-6617", "v1.26.9")])


class RenderTest(unittest.TestCase):
    def test_clean_report(self):
        text = render([], version="0.1.1", go_version="go1.26.10", scan_date="2026-10-28 00:00:00 UTC")
        self.assertIn("**Go Toolchain:** go1.26.10", text)
        self.assertIn("No known vulnerabilities found", text)
        self.assertNotIn("Fixable", text)

    def test_fixable_and_unfixable_sections(self):
        found = [
            Finding("GO-2026-6617", "stdlib", "go1.25.0", "v1.26.9", ["CVE-2026-97032"], "HPACK | race"),
            Finding("GO-2022-0646", "github.com/aws/aws-sdk-go", "v1.45.7", "", ["CVE-2020-8911"], "padding oracle"),
        ]
        text = render(found, version="0.1.0", go_version="go1.25.0", scan_date="2026-10-28 00:00:00 UTC")
        self.assertIn("❌ 2 known vulnerabilities (1 fixable by upgrading, 1 without an upstream fix)", text)
        self.assertIn("| `stdlib` | `go1.25.0` | `v1.26.9` | [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617) | CVE-2026-97032 | HPACK \\| race |", text)
        self.assertIn("| `github.com/aws/aws-sdk-go` | `v1.45.7` | [GO-2022-0646](https://pkg.go.dev/vuln/GO-2022-0646) | CVE-2020-8911 | padding oracle |", text)
        self.assertIn("symbol-level", text)

    def test_only_unfixable_is_a_warning_not_an_error(self):
        found = [Finding("GO-2026-5932", "golang.org/x/crypto", "v0.58.0", "", [], "unmaintained openpgp")]
        text = render(found, version="0.1.1", go_version="go1.26.10")
        self.assertIn("⚠️ 1 known vulnerability (0 fixable by upgrading, 1 without an upstream fix)", text)
        self.assertIn("None — the toolchain and all dependencies are at or above every fixed version.", text)


class CommandLineTest(unittest.TestCase):
    def run_cli(self, text, *extra):
        with tempfile.TemporaryDirectory() as tmp:
            src = Path(tmp) / "gv.json"
            out = Path(tmp) / "report.md"
            src.write_text(text)
            proc = subprocess.run(
                [sys.executable, str(SCRIPT), str(src), "--version", "0.1.0", "--go-version", "go1.25.0",
                 "--output", str(out), *extra],
                capture_output=True, text=True, check=False,
            )
            return proc, out.read_text() if out.exists() else ""

    def test_gate_fails_only_when_fixable_findings_exist(self):
        fixable = stream(OSV_STDLIB, finding("GO-2026-6617", "stdlib", "go1.25.0", "v1.26.9"))
        proc, report = self.run_cli(fixable, "--fail-on-fixable")
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertIn("upgrade: stdlib -> v1.26.9", proc.stderr)
        self.assertIn("GO-2026-6617", report)

        proc, _ = self.run_cli(fixable)  # report-only mode never fails
        self.assertEqual(proc.returncode, 0, proc.stderr)

        unfixable = stream(OSV_AWS, finding("GO-2022-0646", "github.com/aws/aws-sdk-go", "v1.45.7", ""))
        proc, report = self.run_cli(unfixable, "--fail-on-fixable")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("No upstream fix available", report)


if __name__ == "__main__":
    unittest.main()
