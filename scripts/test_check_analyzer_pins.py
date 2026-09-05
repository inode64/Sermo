#!/usr/bin/env python3
"""Tests for the reproducible static-analyzer pin contract."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


def checker_module():
    """Load the executable checker as a module under test."""
    path = Path(__file__).with_name("check_analyzer_pins.py")
    spec = importlib.util.spec_from_file_location("check_analyzer_pins", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load check_analyzer_pins.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


checker = checker_module()

WORKFLOW = """
      - name: Install analyzers
        run: |
          go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
          go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
          go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
          go install golang.org/x/tools/cmd/goimports@v0.49.1-0.20260828025639-2e922938d07f
          go install golang.org/x/tools/cmd/deadcode@v0.49.1-0.20260828025639-2e922938d07f
          pip install 'yamllint==1.38.0' 'ruff==0.16.6' 'semgrep==1.176.1'
"""

CUSTOM_GCL = """
version: v2.13.2
name: custom-gcl
plugins:
  - module: go.uber.org/nilaway
    import: go.uber.org/nilaway/cmd/gclplugin
    version: v0.0.0-20260808063849-8649a03c818a
"""


class AnalyzerPinContractTest(unittest.TestCase):
    """Keep every analyzer install immutable and shared owners synchronized."""

    def test_accepts_exact_consistent_pins(self):
        self.assertEqual(checker.validate_pins(WORKFLOW, CUSTOM_GCL), [])

    def test_rejects_custom_golangci_version_drift(self):
        custom = CUSTOM_GCL.replace("version: v2.13.2", "version: v2.13.1", 1)

        self.assertIn(
            "golangci-lint is v2.13.2 in CI but v2.13.1 in .custom-gcl.yml",
            checker.validate_pins(WORKFLOW, custom),
        )

    def test_rejects_x_tools_version_drift(self):
        workflow = WORKFLOW.replace(
            "deadcode@v0.49.1-0.20260828025639-2e922938d07f",
            "deadcode@v0.49.0",
        )

        self.assertIn(
            "goimports and deadcode must use the same golang.org/x/tools version",
            checker.validate_pins(workflow, CUSTOM_GCL),
        )

    def test_rejects_mutable_go_and_python_pins(self):
        workflow = WORKFLOW.replace("staticcheck@v0.8.1", "staticcheck@latest")
        workflow = workflow.replace("'ruff==0.16.6'", "'ruff>=0.16.6'")
        custom = CUSTOM_GCL.replace(
            "v0.0.0-20260808063849-8649a03c818a", "main"
        )

        problems = checker.validate_pins(workflow, custom)

        self.assertIn(
            "Go analyzer honnef.co/go/tools/cmd/staticcheck uses mutable pin latest",
            problems,
        )
        self.assertIn("Python analyzer ruff>=0.16.6 is not exactly pinned", problems)
        self.assertIn("NilAway uses mutable pin main", problems)


if __name__ == "__main__":
    unittest.main()
