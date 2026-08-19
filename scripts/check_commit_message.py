#!/usr/bin/env python3
"""Validate the evidence-oriented body of a standard Sermo commit."""

from __future__ import annotations

import re
import sys
from pathlib import Path

REQUIRED_SECTIONS = ("Objective", "Invariant", "Evidence", "Limitations")
AUTOMATED_SUBJECT_PREFIXES = ("Merge ", 'Revert "', "fixup! ", "squash! ")
SECTION_PATTERN = re.compile(
    r"^(Objective|Invariant|Evidence|Limitations):(?:\s*(.*))$"
)


def meaningful_lines(message: str) -> list[str]:
    """Remove Git template comments without exposing message content."""
    return [
        line.rstrip()
        for line in message.splitlines()
        if not line.lstrip().startswith("#")
    ]


def validate_message(message: str) -> list[str]:
    """Return structural errors for one commit message."""
    lines = meaningful_lines(message)
    if not lines or not lines[0].strip():
        return ["commit subject is empty"]
    if lines[0].startswith(AUTOMATED_SUBJECT_PREFIXES):
        return []

    found: list[tuple[str, int, str]] = []
    for index, line in enumerate(lines[1:], start=1):
        match = SECTION_PATTERN.fullmatch(line)
        if match:
            found.append((match.group(1), index, match.group(2)))
    names = [name for name, _, _ in found]
    errors: list[str] = []
    if names != list(REQUIRED_SECTIONS):
        errors.append(
            "body requires Objective, Invariant, Evidence, and Limitations in that order"
        )
        return errors

    for position, (name, line_index, inline_value) in enumerate(found):
        end = found[position + 1][1] if position + 1 < len(found) else len(lines)
        values = [inline_value, *lines[line_index + 1 : end]]
        if not any(value.strip() for value in values):
            errors.append(f"{name} section is empty")
    return errors


def main(argv: list[str] | None = None) -> int:
    """Validate the commit message path supplied by Git's commit-msg hook."""
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        print("usage: check_commit_message.py COMMIT_MESSAGE_FILE", file=sys.stderr)
        return 64
    try:
        message = Path(args[0]).read_text(encoding="utf-8")
    except OSError as error:
        print(f"commit message check failed: {error}", file=sys.stderr)
        return 1
    errors = validate_message(message)
    if not errors:
        return 0
    print("commit message check failed:", file=sys.stderr)
    for error in errors:
        print(f"- {error}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
