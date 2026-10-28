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
"""Render a govulncheck JSON stream into the CPI vulnerability report and gate on fixable findings.

The release pipeline runs `govulncheck -mode=binary -json <cpi_binary_linux>` (symbol-level
scan of the linked binary against https://vuln.go.dev) and feeds the output here. Instead
of a flat list of OSV IDs, the report groups findings per affected module, states the
version currently linked into the binary and the version that fixes the issue, and lists
CVE/GHSA aliases with a one-line summary, split into:

  * "Fixable" findings, where the Go vulnerability database records a fixed version: these
    are resolved by upgrading the Go toolchain (module "stdlib") or the dependency.
  * "No upstream fix" findings (for example advisories on end-of-life packages), which can
    only be addressed by removing the dependency and are reported for transparency.

With --fail-on-fixable the exit status is 1 when any fixable finding exists, so a release
cannot ship a binary that is behind on security patches. Only the standard library is used;
scripts/_vuln_report_test.py covers the rendering and gating.
"""
from __future__ import annotations

import argparse
import datetime as _dt
import json
import sys
from dataclasses import dataclass, field
from pathlib import Path

VULN_URL = "https://pkg.go.dev/vuln/"


@dataclass
class Finding:
    osv: str
    module: str
    current: str
    fixed: str
    aliases: list[str] = field(default_factory=list)
    summary: str = ""

    @property
    def fixable(self) -> bool:
        return bool(self.fixed)


def _iter_messages(text: str):
    """Yield top-level JSON objects from a govulncheck -json stream (pretty-printed or NDJSON)."""
    decoder = json.JSONDecoder()
    pos = 0
    length = len(text)
    while pos < length:
        while pos < length and text[pos].isspace():
            pos += 1
        if pos >= length:
            break
        try:
            msg, end = decoder.raw_decode(text, pos)
        except json.JSONDecodeError:
            # Skip to the next line and keep going (tolerates stray non-JSON output)
            newline = text.find("\n", pos)
            if newline == -1:
                break
            pos = newline + 1
            continue
        pos = end
        if isinstance(msg, dict):
            yield msg


def parse_stream(text: str) -> list[Finding]:
    """Collapse govulncheck JSON messages into one Finding per (module, OSV id)."""
    osvs: dict[str, dict] = {}
    findings: dict[tuple[str, str], Finding] = {}
    for msg in _iter_messages(text):
        if "osv" in msg and isinstance(msg["osv"], dict):
            osvs[str(msg["osv"].get("id", ""))] = msg["osv"]
        finding = msg.get("finding")
        if not isinstance(finding, dict):
            continue
        osv_id = str(finding.get("osv") or "")
        trace = finding.get("trace") or [{}]
        module = str(trace[0].get("module") or "unknown")
        current = str(trace[0].get("version") or "")
        fixed = str(finding.get("fixed_version") or "")
        key = (module, osv_id)
        if key not in findings:
            findings[key] = Finding(osv=osv_id, module=module, current=current, fixed=fixed)
        elif fixed and not findings[key].fixed:
            findings[key].fixed = fixed
    for item in findings.values():
        meta = osvs.get(item.osv, {})
        item.aliases = [str(a) for a in meta.get("aliases") or []]
        item.summary = str(meta.get("summary") or "").strip()
    return sorted(findings.values(), key=lambda f: (f.module != "stdlib", f.module, f.osv))


def _md(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ").strip()


def _link(osv_id: str) -> str:
    return f"[{osv_id}]({VULN_URL}{osv_id})"


def render(findings: list[Finding], *, version: str, go_version: str, scan_date: str | None = None) -> str:
    scan_date = scan_date or _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    fixable = [f for f in findings if f.fixable]
    unfixable = [f for f in findings if not f.fixable]
    lines = [
        "### Vulnerability Report for BOSH CPI",
        f"**Version:** {version}",
        f"**Scan Date:** {scan_date}",
        f"**Go Toolchain:** {go_version or 'unknown'}",
        "**Scanner:** `govulncheck -mode=binary` (symbol-level scan of the linked `cpi_binary_linux` against https://vuln.go.dev)",
        "",
    ]
    if not findings:
        lines += ["**Result:** ✅ No known vulnerabilities found.", ""]
        return "\n".join(lines)

    status = "❌" if fixable else "⚠️"
    lines += [
        f"**Result:** {status} {len(findings)} known vulnerabilit{'y' if len(findings) == 1 else 'ies'} "
        f"({len(fixable)} fixable by upgrading, {len(unfixable)} without an upstream fix)",
        "",
        "#### Fixable — upgrade required",
        "",
    ]
    if fixable:
        lines += [
            "| Module | Linked version | Fixed in | ID | Aliases | Summary |",
            "| :--- | :--- | :--- | :--- | :--- | :--- |",
        ]
        for f in fixable:
            lines.append(
                f"| `{f.module}` | `{f.current or '-'}` | `{f.fixed}` | {_link(f.osv)} | "
                f"{_md(', '.join(f.aliases)) or '-'} | {_md(f.summary) or '-'} |"
            )
    else:
        lines.append("None — the toolchain and all dependencies are at or above every fixed version.")
    lines += ["", "#### No upstream fix available", ""]
    if unfixable:
        lines += [
            "| Module | Linked version | ID | Aliases | Summary |",
            "| :--- | :--- | :--- | :--- | :--- |",
        ]
        for f in unfixable:
            lines.append(
                f"| `{f.module}` | `{f.current or '-'}` | {_link(f.osv)} | "
                f"{_md(', '.join(f.aliases)) or '-'} | {_md(f.summary) or '-'} |"
            )
        lines += [
            "",
            "These advisories have no fixed version in the Go vulnerability database (typically end-of-life or "
            "unmaintained packages pulled in transitively). The scan is symbol-level: the vulnerable code is linked "
            "into the binary, which does not by itself mean the CPI calls it. Track them in the dependency roadmap.",
        ]
    else:
        lines.append("None.")
    lines.append("")
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("json_file", type=Path, help="govulncheck -json output (use - for stdin)")
    parser.add_argument("--version", required=True, help="CPI release version shown in the report")
    parser.add_argument("--go-version", default="", help="Go toolchain the binary was built with (e.g. go1.26.10)")
    parser.add_argument("--output", type=Path, help="write the Markdown report here (default: stdout)")
    parser.add_argument("--fail-on-fixable", action="store_true",
                        help="exit 1 if any finding has a fixed version available")
    args = parser.parse_args()

    text = sys.stdin.read() if str(args.json_file) == "-" else args.json_file.read_text(encoding="utf-8")
    findings = parse_stream(text)
    report = render(findings, version=args.version, go_version=args.go_version)
    if args.output:
        args.output.write_text(report, encoding="utf-8")
    else:
        sys.stdout.write(report)

    fixable = [f for f in findings if f.fixable]
    print(
        f"[INFO] govulncheck: {len(findings)} finding(s), {len(fixable)} fixable, "
        f"{len(findings) - len(fixable)} without upstream fix",
        file=sys.stderr,
    )
    if args.fail_on_fixable and fixable:
        wanted: dict[str, str] = {}
        for f in fixable:
            if f.module not in wanted or f.fixed > wanted[f.module]:
                wanted[f.module] = f.fixed
        hints = ", ".join(f"{m} -> {v}" for m, v in sorted(wanted.items()))
        print(f"[ERROR] {len(fixable)} fixable vulnerabilit(ies) found; upgrade: {hints}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
