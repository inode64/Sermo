#!/usr/bin/env python3
"""Fail on high-severity npm advisories outside the reviewed exception list."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ALLOWLIST_PATH = ROOT / "tools/npm-audit.allow"
BLOCKING_SEVERITIES = frozenset({"high", "critical"})


def parse_allowlist(text: str) -> tuple[dict[str, str], list[str]]:
    """Return advisory ID to reason, and structural problems."""
    allowed: dict[str, str] = {}
    problems: list[str] = []
    for lineno, line in enumerate(text.splitlines(), 1):
        if not line.strip() or line.startswith("#"):
            continue
        advisory, _, reason = line.partition("\t")
        if not advisory.startswith("GHSA-") or not reason.strip():
            problems.append(f"allowlist line {lineno} must be '<GHSA id><TAB><reason>'")
            continue
        if advisory in allowed:
            problems.append(f"allowlist names {advisory} more than once")
        allowed[advisory] = reason.strip()
    return allowed, problems


def blocking_advisories(report: dict) -> dict[str, str]:
    """Return advisory ID to description for every high or critical advisory.

    Packages that are vulnerable only through a dependency list that
    dependency's name in `via`; the advisory itself appears once, as an object,
    under the package it was filed against.
    """
    found: dict[str, str] = {}
    for package in report.get("vulnerabilities", {}).values():
        for via in package.get("via", []):
            if not isinstance(via, dict) or via.get("severity") not in BLOCKING_SEVERITIES:
                continue
            advisory = str(via.get("url", "")).rsplit("/", 1)[-1] or str(via.get("source"))
            found[advisory] = f"{via.get('name')} ({via.get('severity')}): {via.get('title')}"
    return found


def gate(report: dict, allowlist: str) -> list[str]:
    """Return the problems that must fail the gate."""
    allowed, problems = parse_allowlist(allowlist)
    found = blocking_advisories(report)
    problems.extend(
        f"{advisory}: {description}"
        for advisory, description in sorted(found.items())
        if advisory not in allowed
    )
    # A stale exception would silently cover the same ID if it ever came back.
    problems.extend(
        f"{advisory}: allowlisted but no longer reported; remove it from {ALLOWLIST_PATH.name}"
        for advisory in sorted(allowed)
        if advisory not in found
    )
    return problems


def main() -> int:
    # npm audit exits non-zero whenever it reports anything; the JSON decides.
    result = subprocess.run(
        ["npm", "audit", "--json"], cwd=ROOT, capture_output=True, text=True, check=False
    )
    try:
        report = json.loads(result.stdout)
    except json.JSONDecodeError:
        print(f"npm audit produced no JSON report:\n{result.stderr}", file=sys.stderr)
        return 1
    if "error" in report:
        print(f"npm audit failed: {report['error']}", file=sys.stderr)
        return 1
    problems = gate(report, ALLOWLIST_PATH.read_text(encoding="utf-8"))
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
