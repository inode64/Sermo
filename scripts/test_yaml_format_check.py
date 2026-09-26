#!/usr/bin/env python3
"""Regression tests for YAML coverage and scalar-safe formatting."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import yaml
from normalize_yaml_flow import ROOT, iter_yaml_files, normalize_flow
from yaml_format_check import canonicalize, yamllint_command


class YAMLFormattingTest(unittest.TestCase):
    def test_inventory_includes_both_extensions_and_analyzer_configs(self):
        included = (
            ".golangci.yml", ".custom-gcl.yml", ".yamllint.yml", ".markdownlint.yml", ".yamlfmt",
            ".github/workflows/new.yaml", ".semgrep/rules/new.yml", "catalog/services/new.yaml",
            "examples/new.yml", "templates/new.yaml", "docs/new.yaml",
        )
        excluded = ("graphify-out/graph.yaml", "node_modules/pkg/ci.yml", ".agents/local.yaml")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for filename in (*included, *excluded):
                path = root / filename
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("name: fixture\n", encoding="utf-8")
            self.assertEqual({str(p.relative_to(root)) for p in iter_yaml_files(root)}, set(included))

    def test_flow_spacing_preserves_scalars_and_block_patterns(self):
        source = '''name: fixture
check: {type: command, nested: {enabled: true}, empty: {}}
regex: 'a{1,3}'
message: "literal {x,y}"
pattern: |
  if $OK {
    f("{x,y}")
  }
command: ["${binary}", "--format={x,y}"]
'''
        formatted = normalize_flow(source)
        self.assertIn("check: { type: command, nested: { enabled: true }, empty: { } }", formatted)
        self.assertEqual(yaml.safe_load(formatted), yaml.safe_load(source))
        self.assertEqual(normalize_flow(formatted), formatted)

    def test_actual_formatter_is_idempotent_and_preserves_scalar_values(self):
        source = '''rules:
  - message: >-
      A folded diagnostic keeps readable source lines
      without changing the scalar consumed by Semgrep.
  - pattern: |
      if $OK {
        return "a{1,3}"
      }
    regex: 'a{1,3}'
    options: {enabled: true, values: [1,2]}
'''
        formatted = canonicalize(source)
        self.assertEqual(yaml.safe_load(formatted), yaml.safe_load(source))
        self.assertEqual(canonicalize(formatted), formatted)

    def test_missing_final_newline_is_repaired(self):
        self.assertEqual(canonicalize("name: fixture"), "name: fixture\n")

    def test_yamllint_paths_stay_relative_to_the_repository(self):
        command = yamllint_command([
            ROOT / ".yamllint.yml",
            ROOT / "catalog" / "services" / "nginx.yml",
        ])
        self.assertEqual(command[1:4], ["--strict", "-c", ".yamllint.yml"])
        self.assertEqual(command[4:], [".yamllint.yml", "catalog/services/nginx.yml"])


if __name__ == "__main__":
    unittest.main()
