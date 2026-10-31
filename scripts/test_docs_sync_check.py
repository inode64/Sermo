#!/usr/bin/env python3
"""Contract tests for the cross-file link check in scripts/docs_sync_check.py."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


def checker_module():
    """Load the executable checker as a module under test."""
    path = Path(__file__).with_name("docs_sync_check.py")
    spec = importlib.util.spec_from_file_location("docs_sync_check", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load docs_sync_check.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


checker = checker_module()


class HeadingAnchorsTest(unittest.TestCase):
    """Anchors must match what GitHub generates."""

    def test_strips_punctuation_and_code_spans(self):
        anchors = checker.heading_anchors("## Per-host overrides (`dir.local`)\n")
        self.assertEqual(anchors, {"per-host-overrides-dirlocal"})

    def test_numbers_repeated_headings(self):
        self.assertEqual(checker.heading_anchors("## Same\n## Same\n"), {"same", "same-1"})

    def test_ignores_headings_inside_code_fences(self):
        self.assertEqual(checker.heading_anchors("```sh\n# comment\n```\n# Real\n"), {"real"})


class RelativeLinksTest(unittest.TestCase):
    """A link must reach both its file and its anchor."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        original = checker.ROOT
        checker.ROOT = self.root
        self.addCleanup(setattr, checker, "ROOT", original)
        (self.root / "target.md").write_text("# Known heading\n", encoding="utf-8")
        self.source = self.root / "source.md"
        self.source.write_text("# Local\n", encoding="utf-8")

    def test_accepts_a_reachable_file_and_anchor(self):
        self.assertEqual(checker.link_problem(self.source, "target.md#known-heading"), "")

    def test_accepts_an_anchor_in_the_same_file(self):
        self.assertEqual(checker.link_problem(self.source, "#local"), "")

    def test_reports_a_missing_file(self):
        self.assertIn("broken link", checker.link_problem(self.source, "gone.md"))

    def test_reports_a_missing_anchor_in_another_file(self):
        self.assertIn("broken anchor", checker.link_problem(self.source, "target.md#gone"))


if __name__ == "__main__":
    unittest.main()
