#!/usr/bin/env python3
"""Tests for the CodeQL SARIF high-severity gate."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


def gate_module():
    path = Path(__file__).with_name("codeql_fail_on_findings.py")
    spec = importlib.util.spec_from_file_location("codeql_fail_on_findings", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load codeql_fail_on_findings.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


gate = gate_module()


def sarif(*results: dict, rules: list[dict] | None = None) -> dict:
    run: dict = {"results": list(results)}
    if rules is not None:
        run["tool"] = {"driver": {"rules": rules}}
    return {"runs": [run]}


class CodeQLFailOnFindingsTest(unittest.TestCase):
    def test_empty_results_are_clean(self):
        self.assertEqual(gate.findings_from_sarif(sarif()), [])

    def test_note_and_low_warning_are_ignored(self):
        data = sarif(
            {"ruleId": "js/unused", "level": "note", "message": {"text": "unused"}},
            {
                "ruleId": "js/xss",
                "level": "warning",
                "properties": {"security-severity": "3.1"},
                "message": {"text": "low xss"},
            },
        )
        self.assertEqual(gate.findings_from_sarif(data), [])

    def test_error_level_blocks(self):
        data = sarif(
            {
                "ruleId": "go/unsafe-use",
                "level": "error",
                "message": {"text": "unsafe.Pointer"},
                "locations": [
                    {
                        "physicalLocation": {
                            "artifactLocation": {"uri": "internal/x.go"},
                            "region": {"startLine": 12},
                        }
                    }
                ],
            }
        )
        self.assertEqual(
            gate.findings_from_sarif(data),
            ["internal/x.go:12: go/unsafe-use: unsafe.Pointer"],
        )

    def test_high_security_severity_on_rule_blocks(self):
        data = sarif(
            {"ruleId": "go/sql-injection", "message": {"text": "query concat"}},
            rules=[
                {
                    "id": "go/sql-injection",
                    "properties": {"security-severity": "8.1"},
                    "defaultConfiguration": {"level": "warning"},
                }
            ],
        )
        self.assertEqual(
            gate.findings_from_sarif(data),
            ["go/sql-injection: query concat"],
        )

    def test_collect_reads_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            (path / "go.sarif").write_text(
                json.dumps(
                    sarif(
                        {
                            "ruleId": "go/path-injection",
                            "level": "error",
                            "message": {"text": "tainted path"},
                        }
                    )
                ),
                encoding="utf-8",
            )
            (path / "ignored.txt").write_text("not sarif", encoding="utf-8")
            findings = gate.collect(path)
        self.assertEqual(findings, ["go/path-injection: tainted path"])


if __name__ == "__main__":
    unittest.main()
