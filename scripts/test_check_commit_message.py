#!/usr/bin/env python3
"""Tests for the Sermo commit message contract."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


def checker_module():
    """Load the executable checker as a module under test."""
    path = Path(__file__).with_name("check_commit_message.py")
    spec = importlib.util.spec_from_file_location("check_commit_message", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load check_commit_message.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


checker = checker_module()


class CommitMessageContractTest(unittest.TestCase):
    """Require decision evidence without constraining generated Git commits."""

    def test_accepts_complete_body_with_inline_or_multiline_values(self):
        message = """chore(git): centralize the commit contract

Objective: one interpretation

Invariant:
Every consumer agrees.

Evidence: make check

Limitations: None.
"""

        self.assertEqual(checker.validate_message(message), [])

    def test_rejects_missing_reordered_and_empty_sections(self):
        missing = "chore(git): incomplete\n\nObjective: done\nEvidence: tests\n"
        reordered = "chore(git): mixed\n\nInvariant: safe\nObjective: done\nEvidence: tests\nLimitations: none\n"
        empty = "chore(git): empty\n\nObjective:\nInvariant: safe\nEvidence: tests\nLimitations: none\n"

        self.assertIn("in that order", checker.validate_message(missing)[0])
        self.assertIn("in that order", checker.validate_message(reordered)[0])
        self.assertEqual(
            checker.validate_message(empty), ["Objective section is empty"]
        )

    def test_ignores_template_comments(self):
        message = "chore(git): comments\n\nObjective:\n# placeholder\nInvariant: safe\nEvidence: tests\nLimitations: none\n"

        self.assertEqual(
            checker.validate_message(message), ["Objective section is empty"]
        )

    def test_allows_git_generated_subjects(self):
        for subject in (
            'Merge branch "main"',
            'Revert "feat: change"',
            "fixup! feat: change",
            "squash! feat: change",
        ):
            with self.subTest(subject=subject):
                self.assertEqual(checker.validate_message(subject), [])


if __name__ == "__main__":
    unittest.main()
