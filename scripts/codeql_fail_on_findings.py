#!/usr/bin/env python3
"""Fail when CodeQL SARIF contains a high-severity security finding.

Usage:
  python3 scripts/codeql_fail_on_findings.py results/

GitHub's analyze action uploads alerts without failing the job. This gate
reads the SARIF written next to that upload and exits 1 when any result is
an error or has security-severity >= 7.0 (High).
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

HIGH_SECURITY_SEVERITY = 7.0
EXIT_OK = 0
EXIT_FINDINGS = 1
EXIT_USAGE = 2


def rule_index(run: dict) -> dict[str, dict]:
    """Map rule id to the driver rule object."""
    driver = ((run.get("tool") or {}).get("driver") or {})
    return {rule.get("id"): rule for rule in driver.get("rules") or [] if rule.get("id")}


def security_severity(result: dict, rules: dict[str, dict]) -> float | None:
    """Return CVSS-like security-severity from the result or its rule."""
    props = result.get("properties") or {}
    raw = props.get("security-severity")
    if raw is None:
        rule = rules.get(result.get("ruleId") or "")
        if rule:
            raw = (rule.get("properties") or {}).get("security-severity")
    if raw is None:
        return None
    try:
        return float(raw)
    except (TypeError, ValueError):
        return None


def result_level(result: dict, rules: dict[str, dict]) -> str:
    """SARIF level: result override, else the rule default, else warning."""
    if level := result.get("level"):
        return str(level)
    rule = rules.get(result.get("ruleId") or "")
    if rule:
        default = (rule.get("defaultConfiguration") or {}).get("level")
        if default:
            return str(default)
    return "warning"


def result_message(result: dict) -> str:
    message = result.get("message") or {}
    if text := message.get("text"):
        return str(text)
    return ""


def result_location(result: dict) -> str:
    for loc in result.get("locations") or []:
        phys = (loc.get("physicalLocation") or {})
        art = (phys.get("artifactLocation") or {})
        uri = art.get("uri") or ""
        region = phys.get("region") or {}
        line = region.get("startLine")
        if uri and line:
            return f"{uri}:{line}"
        if uri:
            return str(uri)
    return ""


def is_blocking(result: dict, rules: dict[str, dict]) -> bool:
    if result_level(result, rules) == "error":
        return True
    severity = security_severity(result, rules)
    return severity is not None and severity >= HIGH_SECURITY_SEVERITY


def findings_from_sarif(data: dict) -> list[str]:
    lines: list[str] = []
    for run in data.get("runs") or []:
        rules = rule_index(run)
        for result in run.get("results") or []:
            if not is_blocking(result, rules):
                continue
            rule = result.get("ruleId") or "unknown"
            loc = result_location(result)
            msg = result_message(result)
            prefix = f"{loc}: " if loc else ""
            lines.append(f"{prefix}{rule}: {msg}".rstrip())
    return lines


def collect(path: Path) -> list[str]:
    files = sorted(path.glob("*.sarif")) if path.is_dir() else [path]
    findings: list[str] = []
    for sarif in files:
        data = json.loads(sarif.read_text(encoding="utf-8"))
        findings.extend(findings_from_sarif(data))
    return findings


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: codeql_fail_on_findings.py <sarif-file-or-dir>", file=sys.stderr)
        return EXIT_USAGE
    target = Path(argv[1])
    if not target.exists():
        print(f"codeql_fail_on_findings: {target} does not exist", file=sys.stderr)
        return EXIT_USAGE
    findings = collect(target)
    if not findings:
        return EXIT_OK
    print(f"codeql_fail_on_findings: {len(findings)} high-severity finding(s)", file=sys.stderr)
    for line in findings:
        print(line, file=sys.stderr)
    return EXIT_FINDINGS


if __name__ == "__main__":
    sys.exit(main(sys.argv))
