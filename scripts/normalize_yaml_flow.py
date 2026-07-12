#!/usr/bin/env python3
"""Normalize inline flow-map spacing in Sermo YAML after yamlfmt.

yamlfmt re-encodes flow mappings without interior spaces. Sermo catalog style:
  binary: { type: binary, path: "${binary}" }
  - { id: emerg, match: '...', severity: error }
"""

from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

INCLUDE_ROOTS = (
    ROOT / "catalog",
    ROOT / "examples",
    ROOT / "templates",
    ROOT / "docs",
    ROOT / ".github",
)
EXTRA_FILES = (ROOT / ".golangci.yml",)
EXCLUDE_PARTS = ("graphify-out", ".agents")


def iter_yaml_files() -> list[Path]:
    out: list[Path] = []
    for base in INCLUDE_ROOTS:
        if not base.exists():
            continue
        if base.is_file():
            out.append(base)
            continue
        for path in sorted(base.rglob("*.yml")):
            if any(part in path.parts for part in EXCLUDE_PARTS):
                continue
            out.append(path)
        for path in sorted(base.rglob("*.yaml")):
            if any(part in path.parts for part in EXCLUDE_PARTS):
                continue
            out.append(path)
    for path in EXTRA_FILES:
        if path.exists():
            out.append(path)
    return sorted(set(out))


def find_matching_brace(text: str, start: int) -> int:
    depth = 0
    in_string: str | None = None
    i = start
    while i < len(text):
        ch = text[i]
        if in_string is not None:
            if ch == "\\":
                i += 2
                continue
            if ch == in_string:
                in_string = None
            i += 1
            continue
        if ch in "\"'":
            in_string = ch
            i += 1
            continue
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return i
        i += 1
    raise ValueError(f"unbalanced '{{' at column {start}")


def add_comma_spaces(text: str) -> str:
    out: list[str] = []
    in_string: str | None = None
    i = 0
    while i < len(text):
        ch = text[i]
        if in_string is not None:
            out.append(ch)
            if ch == "\\":
                if i + 1 < len(text):
                    out.append(text[i + 1])
                    i += 2
                    continue
            elif ch == in_string:
                in_string = None
            i += 1
            continue
        if ch in "\"'":
            in_string = ch
            out.append(ch)
            i += 1
            continue
        if ch == "," and i + 1 < len(text) and text[i + 1] not in " \t\n":
            out.append(", ")
            i += 1
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def normalize_flow_segment(segment: str) -> str:
    if not segment:
        return segment
    out: list[str] = []
    i = 0
    while i < len(segment):
        if segment[i] == "{" and (i == 0 or segment[i - 1] != "$"):
            end = find_matching_brace(segment, i)
            inner = segment[i + 1 : end]
            inner = normalize_flow_segment(inner)
            inner = add_comma_spaces(inner.strip())
            if inner:
                out.append("{ " + inner + " }")
            else:
                out.append("{ }")
            i = end + 1
            continue
        out.append(segment[i])
        i += 1
    return "".join(out)


def normalize_line(line: str) -> str:
    if "{" not in line or "${" == line.strip():
        return line
    return normalize_flow_segment(line)


def normalize_file(path: Path) -> bool:
    original = path.read_text(encoding="utf-8")
    updated = "\n".join(normalize_line(line) for line in original.splitlines())
    if original.endswith("\n"):
        updated += "\n"
    elif updated and not updated.endswith("\n"):
        updated += "\n"
    if updated == original:
        return False
    path.write_text(updated, encoding="utf-8")
    return True


def main() -> int:
    check_only = "--check" in sys.argv
    changed = 0
    for path in iter_yaml_files():
        if check_only:
            original = path.read_text(encoding="utf-8")
            updated = "\n".join(normalize_line(line) for line in original.splitlines())
            if original.endswith("\n"):
                updated += "\n"
            elif updated and not updated.endswith("\n"):
                updated += "\n"
            if updated != original:
                print(path.relative_to(ROOT))
                changed += 1
            continue
        if normalize_file(path):
            changed += 1
            print(path.relative_to(ROOT))
    if changed:
        if check_only:
            print(f"flow spacing needs normalize in {changed} file(s)", file=sys.stderr)
            return 1
        print(f"normalized flow spacing in {changed} file(s)", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())