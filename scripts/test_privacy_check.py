#!/usr/bin/env python3
"""Contract tests for scripts/privacy_check.py."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


def checker_module():
    """Load the executable checker as a module under test."""
    path = Path(__file__).with_name("privacy_check.py")
    spec = importlib.util.spec_from_file_location("privacy_check", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load privacy_check.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


checker = checker_module()

INVENTORY = """
# fleet
root@alpha.vpn.example.org ClientName label colour
beta.internal OtherClient
root@sandbox.internal
"""


class InventoryTermsTest(unittest.TestCase):
    """Only the inventory's first column may ever become a search term."""

    def test_uses_the_first_column_only(self):
        terms = checker.inventory_terms(INVENTORY)
        self.assertEqual(terms, {"alpha", "beta", "example.org"})
        self.assertNotIn("clientname", terms)

    def test_generic_labels_are_not_terms(self):
        self.assertNotIn("sandbox", checker.inventory_terms(INVENTORY))

    def test_address_prefixes_ignore_rows_without_an_address(self):
        rows = [{"ip_vpn": "198.18.4.7"}, {"ip_vpn": ""}, {"ip_vpn": "not-an-address"}, {}]
        self.assertEqual(checker.address_prefixes(rows), {"198.18."})

    def test_address_prefixes_accept_spreadsheet_quoting(self):
        self.assertEqual(checker.address_prefixes([{"ip_vpn": "'198.18.4.7"}]), {"198.18."})


class FindingsTest(unittest.TestCase):
    """A finding names the term and its location, and nothing else."""

    def scan(self, files: dict[str, str]) -> list[str]:
        """Scan temporary files with the fixture's terms."""
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for name, content in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")
            pattern = checker.term_pattern({"alpha"}, {"198.18."})
            self.assertIsNotNone(pattern)
            return checker.findings(sorted(files), root, pattern)

    def test_reports_a_host_label_with_its_location(self):
        self.assertEqual(self.scan({"docs/a.md": "ok\nhost Alpha failed\n"}), ["docs/a.md:2: Alpha"])

    def test_reports_an_address_inside_the_prefix(self):
        self.assertEqual(self.scan({"a.go": "source 198.18.4.7\n"}), ["a.go:1: 198.18.4"])

    def test_ignores_a_longer_word_and_a_longer_address(self):
        self.assertEqual(self.scan({"a.go": "alphabet 10.198.18.4\n"}), [])

    def test_skips_lockfiles_and_vendored_code(self):
        files = {"package-lock.json": "alpha", "internal/web/src/vendor/lit.js": "alpha"}
        self.assertEqual(self.scan(files), [])

    def test_no_terms_means_no_scan(self):
        self.assertIsNone(checker.term_pattern(set(), set()))


if __name__ == "__main__":
    unittest.main()
