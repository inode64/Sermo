#!/usr/bin/env python3
"""Tests for read-only init contract prevalidation."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


def prevalidation_module():
    """Load the executable script as a module under test."""
    path = Path(__file__).with_name("prevalidate_init_contracts.py")
    spec = importlib.util.spec_from_file_location("prevalidate_init_contracts", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load prevalidate_init_contracts.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


prevalidation = prevalidation_module()


class InitContractPrevalidationTest(unittest.TestCase):
    """Keep inventory and result classification independent from SSH."""

    def write(self, body: str) -> Path:
        """Create a disposable inventory."""
        with tempfile.NamedTemporaryFile(
            "w", delete=False, encoding="utf-8"
        ) as temporary:
            temporary.write(body)
            path = Path(temporary.name)
        self.addCleanup(path.unlink, missing_ok=True)
        return path

    def test_inventory_uses_keys_only_and_deduplicates(self):
        inventory = self.write(
            "# fleet\nserver-a=sensitive value\nserver-b other value\nserver-a=repeated\n"
        )

        self.assertEqual(prevalidation.load_hosts(inventory), ["server-a", "server-b"])

    def test_rejects_malformed_inventory_without_echoing_value(self):
        inventory = self.write("bad host=do-not-print\n")

        with self.assertRaisesRegex(ValueError, "invalid host entry") as raised:
            prevalidation.load_hosts(inventory)
        self.assertNotIn("do-not-print", str(raised.exception))

    def test_accepts_matching_systemd_contract(self):
        output = (
            "SERVICE\tdemo\tsystemd\tdemo.service\tactive\tactive\tloaded\tyes\tyes\tno\n"
            "SUMMARY\tsystemd\tactive\tvalid\t1\n"
        )

        result = prevalidation.parse_remote_output("server-a", output)

        self.assertEqual(result.status, "ok")
        self.assertEqual(result.checked, 1)
        self.assertEqual(result.mismatches, ())

    def test_reports_state_and_inventory_mismatches(self):
        output = (
            "SERVICE\tdemo\topenrc\tdemo\tactive\tinactive\tloaded\tno\tunknown\tyes\n"
            "SUMMARY\topenrc\tinactive\tvalid\t2\n"
        )

        result = prevalidation.parse_remote_output("server-b", output)

        self.assertEqual(result.status, "mismatch")
        self.assertIn("sermod inactive", result.mismatches)
        self.assertIn("configured 2, checked 1", result.mismatches)
        self.assertIn("demo: Sermo=active init=inactive", result.mismatches)


if __name__ == "__main__":
    unittest.main()
