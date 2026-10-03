#!/usr/bin/env python3
"""Tests for the npm advisory gate and its reviewed exception list."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


def gate_module():
    """Load the executable gate as a module under test."""
    path = Path(__file__).with_name("npm_audit_gate.py")
    spec = importlib.util.spec_from_file_location("npm_audit_gate", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load npm_audit_gate.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


gate = gate_module()


def advisory(ghsa: str, severity: str = "high") -> dict:
    return {
        "source": 1,
        "name": "braces",
        "title": "stack exhaustion",
        "url": f"https://github.com/advisories/{ghsa}",
        "severity": severity,
    }


REPORT = {
    "vulnerabilities": {
        "braces": {"severity": "high", "via": [advisory("GHSA-aaaa-bbbb-cccc")]},
        # Vulnerable only through braces: names the dependency, not an advisory.
        "micromatch": {"severity": "high", "via": ["braces"]},
    }
}
ALLOWLIST = "# comment\nGHSA-aaaa-bbbb-cccc\tno patched release\n"


class NpmAuditGateTest(unittest.TestCase):
    def test_allowlisted_advisory_passes(self):
        self.assertEqual(gate.gate(REPORT, ALLOWLIST), [])

    def test_unlisted_advisory_fails(self):
        problems = gate.gate(REPORT, "")
        self.assertEqual(len(problems), 1)
        self.assertIn("GHSA-aaaa-bbbb-cccc", problems[0])

    def test_second_advisory_on_allowlisted_package_fails(self):
        report = {
            "vulnerabilities": {
                "braces": {
                    "via": [advisory("GHSA-aaaa-bbbb-cccc"), advisory("GHSA-dddd-eeee-ffff", "critical")]
                }
            }
        }
        problems = gate.gate(report, ALLOWLIST)
        self.assertEqual(len(problems), 1)
        self.assertIn("GHSA-dddd-eeee-ffff", problems[0])

    def test_moderate_advisory_does_not_block(self):
        report = {"vulnerabilities": {"x": {"via": [advisory("GHSA-dddd-eeee-ffff", "moderate")]}}}
        self.assertEqual(gate.gate(report, ""), [])

    def test_stale_entry_fails(self):
        problems = gate.gate({"vulnerabilities": {}}, ALLOWLIST)
        self.assertEqual(len(problems), 1)
        self.assertIn("no longer reported", problems[0])

    def test_entry_without_reason_fails(self):
        problems = gate.gate(REPORT, "GHSA-aaaa-bbbb-cccc\n")
        self.assertTrue(any("allowlist line 1" in problem for problem in problems))


if __name__ == "__main__":
    unittest.main()
