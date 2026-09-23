#!/usr/bin/env python3
"""Own YAML file selection and normalize flow punctuation without touching scalars."""

from __future__ import annotations

from pathlib import Path

# PyYAML is also required by yamllint; use its scanner instead of interpreting
# braces ourselves. Semgrep patterns and shell/regex scalars are not YAML maps.
import yaml

ROOT = Path(__file__).resolve().parents[1]
YAML_ROOTS = ("catalog", "examples", "templates", "docs", ".github", ".semgrep")


def iter_yaml_files(root: Path = ROOT) -> list[Path]:
    """Return the shared formatting/lint inventory, including new untracked files."""
    found = {p for suffix in ("*.yml", "*.yaml") for p in root.glob(suffix)}
    found.add(root / ".yamlfmt")
    for directory in YAML_ROOTS:
        for suffix in ("*.yml", "*.yaml"):
            found.update((root / directory).rglob(suffix))
    return sorted(p for p in found if p.is_file())


def normalize_flow(text: str) -> str:
    """Pad flow maps and commas using token offsets; preserve scalar contents."""
    edits: dict[tuple[int, int], str] = {}
    for token in yaml.scan(text):
        if isinstance(token, (yaml.tokens.FlowMappingStartToken, yaml.tokens.FlowEntryToken)):
            start = end = token.end_mark.index
            while end < len(text) and text[end] in " \t":
                end += 1
            if end < len(text) and text[end] not in "\r\n":
                edits[start, end] = " "
        elif isinstance(token, yaml.tokens.FlowMappingEndToken):
            start = end = token.start_mark.index
            while start > 0 and text[start - 1] in " \t":
                start -= 1
            if start > 0 and text[start - 1] not in "\r\n":
                edits[start, end] = " "
    for (start, end), replacement in sorted(edits.items(), reverse=True):
        text = text[:start] + replacement + text[end:]
    return text
